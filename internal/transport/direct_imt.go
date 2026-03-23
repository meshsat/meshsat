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
	connectMu sync.Mutex // separate from mu — protects connect() without blocking status reads
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

// SetPort sets the serial port path. Called by DeviceSupervisor.
func (t *DirectIMTTransport) SetPort(port string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.port = port
}

// IsConnected returns true if the transport has an active serial connection.
func (t *DirectIMTTransport) IsConnected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connected
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
	// Connect without holding the mutex — jsprBegin() does serial I/O that can
	// take 30+ seconds on timeout. Holding mu would block GetStatus/GetSignalFast.
	if err := t.connectOnce(); err != nil {
		return nil, fmt.Errorf("subscribe: %w", err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

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

// connectOnce ensures a single connect attempt at a time without blocking status reads.
func (t *DirectIMTTransport) connectOnce() error {
	t.connectMu.Lock()
	defer t.connectMu.Unlock()
	return t.connect()
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

	// Drain stale serial data, then start the async reader goroutine
	t.conn.drainSerial()
	t.conn.startReader()

	// JSPR handshake (uses async sendRequest via reader goroutine)
	if err := t.conn.jsprBegin(); err != nil {
		t.conn.stopReader()
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
	if t.port == "supervisor" {
		return "" // wait for device supervisor to assign port
	}
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

// CheckProvisioning queries the modem for its provisioned topics.
// Returns the topic list; empty means not yet provisioned.
func (t *DirectIMTTransport) CheckProvisioning() ([]ProvisioningTopic, error) {
	t.mu.Lock()
	if !t.connected || t.conn == nil {
		t.mu.Unlock()
		return nil, fmt.Errorf("imt: not connected")
	}
	conn := t.conn
	t.mu.Unlock()

	topics, err := conn.jsprCheckProvisioning()
	if err != nil {
		return nil, err
	}

	result := make([]ProvisioningTopic, len(topics))
	for i, t := range topics {
		result[i] = ProvisioningTopic{
			TopicID:   t.TopicID,
			TopicName: t.TopicName,
			Priority:  t.Priority,
		}
	}
	return result, nil
}

// ProvisioningTopic represents a provisioned topic on the 9704 modem.
type ProvisioningTopic struct {
	TopicID   int    `json:"topic_id"`
	TopicName string `json:"topic_name"`
	Priority  string `json:"priority"`
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

	// No pending MT — process any announcements and check again
	t.processMTAnnouncements()

	// Brief wait for reader goroutine to deliver new unsolicited messages
	t.mu.Lock()
	conn := t.conn
	t.mu.Unlock()
	if conn != nil {
		conn.waitForUnsolicited(100 * time.Millisecond)
	}

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
	t.mu.Unlock()

	// Reader goroutine handles serial reads; just process any buffered announcements
	t.processMTAnnouncements()

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

// GetSystemTime is not available on IMT (9704 uses JSPR, not AT commands).
func (t *DirectIMTTransport) GetSystemTime(ctx context.Context) (*IridiumTime, error) {
	return nil, fmt.Errorf("imt: system time not available (JSPR protocol)")
}

// GetFirmwareVersion returns the cached hardware version from JSPR status.
func (t *DirectIMTTransport) GetFirmwareVersion(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.hwVersion, nil
}

// Sleep is not supported on IMT — the 9704 has no sleep pin interface.
func (t *DirectIMTTransport) Sleep(ctx context.Context) error {
	return fmt.Errorf("imt: sleep not supported")
}

// Wake is not supported on IMT.
func (t *DirectIMTTransport) Wake(ctx context.Context) error {
	return nil // always awake
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

	// Stop the reader goroutine before closing the serial port
	if t.conn != nil {
		t.conn.stopReader()
	}

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

// pollLoop processes unsolicited messages buffered by the reader goroutine.
// The reader goroutine handles all serial reads at full speed; this loop
// only consumes the unsolicited buffer for signal updates and MT announcements.
func (t *DirectIMTTransport) pollLoop(ctx context.Context) {
	defer close(t.pollDone)

	ticker := time.NewTicker(50 * time.Millisecond)
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
			conn := t.conn
			t.mu.Unlock()

			// Process signal updates from unsolicited buffer
			sigMsgs := conn.takeUnsolicited("constellationState")
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

			// Process MT message announcements (in a goroutine so jsprReceiveMT
			// doesn't block the poll loop)
			t.processMTAnnouncements()
		}
	}
}

// processMTAnnouncements handles any buffered messageTerminate announcements.
// Each MT receive runs in a separate goroutine so it doesn't block the poll loop.
func (t *DirectIMTTransport) processMTAnnouncements() {
	t.mu.Lock()
	conn := t.conn
	t.mu.Unlock()
	if conn == nil {
		return
	}

	announcements := conn.takeUnsolicited("messageTerminate")
	for _, ann := range announcements {
		var mt jsprMTAnnounce
		if err := json.Unmarshal(ann.JSON, &mt); err != nil {
			log.Warn().Err(err).Msg("imt: failed to parse MT announcement")
			continue
		}

		log.Info().Int("message_id", mt.MessageID).Int("topic", mt.TopicID).Int("max_len", mt.MessageLengthMax).Msg("imt: MT message announced")

		// Receive the full message in a goroutine so it doesn't block pollLoop
		go func(conn *jsprConn, mt jsprMTAnnounce) {
			payload, topicID, err := conn.jsprReceiveMT(mt)
			if err != nil {
				log.Error().Err(err).Int("message_id", mt.MessageID).Msg("imt: MT receive failed")
				return
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
		}(conn, mt)
	}
}

// signalPoller ensures the signal recorder always has a timestamped reading.
// The primary signal source is unsolicited 299 constellationState messages
// processed by pollLoop (per the official RockBLOCK 9704 C library design).
// This goroutine does NOT send active GET queries — those consistently time out
// (MESHSAT-275) because the modem only responds to GET constellationState when
// a satellite happens to be visible at query time (1s official timeout).
// Instead, this goroutine stamps the cached reading so the signal recorder
// picks it up, and only records 0 bars if no unsolicited update has arrived
// within 5 minutes (indicating the modem has no satellite visibility).
func (t *DirectIMTTransport) signalPoller(ctx context.Context) {
	defer close(t.sigDone)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.signalMu.RLock()
			existing := t.lastSignal
			t.signalMu.RUnlock()

			now := time.Now().UTC()

			if existing.Timestamp != "" {
				if ts, err := time.Parse(time.RFC3339, existing.Timestamp); err == nil {
					if now.Sub(ts) <= 5*time.Minute {
						// Recent unsolicited reading exists — re-stamp it so the
						// signal recorder sees a fresh timestamp and records it.
						refreshed := SignalInfo{
							Bars:       existing.Bars,
							Timestamp:  now.Format(time.RFC3339),
							Assessment: existing.Assessment,
						}
						t.signalMu.Lock()
						t.lastSignal = refreshed
						t.signalMu.Unlock()
						continue
					}
				}
			}

			// No recent unsolicited update — record 0 bars.
			info := SignalInfo{Bars: 0, Timestamp: now.Format(time.RFC3339), Assessment: "none"}
			t.signalMu.Lock()
			t.lastSignal = info
			t.signalMu.Unlock()
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
