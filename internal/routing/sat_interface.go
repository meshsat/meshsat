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
}

// NewSatInterface creates a new satellite Reticulum interface.
// The callback is invoked for each received Reticulum packet from MT messages.
func NewSatInterface(config SatInterfaceConfig, sat transport.SatTransport, callback func(packet []byte)) *SatInterface {
	return &SatInterface{
		config:   config,
		sat:      sat,
		callback: callback,
		stopCh:   make(chan struct{}),
	}
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

	log.Info().Str("iface", s.config.Name).Str("type", string(s.config.Type)).
		Int("mtu", s.config.MTU).Bool("online", s.IsOnline()).
		Msg("satellite reticulum interface started")
	return nil
}

// Send transmits a Reticulum packet as an MO satellite message.
func (s *SatInterface) Send(ctx context.Context, packet []byte) error {
	s.mu.Lock()
	online := s.online
	s.mu.Unlock()

	if !online {
		return fmt.Errorf("satellite interface %s is offline", s.config.Name)
	}
	if len(packet) > s.config.MTU {
		return fmt.Errorf("packet %d bytes exceeds MTU %d for %s", len(packet), s.config.MTU, s.config.Name)
	}

	result, err := s.sat.Send(ctx, packet)
	if err != nil {
		return fmt.Errorf("sat send: %w", err)
	}
	if !result.MOSuccess() {
		return fmt.Errorf("sat send failed (mo_status=%d)", result.MOStatus)
	}

	log.Debug().Str("iface", s.config.Name).Int("size", len(packet)).
		Int("mo_status", result.MOStatus).Msg("sat iface: packet sent")
	return nil
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

	// Validate: Reticulum packets have at least a 2-byte header
	if len(data) < 2 {
		log.Debug().Str("iface", s.config.Name).Int("size", len(data)).
			Msg("sat iface: received data too short for Reticulum packet, ignoring")
		return
	}

	log.Debug().Str("iface", s.config.Name).Int("size", len(data)).
		Msg("sat iface: received Reticulum packet via MT")
	s.callback(data)
}

func (s *SatInterface) isStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}
