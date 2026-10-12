package routing

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/reticulum"
	"meshsat/internal/transport"
)

// SatInterfaceConfig configures a satellite Reticulum interface.
type SatInterfaceConfig struct {
	// Name is the interface identifier (e.g. "iridium_0", "iridium_imt_0").
	Name string
	// Type is the Reticulum interface type for cost calculation.
	Type reticulum.InterfaceType
	// MTU is the maximum packet size the transport can carry.
	MTU int
	// PollInterval is how often to check for inbound MT messages (0 = event-driven only).
	PollInterval time.Duration
	// RNSFraming wraps every packet in CrossTalk's "RNSI\x01" IMT header on
	// send and requires it on receive (auto-detected when off). [MESHSAT-1351]
	RNSFraming bool

	// Send queue [MESHSAT-1548]. Packets go to the modem one at a time, in
	// the order they were handed over, and a failed MO is retried before
	// the next packet goes: a Reticulum link sends its RTT packet and the
	// first message back to back, and the far end drops a message that
	// overtakes the RTT packet. Zero values take the defaults.
	QueueSize    int             // packets waiting for the modem (64)
	SendTimeout  time.Duration   // one MO attempt (4 min: the 9704 itself waits up to 180 s)
	MaxAge       time.Duration   // a packet not sent by then is dropped (15 min)
	RetryBackoff []time.Duration // waits between attempts (5 s, 15 s, 30 s, 60 s, then 120 s)
}

type satOut struct {
	wire   []byte
	queued time.Time
}

// SatQueueStats are the send queue's counters.
type SatQueueStats struct {
	Queued, Sent, Retried, Dropped int64
	Waiting                        int
}

// SatInterface is a bidirectional Reticulum interface over a satellite transport.
// It wraps an existing SatTransport (SBD or IMT) so that Reticulum packets can
// be sent as MO messages and received as MT messages.
// No HDLC framing — satellite transports are message-oriented.
type SatInterface struct {
	config   SatInterfaceConfig
	sat      transport.SatTransport
	callback func(packet []byte) // called when an inbound packet is received

	mu      sync.Mutex
	online  bool
	stopCh  chan struct{}
	stopped bool
	framing bool
	dedup   *imtDedup

	queue                          chan satOut
	queued, sent, retried, dropped int64
}

// NewSatInterface creates a new satellite Reticulum interface.
// The callback is invoked for each received Reticulum packet from MT messages.
func NewSatInterface(config SatInterfaceConfig, sat transport.SatTransport, callback func(packet []byte)) *SatInterface {
	if config.QueueSize <= 0 {
		config.QueueSize = 64
	}
	if config.SendTimeout <= 0 {
		config.SendTimeout = 4 * time.Minute
	}
	if config.MaxAge <= 0 {
		config.MaxAge = 15 * time.Minute
	}
	if len(config.RetryBackoff) == 0 {
		config.RetryBackoff = []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute}
	}
	return &SatInterface{
		queue:    make(chan satOut, config.QueueSize),
		config:   config,
		sat:      sat,
		callback: callback,
		stopCh:   make(chan struct{}),
		framing:  config.RNSFraming,
		dedup:    newIMTDedup(15*time.Minute, 256),
	}
}

// SetRNSFraming turns the CrossTalk IMT header on or off at runtime.
func (s *SatInterface) SetRNSFraming(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.framing = on
}

// RNSFraming reports whether the CrossTalk IMT header is on.
func (s *SatInterface) RNSFraming() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.framing
}

// Start begins monitoring for inbound MT messages and marks the interface online.
func (s *SatInterface) Start(ctx context.Context) error {
	status, err := s.sat.GetStatus(ctx)
	if err != nil {
		log.Warn().Err(err).Str("iface", s.config.Name).Msg("sat iface: could not get modem status")
	}

	s.mu.Lock()
	s.online = err == nil && status.Connected
	s.mu.Unlock()

	// Subscribe to transport events for push-based MT and status changes
	go s.eventLoop(ctx)
	go s.sendLoop(ctx)

	log.Info().Str("iface", s.config.Name).Str("type", string(s.config.Type)).
		Int("mtu", s.config.MTU).Bool("online", s.IsOnline()).
		Msg("satellite reticulum interface started")
	return nil
}

// Send transmits a Reticulum packet as an MO satellite message.
func (s *SatInterface) Send(ctx context.Context, packet []byte) error {
	s.mu.Lock()
	online := s.online
	framing := s.framing
	s.mu.Unlock()

	if !online {
		return fmt.Errorf("satellite interface %s is offline", s.config.Name)
	}
	wire := packet
	if framing {
		wire = EncodeIMTFrame(packet)
	}
	if len(wire) > s.config.MTU {
		return fmt.Errorf("packet %d bytes exceeds MTU %d for %s", len(wire), s.config.MTU, s.config.Name)
	}

	// Queued, not sent: an MO can take minutes, and the caller (the
	// Reticulum node) must not wait for it or race another packet past it.
	select {
	case s.queue <- satOut{wire: append([]byte(nil), wire...), queued: time.Now()}:
		s.mu.Lock()
		s.queued++
		s.mu.Unlock()
		return nil
	default:
		s.mu.Lock()
		s.dropped++
		s.mu.Unlock()
		return fmt.Errorf("satellite interface %s: send queue full (%d)", s.config.Name, s.config.QueueSize)
	}
}

// QueueStats reports the send queue's counters.
func (s *SatInterface) QueueStats() SatQueueStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SatQueueStats{Queued: s.queued, Sent: s.sent, Retried: s.retried, Dropped: s.dropped, Waiting: len(s.queue)}
}

// sendLoop is the only sender: one packet at a time, in order.
func (s *SatInterface) sendLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case out := <-s.queue:
			s.sendOne(ctx, out)
		}
	}
}

// sendOne tries one packet until it is sent or too old. The packets behind
// it wait: order matters more than any single packet's latency.
func (s *SatInterface) sendOne(ctx context.Context, out satOut) {
	for attempt := 0; ; attempt++ {
		if age := time.Since(out.queued); age > s.config.MaxAge {
			s.mu.Lock()
			s.dropped++
			s.mu.Unlock()
			log.Warn().Str("iface", s.config.Name).Dur("age", age).Int("attempts", attempt).Msg("sat iface: packet dropped, not sent in time")
			return
		}
		actx, cancel := context.WithTimeout(ctx, s.config.SendTimeout)
		result, err := s.sat.Send(actx, out.wire)
		cancel()
		if err == nil && result != nil && result.MOSuccess() {
			s.mu.Lock()
			s.sent++
			s.mu.Unlock()
			log.Debug().Str("iface", s.config.Name).Int("size", len(out.wire)).Int("attempt", attempt+1).
				Dur("waited", time.Since(out.queued)).Msg("sat iface: packet sent")
			return
		}
		if err == nil {
			mo := -1
			if result != nil {
				mo = result.MOStatus
			}
			err = fmt.Errorf("mo_status=%d", mo)
		}
		wait := s.config.RetryBackoff[min(attempt, len(s.config.RetryBackoff)-1)]
		s.mu.Lock()
		s.retried++
		s.mu.Unlock()
		log.Warn().Err(err).Str("iface", s.config.Name).Int("attempt", attempt+1).Dur("retry_in", wait).Msg("sat iface: send failed, retrying before the next packet")
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-time.After(wait):
		}
	}
}

// Stop shuts down the satellite interface.
func (s *SatInterface) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return
	}
	s.stopped = true
	s.online = false
	close(s.stopCh)
	log.Info().Str("iface", s.config.Name).Msg("satellite reticulum interface stopped")
}

// IsOnline returns whether the satellite modem is connected.
func (s *SatInterface) IsOnline() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.online
}

// eventLoop subscribes to transport events and handles inbound MT + status changes.
func (s *SatInterface) eventLoop(ctx context.Context) {
	backoff := time.Second

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		default:
		}

		start := time.Now()
		err := s.listenOnce(ctx)
		if ctx.Err() != nil || s.isStopped() {
			return
		}

		if time.Since(start) > 10*time.Second {
			backoff = time.Second
		}
		if err != nil {
			log.Debug().Err(err).Str("iface", s.config.Name).Dur("backoff", backoff).
				Msg("sat iface: event loop reconnecting")
		}

		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

// listenOnce subscribes and processes events until disconnected.
func (s *SatInterface) listenOnce(ctx context.Context) error {
	events, err := s.sat.Subscribe(ctx)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	// Check connection status after subscribe
	if status, err := s.sat.GetStatus(ctx); err == nil {
		s.mu.Lock()
		s.online = status.Connected
		s.mu.Unlock()
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.stopCh:
			return nil
		case event, ok := <-events:
			if !ok {
				return fmt.Errorf("event channel closed")
			}
			switch event.Type {
			case "ring_alert", "mt_received":
				// Inbound MT — read the message and deliver as Reticulum packet
				s.handleInbound(ctx)
			case "connected", "reconnected", "status":
				s.mu.Lock()
				s.online = true
				s.mu.Unlock()
			case "disconnected":
				s.mu.Lock()
				s.online = false
				s.mu.Unlock()
				return fmt.Errorf("modem disconnected")
			}
		}
	}
}

// handleInbound reads an MT message and delivers it as a Reticulum packet.
func (s *SatInterface) handleInbound(ctx context.Context) {
	// Check mailbox for pending MT
	result, err := s.sat.MailboxCheck(ctx)
	if err != nil {
		log.Debug().Err(err).Str("iface", s.config.Name).Msg("sat iface: mailbox check failed")
		return
	}
	if result.MTStatus != 1 || result.MTLength == 0 {
		return // no MT message
	}

	data, err := s.sat.Receive(ctx)
	if err != nil {
		log.Warn().Err(err).Str("iface", s.config.Name).Msg("sat iface: receive failed")
		return
	}
	if len(data) == 0 {
		return
	}

	s.deliver(data)
}

// deliver unwraps CrossTalk's IMT header when present, drops repeats and
// hands the packet up.
func (s *SatInterface) deliver(data []byte) {
	s.mu.Lock()
	strict := s.framing
	s.mu.Unlock()
	packet, framed, err := DecodeIMTFrame(data, strict)
	if err != nil {
		log.Warn().Err(err).Str("iface", s.config.Name).Int("size", len(data)).
			Msg("sat iface: MT message rejected")
		return
	}
	// Validate: Reticulum packets have at least a 2-byte header
	if len(packet) < 2 {
		log.Debug().Str("iface", s.config.Name).Int("size", len(packet)).
			Msg("sat iface: received data too short for Reticulum packet, ignoring")
		return
	}
	if s.dedup.Seen(packet, time.Now()) {
		log.Info().Str("iface", s.config.Name).Int("size", len(packet)).Bool("framed", framed).
			Msg("sat iface: duplicate MT message dropped")
		return
	}
	log.Debug().Str("iface", s.config.Name).Int("size", len(packet)).Bool("framed", framed).
		Msg("sat iface: received Reticulum packet via MT")
	s.callback(packet)
}

func (s *SatInterface) isStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}
