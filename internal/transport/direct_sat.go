package transport

// DirectSatTransport implements SatTransport with direct serial access to an Iridium modem.
// Ported from HAL's IridiumDriver — no HAL dependency, talks to USB modem directly.

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	iridiumBaud         = 19200
	iridiumReadTimeout  = 3 * time.Second
	iridiumSBDIXTimeout = 90 * time.Second
	minSBDIXInterval    = 10 * time.Second
	signalPollInterval  = 60 * time.Second
)

// DirectSatTransport implements SatTransport via direct serial access to an Iridium modem.
type DirectSatTransport struct {
	port string // "/dev/ttyUSB1" or "auto"

	mu        sync.Mutex
	file      *os.File
	connected bool
	imei      string
	model     string

	// SBDIX rate limiting
	lastSBDIX   time.Time
	lastGSSSync time.Time // last successful SBDIX that reached the GSS (for MT discovery)

	// Signal state
	signalMu   sync.RWMutex
	lastSignal SignalInfo

	// Ring alert
	ringCh     chan struct{}
	stopRingCh chan struct{}
	ringDone   chan struct{}
	ringActive bool

	// Signal poller
	stopSignalCh chan struct{}
	signalDone   chan struct{}

	// SSE subscribers
	eventMu   sync.RWMutex
	eventSubs map[uint64]chan SatEvent
	nextSubID uint64

	cancelFunc context.CancelFunc

	// Exclude port (if Meshtastic already claimed it)
	excludePort   string
	excludePortFn func() string // dynamic resolver (takes precedence over static)
}

// NewDirectSatTransport creates a new direct serial Iridium transport.
// Pass "auto" or "" for port to use auto-detection.
func NewDirectSatTransport(port string) *DirectSatTransport {
	return &DirectSatTransport{
		port:      port,
		ringCh:    make(chan struct{}, 1),
		eventSubs: make(map[uint64]chan SatEvent),
	}
}

// GetPort returns the resolved serial port path.
// Returns "auto" or "" if not yet connected.
func (t *DirectSatTransport) GetPort() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.port
}

// SetExcludePort tells auto-detection to skip a port (e.g., Meshtastic's port).
func (t *DirectSatTransport) SetExcludePort(port string) {
	t.excludePort = port
}

// SetExcludePortFunc sets a dynamic port resolver for exclusion.
// Called at auto-detect time to get the current port of another transport.
func (t *DirectSatTransport) SetExcludePortFunc(fn func() string) {
	t.excludePortFn = fn
}

// Subscribe opens the serial connection and starts ring alert + signal monitoring.
func (t *DirectSatTransport) Subscribe(ctx context.Context) (<-chan SatEvent, error) {
	t.mu.Lock()
	if !t.connected {
		if err := t.connectLocked(ctx); err != nil {
			t.mu.Unlock()
			return nil, fmt.Errorf("connect: %w", err)
		}
	}
	t.mu.Unlock()

	ch := make(chan SatEvent, 32)
	t.eventMu.Lock()
	id := t.nextSubID
	t.nextSubID++
	t.eventSubs[id] = ch
	t.eventMu.Unlock()

	go func() {
		<-ctx.Done()
		t.eventMu.Lock()
		if _, exists := t.eventSubs[id]; exists {
			delete(t.eventSubs, id)
			close(ch)
		}
		t.eventMu.Unlock()
	}()

	return ch, nil
}

func (t *DirectSatTransport) connectLocked(ctx context.Context) error {
	port := t.port
	if port == "" || port == "auto" {
		exclude := t.excludePort
		if t.excludePortFn != nil {
			if resolved := t.excludePortFn(); resolved != "" && resolved != "auto" {
				exclude = resolved
			}
		}
		port = autoDetectIridium(exclude)
		if port == "" {
			return fmt.Errorf("no Iridium modem found")
		}
	}

	file, err := openSerial(port, iridiumBaud)
	if err != nil {
		return err
	}

	t.file = file
	t.port = port

	// Drain any stale data from the serial buffer before first command
	drainPort(file)

	// Initialize modem
	// AT&K0 — disable flow control
	if _, err := sendAT(file, "AT&K0", iridiumReadTimeout); err != nil {
		file.Close()
		return fmt.Errorf("AT&K0 failed: %w", err)
	}
	// ATE0 — disable command echo (reduces response parsing noise)
	sendAT(file, "ATE0", iridiumReadTimeout)
	// AT&D0 — ignore DTR
	sendAT(file, "AT&D0", iridiumReadTimeout)
	// AT — basic check
	resp, err := sendAT(file, "AT", iridiumReadTimeout)
	if err != nil || !strings.Contains(resp, "OK") {
		file.Close()
		return fmt.Errorf("AT check failed")
	}
	// AT+CGSN — get IMEI
	resp, err = sendAT(file, "AT+CGSN", iridiumReadTimeout)
	if err == nil {
		t.imei = parseATValue(resp)
	}
	// AT+CGMM — get model
	resp, err = sendAT(file, "AT+CGMM", iridiumReadTimeout)
	if err == nil {
		t.model = parseATValue(resp)
	}
	// AT+SBDMTA=1 — enable ring alert indications
	sendAT(file, "AT+SBDMTA=1", iridiumReadTimeout)
	// Clear MO and MT buffers — prevents stale data from previous sessions
	// triggering endless SBDIX resend loops
	sendAT(file, "AT+SBDD0", iridiumReadTimeout) // clear MO
	sendAT(file, "AT+SBDD1", iridiumReadTimeout) // clear MT

	t.connected = true
	t.lastSBDIX = time.Now() // prevent stale SBDIX from firing immediately after connect
	log.Info().Str("port", port).Str("imei", t.imei).Str("model", t.model).Msg("iridium modem connected")

	t.emitEvent(SatEvent{
		Type:    "connected",
		Message: fmt.Sprintf("Connected to %s (IMEI: %s)", t.model, t.imei),
		Time:    time.Now().UTC().Format(time.RFC3339),
	})

	// Start ring alert monitor
	t.startMonitor()

	return nil
}

// ============================================================================
// Ring Alert Monitor + Signal Poller
// ============================================================================

func (t *DirectSatTransport) startMonitor() {
	if t.ringActive {
		return
	}
	t.stopRingCh = make(chan struct{})
	t.ringDone = make(chan struct{})
	t.ringActive = true
	go t.monitorLoop()

	t.stopSignalCh = make(chan struct{})
	t.signalDone = make(chan struct{})
	go t.signalPollerLoop()
}

// stopMonitor stops the ring monitor and signal poller.
// Caller must hold t.mu. Temporarily releases it so goroutines can exit.
// Matches HAL's stopMonitorLocked pattern with non-blocking done checks.
func (t *DirectSatTransport) stopMonitor() {
	if !t.ringActive {
		return
	}
	t.ringActive = false

	// Stop signal poller first (doesn't hold serial port)
	if t.stopSignalCh != nil {
		select {
		case <-t.signalDone:
			// already exited
		default:
			close(t.stopSignalCh)
			done := t.signalDone
			t.mu.Unlock()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				log.Warn().Msg("iridium: signal poller did not exit in time")
			}
			t.mu.Lock()
		}
		t.stopSignalCh = nil
		t.signalDone = nil
	}

	// Stop ring monitor (check if already exited, e.g. serial error)
	select {
	case <-t.ringDone:
		// monitor already exited — just clean up
	default:
		close(t.stopRingCh)
		done := t.ringDone
		t.mu.Unlock()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			log.Warn().Msg("iridium: ring monitor did not exit in time")
		}
		t.mu.Lock()
	}
}

// monitorLoop reads serial for unsolicited SBDRING alerts.
// Holds the mutex during reads (like HAL) to prevent racing with sendAT.
func (t *DirectSatTransport) monitorLoop() {
	defer close(t.ringDone)

	buf := make([]byte, 1)
	var line []byte

	for {
		select {
		case <-t.stopRingCh:
			return
		default:
		}

		t.mu.Lock()
		if t.file == nil {
			t.mu.Unlock()
			return
		}

		// Read under lock with 100ms deadline — releases lock quickly on timeout
		t.file.SetDeadline(time.Now().Add(100 * time.Millisecond))
		n, err := t.file.Read(buf)
		t.file.SetDeadline(time.Time{})
		t.mu.Unlock()

		if err != nil {
			if isTimeoutError(err) {
				continue
			}
			select {
			case <-t.stopRingCh:
				return
			default:
			}
			log.Error().Err(err).Msg("iridium monitor serial error")
			t.emitEvent(SatEvent{
				Type:    "disconnected",
				Message: "Serial connection lost",
				Time:    time.Now().UTC().Format(time.RFC3339),
			})
			t.mu.Lock()
			t.connected = false
			if t.file != nil {
				t.file.Close()
				t.file = nil
			}
			t.mu.Unlock()
			return
		}

		if n > 0 {
			line = append(line, buf[0])
			if buf[0] == '\n' {
				s := strings.TrimSpace(string(line))
				if s == "SBDRING" {
					log.Info().Msg("iridium SBDRING received")
					t.emitEvent(SatEvent{
						Type:    "ring_alert",
						Message: "MT message waiting at gateway",
						Time:    time.Now().UTC().Format(time.RFC3339),
					})
					select {
					case t.ringCh <- struct{}{}:
					default:
					}
				}
				line = line[:0]
			}
			if len(line) > 256 {
				line = line[:0]
			}
		}
	}
}

func (t *DirectSatTransport) signalPollerLoop() {
	defer close(t.signalDone)
	ticker := time.NewTicker(signalPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-t.stopSignalCh:
			return
		case <-ticker.C:
			if !t.isConnected() {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			info, err := t.getSignalInternal(ctx, "AT+CSQF", 5*time.Second)
			cancel()
			if err != nil {
				log.Warn().Err(err).Msg("iridium signal poll failed")
				continue
			}
			t.signalMu.Lock()
			t.lastSignal = *info
			t.signalMu.Unlock()

			t.emitEvent(SatEvent{
				Type:    "signal",
				Message: signalDescriptions[info.Bars],
				Signal:  info.Bars,
				Time:    info.Timestamp,
			})
		}
	}
}

func (t *DirectSatTransport) isConnected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connected
}

// ============================================================================
// SatTransport interface implementation
// ============================================================================

// Send transmits binary data via SBD (AT+SBDWB + AT+SBDIX).
// Holds mu throughout — monitor is blocked on mu.Lock() (matches HAL SendBinary).
func (t *DirectSatTransport) Send(ctx context.Context, data []byte) (*SBDResult, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("data is empty")
	}
	if len(data) > 340 {
		return nil, fmt.Errorf("data too large (max 340 bytes for SBD)")
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.connected || t.file == nil {
		return nil, fmt.Errorf("not connected")
	}

	// Clear MO buffer
	resp, err := sendAT(t.file, "AT+SBDD0", iridiumReadTimeout)
	if err != nil || strings.Contains(resp, "ERROR") {
		return nil, fmt.Errorf("failed to clear MO buffer")
	}

	// Initiate binary write
	resp, err = sendAT(t.file, fmt.Sprintf("AT+SBDWB=%d", len(data)), 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("AT+SBDWB failed: %w", err)
	}
	if !strings.Contains(resp, "READY") {
		return nil, fmt.Errorf("modem did not respond READY for binary write")
	}

	// Calculate checksum and write
	var checksum uint16
	for _, b := range data {
		checksum += uint16(b)
	}
	var payload bytes.Buffer
	payload.Write(data)
	binary.Write(&payload, binary.BigEndian, checksum)

	if t.file == nil {
		return nil, fmt.Errorf("disconnected")
	}
	if _, err := t.file.Write(payload.Bytes()); err != nil {
		return nil, fmt.Errorf("binary write failed: %w", err)
	}

	// Read write result
	writeResp, err := readATResponse(t.file, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("binary write response failed: %w", err)
	}
	writeOK := false
	for _, line := range strings.Split(writeResp, "\n") {
		line = strings.TrimSpace(line)
		switch line {
		case "0":
			writeOK = true
		case "1":
			return nil, fmt.Errorf("binary write timeout on modem")
		case "2":
			return nil, fmt.Errorf("binary write checksum mismatch")
		case "3":
			return nil, fmt.Errorf("binary write wrong size")
		}
	}
	if !writeOK {
		return nil, fmt.Errorf("binary write: no success confirmation from modem")
	}

	// SBDIX
	result, err := t.sbdixLocked(ctx)
	// Always clear MO buffer after SBDIX attempt — the caller (gateway DLQ)
	// retains the payload for retry. Leaving stale MO data causes MailboxCheck
	// to endlessly re-trigger SBDIX on every poll cycle.
	if t.connected && t.file != nil {
		sendAT(t.file, "AT+SBDD0", 3*time.Second)
	}
	return result, err
}

// SendText transmits a text SBD message (AT+SBDWT + AT+SBDIX).
// Holds mu throughout — monitor is blocked on mu.Lock() (matches HAL pattern).
func (t *DirectSatTransport) SendText(ctx context.Context, text string) (*SBDResult, error) {
	if len(text) > 120 {
		return nil, fmt.Errorf("text too long (max 120 chars for AT+SBDWT)")
	}
	// Reject characters that corrupt AT command framing
	if strings.ContainsAny(text, "\r\n\x00") {
		return nil, fmt.Errorf("text contains invalid characters (CR, LF, or null)")
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.connected || t.file == nil {
		return nil, fmt.Errorf("not connected")
	}

	// Clear MO buffer before write to prevent stale data resend
	sendAT(t.file, "AT+SBDD0", iridiumReadTimeout)

	resp, err := sendAT(t.file, "AT+SBDWT="+text, 5*time.Second)
	if err != nil || !strings.Contains(resp, "OK") {
		return nil, fmt.Errorf("AT+SBDWT failed: %s", resp)
	}

	result, err := t.sbdixLocked(ctx)
	// Always clear MO after SBDIX attempt (same rationale as Send)
	if t.connected && t.file != nil {
		sendAT(t.file, "AT+SBDD0", 3*time.Second)
	}
	return result, err
}

// Receive reads the MT buffer (AT+SBDRB).
// Stops monitor because it does raw serial reads (matches HAL ReadBinaryMT).
func (t *DirectSatTransport) Receive(_ context.Context) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.connected || t.file == nil {
		return nil, fmt.Errorf("not connected")
	}

	t.stopMonitor()
	defer t.startMonitor()

	// Re-check state after stopMonitor (mutex was briefly released)
	if !t.connected || t.file == nil {
		return nil, fmt.Errorf("disconnected during monitor stop")
	}

	// Send AT+SBDRB
	if _, err := t.file.WriteString("AT+SBDRB\r"); err != nil {
		return nil, fmt.Errorf("write failed: %w", err)
	}

	// Read binary response
	t.file.SetDeadline(time.Now().Add(5 * time.Second))
	defer t.file.SetDeadline(time.Time{})

	var all []byte
	buf := make([]byte, 512)
	for {
		n, err := t.file.Read(buf)
		if n > 0 {
			all = append(all, buf[:n]...)
			if len(all) >= 16 || (len(all) >= 4 && bytes.Contains(all, []byte("OK"))) {
				break
			}
		}
		if err != nil {
			if isTimeoutError(err) && len(all) >= 4 {
				break
			}
			if len(all) < 4 {
				return nil, fmt.Errorf("binary read failed: %w", err)
			}
			break
		}
	}

	data, err := parseSBDRBResponse(all)
	if err != nil {
		return nil, err
	}

	// Clear MT buffer after successful read to prevent re-reading stale data
	sendAT(t.file, "AT+SBDD1", iridiumReadTimeout)

	return data, nil
}

// MailboxCheck performs SBDSX (free local check) then conditional SBDIX.
// SBDIX only runs when there's a reason — each empty-MO SBDIX costs 1 credit.
//
// SBDIX triggers:
//   - MO buffer has data (outbound message pending)
//   - Ring alert (RA) flag set
//   - MT waiting > 0 (from previous SBDIX)
//   - GSS sync overdue (>15min since last successful SBDIX) — periodic MT discovery
//
// Holds mu throughout — monitor is blocked on mu.Lock().
func (t *DirectSatTransport) MailboxCheck(ctx context.Context) (*SBDResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.connected || t.file == nil {
		return nil, fmt.Errorf("not connected")
	}

	// Step 1: SBDSX — free local status check
	resp, err := sendAT(t.file, "AT+SBDSX", 5*time.Second)
	if err != nil {
		log.Warn().Err(err).Msg("iridium: SBDSX failed, forcing serial reconnect")
		t.disconnectLocked()
		return nil, fmt.Errorf("SBDSX failed: %w", err)
	}
	status, err := parseSBDSX(resp)
	if err != nil {
		log.Warn().Err(err).Msg("iridium: SBDSX parse failed, skipping SBDIX (will retry next poll)")
		return nil, fmt.Errorf("SBDSX parse failed: %w", err)
	}

	log.Info().Bool("mo", status.MOFlag).Bool("mt", status.MTFlag).
		Bool("ra", status.RAFlag).Int("waiting", status.MTWaiting).
		Msg("iridium: mailbox SBDSX status")

	// If MT buffer already has data from a piggybacked delivery, report immediately
	if status.MTFlag {
		log.Info().Msg("iridium: MT buffer has data (piggybacked), no SBDIX needed")
		return &SBDResult{MTStatus: 1, MTLength: 1, MTReceived: true}, nil
	}

	// Determine if SBDIX is warranted (each costs 1 credit with empty MO)
	gssSyncOverdue := time.Since(t.lastGSSSync) > 15*time.Minute
	hasReason := status.MOFlag || status.RAFlag || status.MTWaiting > 0 || gssSyncOverdue

	if !hasReason {
		log.Debug().Msg("iridium: no reason for SBDIX, skipping")
		return &SBDResult{}, nil
	}

	if gssSyncOverdue && !status.MOFlag && !status.RAFlag && status.MTWaiting == 0 {
		log.Info().Dur("since_last_sync", time.Since(t.lastGSSSync)).
			Msg("iridium: GSS sync overdue, forcing SBDIX for MT discovery")
	}

	// Step 2: SBDIX — satellite session
	// Clear MO if empty to prevent "[No payload]" sends
	if !status.MOFlag {
		sendAT(t.file, "AT+SBDD0", 3*time.Second)
	} else {
		log.Info().Msg("iridium: MO buffer has outbound data, sending via SBDIX")
	}

	// Update GSS sync timestamp BEFORE SBDIX — even if it fails, we attempted.
	// This enforces the 15-min cooldown and prevents endless retry spam.
	t.lastGSSSync = time.Now()

	result, err := t.sbdixLocked(ctx)
	// Always clear MO after SBDIX — DLQ retains payload for retry
	if t.connected && t.file != nil {
		sendAT(t.file, "AT+SBDD0", 3*time.Second)
	}
	return result, err
}

// GetSignal returns current signal (blocking AT+CSQ, up to 60s).
func (t *DirectSatTransport) GetSignal(ctx context.Context) (*SignalInfo, error) {
	return t.getSignalInternal(ctx, "AT+CSQ", 60*time.Second)
}

// GetSignalFast returns cached signal (AT+CSQF, ~100ms).
func (t *DirectSatTransport) GetSignalFast(ctx context.Context) (*SignalInfo, error) {
	return t.getSignalInternal(ctx, "AT+CSQF", 5*time.Second)
}

func (t *DirectSatTransport) getSignalInternal(_ context.Context, cmd string, timeout time.Duration) (*SignalInfo, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.connected || t.file == nil {
		return nil, fmt.Errorf("not connected")
	}

	resp, err := sendAT(t.file, cmd, timeout)
	if err != nil {
		return nil, err
	}

	bars := parseCSQ(resp)
	return &SignalInfo{
		Bars:       bars,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Assessment: signalAssessment(bars),
	}, nil
}

// GetStatus returns the modem connection status.
func (t *DirectSatTransport) GetStatus(_ context.Context) (*SatStatus, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return &SatStatus{
		Connected: t.connected,
		Port:      t.port,
		IMEI:      t.imei,
		Model:     t.model,
	}, nil
}

// GetGeolocation returns Iridium-derived geolocation (AT-MSGEO).
func (t *DirectSatTransport) GetGeolocation(_ context.Context) (*GeolocationInfo, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.connected || t.file == nil {
		return nil, fmt.Errorf("not connected")
	}

	resp, err := sendAT(t.file, "AT-MSGEO", 30*time.Second)
	if err != nil {
		return nil, err
	}

	return parseMSGEO(resp)
}

func (t *DirectSatTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Use stopMonitor for clean goroutine shutdown (waits with 2s timeout)
	t.stopMonitor()

	t.connected = false
	if t.file != nil {
		t.file.Close()
		t.file = nil
	}
	return nil
}

// disconnectLocked tears down the serial connection.
// Caller must hold t.mu. The gateway's ctx.Done() unsubscribe goroutine
// handles channel cleanup — we must NOT close subscriber channels here,
// as that kills ALL SSE streams and causes reconnection storms.
func (t *DirectSatTransport) disconnectLocked() {
	t.connected = false
	if t.file != nil {
		t.file.Close()
		t.file = nil
	}

	// Stop monitor and signal poller goroutines cleanly.
	// stopMonitor temporarily releases t.mu for goroutines to exit.
	t.stopMonitor()

	// Emit the disconnected event — subscribers stay open for reconnect.
	t.emitEvent(SatEvent{
		Type:    "disconnected",
		Message: "Serial connection reset (will reconnect)",
		Time:    time.Now().UTC().Format(time.RFC3339),
	})
}

// ============================================================================
// Internal helpers
// ============================================================================

// sbdixLocked performs AT+SBDIX with rate limiting. Caller must hold t.mu.
// Context-aware rate limit wait matches HAL's sbdixLocked pattern.
func (t *DirectSatTransport) sbdixLocked(ctx context.Context) (*SBDResult, error) {
	if !t.connected || t.file == nil {
		return nil, fmt.Errorf("not connected")
	}

	// Rate limit: min 10s between SBDIX.
	// Release mutex during wait so signal polls can get through (matches HAL).
	if elapsed := time.Since(t.lastSBDIX); elapsed < minSBDIXInterval {
		wait := minSBDIXInterval - elapsed
		log.Info().Dur("wait", wait).Msg("iridium SBDIX rate limit")
		t.mu.Unlock()
		select {
		case <-ctx.Done():
			t.mu.Lock()
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		t.mu.Lock()
		// Re-check state after re-acquiring (HAL pattern)
		if !t.connected || t.file == nil {
			return nil, fmt.Errorf("disconnected during rate limit wait")
		}
	}

	t.lastSBDIX = time.Now()

	timeout := iridiumSBDIXTimeout
	if v := os.Getenv("IRIDIUM_SBDIX_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			timeout = time.Duration(n) * time.Second
		}
	}

	resp, err := sendAT(t.file, "AT+SBDIX", timeout)
	if err != nil {
		// SBDIX timeout corrupts the serial buffer — the modem may still be
		// processing the satellite session and will send its response later.
		// Force disconnect so the reconnect loop re-establishes a clean port.
		log.Warn().Err(err).Msg("iridium: SBDIX failed, forcing serial reconnect")
		t.disconnectLocked()
		return nil, fmt.Errorf("SBDIX failed: %w", err)
	}

	ix, err := parseSBDIX(resp)
	if err != nil {
		return nil, err
	}

	return &SBDResult{
		MOStatus:   ix.moStatus,
		MOMSN:      ix.moMSN,
		MTReceived: ix.mtStatus == 1,
		MTStatus:   ix.mtStatus,
		MTMSN:      ix.mtMSN,
		MTLength:   ix.mtLength,
		MTQueued:   ix.mtQueued,
		StatusText: ix.statusText(),
	}, nil
}

func (t *DirectSatTransport) emitEvent(event SatEvent) {
	t.eventMu.RLock()
	defer t.eventMu.RUnlock()
	for _, ch := range t.eventSubs {
		select {
		case ch <- event:
		default:
		}
	}
}

// ============================================================================
// AT Response Parsers
// ============================================================================

type sbdixResult struct {
	moStatus int
	moMSN    int
	mtStatus int
	mtMSN    int
	mtLength int
	mtQueued int
}

func (r sbdixResult) statusText() string {
	if r.moStatus >= 0 && r.moStatus <= 4 {
		return "success"
	}
	return fmt.Sprintf("MO status %d", r.moStatus)
}

func parseSBDIX(resp string) (sbdixResult, error) {
	idx := strings.Index(resp, "+SBDIX:")
	if idx == -1 {
		return sbdixResult{}, fmt.Errorf("no +SBDIX in response: %s", resp)
	}

	remainder := strings.TrimSpace(resp[idx+7:])
	firstLine := strings.Split(remainder, "\n")[0]
	firstLine = strings.TrimRight(firstLine, "\r") // strip trailing CR
	parts := strings.Split(firstLine, ",")
	if len(parts) < 5 {
		return sbdixResult{}, fmt.Errorf("malformed SBDIX response (expected 5-6 fields, got %d): %s", len(parts), firstLine)
	}

	result := sbdixResult{}
	result.moStatus, _ = strconv.Atoi(strings.TrimSpace(parts[0]))
	result.moMSN, _ = strconv.Atoi(strings.TrimSpace(parts[1]))
	result.mtStatus, _ = strconv.Atoi(strings.TrimSpace(parts[2]))
	result.mtMSN, _ = strconv.Atoi(strings.TrimSpace(parts[3]))
	result.mtLength, _ = strconv.Atoi(strings.TrimSpace(parts[4]))
	if len(parts) >= 6 {
		result.mtQueued, _ = strconv.Atoi(strings.TrimSpace(parts[5]))
	}

	return result, nil
}

// sbdStatus holds the result of AT+SBDSX (free local check, no satellite session).
type sbdStatus struct {
	MOFlag    bool
	MTFlag    bool
	RAFlag    bool
	MTWaiting int
}

func parseSBDSX(resp string) (sbdStatus, error) {
	idx := strings.Index(resp, "+SBDSX:")
	if idx == -1 {
		return sbdStatus{}, fmt.Errorf("no +SBDSX in response")
	}
	remainder := strings.TrimSpace(resp[idx+7:])
	firstLine := strings.Split(remainder, "\n")[0]
	parts := strings.Split(firstLine, ",")
	if len(parts) < 6 {
		return sbdStatus{}, fmt.Errorf("malformed SBDSX response")
	}
	s := sbdStatus{}
	moFlag, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
	s.MOFlag = moFlag != 0
	mtFlag, _ := strconv.Atoi(strings.TrimSpace(parts[2]))
	s.MTFlag = mtFlag != 0
	raFlag, _ := strconv.Atoi(strings.TrimSpace(parts[4]))
	s.RAFlag = raFlag != 0
	s.MTWaiting, _ = strconv.Atoi(strings.TrimSpace(parts[5]))
	return s, nil
}

func parseCSQ(resp string) int {
	idx := strings.Index(resp, "+CSQF:")
	offset := 6
	if idx == -1 {
		idx = strings.Index(resp, "+CSQ:")
		offset = 5
	}
	if idx == -1 {
		return 0
	}
	remainder := strings.TrimSpace(resp[idx+offset:])
	sigStr := strings.Split(remainder, "\n")[0]
	sigStr = strings.TrimSpace(sigStr)
	sig, err := strconv.Atoi(sigStr)
	if err != nil || sig < 0 || sig > 5 {
		return 0
	}
	return sig
}

func parseMSGEO(resp string) (*GeolocationInfo, error) {
	// -MSGEO: <lat>,<lon>,<alt>,<timestamp>
	idx := strings.Index(resp, "-MSGEO:")
	if idx == -1 {
		return nil, fmt.Errorf("no -MSGEO in response")
	}
	remainder := strings.TrimSpace(resp[idx+7:])
	firstLine := strings.Split(remainder, "\n")[0]
	parts := strings.Split(firstLine, ",")
	if len(parts) < 3 {
		return nil, fmt.Errorf("malformed MSGEO response")
	}

	lat, _ := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	lon, _ := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	alt, _ := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)

	// Sanitize altitude: AT-MSGEO sometimes returns satellite altitude (~780km)
	// or garbage values. Ground stations are always below 10km.
	altKm := alt
	if altKm > 10.0 || altKm < -1.0 {
		altKm = 0.0
	}

	return &GeolocationInfo{
		Lat:       lat / 1000.0, // Iridium returns milli-degrees
		Lon:       lon / 1000.0,
		AltKm:     altKm,
		Accuracy:  10.0, // Iridium geolocation ~10km typical
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func parseSBDRBResponse(raw []byte) ([]byte, error) {
	if len(raw) < 4 {
		return nil, fmt.Errorf("response too short for binary read")
	}

	// Skip AT echo — find plausible length value
	startIdx := 0
	for i := 0; i <= len(raw)-4; i++ {
		length := binary.BigEndian.Uint16(raw[i : i+2])
		if length <= 270 && i+2+int(length)+2 <= len(raw) {
			startIdx = i
			break
		}
	}

	if startIdx+4 > len(raw) {
		return nil, fmt.Errorf("cannot find binary payload in response")
	}

	length := binary.BigEndian.Uint16(raw[startIdx : startIdx+2])
	dataStart := startIdx + 2
	dataEnd := dataStart + int(length)

	if length == 0 {
		return nil, nil // Empty MT buffer
	}

	if dataEnd+2 > len(raw) {
		return nil, fmt.Errorf("response truncated")
	}

	data := raw[dataStart:dataEnd]
	receivedChecksum := binary.BigEndian.Uint16(raw[dataEnd : dataEnd+2])

	var computed uint16
	for _, b := range data {
		computed += uint16(b)
	}
	if computed != receivedChecksum {
		return nil, fmt.Errorf("binary checksum mismatch (computed=%d, received=%d)", computed, receivedChecksum)
	}

	return data, nil
}

// parseATValue extracts a value from a typical AT response.
// E.g., "AT+CGSN\r\n300234063904190\r\nOK" → "300234063904190"
func parseATValue(resp string) string {
	lines := strings.Split(resp, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "AT") || line == "OK" || line == "ERROR" {
			continue
		}
		return line
	}
	return ""
}
