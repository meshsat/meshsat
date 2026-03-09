package transport

// DirectAstrocastTransport implements AstrocastTransport with direct serial access
// to an Astrocast Astronode S module. Uses the Astronode binary protocol (not AT commands).

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"go.bug.st/serial"
)

const (
	astroDefaultBaud   = 9600
	astroReadTimeout   = 2 * time.Second
	astroFrameTimeout  = 5 * time.Second
	astroPollInterval  = 10 * time.Second
	astroMaxReadBuf    = 512
	astroReconnectWait = 10 * time.Second
)

// Known Astronode S USB VID:PID pairs
var knownAstrocastVIDPIDs = map[string]bool{
	"0403:6001": true, // FTDI FT232R (Astronode S devkit)
	"0403:6015": true, // FTDI FT231X (Astronode S USB adapter)
	"10c4:ea60": true, // CP2102 (common Astronode S breakout)
}

// DirectAstrocastTransport implements AstrocastTransport via direct serial to an Astronode S.
type DirectAstrocastTransport struct {
	port string // "/dev/ttyUSB2" or "auto"

	mu        sync.Mutex
	file      serial.Port
	connected bool

	// Event subscribers
	eventMu   sync.RWMutex
	eventSubs map[uint64]chan AstrocastEvent
	nextSubID uint64

	// Exclude ports (already claimed by other transports)
	excludePortFns []func() string

	// Event polling
	stopPollCh chan struct{}
	pollDone   chan struct{}
	pollActive bool
}

// NewDirectAstrocastTransport creates a new direct serial Astronode S transport.
// Pass "auto" or "" for port to use auto-detection.
func NewDirectAstrocastTransport(port string) *DirectAstrocastTransport {
	return &DirectAstrocastTransport{
		port:      port,
		eventSubs: make(map[uint64]chan AstrocastEvent),
	}
}

// GetPort returns the resolved serial port path.
func (t *DirectAstrocastTransport) GetPort() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.port
}

// SetExcludePortFuncs sets dynamic port resolvers for exclusion (e.g., Meshtastic, Iridium, Cellular).
func (t *DirectAstrocastTransport) SetExcludePortFuncs(fns []func() string) {
	t.excludePortFns = fns
}

// Send enqueues an uplink payload via the Astronode S binary protocol (PLD_ER command).
func (t *DirectAstrocastTransport) Send(ctx context.Context, data []byte) (*AstrocastResult, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("data is empty")
	}
	if len(data) > AstroMaxUplink {
		return nil, fmt.Errorf("data too large (max %d bytes for Astronode S uplink)", AstroMaxUplink)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureConnectedLocked(ctx); err != nil {
		return nil, err
	}

	// PLD_ER: enqueue uplink payload
	// Payload format: [payload_id:2 LE] [data:N]
	// We use 0x0000 as payload_id (module assigns its own)
	pldPayload := make([]byte, 2+len(data))
	pldPayload[0] = 0x00
	pldPayload[1] = 0x00
	copy(pldPayload[2:], data)

	frame := EncodeAstroFrame(AstroCmdPldER, pldPayload)
	if _, err := t.file.Write(frame); err != nil {
		t.disconnectLocked()
		return nil, fmt.Errorf("write PLD_ER: %w", err)
	}

	resp, err := t.readFrameLocked(astroFrameTimeout)
	if err != nil {
		return nil, fmt.Errorf("PLD_ER response: %w", err)
	}

	// Response command is PLD_ER + 0x80 for ACK, or 0xFF for NACK
	if resp.CommandID == 0xFF {
		errCode := uint8(0)
		if len(resp.Payload) > 0 {
			errCode = resp.Payload[0]
		}
		return nil, fmt.Errorf("PLD_ER rejected (error code 0x%02x)", errCode)
	}

	// ACK response payload: [payload_id:2 LE]
	var msgID uint16
	if len(resp.Payload) >= 2 {
		msgID = uint16(resp.Payload[0]) | uint16(resp.Payload[1])<<8
	}

	return &AstrocastResult{
		MessageID: msgID,
		Queued:    true,
	}, nil
}

// Receive dequeues a downlink payload via PLD_DR command.
// Returns nil data and nil error if no downlink is available.
func (t *DirectAstrocastTransport) Receive(ctx context.Context) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureConnectedLocked(ctx); err != nil {
		return nil, err
	}

	// PLD_DR: dequeue downlink payload
	frame := EncodeAstroFrame(AstroCmdPldDR, nil)
	if _, err := t.file.Write(frame); err != nil {
		t.disconnectLocked()
		return nil, fmt.Errorf("write PLD_DR: %w", err)
	}

	resp, err := t.readFrameLocked(astroFrameTimeout)
	if err != nil {
		// Timeout or error likely means no downlink available
		return nil, nil
	}

	if resp.CommandID == 0xFF {
		// NACK — no downlink available
		return nil, nil
	}

	// ACK response payload: [payload_id:2 LE] [data:N]
	if len(resp.Payload) < 2 {
		return nil, nil
	}
	data := resp.Payload[2:]

	// Free the downlink slot (PLD_FR)
	freeFrame := EncodeAstroFrame(AstroCmdPldFR, nil)
	t.file.Write(freeFrame)
	// Best effort — read and discard PLD_FR response
	t.readFrameLocked(2 * time.Second)

	return data, nil
}

// GetStatus returns the module connection status.
func (t *DirectAstrocastTransport) GetStatus(ctx context.Context) (*AstrocastStatus, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	status := &AstrocastStatus{
		Connected: t.connected,
		Port:      t.port,
	}

	if !t.connected {
		return status, nil
	}

	// Read event register to determine module state
	evtFrame := EncodeAstroFrame(AstroCmdEvtRR, nil)
	if _, err := t.file.Write(evtFrame); err != nil {
		return status, nil
	}

	resp, err := t.readFrameLocked(astroFrameTimeout)
	if err != nil {
		return status, nil
	}

	if resp.CommandID != 0xFF && len(resp.Payload) >= 1 {
		evtReg := resp.Payload[0]
		switch {
		case evtReg&AstroEvtSatDetected != 0:
			status.ModuleState = "communication"
		case evtReg&AstroEvtReset != 0:
			status.ModuleState = "reset"
		default:
			status.ModuleState = "idle"
		}
	} else {
		status.ModuleState = "idle"
	}

	return status, nil
}

// Subscribe returns a channel of events from the Astronode S module.
func (t *DirectAstrocastTransport) Subscribe(ctx context.Context) (<-chan AstrocastEvent, error) {
	t.mu.Lock()
	if err := t.ensureConnectedLocked(ctx); err != nil {
		t.mu.Unlock()
		return nil, err
	}
	t.mu.Unlock()

	ch := make(chan AstrocastEvent, 32)
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

// Close tears down the serial connection and stops the event poller.
func (t *DirectAstrocastTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.stopPoller()
	t.connected = false
	if t.file != nil {
		t.file.Close()
		t.file = nil
	}
	return nil
}

// ============================================================================
// Internal: connection management
// ============================================================================

func (t *DirectAstrocastTransport) ensureConnectedLocked(ctx context.Context) error {
	if t.connected && t.file != nil {
		return nil
	}
	return t.connectLocked(ctx)
}

func (t *DirectAstrocastTransport) connectLocked(ctx context.Context) error {
	port := t.port
	if port == "" || port == "auto" {
		var excludePorts []string
		for _, fn := range t.excludePortFns {
			if p := fn(); p != "" && p != "auto" {
				excludePorts = append(excludePorts, p)
			}
		}
		port = autoDetectAstrocast(excludePorts)
		if port == "" {
			return fmt.Errorf("no Astronode S module found")
		}
	}

	sp, err := openSerial(port, astroDefaultBaud)
	if err != nil {
		return err
	}

	t.file = sp
	t.port = port

	// Drain stale data
	drainPort(sp)

	// Probe: read event register to verify communication
	evtFrame := EncodeAstroFrame(AstroCmdEvtRR, nil)
	if _, err := sp.Write(evtFrame); err != nil {
		sp.Close()
		t.file = nil
		return fmt.Errorf("EVT_RR probe write: %w", err)
	}

	resp, err := t.readFrameLocked(astroFrameTimeout)
	if err != nil {
		sp.Close()
		t.file = nil
		return fmt.Errorf("EVT_RR probe read: %w", err)
	}

	// Any valid response (ACK or NACK) confirms communication
	_ = resp

	t.connected = true
	log.Info().Str("port", port).Msg("astronode S module connected")

	t.emitEvent(AstrocastEvent{
		Type:    "connected",
		Message: fmt.Sprintf("Connected to Astronode S on %s", port),
	})

	// Start event poller
	t.startPoller()

	return nil
}

func (t *DirectAstrocastTransport) disconnectLocked() {
	t.connected = false
	t.stopPoller()
	if t.file != nil {
		t.file.Close()
		t.file = nil
	}

	t.emitEvent(AstrocastEvent{
		Type:    "reset",
		Message: "Serial connection lost (will reconnect)",
	})
}

// ============================================================================
// Internal: event poller
// ============================================================================

// startPoller launches a goroutine that periodically reads the event register.
// Caller must hold t.mu.
func (t *DirectAstrocastTransport) startPoller() {
	if t.pollActive {
		return
	}
	t.stopPollCh = make(chan struct{})
	t.pollDone = make(chan struct{})
	t.pollActive = true
	go t.pollLoop()
}

// stopPoller stops the event poller. Caller must hold t.mu.
func (t *DirectAstrocastTransport) stopPoller() {
	if !t.pollActive {
		return
	}
	t.pollActive = false
	close(t.stopPollCh)

	done := t.pollDone
	t.mu.Unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		log.Warn().Msg("astronode: event poller did not exit in time")
	}
	t.mu.Lock()
}

func (t *DirectAstrocastTransport) pollLoop() {
	defer close(t.pollDone)

	ticker := time.NewTicker(astroPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-t.stopPollCh:
			return
		case <-ticker.C:
			t.pollEvents()
		}
	}
}

func (t *DirectAstrocastTransport) pollEvents() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.connected || t.file == nil {
		return
	}

	evtFrame := EncodeAstroFrame(AstroCmdEvtRR, nil)
	if _, err := t.file.Write(evtFrame); err != nil {
		log.Warn().Err(err).Msg("astronode: EVT_RR poll write failed")
		return
	}

	resp, err := t.readFrameLocked(astroFrameTimeout)
	if err != nil {
		return
	}

	if resp.CommandID == 0xFF || len(resp.Payload) < 1 {
		return
	}

	evtReg := resp.Payload[0]

	if evtReg&AstroEvtSatDetected != 0 {
		t.emitEvent(AstrocastEvent{
			Type:    "sat_search",
			Message: "Satellite detected",
		})
	}
	if evtReg&AstroEvtUplinkDone != 0 {
		t.emitEvent(AstrocastEvent{
			Type:    "uplink_ack",
			Message: "Uplink confirmed by satellite",
		})
	}
	if evtReg&AstroEvtDownlinkReady != 0 {
		t.emitEvent(AstrocastEvent{
			Type:    "downlink",
			Message: "Downlink payload available",
		})
	}
	if evtReg&AstroEvtReset != 0 {
		t.emitEvent(AstrocastEvent{
			Type:    "reset",
			Message: "Module reset detected",
		})
	}
}

// ============================================================================
// Internal: binary frame I/O
// ============================================================================

// readFrameLocked reads a complete Astronode binary frame from the serial port.
// Caller must hold t.mu.
func (t *DirectAstrocastTransport) readFrameLocked(timeout time.Duration) (*AstroFrame, error) {
	deadline := time.Now().Add(timeout)
	var accum []byte
	buf := make([]byte, astroMaxReadBuf)

	t.file.SetReadTimeout(100 * time.Millisecond)

	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("frame read timeout")
		}

		n, err := t.file.Read(buf)

		if n > 0 {
			accum = append(accum, buf[:n]...)

			// Try to extract a frame from accumulated data
			if frame := t.tryParseFrame(accum); frame != nil {
				return frame, nil
			}
		}

		if n == 0 && err == nil {
			continue // read timeout, no data
		}
		if err != nil {
			return nil, err
		}
	}
}

// tryParseFrame scans for a valid STX..ETX frame in the buffer.
func (t *DirectAstrocastTransport) tryParseFrame(data []byte) *AstroFrame {
	// Find STX (0x02)
	stxIdx := -1
	for i, b := range data {
		if b == 0x02 {
			stxIdx = i
			break
		}
	}
	if stxIdx < 0 {
		return nil
	}

	// Need at least STX + LEN(2) + CMD(1) + CRC(2) + ETX(1) = 7 bytes after STX
	remaining := data[stxIdx:]
	if len(remaining) < 7 {
		return nil
	}

	// Read length
	dataLen := int(remaining[1]) | int(remaining[2])<<8
	totalLen := 1 + 2 + dataLen + 2 + 1 // STX + LEN + data + CRC + ETX
	if len(remaining) < totalLen {
		return nil
	}

	frameData := remaining[:totalLen]
	frame, err := DecodeAstroFrame(frameData)
	if err != nil {
		return nil
	}
	return frame
}

// ============================================================================
// Internal: event emission
// ============================================================================

func (t *DirectAstrocastTransport) emitEvent(event AstrocastEvent) {
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
// USB Device Auto-Detection
// ============================================================================

// autoDetectAstrocast scans serial ports for an Astronode S module.
// Two-pass strategy: VID:PID match → binary protocol probe.
func autoDetectAstrocast(excludePorts []string) string {
	excludeSet := make(map[string]bool)
	for _, p := range excludePorts {
		excludeSet[p] = true
	}

	var ports []string
	if matches, err := filepath.Glob("/dev/ttyUSB*"); err == nil {
		ports = matches
	}
	if matches, err := filepath.Glob("/dev/ttyACM*"); err == nil {
		ports = append(ports, matches...)
	}

	// Pass 1: VID:PID match
	for _, port := range ports {
		if excludeSet[port] {
			continue
		}
		vidpid := findUSBVIDPID(port)
		if knownAstrocastVIDPIDs[vidpid] {
			// Skip if also matches Meshtastic or GPS
			if knownMeshtasticVIDPIDs[vidpid] || gpsVIDPIDs[vidpid] {
				continue
			}
			log.Info().Str("port", port).Str("vidpid", vidpid).Msg("astronode auto-detected by VID:PID")
			return port
		}
	}

	// Pass 2: binary protocol probe on unclaimed FTDI/CP210x ports
	for _, port := range ports {
		if excludeSet[port] {
			continue
		}
		vidpid := findUSBVIDPID(port)
		if knownMeshtasticVIDPIDs[vidpid] || gpsVIDPIDs[vidpid] {
			continue
		}
		// Only probe FTDI and CP210x devices (common Astronode adapters)
		if !strings.HasPrefix(vidpid, "0403:") && !strings.HasPrefix(vidpid, "10c4:") {
			continue
		}

		if probeAstronode(port) {
			log.Info().Str("port", port).Str("vidpid", vidpid).Msg("astronode auto-detected by protocol probe")
			return port
		}
	}

	return ""
}

// probeAstronode sends an EVT_RR command to check if a port is an Astronode S module.
func probeAstronode(portPath string) bool {
	sp, err := openSerial(portPath, astroDefaultBaud)
	if err != nil {
		return false
	}
	defer sp.Close()

	drainPort(sp)

	// Send EVT_RR (read event register) — benign probe
	frame := EncodeAstroFrame(AstroCmdEvtRR, nil)
	if _, err := sp.Write(frame); err != nil {
		return false
	}

	// Read response with short timeout
	deadline := time.Now().Add(3 * time.Second)
	var accum []byte
	buf := make([]byte, 128)

	sp.SetReadTimeout(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		n, err := sp.Read(buf)

		if n > 0 {
			accum = append(accum, buf[:n]...)
			// Check for valid STX...ETX frame
			for i, b := range accum {
				if b == 0x02 && len(accum[i:]) >= 7 {
					dataLen := int(accum[i+1]) | int(accum[i+2])<<8
					totalLen := 1 + 2 + dataLen + 2 + 1
					if len(accum[i:]) >= totalLen && accum[i+totalLen-1] == 0x03 {
						_, decErr := DecodeAstroFrame(accum[i : i+totalLen])
						if decErr == nil {
							return true
						}
					}
				}
			}
		}

		if err != nil {
			return false
		}
	}

	return false
}
