package transport

// DirectSatTransport implements SatTransport with direct serial access to an Iridium modem.
// Ported from HAL's IridiumDriver — no HAL dependency, talks to USB modem directly.

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/warthog618/go-gpiocdev"
	"go.bug.st/serial"
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
	file      serial.Port
	connected bool
	imei      string
	model     string
	firmware  string

	// MSSTM workaround: older firmware TA16005 hangs on SBDIX without prior MSSTM
	needsMSSTMWorkaround bool

	// Sleep/wake power management (GPIO pin, 0 = disabled).
	// sleepPin is the configured BCM number; sleepLine is the chardev
	// reservation held while the transport is connected.
	sleepPin     int
	sleepLine    GPIOLine
	awake        bool
	lastWakeTime time.Time

	// 9603 NetAv GPIO input (BCM, 0 = disabled). HIGH when an Iridium
	// satellite is visible. Used as a cheap SBDIX veto and exposed on
	// SatStatus for the dashboard. See MESHSAT-666.
	netAvPin    int
	netAvLine   GPIOLine
	netAvState  atomic.Bool
	netAvSince  atomic.Pointer[time.Time] // set when state transitions to HIGH
	stopNetAvCh chan struct{}
	netAvDone   chan struct{}

	// 9603 RI GPIO input (BCM, 0 = disabled). Active-LOW with a 5 s
	// pulse + 5 s pulse 20 s later on each MT arrival. Kernel delivers
	// edges via go-gpiocdev's event handler goroutine — no polling.
	// Parallels the existing UART SBDRING path (monitorLoop below);
	// handleRingAlert in the gateway dedupes when both fire. See
	// MESHSAT-667.
	riPin         int
	riLine        GPIOLine
	lastRIEdge    atomic.Pointer[time.Time] // 15 s dedupe window
	riPulseCount  atomic.Int64              // total edges since boot (before dedupe)
	lastRingAlert atomic.Pointer[time.Time] // either source (UART or GPIO)

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

// SetPort sets the serial port path. Called by DeviceSupervisor.
func (t *DirectSatTransport) SetPort(port string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.port = port
}

// IsConnected returns true if the transport has an active serial connection.
func (t *DirectSatTransport) IsConnected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connected
}

// Reconnect closes any existing connection and reconnects on the current port.
func (t *DirectSatTransport) Reconnect(ctx context.Context) error {
	t.Close()
	t.mu.Lock()
	err := t.connectLocked(ctx)
	t.mu.Unlock()
	return err
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
	portPath := t.port
	if portPath == "supervisor" {
		return fmt.Errorf("waiting for device supervisor to assign port")
	}
	if portPath == "" || portPath == "auto" {
		exclude := t.excludePort
		if t.excludePortFn != nil {
			if resolved := t.excludePortFn(); resolved != "" && resolved != "auto" {
				exclude = resolved
			}
		}
		portPath = autoDetectIridium(exclude)
		if portPath == "" {
			return fmt.Errorf("no Iridium modem found")
		}
	}

	// Configure sleep pin GPIO (if set) and ensure modem is awake.
	// Uses chardev (/dev/gpiochip4 on Pi 5) rather than sysfs — the
	// standalone compose mounts /sys read-only so sysfs writes fail.
	if t.sleepPin > 0 && t.sleepLine == nil {
		line, err := OpenOutput(t.sleepPin, 0, "meshsat-iridium-sleep") // 0 = awake
		if err != nil {
			log.Warn().Err(err).Int("pin", t.sleepPin).Msg("iridium: sleep pin setup failed, continuing without")
			t.sleepPin = 0
		} else {
			t.sleepLine = line
			t.awake = true
			t.lastWakeTime = time.Now()
			log.Info().Int("pin", t.sleepPin).Msg("iridium: sleep pin configured")
		}
	}

	// Configure NetAv GPIO input (if set). No bias — the modem drives
	// it as a CMOS output; adding a Pi-side pull would fight the driver.
	if t.netAvPin > 0 && t.netAvLine == nil {
		line, err := OpenInput(t.netAvPin, gpiocdev.WithBiasDisabled, "meshsat-iridium-netav")
		if err != nil {
			log.Warn().Err(err).Int("pin", t.netAvPin).Msg("iridium: NetAv pin setup failed, continuing without")
			t.netAvPin = 0
		} else {
			t.netAvLine = line
			log.Info().Int("pin", t.netAvPin).Msg("iridium: NetAv pin configured")
		}
	}

	// Configure RI GPIO input with a kernel-driven falling-edge watcher
	// (internal pull-up keeps a disconnected/unwired line reading HIGH
	// so we don't spawn spurious ring alerts).
	if t.riPin > 0 && t.riLine == nil {
		line, err := WatchFallingEdge(t.riPin, "meshsat-iridium-ri", t.riEventHandler)
		if err != nil {
			log.Warn().Err(err).Int("pin", t.riPin).Msg("iridium: RI pin setup failed, continuing without")
			t.riPin = 0
		} else {
			t.riLine = line
			log.Info().Int("pin", t.riPin).Msg("iridium: RI pin configured (hardware ring-alert)")
		}
	}

	sp, err := openSerial(portPath, iridiumBaud)
	if err != nil {
		return err
	}

	t.file = sp
	t.port = portPath

	// Drain any stale data from the serial buffer before first command
	drainPort(sp)

	// Initialize modem
	// AT&K0 — disable flow control
	if _, err := sendAT(sp, "AT&K0", iridiumReadTimeout); err != nil {
		sp.Close()
		return fmt.Errorf("AT&K0 failed: %w", err)
	}
	// ATE0 — disable command echo (reduces response parsing noise)
	sendAT(sp, "ATE0", iridiumReadTimeout)
	// AT&D0 — ignore DTR
	sendAT(sp, "AT&D0", iridiumReadTimeout)
	// AT — basic check
	resp, err := sendAT(sp, "AT", iridiumReadTimeout)
	if err != nil || !strings.Contains(resp, "OK") {
		sp.Close()
		return fmt.Errorf("AT check failed")
	}
	// AT+CGSN — get IMEI
	resp, err = sendAT(sp, "AT+CGSN", iridiumReadTimeout)
	if err == nil {
		t.imei = parseATValue(resp)
	}
	// AT+CGMM — get model
	resp, err = sendAT(sp, "AT+CGMM", iridiumReadTimeout)
	if err == nil {
		t.model = parseATValue(resp)
	}
	// AT+CGMR — get firmware version
	resp, err = sendAT(sp, "AT+CGMR", iridiumReadTimeout)
	if err == nil {
		t.firmware = parseATValue(resp)
		// TA16005 firmware has a bug where SBDIX hangs unless AT-MSSTM is sent first
		t.needsMSSTMWorkaround = strings.Contains(t.firmware, "TA16005")
		if t.needsMSSTMWorkaround {
			log.Warn().Str("firmware", t.firmware).Msg("iridium: TA16005 detected, enabling MSSTM workaround")
		}
	}
	// AT+SBDMTA=1 — enable ring alert indications
	sendAT(sp, "AT+SBDMTA=1", iridiumReadTimeout)
	// Clear MO and MT buffers — prevents stale data from previous sessions
	// triggering endless SBDIX resend loops
	sendAT(sp, "AT+SBDD0", iridiumReadTimeout) // clear MO
	sendAT(sp, "AT+SBDD1", iridiumReadTimeout) // clear MT

	t.connected = true
	t.awake = true
	t.lastWakeTime = time.Now()
	t.lastSBDIX = time.Now() // prevent stale SBDIX from firing immediately after connect
	log.Info().Str("port", portPath).Str("imei", t.imei).Str("model", t.model).Str("firmware", t.firmware).Msg("iridium modem connected")

	t.emitEvent(SatEvent{
		Type:    "connected",
		Message: fmt.Sprintf("Connected to %s (IMEI: %s, FW: %s)", t.model, t.imei, t.firmware),
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

	if t.netAvLine != nil {
		t.stopNetAvCh = make(chan struct{})
		t.netAvDone = make(chan struct{})
		go t.netAvMonitorLoop()
	}
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

	// Stop NetAv poller (if running)
	if t.stopNetAvCh != nil {
		select {
		case <-t.netAvDone:
			// already exited
		default:
			close(t.stopNetAvCh)
			done := t.netAvDone
			t.mu.Unlock()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				log.Warn().Msg("iridium: NetAv poller did not exit in time")
			}
			t.mu.Lock()
		}
		t.stopNetAvCh = nil
		t.netAvDone = nil
	}
}

// netAvMonitorLoop polls the 9603 NetAv GPIO at 1 Hz. Updates
// t.netAvState + t.netAvSince atomics and emits a rate-limited
// netav_change SSE event on transitions. Only emits when t.awake is
// true (modem outputs are LOW when sleeping, which would otherwise
// spam the event stream).
func (t *DirectSatTransport) netAvMonitorLoop() {
	defer close(t.netAvDone)
	tick := time.NewTicker(1 * time.Second)
	defer tick.Stop()

	var lastEmit time.Time
	const emitGap = 5 * time.Second

	for {
		select {
		case <-t.stopNetAvCh:
			return
		case <-tick.C:
		}

		if t.netAvLine == nil {
			return
		}
		v, err := t.netAvLine.Value()
		if err != nil {
			log.Debug().Err(err).Int("pin", t.netAvPin).Msg("iridium: NetAv read error")
			continue
		}
		newState := v == 1
		oldState := t.netAvState.Swap(newState)
		if newState == oldState {
			continue
		}

		now := time.Now()
		if newState {
			stamp := now
			t.netAvSince.Store(&stamp)
		} else {
			t.netAvSince.Store(nil)
		}

		// Emit only while modem is awake; otherwise suppress the flap
		// that happens when the modem powers down its outputs.
		t.mu.Lock()
		awake := t.awake
		t.mu.Unlock()

		if awake && time.Since(lastEmit) >= emitGap {
			lastEmit = now
			msg := "NetAv=0 (no Iridium satellite visible)"
			if newState {
				msg = "NetAv=1 (Iridium satellite visible)"
			}
			t.emitEvent(SatEvent{
				Type:    "netav_change",
				Message: msg,
				Time:    now.UTC().Format(time.RFC3339),
			})
			log.Info().Bool("state", newState).Msg("iridium: NetAv change")
		}
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

		// Read under lock with 100ms timeout — releases lock quickly
		t.file.SetReadTimeout(100 * time.Millisecond)
		n, err := t.file.Read(buf)
		t.mu.Unlock()

		if n == 0 && err == nil {
			continue // read timeout, no data
		}
		if err != nil {
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

	// Drain any residual bytes from the binary write before SBDIX.
	// The modem may echo parts of the binary payload or send trailing
	// bytes after the "0\r\nOK\r\n" confirmation. Without this drain,
	// SBDIX response parsing fails with "no +SBDIX in response" because
	// the binary garbage gets prepended to the actual SBDIX response.
	time.Sleep(500 * time.Millisecond)
	drainPort(t.file)
	// Second drain pass — some modems trickle bytes slowly after SBDWB
	time.Sleep(200 * time.Millisecond)
	drainPort(t.file)

	// Verify modem is responsive with a simple AT probe before SBDIX
	if probeResp, probeErr := sendAT(t.file, "AT", 3*time.Second); probeErr != nil || !strings.Contains(probeResp, "OK") {
		drainPort(t.file)
		log.Warn().Str("probe", probeResp).Msg("iridium: modem not clean after SBDWB, extra drain")
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
	if _, err := t.file.Write([]byte("AT+SBDRB\r")); err != nil {
		return nil, fmt.Errorf("write failed: %w", err)
	}

	// Read binary response with 5s timeout
	t.file.SetReadTimeout(5 * time.Second)
	defer t.file.SetReadTimeout(100 * time.Millisecond) // restore monitor timeout

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
		if n == 0 && err == nil && len(all) >= 4 {
			break // timeout with enough data
		}
		if err != nil {
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

	// Determine if SBDIX is warranted (each costs 1 credit with empty MO).
	// NetAv veto: if the NetAv GPIO is wired, skip RA- and sync-driven
	// SBDIX attempts when no satellite is currently visible. MOFlag and
	// MTWaiting always pass (caller has committed to a session anyway).
	gssSyncOverdue := time.Since(t.lastGSSSync) > 15*time.Minute
	netAvWired := t.netAvLine != nil
	netAvOk := !netAvWired || t.netAvState.Load()
	hasReason := status.MOFlag ||
		(status.RAFlag && netAvOk) ||
		status.MTWaiting > 0 ||
		(gssSyncOverdue && netAvOk)

	if !hasReason {
		if netAvWired && !netAvOk && (status.RAFlag || gssSyncOverdue) {
			log.Debug().Bool("ra", status.RAFlag).Bool("gss_overdue", gssSyncOverdue).
				Msg("iridium: SBDIX vetoed by NetAv=LOW (no sat visible)")
		} else {
			log.Debug().Msg("iridium: no reason for SBDIX, skipping")
		}
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

// MOBufferEmpty checks AT+SBDSX and returns true if the MO buffer is empty,
// meaning a previous SBDIX already transmitted and cleared it. This is a free
// local check (no satellite session, no credits).
func (t *DirectSatTransport) MOBufferEmpty(ctx context.Context) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.connected || t.file == nil {
		return false, fmt.Errorf("not connected")
	}

	resp, err := sendAT(t.file, "AT+SBDSX", 5*time.Second)
	if err != nil {
		return false, fmt.Errorf("SBDSX failed: %w", err)
	}

	status, err := parseSBDSX(resp)
	if err != nil {
		return false, err
	}
	return !status.MOFlag, nil
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
		Source:     "sbd",
	}, nil
}

// GetStatus returns the modem connection status.
func (t *DirectSatTransport) GetStatus(_ context.Context) (*SatStatus, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := &SatStatus{
		Connected: t.connected,
		Port:      t.port,
		IMEI:      t.imei,
		Model:     t.model,
		Type:      "sbd",
		Firmware:  t.firmware,
	}
	if t.netAvLine != nil {
		s.NetworkAvailable = t.netAvState.Load()
		if ts := t.netAvSince.Load(); ts != nil {
			s.NetworkAvailableSince = *ts
		}
	}
	if t.riLine != nil {
		s.RIPulseCount = t.riPulseCount.Load()
		if ts := t.lastRingAlert.Load(); ts != nil {
			s.LastRingAlert = *ts
		}
	}
	return s, nil
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

// GetSystemTime returns the Iridium network time via AT-MSSTM.
// The response is a hex tick count since the Iridium epoch (May 11, 2014 14:23:55 UTC),
// with each tick representing 90ms.
func (t *DirectSatTransport) GetSystemTime(_ context.Context) (*IridiumTime, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.connected || t.file == nil {
		return nil, fmt.Errorf("not connected")
	}

	resp, err := sendAT(t.file, "AT-MSSTM", 5*time.Second)
	if err != nil {
		return nil, err
	}

	return parseMSSTM(resp)
}

// GetFirmwareVersion returns the cached modem firmware version.
func (t *DirectSatTransport) GetFirmwareVersion(_ context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.connected {
		return "", fmt.Errorf("not connected")
	}
	return t.firmware, nil
}

// SetSleepPin configures the GPIO pin for modem sleep/wake control.
// Pin 0 means disabled (default). Must be called before Connect().
func (t *DirectSatTransport) SetSleepPin(pin int) {
	t.sleepPin = pin
}

// SetNetAvPin configures the GPIO pin that reads the 9603 NetAv
// output (HIGH = Iridium satellite visible). Pin 0 = disabled.
// Must be called before Connect(). See MESHSAT-666.
func (t *DirectSatTransport) SetNetAvPin(pin int) {
	t.netAvPin = pin
}

// SetRIPin configures the GPIO pin that reads the 9603 RI (ring
// indicator) output — an active-LOW pulse signalling that an MT
// message is waiting at the GSS. Pin 0 = disabled. Must be called
// before Connect(). See MESHSAT-667.
func (t *DirectSatTransport) SetRIPin(pin int) {
	t.riPin = pin
}

// riEventHandler is invoked by go-gpiocdev's event dispatcher
// goroutine on every falling edge of the RI line. Duplicates against
// the UART SBDRING path are dedup'd by the gateway's handleRingAlert
// CompareAndSwap; we dedupe the 20 s double-pulse locally with a 15 s
// window so we only emit one ring_alert per MT.
func (t *DirectSatTransport) riEventHandler(evt gpiocdev.LineEvent) {
	now := time.Now()
	count := t.riPulseCount.Add(1)
	log.Debug().
		Int("pin", t.riPin).
		Int64("count", count).
		Str("edge", "falling").
		Time("ts", now).
		Uint32("kernel_seq", evt.Seqno).
		Msg("iridium: RI edge")

	if last := t.lastRIEdge.Load(); last != nil && now.Sub(*last) < 15*time.Second {
		return // second pulse of the 20 s pair, or spurious
	}
	ts := now
	t.lastRIEdge.Store(&ts)
	t.lastRingAlert.Store(&ts)

	log.Info().Int("pin", t.riPin).Msg("iridium: ring alert from RI pin")
	t.emitEvent(SatEvent{
		Type:    "ring_alert",
		Message: "MT message waiting (RI pin)",
		Time:    now.UTC().Format(time.RFC3339),
	})

	// Non-blocking send — mirrors the UART SBDRING path. The gateway's
	// ringAlertActive CAS dedupes if UART fired first.
	select {
	case t.ringCh <- struct{}{}:
	default:
	}
}

// Sleep puts the modem into low-power sleep mode via GPIO.
// Enforces a minimum 2-second on-time before allowing sleep.
func (t *DirectSatTransport) Sleep(_ context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sleepLocked()
}

func (t *DirectSatTransport) sleepLocked() error {
	if t.sleepLine == nil {
		return fmt.Errorf("sleep pin not configured")
	}
	if !t.awake {
		return nil // already asleep
	}
	// Enforce minimum 2s on-time
	if elapsed := time.Since(t.lastWakeTime); elapsed < 2*time.Second {
		time.Sleep(2*time.Second - elapsed)
	}
	if err := t.sleepLine.SetValue(1); err != nil {
		return fmt.Errorf("sleep pin write: %w", err)
	}
	t.awake = false
	t.stopMonitor()
	log.Info().Int("pin", t.sleepPin).Msg("iridium: modem sleeping")
	return nil
}

// Wake brings the modem out of sleep mode via GPIO.
func (t *DirectSatTransport) Wake(_ context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.wakeLocked()
}

func (t *DirectSatTransport) wakeLocked() error {
	if t.sleepLine == nil {
		return nil // no sleep pin, always awake
	}
	if t.awake {
		return nil // already awake
	}
	if err := t.sleepLine.SetValue(0); err != nil {
		return fmt.Errorf("wake pin write: %w", err)
	}
	// Give modem time to boot before AT commands
	time.Sleep(250 * time.Millisecond)
	t.awake = true
	t.lastWakeTime = time.Now()
	t.startMonitor()
	log.Info().Int("pin", t.sleepPin).Msg("iridium: modem waking")
	return nil
}

func (t *DirectSatTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Use stopMonitor for clean goroutine shutdown (waits with 2s timeout)
	t.stopMonitor()

	// Put modem to sleep before closing (saves power), then release
	// the chardev line back to the kernel.
	if t.sleepLine != nil {
		if t.awake {
			_ = t.sleepLocked()
		}
		_ = t.sleepLine.Close()
		t.sleepLine = nil
	}

	// Release NetAv input (monitor loop already stopped by stopMonitor above).
	if t.netAvLine != nil {
		_ = t.netAvLine.Close()
		t.netAvLine = nil
	}

	// Release RI watcher. Closing the line stops the kernel from
	// dispatching further edge events to our handler.
	if t.riLine != nil {
		_ = t.riLine.Close()
		t.riLine = nil
	}

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
	// Stop monitor+poller during wait instead of releasing mutex — prevents
	// other goroutines from consuming serial bytes that corrupt SBDIX parsing.
	if elapsed := time.Since(t.lastSBDIX); elapsed < minSBDIXInterval {
		wait := minSBDIXInterval - elapsed
		log.Info().Dur("wait", wait).Msg("iridium SBDIX rate limit")
		t.stopMonitor()
		t.mu.Unlock()
		select {
		case <-ctx.Done():
			t.mu.Lock()
			t.startMonitor()
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		t.mu.Lock()
		// Re-check state after re-acquiring
		if !t.connected || t.file == nil {
			t.startMonitor()
			return nil, fmt.Errorf("disconnected during rate limit wait")
		}
		// Drain any bytes the modem sent during the wait (unsolicited URCs, echo)
		drainPort(t.file)
	} else {
		// Even without rate-limit wait, stop monitor before SBDIX to prevent
		// concurrent serial reads from corrupting the response
		t.stopMonitor()
	}

	t.lastSBDIX = time.Now()

	// Drain serial buffer and verify modem is responsive before SBDIX.
	// Without this, residual bytes from prior AT commands (signal polls, etc.)
	// get prepended to the SBDIX response, causing "no +SBDIX in response" errors.
	drainPort(t.file)
	if probeResp, probeErr := sendAT(t.file, "AT", 3*time.Second); probeErr != nil || !strings.Contains(probeResp, "OK") {
		drainPort(t.file)
		log.Warn().Str("probe", probeResp).Msg("iridium: modem not clean before SBDIX, extra drain")
	}

	timeout := iridiumSBDIXTimeout
	if v := os.Getenv("IRIDIUM_SBDIX_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			timeout = time.Duration(n) * time.Second
		}
	}

	// MSSTM workaround: TA16005 firmware hangs on SBDIX unless AT-MSSTM is sent first.
	// This forces the modem to sync with the network before the SBD session.
	if t.needsMSSTMWorkaround {
		sendAT(t.file, "AT-MSSTM", 5*time.Second)
	}

	// Send SBDIX using custom reader that waits for "+SBDIX:" or "ERROR",
	// not just "OK". The modem sometimes outputs binary/residual data followed
	// by "OK" before the actual +SBDIX: response, which confuses the generic
	// readATResponse (it stops at the first "OK").
	drainPort(t.file)
	if _, err := t.file.Write([]byte("AT+SBDIX\r")); err != nil {
		t.startMonitor()
		return nil, fmt.Errorf("SBDIX write failed: %w", err)
	}
	resp, err := readSBDIXResponse(t.file, timeout)
	// Restart monitor after SBDIX (whether success or failure)
	t.startMonitor()

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

// readSBDIXResponse reads the serial port waiting specifically for "+SBDIX:" or
// "+SBDI:" (older firmware). Unlike readATResponse which stops at the first "OK",
// this skips intermediate binary/residual data that may precede the +SBDIX: line.
// The modem outputs binary buffer remnants before the actual SBDIX response when
// the MO buffer held data from a previous binary write (AT+SBDWB).
func readSBDIXResponse(port serial.Port, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var resp strings.Builder
	buf := make([]byte, 256)
	const maxResp = 4096

	port.SetReadTimeout(50 * time.Millisecond)

	for {
		if time.Now().After(deadline) {
			return resp.String(), fmt.Errorf("read timeout")
		}

		n, err := port.Read(buf)
		if n > 0 {
			resp.Write(buf[:n])
			full := resp.String()

			// Wait for the +SBDIX response followed by OK/ERROR
			hasSBDIX := strings.Contains(full, "+SBDIX:") || strings.Contains(full, "IX:")
			hasTerminator := strings.Contains(full, "\r\nOK\r\n") ||
				strings.HasSuffix(strings.TrimSpace(full), "OK") ||
				strings.Contains(full, "\r\nERROR\r\n") ||
				strings.HasSuffix(strings.TrimSpace(full), "ERROR")

			if hasSBDIX && hasTerminator {
				return full, nil
			}
			// If we see ERROR without +SBDIX, the command was rejected
			if !hasSBDIX && (strings.Contains(full, "\r\nERROR\r\n") ||
				strings.HasSuffix(strings.TrimSpace(full), "ERROR")) {
				return full, nil
			}

			if resp.Len() > maxResp {
				return full, fmt.Errorf("response too large (%d bytes)", resp.Len())
			}
		}

		if err != nil {
			return resp.String(), err
		}
	}
}

func (r sbdixResult) statusText() string {
	if r.moStatus >= 0 && r.moStatus <= 4 {
		return "success"
	}
	return fmt.Sprintf("MO status %d", r.moStatus)
}

func parseSBDIX(resp string) (sbdixResult, error) {
	idx := strings.Index(resp, "+SBDIX:")
	skip := 7 // len("+SBDIX:")
	if idx == -1 {
		// Fallback: serial read interleaving can consume the "+SBD" prefix,
		// leaving just "IX: <fields>". Accept this truncated form.
		idx = strings.Index(resp, "IX:")
		skip = 3 // len("IX:")
		if idx == -1 {
			return sbdixResult{}, fmt.Errorf("no +SBDIX in response: %s", resp)
		}
	}

	remainder := strings.TrimSpace(resp[idx+skip:])
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

// parseCSQ parses AT+CSQ or AT+CSQF response. MAN0009 §5.95-96 documents
// "+CSQ:" as the response prefix for both commands, but real 9603N firmware
// returns "+CSQF:" for AT+CSQF (confirmed by ModemManager and real hardware).
// We check for both prefixes to handle all firmware versions.
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

// parseMSGEO parses AT-MSGEO response. Per MAN0009 §5.163, the response is:
//
//	-MSGEO: <x>,<y>,<z>,<timestamp>
//
// where x,y,z are ECEF Cartesian coordinates in kilometres (range -6376 to +6376,
// resolution 4 km). The timestamp is a hex Iridium system time value.
// We convert ECEF to geodetic (lat/lon) for display.
func parseMSGEO(resp string) (*GeolocationInfo, error) {
	idx := strings.Index(resp, "-MSGEO:")
	if idx == -1 {
		return nil, fmt.Errorf("no -MSGEO in response")
	}
	remainder := strings.TrimSpace(resp[idx+7:])
	firstLine := strings.Split(remainder, "\n")[0]
	parts := strings.Split(firstLine, ",")
	if len(parts) < 4 {
		return nil, fmt.Errorf("malformed MSGEO response (need 4 fields, got %d)", len(parts))
	}

	x, _ := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	y, _ := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	z, _ := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)

	// Reject zero coordinates (modem has no fix)
	if x == 0 && y == 0 && z == 0 {
		return nil, fmt.Errorf("MSGEO returned zero coordinates (no fix)")
	}

	// ECEF to geodetic conversion (spherical approximation — sufficient at 4 km resolution)
	lat, lon := ecefToGeodetic(x, y, z)

	// Accuracy: ECEF resolution is 4 km per axis, so positional uncertainty
	// is ~sqrt(3)*4 ≈ 7 km best case. Real-world accuracy is worse because
	// the coordinates represent the satellite sub-point, not the modem.
	// The modem is somewhere within the satellite's beam footprint (~400 km).
	// We report 200 km as a conservative median estimate.
	accuracy := 200.0

	// Parse Iridium system timestamp (hex, 90ms ticks since Iridium epoch 2007-03-08T03:50:35Z)
	ts := parseIridiumTimestamp(strings.TrimSpace(parts[3]))

	return &GeolocationInfo{
		Lat:       lat,
		Lon:       lon,
		AltKm:     0, // ECEF gives satellite altitude, not ground altitude
		Accuracy:  accuracy,
		Timestamp: ts.UTC().Format(time.RFC3339),
	}, nil
}

// ecefToGeodetic converts ECEF (x, y, z) in km to geodetic (lat, lon) in degrees.
// Uses spherical approximation — more than sufficient for 4 km resolution ECEF data.
func ecefToGeodetic(x, y, z float64) (lat, lon float64) {
	lon = math.Atan2(y, x) * 180.0 / math.Pi
	lat = math.Atan2(z, math.Sqrt(x*x+y*y)) * 180.0 / math.Pi
	return lat, lon
}

// iridiumEpoch is 2007-03-08 03:50:35 UTC (Iridium system time origin).
var iridiumEpoch = time.Date(2007, 3, 8, 3, 50, 35, 0, time.UTC)

// parseIridiumTimestamp parses a hex Iridium system timestamp (90ms ticks since epoch).
func parseIridiumTimestamp(hexStr string) time.Time {
	hexStr = strings.TrimSpace(hexStr)
	ticks, err := strconv.ParseUint(hexStr, 16, 64)
	if err != nil || ticks == 0 {
		return time.Now().UTC()
	}
	ms := ticks * 90
	return iridiumEpoch.Add(time.Duration(ms) * time.Millisecond)
}

func parseSBDRBResponse(raw []byte) ([]byte, error) {
	if len(raw) < 4 {
		return nil, fmt.Errorf("response too short for binary read")
	}

	// Skip AT echo — find plausible length value.
	// 9603N hardware buffer limit is 270 bytes MT (9603 Developer's Guide §3.3).
	// The 1960-byte figure in MAN0009 §5.147 applies to the 9522A/B L-Band
	// transceiver, not the 9603N.
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
// Filters out URCs (unsolicited result codes like +CGEV, +CMTI, +CREG)
// that may arrive between the command and the OK response. [MESHSAT-403]
func parseATValue(resp string) string {
	lines := strings.Split(resp, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "AT") || line == "OK" || line == "ERROR" {
			continue
		}
		// Skip URCs — they start with "+" and contain a colon (e.g., "+CGEV: EPS PDN ACT 1")
		if strings.HasPrefix(line, "+") && strings.Contains(line, ":") {
			continue
		}
		return line
	}
	return ""
}

// msstmEpoch is the Iridium MSSTM time origin: May 11, 2014 14:23:55 UTC.
// This is different from the MSGEO epoch (2007-03-08).
var msstmEpoch = time.Date(2014, 5, 11, 14, 23, 55, 0, time.UTC)

// parseMSSTM parses an AT-MSSTM response.
// Response format: "-MSSTM: XXXXXXXX" (8 hex chars, 90ms ticks since msstmEpoch)
// or "no network service" if not registered.
func parseMSSTM(resp string) (*IridiumTime, error) {
	if strings.Contains(resp, "no network") {
		return &IridiumTime{IsValid: false}, nil
	}

	// Find hex value after "-MSSTM:" or "MSSTM:"
	idx := strings.Index(resp, "MSSTM:")
	if idx < 0 {
		return nil, fmt.Errorf("no MSSTM in response: %q", resp)
	}
	hex := strings.TrimSpace(resp[idx+6:])
	// Trim trailing OK/newlines
	if nl := strings.IndexAny(hex, "\r\n"); nl >= 0 {
		hex = hex[:nl]
	}
	hex = strings.TrimSpace(hex)

	ticks, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return nil, fmt.Errorf("parse MSSTM hex %q: %w", hex, err)
	}

	ms := ticks * 90
	t := msstmEpoch.Add(time.Duration(ms) * time.Millisecond)

	return &IridiumTime{
		SystemTime: uint32(ticks),
		EpochUTC:   t.UTC().Format(time.RFC3339),
		IsValid:    true,
	}, nil
}
