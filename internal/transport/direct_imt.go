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
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/sys/unix"
)

// DirectIMTTransport implements SatTransport via JSPR for the RockBLOCK 9704.
type DirectIMTTransport struct {
	port string // "/dev/ttyUSB0" or "auto"

	mu        sync.Mutex
	connectMu sync.Mutex // separate from mu — protects connect() without blocking status reads
	conn      *jsprConn
	file      jsprPort
	connected bool
	imei      string
	hwVersion string
	serial    string

	// Signal state
	signalMu   sync.RWMutex
	lastSignal SignalInfo

	// MT message buffers — classified at L4 receive boundary [MESHSAT-447]
	mtMu               sync.Mutex
	mtReticulumPending [][]byte // Reticulum packets → sat_interface.Receive()
	mtMessagePending   [][]byte // app payloads → imt_gateway → messages DB → dashboard

	// SSE subscribers
	eventMu   sync.RWMutex
	eventSubs map[uint64]chan SatEvent
	nextSubID uint64

	// Exclude ports (avoid probing ports already claimed by other transports)
	excludePort   string
	excludePortFn func() string

	cancelFunc   context.CancelFunc
	pollDone     chan struct{}
	sigDone      chan struct{}
	watchdogDone chan struct{}
	usbResetDone bool // prevents recursive USB reset in connect()
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

	// Start watchdog — monitors serial reads and cycles port when URBs die
	t.watchdogDone = make(chan struct{})
	go t.serialWatchdog(ctx)

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

	// Use the jspr-helper C binary for serial I/O. Go's runtime interferes
	// with serial syscalls (select/read) on USB serial devices — the C helper
	// handles all serial I/O and communicates via stdin/stdout JSON lines.
	helperPath := findJSPRHelper()
	if helperPath == "" {
		return fmt.Errorf("imt: jspr-helper binary not found (checked /usr/local/bin, /usr/bin, ./build)")
	}

	helper, err := startJSPRHelper(helperPath, portPath, jsprBaud)
	if err != nil {
		return fmt.Errorf("imt: start helper: %w", err)
	}

	t.file = helper
	t.port = portPath
	t.conn = newJSPRConn(helper)

	// The C helper drains stale data on startup. Start the Go reader goroutine
	// which reads from the helper's buffered output (not from serial directly).
	t.conn.startReader()

	// JSPR handshake (uses async sendRequest via reader goroutine).
	// Retry without killing the helper — on ARM64, closing and reopening
	// the FTDI serial port corrupts USB URBs in the host controller.
	// Keeping the helper (and serial port) alive avoids this.
	var beginErr error
	for attempt := 1; attempt <= 3; attempt++ {
		beginErr = t.conn.jsprBegin()
		if beginErr == nil {
			break
		}
		log.Warn().Err(beginErr).Int("attempt", attempt).
			Msg("imt: JSPR handshake failed, retrying with same helper")
		if attempt < 3 {
			time.Sleep(2 * time.Second)
		}
	}
	if beginErr != nil {
		t.conn.stopReader()
		helper.Close()
		t.file = nil
		t.conn = nil

		// The modem gets into a hung state after repeated failed serial
		// sessions. A USB device reset (unbind/bind) recovers it.
		if !t.usbResetDone && usbResetIMTDevice(portPath) {
			t.usbResetDone = true // prevent infinite recursion
			log.Info().Msg("imt: USB reset succeeded, retrying connect")
			time.Sleep(2 * time.Second)
			err := t.connect()
			t.usbResetDone = false // allow future resets
			return err
		}

		return fmt.Errorf("imt: JSPR begin failed after 3 attempts: %w", beginErr)
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

// IMT-native result codes (100+ range, distinct from SBD mo_status codes).
// This prevents SBD-specific DLQ backoff logic from triggering on IMT results.
const (
	IMTStatusSuccess     = 0   // mo_ack_received — same as SBD success
	IMTStatusExpired     = 100 // message_expired — satellite session timed out
	IMTStatusNoNetwork   = 101 // network_error — no Certus connectivity
	IMTStatusOverflow    = 102 // message_discarded_on_overflow — queue full
	IMTStatusGenericFail = 103 // unknown/generic failure
)

// Send sends a binary message via IMT (Mobile Originated).
// Returns native JSPR result codes (not synthetic SBD mapping).
func (t *DirectIMTTransport) Send(ctx context.Context, data []byte) (*SatResult, error) {
	t.mu.Lock()
	if !t.connected {
		t.mu.Unlock()
		return nil, fmt.Errorf("imt: not connected")
	}
	conn := t.conn
	t.mu.Unlock()

	status, err := conn.jsprSendMO(jsprRawTopic, data)
	if err != nil {
		return &SatResult{
			MOStatus:   IMTStatusGenericFail,
			StatusText: err.Error(),
		}, err
	}

	result := &SatResult{
		StatusText: status,
	}

	switch status {
	case "mo_ack_received":
		result.MOStatus = IMTStatusSuccess
	case "message_expired":
		result.MOStatus = IMTStatusExpired
	case "network_error":
		result.MOStatus = IMTStatusNoNetwork
	case "message_discarded_on_overflow":
		result.MOStatus = IMTStatusOverflow
	default:
		result.MOStatus = IMTStatusGenericFail
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
// Receive returns the next pending Reticulum MT packet (for sat_interface L6). [MESHSAT-447]
func (t *DirectIMTTransport) Receive(ctx context.Context) ([]byte, error) {
	t.mtMu.Lock()
	if len(t.mtReticulumPending) > 0 {
		msg := t.mtReticulumPending[0]
		t.mtReticulumPending = t.mtReticulumPending[1:]
		t.mtMu.Unlock()
		return msg, nil
	}
	t.mtMu.Unlock()

	// No pending — process any announcements and check again
	t.processMTAnnouncements()

	t.mu.Lock()
	conn := t.conn
	t.mu.Unlock()
	if conn != nil {
		conn.waitForUnsolicited(100 * time.Millisecond)
	}

	t.mtMu.Lock()
	defer t.mtMu.Unlock()
	if len(t.mtReticulumPending) > 0 {
		msg := t.mtReticulumPending[0]
		t.mtReticulumPending = t.mtReticulumPending[1:]
		return msg, nil
	}
	return nil, fmt.Errorf("no MT messages available")
}

// ReceiveMessage returns the next pending app-level MT message (for gateway L5 → dashboard L7). [MESHSAT-447]
func (t *DirectIMTTransport) ReceiveMessage() ([]byte, error) {
	t.mtMu.Lock()
	defer t.mtMu.Unlock()
	if len(t.mtMessagePending) > 0 {
		msg := t.mtMessagePending[0]
		t.mtMessagePending = t.mtMessagePending[1:]
		return msg, nil
	}
	return nil, nil
}

// MailboxCheck on IMT doesn't initiate a satellite session like SBDIX.
// Instead, it polls for any buffered MT messages and returns status.
func (t *DirectIMTTransport) MailboxCheck(ctx context.Context) (*SatResult, error) {
	t.mu.Lock()
	if !t.connected || t.conn == nil {
		t.mu.Unlock()
		return &SatResult{MOStatus: IMTStatusNoNetwork, StatusText: "not connected"}, nil
	}
	t.mu.Unlock()

	// Reader goroutine handles serial reads; just process any buffered announcements
	t.processMTAnnouncements()

	t.mtMu.Lock()
	pending := len(t.mtReticulumPending) + len(t.mtMessagePending)
	firstLen := 0
	if len(t.mtReticulumPending) > 0 {
		firstLen = len(t.mtReticulumPending[0])
	} else if len(t.mtMessagePending) > 0 {
		firstLen = len(t.mtMessagePending[0])
	}
	t.mtMu.Unlock()

	result := &SatResult{
		MOStatus:   0,
		MTQueued:   pending,
		MTReceived: pending > 0,
		StatusText: "IMT poll complete",
	}
	if pending > 0 {
		result.MTStatus = 1
		// Set MTLength so handleRingAlertWithRetry proceeds to Receive().
		// On SBD this is the actual MT size from SBDIX; on IMT we report
		// the first pending message length as a non-zero indicator.
		result.MTLength = firstLen
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
		Source:     "imt",
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
		return &SignalInfo{Bars: 0, Assessment: "none", Source: "imt"}, nil
	}
	sig.Source = "imt"
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
		Type:      "imt",
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
	if t.watchdogDone != nil {
		<-t.watchdogDone
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
	if len(announcements) > 0 {
		log.Info().Int("count", len(announcements)).Msg("imt: processMTAnnouncements found MT messages")
	}
	for _, ann := range announcements {
		var mt jsprMTAnnounce
		if err := json.Unmarshal(ann.JSON, &mt); err != nil {
			log.Warn().Err(err).Msg("imt: failed to parse MT announcement")
			continue
		}

		log.Info().Int("message_id", mt.MessageID).Int("topic", mt.TopicID).Int("max_len", mt.MessageLengthMax).Msg("imt: MT message announced")

		// Receive the full message in a goroutine so it doesn't block pollLoop.
		// L4 classification: inspect header to route to the correct L5/L6 consumer. [MESHSAT-447]
		go func(conn *jsprConn, mt jsprMTAnnounce) {
			payload, topicID, err := conn.jsprReceiveMT(mt)
			if err != nil {
				log.Error().Err(err).Int("message_id", mt.MessageID).Msg("imt: MT receive failed")
				return
			}

			log.Info().Int("topic", topicID).Int("size", len(payload)).Msg("imt: MT message received")

			// Classify at L4 receive boundary — three possible L5/L6 consumers
			t.mtMu.Lock()
			if isReticulumPacket(payload) {
				t.mtReticulumPending = append(t.mtReticulumPending, payload)
				t.mtMu.Unlock()
				log.Info().Int("size", len(payload)).Msg("imt: MT classified as Reticulum packet")
				t.emitEvent(SatEvent{
					Type:    "mt_received",
					Message: fmt.Sprintf("MT Reticulum packet (%d bytes, topic %d)", len(payload), topicID),
					Time:    time.Now().UTC().Format(time.RFC3339),
				})
			} else {
				t.mtMessagePending = append(t.mtMessagePending, payload)
				t.mtMu.Unlock()
				log.Info().Int("size", len(payload)).Str("text", string(payload)).Msg("imt: MT classified as app message")
				t.emitEvent(SatEvent{
					Type:    "mt_message",
					Message: fmt.Sprintf("MT message received (%d bytes, topic %d)", len(payload), topicID),
					Time:    time.Now().UTC().Format(time.RFC3339),
				})
			}
		}(conn, mt)
	}
}

// isReticulumPacket checks if the payload is a valid Reticulum packet by
// attempting header unmarshal. Simple bit-pattern checks are too permissive
// (ASCII text starting with uppercase letters matches Reticulum Type 1).
// Used at L4 to classify MT payloads before queuing. [MESHSAT-447]
func isReticulumPacket(data []byte) bool {
	if len(data) < 19 {
		return false
	}
	// Reticulum Type 1: flags(1) + hops(1) + dest(16) + context(1) = 19 bytes min
	// Reticulum Type 2: flags(1) + hops(1) + transport(16) + dest(16) + context(1) = 35 bytes min
	flags := data[0]
	headerType := (flags >> 6) & 0x01
	packetType := flags & 0x03
	destType := (flags >> 2) & 0x03

	// PacketType must be 0-3, DestType must be 0-2
	if packetType > 3 || destType > 2 {
		return false
	}

	// Type 2 requires at least 35 bytes
	if headerType == 1 && len(data) < 35 {
		return false
	}

	// Hops byte (data[1]) should be 0-128 (7-bit hop count)
	if data[1] > 128 {
		return false
	}

	// Final heuristic: pure ASCII text (all bytes 0x20-0x7E) is NOT a Reticulum packet
	allPrintable := true
	for _, b := range data[:min(len(data), 19)] {
		if b < 0x20 || b > 0x7E {
			allPrintable = false
			break
		}
	}
	if allPrintable {
		return false // plaintext message, not binary Reticulum
	}

	return true
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// signalPoller actively queries the modem for signal strength every 30 seconds,
// matching the official RockBLOCK 9704 C library's rbGetSignal() pattern which
// sends "GET constellationState {}" and waits for the response.
//
// Previously (MESHSAT-275) this was changed to passive-only (unsolicited 299),
// but that was caused by the OLD synchronous JSPR code blocking the pollLoop.
// With the async reader goroutine, active GET queries work correctly.
//
// Signal is also updated by unsolicited 299 constellationState messages
// processed by pollLoop — both sources feed lastSignal.
func (t *DirectIMTTransport) signalPoller(ctx context.Context) {
	defer close(t.sigDone)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.mu.Lock()
			conn := t.conn
			connected := t.connected
			t.mu.Unlock()

			if !connected || conn == nil {
				continue
			}

			// Active signal query — matches official C library rbGetSignal()
			sig, err := conn.jsprGetSignal()
			if err != nil {
				log.Debug().Err(err).Msg("imt: signal poll failed (active GET)")
				// Fall back: if no recent reading, record 0 bars
				t.signalMu.RLock()
				existing := t.lastSignal
				t.signalMu.RUnlock()
				now := time.Now().UTC()
				if existing.Timestamp != "" {
					if ts, parseErr := time.Parse(time.RFC3339, existing.Timestamp); parseErr == nil {
						if now.Sub(ts) <= 5*time.Minute {
							continue // recent unsolicited reading still valid
						}
					}
				}
				info := SignalInfo{Bars: 0, Timestamp: now.Format(time.RFC3339), Assessment: "none"}
				t.signalMu.Lock()
				t.lastSignal = info
				t.signalMu.Unlock()
				continue
			}

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

// serialWatchdog monitors the serial port for URB death and cycles the
// connection when reads stall. On ARM Linux with ftdi_sio, the USB host
// controller can permanently kill read URBs (-EPIPE / urb stopped: -32).
// The only recovery is closing and reopening the serial port, which forces
// the kernel to cancel stale URBs and submit fresh ones.
//
// The watchdog checks the rawSerialPort's lastRead timestamp every 60 seconds.
// If no data has been read for 2 minutes, it cycles the port.
func (t *DirectIMTTransport) serialWatchdog(ctx context.Context) {
	defer close(t.watchdogDone)

	const (
		checkInterval = 60 * time.Second
		staleTimeout  = 2 * time.Minute
	)

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.mu.Lock()
			if !t.connected || t.file == nil {
				t.mu.Unlock()
				continue
			}
			file := t.file
			t.mu.Unlock()

			lr, ok := file.(interface{ LastRead() time.Time })
			if !ok {
				continue
			}

			lastRead := lr.LastRead()
			stale := time.Since(lastRead)
			if stale < staleTimeout {
				continue
			}

			portPath := ""
			if pp, ok := file.(interface{ Path() string }); ok {
				portPath = pp.Path()
			}

			log.Warn().
				Dur("stale", stale).
				Str("port", portPath).
				Msg("imt: serial watchdog — no data received, cycling port")

			t.emitEvent(SatEvent{
				Type:    "watchdog",
				Message: fmt.Sprintf("Serial watchdog cycling port (no data for %s)", stale.Truncate(time.Second)),
				Time:    time.Now().UTC().Format(time.RFC3339),
			})

			if err := t.reconnect(); err != nil {
				log.Error().Err(err).Msg("imt: serial watchdog reconnect failed")
			} else {
				log.Info().Msg("imt: serial watchdog reconnected successfully")
			}
		}
	}
}

// reconnect closes the current serial connection and opens a fresh one.
// This forces the kernel to cancel dead URBs and submit new ones.
func (t *DirectIMTTransport) reconnect() error {
	t.connectMu.Lock()
	defer t.connectMu.Unlock()

	t.mu.Lock()
	// Stop the reader goroutine
	if t.conn != nil {
		t.conn.stopReader()
	}
	// Close the serial port — cancels all URBs in the kernel
	if t.file != nil {
		t.file.Close()
	}
	t.file = nil
	t.conn = nil
	t.connected = false
	t.mu.Unlock()

	// Brief pause for the kernel to clean up USB state
	time.Sleep(500 * time.Millisecond)

	// Reconnect
	if err := t.connect(); err != nil {
		return fmt.Errorf("reconnect: %w", err)
	}

	return nil
}

// usbResetIMTDevice resets the USB device behind the given serial port using
// the USBDEVFS_RESET ioctl. The RockBLOCK 9704 (FTDI FT234XD) gets into a
// hung state after repeated failed serial sessions — the firmware stops
// responding to JSPR commands. A USB reset forces full re-enumeration.
//
// Uses the ioctl approach (works inside Docker containers) rather than sysfs
// unbind/bind (which requires writable sysfs, blocked inside containers).
//
// Returns true if the reset was performed successfully.
func usbResetIMTDevice(portPath string) bool {
	// portPath is e.g. "/dev/ttyUSB0" — extract "ttyUSB0"
	devName := portPath
	for i := len(devName) - 1; i >= 0; i-- {
		if devName[i] == '/' {
			devName = devName[i+1:]
			break
		}
	}

	// Find the USB bus/device numbers from sysfs.
	// /sys/class/tty/ttyUSB0/device/../../ → USB device dir with busnum/devnum
	out, err := exec.Command("sh", "-c", fmt.Sprintf(
		`DEV=$(readlink -f /sys/class/tty/%s/device/../../) && `+
			`cat "$DEV/busnum" && cat "$DEV/devnum"`, devName)).CombinedOutput()
	if err != nil {
		log.Debug().Err(err).Str("output", string(out)).Msg("imt: usb reset — can't resolve bus/dev numbers")
		return false
	}

	// Parse busnum and devnum
	var busNum, devNum int
	if _, err := fmt.Sscanf(string(out), "%d\n%d", &busNum, &devNum); err != nil {
		log.Debug().Err(err).Str("output", string(out)).Msg("imt: usb reset — can't parse bus/dev")
		return false
	}

	// Open /dev/bus/usb/BBB/DDD and issue USBDEVFS_RESET ioctl
	usbDevPath := fmt.Sprintf("/dev/bus/usb/%03d/%03d", busNum, devNum)
	fd, err := unix.Open(usbDevPath, unix.O_WRONLY, 0)
	if err != nil {
		log.Warn().Err(err).Str("path", usbDevPath).Msg("imt: USB reset — can't open USB device")
		return false
	}
	defer unix.Close(fd)

	// USBDEVFS_RESET = _IO('U', 20) = 0x5514
	const usbdevfsReset = 0x5514
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), usbdevfsReset, 0); errno != 0 {
		log.Warn().Int("errno", int(errno)).Str("path", usbDevPath).Msg("imt: USB reset ioctl failed")
		return false
	}

	log.Info().Str("port", portPath).Str("usb", usbDevPath).Msg("imt: USB device reset completed")
	return true
}

// findJSPRHelper locates the jspr-helper executable.
// Prefers the Python script (reliable serial I/O via pyserial) over the C binary.
func findJSPRHelper() string {
	// Python script — preferred
	pyCandidates := []string{
		"/usr/local/bin/jspr_helper.py",
		"/usr/bin/jspr_helper.py",
		"./cmd/jspr-helper/jspr_helper.py",
	}
	for _, p := range pyCandidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	// C binary — fallback
	cCandidates := []string{
		"/usr/local/bin/jspr-helper",
		"/usr/bin/jspr-helper",
		"./build/jspr-helper",
		"./jspr-helper",
	}
	for _, p := range cCandidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// Check PATH
	if p, err := exec.LookPath("jspr-helper"); err == nil {
		return p
	}
	return ""
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
