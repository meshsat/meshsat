package transport

// DirectIMTTransport implements SatTransport for the RockBLOCK 9704 (Iridium IMT).
// Uses JSPR protocol at 230400 baud instead of AT commands.
// Max message size: 100 KB (vs 340 bytes on SBD/9603N).
//
// The 9704 has no onboard message caching, so MT messages must be polled
// frequently or they will be lost when the satellite link drops.

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"go.bug.st/serial"
)

// DirectIMTTransport implements SatTransport via JSPR for the RockBLOCK 9704.
type DirectIMTTransport struct {
	port string // "/dev/ttyUSB0" or "auto"

	mu        sync.Mutex
	conn      *jsprConn
	file      serial.Port
	connected bool
	imei      string
	hwVersion string
	serial    string

	// Signal state
	signalMu   sync.RWMutex
	lastSignal SignalInfo

	// MT message buffer (populated by poll loop)
	mtMu      sync.Mutex
	mtPending [][]byte // complete MT payloads waiting for Receive()

	// SSE subscribers
	eventMu   sync.RWMutex
	eventSubs map[uint64]chan SatEvent
	nextSubID uint64

	// Exclude ports (avoid probing ports already claimed by other transports)
	excludePort   string
	excludePortFn func() string

	cancelFunc context.CancelFunc
	pollDone   chan struct{}
	sigDone    chan struct{}
}

// NewDirectIMTTransport creates a new direct serial IMT transport for the RockBLOCK 9704.
// Pass "auto" or "" for port to use auto-detection.
func NewDirectIMTTransport(port string) *DirectIMTTransport {
	return &DirectIMTTransport{
		port:      port,
		eventSubs: make(map[uint64]chan SatEvent),
	}
}

// GetPort returns the resolved serial port path.
func (t *DirectIMTTransport) GetPort() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.port
}

// SetExcludePort tells auto-detection to skip a port.
func (t *DirectIMTTransport) SetExcludePort(port string) {
	t.excludePort = port
}

// SetExcludePortFunc sets a dynamic port resolver for exclusion.
func (t *DirectIMTTransport) SetExcludePortFunc(fn func() string) {
	t.excludePortFn = fn
}

// SetExcludePortFuncs sets multiple dynamic port resolvers for exclusion.
func (t *DirectIMTTransport) SetExcludePortFuncs(fns []func() string) {
	if len(fns) > 0 {
		t.excludePortFn = func() string {
			for _, fn := range fns {
				if p := fn(); p != "" {
					return p
				}
			}
			return ""
		}
	}
}

// Subscribe opens the JSPR connection and starts background polling.
func (t *DirectIMTTransport) Subscribe(ctx context.Context) (<-chan SatEvent, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.connect(); err != nil {
		return nil, err
	}

	ctx, t.cancelFunc = context.WithCancel(ctx)

	ch := make(chan SatEvent, 16)
	t.eventMu.Lock()
	id := t.nextSubID
	t.nextSubID++
	t.eventSubs[id] = ch
	t.eventMu.Unlock()

	// Start background poll loop (handles MT messages and signal updates)
	t.pollDone = make(chan struct{})
	go t.pollLoop(ctx)

	// Start signal poller
	t.sigDone = make(chan struct{})
	go t.signalPoller(ctx)

	return ch, nil
}

// connect opens the serial port and initialises the JSPR session.
func (t *DirectIMTTransport) connect() error {
	if t.connected {
		return nil
	}

	portPath := t.resolvePort()
	if portPath == "" {
		return fmt.Errorf("imt: no RockBLOCK 9704 found")
	}

	file, err := openSerial(portPath, jsprBaud)
	if err != nil {
		return fmt.Errorf("imt: open %s: %w", portPath, err)
	}

	t.file = file
	t.port = portPath
	t.conn = newJSPRConn(file)

	// JSPR handshake
	if err := t.conn.jsprBegin(); err != nil {
		file.Close()
		t.file = nil
		t.conn = nil
		return fmt.Errorf("imt: JSPR begin failed: %w", err)
	}

	// Get hardware info
	info, err := t.conn.jsprGetHWInfo()
	if err != nil {
		log.Warn().Err(err).Msg("imt: failed to get hardware info")
	} else {
		t.imei = info.IMEI
		t.hwVersion = info.HWVersion
		t.serial = info.SerialNumber
		log.Info().
			Str("imei", info.IMEI).
			Str("serial", info.SerialNumber).
			Str("hw", info.HWVersion).
			Int("temp", info.BoardTemp).
			Msg("imt: RockBLOCK 9704 connected")
	}

	t.connected = true

	t.emitEvent(SatEvent{
		Type:    "connected",
		Message: fmt.Sprintf("RockBLOCK 9704 connected on %s (IMEI: %s)", portPath, t.imei),
		Time:    time.Now().UTC().Format(time.RFC3339),
	})

	return nil
}

// resolvePort determines the serial port to use.
func (t *DirectIMTTransport) resolvePort() string {
	if t.port != "" && t.port != "auto" {
		return t.port
	}
	return ProbeIMT(t.getExcludePorts())
}

// getExcludePorts returns a list of ports to exclude from auto-detection.
func (t *DirectIMTTransport) getExcludePorts() []string {
	var exclude []string
	if t.excludePort != "" {
		exclude = append(exclude, t.excludePort)
	}
	if t.excludePortFn != nil {
		if p := t.excludePortFn(); p != "" {
			exclude = append(exclude, p)
		}
	}
	return exclude
}

// Send sends a binary message via IMT (Mobile Originated).
// Maps JSPR result to SBDResult for compatibility with IridiumGateway.
func (t *DirectIMTTransport) Send(ctx context.Context, data []byte) (*SBDResult, error) {
	t.mu.Lock()
	if !t.connected {
		t.mu.Unlock()
		return nil, fmt.Errorf("imt: not connected")
	}
	conn := t.conn
	t.mu.Unlock()

	status, err := conn.jsprSendMO(jsprRawTopic, data)
	if err != nil {
		return &SBDResult{
			MOStatus:   10, // generic failure
			StatusText: err.Error(),
		}, err
	}

	result := &SBDResult{
		StatusText: status,
	}

	switch status {
	case "mo_ack_received":
		result.MOStatus = 0 // success
	case "message_expired":
		result.MOStatus = 17 // timeout
	case "network_error":
		result.MOStatus = 32 // no network
	case "message_discarded_on_overflow":
		result.MOStatus = 35 // busy
	default:
		result.MOStatus = 10 // generic failure
	}

	return result, nil
}

// SendText sends a text message via IMT.
// On IMT, text is just binary data — there's no AT+SBDWT equivalent.
func (t *DirectIMTTransport) SendText(ctx context.Context, text string) (*SBDResult, error) {
	return t.Send(ctx, []byte(text))
}

// Receive returns the next buffered MT message, or blocks briefly.
func (t *DirectIMTTransport) Receive(ctx context.Context) ([]byte, error) {
	t.mtMu.Lock()
	if len(t.mtPending) > 0 {
		msg := t.mtPending[0]
		t.mtPending = t.mtPending[1:]
		t.mtMu.Unlock()
		return msg, nil
	}
	t.mtMu.Unlock()

	// No pending MT — poll once and check again
	t.mu.Lock()
	if t.conn != nil {
		t.conn.poll()
		t.processMTAnnouncements()
	}
	t.mu.Unlock()

	t.mtMu.Lock()
	defer t.mtMu.Unlock()
	if len(t.mtPending) > 0 {
		msg := t.mtPending[0]
		t.mtPending = t.mtPending[1:]
		return msg, nil
	}
	return nil, fmt.Errorf("no MT messages available")
}

// MailboxCheck on IMT doesn't initiate a satellite session like SBDIX.
// Instead, it polls for any buffered MT messages and returns status.
func (t *DirectIMTTransport) MailboxCheck(ctx context.Context) (*SBDResult, error) {
	t.mu.Lock()
	if !t.connected || t.conn == nil {
		t.mu.Unlock()
		return &SBDResult{MOStatus: 32, StatusText: "not connected"}, nil
	}

	t.conn.poll()
	t.processMTAnnouncements()
	t.mu.Unlock()

	t.mtMu.Lock()
	pending := len(t.mtPending)
	t.mtMu.Unlock()

	result := &SBDResult{
		MOStatus:   0,
		MTQueued:   pending,
		MTReceived: pending > 0,
		StatusText: "IMT poll complete",
	}
	if pending > 0 {
		result.MTStatus = 1
	}

	return result, nil
}

// GetSignal queries the modem for constellation/signal state.
func (t *DirectIMTTransport) GetSignal(ctx context.Context) (*SignalInfo, error) {
	t.mu.Lock()
	if !t.connected || t.conn == nil {
		t.mu.Unlock()
		return &SignalInfo{Bars: 0, Assessment: "none"}, nil
	}
	conn := t.conn
	t.mu.Unlock()

	sig, err := conn.jsprGetSignal()
	if err != nil {
		return nil, err
	}

	info := &SignalInfo{
		Bars:       sig.SignalBars,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Assessment: assessSignal(sig.SignalBars),
	}

	t.signalMu.Lock()
	t.lastSignal = *info
	t.signalMu.Unlock()

	return info, nil
}

// GetSignalFast returns the cached signal reading (non-blocking, never takes serial mutex).
func (t *DirectIMTTransport) GetSignalFast(_ context.Context) (*SignalInfo, error) {
	t.signalMu.RLock()
	sig := t.lastSignal
	t.signalMu.RUnlock()

	if sig.Timestamp == "" {
		// No cached value yet — return zero rather than blocking on serial
		return &SignalInfo{Bars: 0, Assessment: "none"}, nil
	}
	return &sig, nil
}

// GetStatus returns the modem connection status (non-blocking, cached values).
func (t *DirectIMTTransport) GetStatus(_ context.Context) (*SatStatus, error) {
	// Read cached fields without taking the serial mutex — these are set
	// during connect and never written concurrently with reads.
	return &SatStatus{
		Connected: t.connected,
		Port:      t.port,
		IMEI:      t.imei,
		Model:     "RockBLOCK 9704",
	}, nil
}

// GetGeolocation is not supported on the 9704 (no AT-MSGEO equivalent).
// Returns a stub. The 9704 has a u-blox GPS — use GPSReader instead.
func (t *DirectIMTTransport) GetGeolocation(ctx context.Context) (*GeolocationInfo, error) {
	return nil, fmt.Errorf("imt: geolocation not available (use GPS reader for u-blox 7)")
}

// MOBufferEmpty always returns true for IMT — there's no persistent MO buffer.
// Messages are queued and transmitted immediately.
func (t *DirectIMTTransport) MOBufferEmpty(ctx context.Context) (bool, error) {
	return true, nil
}

// Close stops background goroutines and closes the serial port.
func (t *DirectIMTTransport) Close() error {
	if t.cancelFunc != nil {
		t.cancelFunc()
	}
	if t.pollDone != nil {
		<-t.pollDone
	}
	if t.sigDone != nil {
		<-t.sigDone
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.file != nil {
		t.file.Close()
		t.file = nil
	}
	t.connected = false
	t.conn = nil

	// Close event channels
	t.eventMu.Lock()
	for id, ch := range t.eventSubs {
		close(ch)
		delete(t.eventSubs, id)
	}
	t.eventMu.Unlock()

	log.Info().Msg("imt: transport closed")
	return nil
}

// ============================================================================
// Background goroutines
// ============================================================================

// pollLoop continuously polls the modem for unsolicited messages (MT, signal changes).
func (t *DirectIMTTransport) pollLoop(ctx context.Context) {
	defer close(t.pollDone)

	ticker := time.NewTicker(200 * time.Millisecond) // poll every 200ms
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.mu.Lock()
			if t.conn == nil {
				t.mu.Unlock()
				continue
			}
			t.conn.poll()

			// Process signal updates
			sigMsgs := t.conn.takeUnsolicited("constellationState")
			for _, msg := range sigMsgs {
				var sig jsprConstellationState
				if err := json.Unmarshal(msg.JSON, &sig); err == nil {
					info := SignalInfo{
						Bars:       sig.SignalBars,
						Timestamp:  time.Now().UTC().Format(time.RFC3339),
						Assessment: assessSignal(sig.SignalBars),
					}
					t.signalMu.Lock()
					t.lastSignal = info
					t.signalMu.Unlock()

					t.emitEvent(SatEvent{
						Type:    "signal",
						Message: fmt.Sprintf("Signal: %d/5", sig.SignalBars),
						Signal:  sig.SignalBars,
						Time:    time.Now().UTC().Format(time.RFC3339),
					})
				}
			}

			// Process MT message announcements
			t.processMTAnnouncements()

			t.mu.Unlock()
		}
	}
}

// processMTAnnouncements handles any buffered messageTerminate announcements.
// Must be called with t.mu held.
func (t *DirectIMTTransport) processMTAnnouncements() {
	if t.conn == nil {
		return
	}

	announcements := t.conn.takeUnsolicited("messageTerminate")
	for _, ann := range announcements {
		var mt jsprMTAnnounce
		if err := json.Unmarshal(ann.JSON, &mt); err != nil {
			log.Warn().Err(err).Msg("imt: failed to parse MT announcement")
			continue
		}

		log.Info().Int("message_id", mt.MessageID).Int("topic", mt.TopicID).Int("max_len", mt.MessageLengthMax).Msg("imt: MT message announced")

		// Receive the full message (handles segment collection)
		payload, topicID, err := t.conn.jsprReceiveMT(mt)
		if err != nil {
			log.Error().Err(err).Int("message_id", mt.MessageID).Msg("imt: MT receive failed")
			continue
		}

		log.Info().Int("topic", topicID).Int("size", len(payload)).Msg("imt: MT message received")

		t.mtMu.Lock()
		t.mtPending = append(t.mtPending, payload)
		t.mtMu.Unlock()

		t.emitEvent(SatEvent{
			Type:    "mt_received",
			Message: fmt.Sprintf("MT message received (%d bytes, topic %d)", len(payload), topicID),
			Time:    time.Now().UTC().Format(time.RFC3339),
		})
	}
}

// signalPoller periodically queries signal strength.
func (t *DirectIMTTransport) signalPoller(ctx context.Context) {
	defer close(t.sigDone)

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := t.GetSignal(ctx); err != nil {
				log.Debug().Err(err).Msg("imt: signal poll failed")
			}
		}
	}
}

// emitEvent sends an event to all subscribers.
func (t *DirectIMTTransport) emitEvent(ev SatEvent) {
	t.eventMu.RLock()
	defer t.eventMu.RUnlock()
	for _, ch := range t.eventSubs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// assessSignal maps signal bars (0-5) to a human-readable assessment.
func assessSignal(bars int) string {
	switch {
	case bars == 0:
		return "none"
	case bars <= 1:
		return "poor"
	case bars <= 2:
		return "fair"
	case bars <= 3:
		return "good"
	default:
		return "excellent"
	}
}
