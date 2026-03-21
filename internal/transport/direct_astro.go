package transport

// DirectAstrocastTransport implements AstrocastTransport with direct serial access
// to an Astrocast Astronode S module. Uses the Astronode ASCII hex protocol.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
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
	astroFrameTimeout  = 1500 * time.Millisecond // official answer timeout
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

// SetPort sets the serial port path. Called by DeviceSupervisor.
func (t *DirectAstrocastTransport) SetPort(port string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.port = port
}

// IsConnected returns true if the transport has an active serial connection.
func (t *DirectAstrocastTransport) IsConnected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connected
}

// Send enqueues an uplink payload via the Astronode S protocol (PLD_ER command).
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

	// Error response: opcode 0xFF with 2-byte LE error code
	if IsErrorResponse(resp) {
		errCode := ParseErrorCode(resp.Payload)
		return nil, fmt.Errorf("PLD_ER rejected (error code 0x%04x)", errCode)
	}

	// ACK response payload: [payload_id:2 LE]
	var msgID uint16
	if len(resp.Payload) >= 2 {
		msgID = binary.LittleEndian.Uint16(resp.Payload[:2])
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

	if IsErrorResponse(resp) {
		// NACK — no downlink available (likely 0x2601 buffer empty)
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
	t.readFrameLocked(astroFrameTimeout)

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

	if !IsErrorResponse(resp) && len(resp.Payload) >= 1 {
		evtReg := resp.Payload[0]
		switch {
		case evtReg&AstroEvtBusy != 0:
			status.ModuleState = "busy"
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

// ReadSAK reads the satellite acknowledgment register. Returns nil if no ACK pending.
func (t *DirectAstrocastTransport) ReadSAK(ctx context.Context) (*AstrocastSAK, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureConnectedLocked(ctx); err != nil {
		return nil, err
	}

	frame := EncodeAstroFrame(AstroCmdSakRR, nil)
	if _, err := t.file.Write(frame); err != nil {
		t.disconnectLocked()
		return nil, fmt.Errorf("write SAK_RR: %w", err)
	}

	resp, err := t.readFrameLocked(astroFrameTimeout)
	if err != nil {
		return nil, fmt.Errorf("SAK_RR response: %w", err)
	}

	if IsErrorResponse(resp) {
		return nil, nil // no ACK pending (0x4501)
	}

	if len(resp.Payload) < 2 {
		return nil, nil
	}

	return &AstrocastSAK{
		PayloadID: binary.LittleEndian.Uint16(resp.Payload[:2]),
	}, nil
}

// ClearSAK clears the satellite acknowledgment register.
func (t *DirectAstrocastTransport) ClearSAK(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureConnectedLocked(ctx); err != nil {
		return err
	}

	frame := EncodeAstroFrame(AstroCmdSakCR, nil)
	if _, err := t.file.Write(frame); err != nil {
		t.disconnectLocked()
		return fmt.Errorf("write SAK_CR: %w", err)
	}

	resp, err := t.readFrameLocked(astroFrameTimeout)
	if err != nil {
		return fmt.Errorf("SAK_CR response: %w", err)
	}

	if IsErrorResponse(resp) {
		errCode := ParseErrorCode(resp.Payload)
		return fmt.Errorf("SAK_CR rejected (error code 0x%04x)", errCode)
	}

	return nil
}

// ReadCommand reads a downlink command from the Astrocast cloud. Returns nil if none pending.
func (t *DirectAstrocastTransport) ReadCommand(ctx context.Context) (*AstrocastCommand, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureConnectedLocked(ctx); err != nil {
		return nil, err
	}

	frame := EncodeAstroFrame(AstroCmdCmdRR, nil)
	if _, err := t.file.Write(frame); err != nil {
		t.disconnectLocked()
		return nil, fmt.Errorf("write CMD_RR: %w", err)
	}

	resp, err := t.readFrameLocked(astroFrameTimeout)
	if err != nil {
		return nil, fmt.Errorf("CMD_RR response: %w", err)
	}

	if IsErrorResponse(resp) {
		return nil, nil // no command pending
	}

	// Response: [created_date:4 LE] [data:N]
	if len(resp.Payload) < 4 {
		return nil, nil
	}

	createdDate := binary.LittleEndian.Uint32(resp.Payload[:4])

	return &AstrocastCommand{
		CreatedDate: createdDate,
		Data:        resp.Payload[4:],
	}, nil
}

// ClearCommand clears the downlink command register.
func (t *DirectAstrocastTransport) ClearCommand(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureConnectedLocked(ctx); err != nil {
		return err
	}

	frame := EncodeAstroFrame(AstroCmdCmdCR, nil)
	if _, err := t.file.Write(frame); err != nil {
		t.disconnectLocked()
		return fmt.Errorf("write CMD_CR: %w", err)
	}

	resp, err := t.readFrameLocked(astroFrameTimeout)
	if err != nil {
		return fmt.Errorf("CMD_CR response: %w", err)
	}

	if IsErrorResponse(resp) {
		errCode := ParseErrorCode(resp.Payload)
		return fmt.Errorf("CMD_CR rejected (error code 0x%04x)", errCode)
	}

	return nil
}

// WriteGeolocation writes a GPS position to the module. This is free (no MTU cost).
func (t *DirectAstrocastTransport) WriteGeolocation(ctx context.Context, geo AstrocastGeolocation) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureConnectedLocked(ctx); err != nil {
		return err
	}

	// Payload: [latitude:4 LE signed] [longitude:4 LE signed]
	payload := make([]byte, 8)
	binary.LittleEndian.PutUint32(payload[0:4], uint32(geo.Latitude))
	binary.LittleEndian.PutUint32(payload[4:8], uint32(geo.Longitude))

	frame := EncodeAstroFrame(AstroCmdGeoWR, payload)
	if _, err := t.file.Write(frame); err != nil {
		t.disconnectLocked()
		return fmt.Errorf("write GEO_WR: %w", err)
	}

	resp, err := t.readFrameLocked(astroFrameTimeout)
	if err != nil {
		return fmt.Errorf("GEO_WR response: %w", err)
	}

	if IsErrorResponse(resp) {
		errCode := ParseErrorCode(resp.Payload)
		return fmt.Errorf("GEO_WR rejected (error code 0x%04x)", errCode)
	}

	return nil
}

// GetNextContact returns the time until the next satellite contact opportunity.
func (t *DirectAstrocastTransport) GetNextContact(ctx context.Context) (*AstrocastNextContact, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureConnectedLocked(ctx); err != nil {
		return nil, err
	}

	frame := EncodeAstroFrame(AstroCmdNcoRR, nil)
	if _, err := t.file.Write(frame); err != nil {
		t.disconnectLocked()
		return nil, fmt.Errorf("write NCO_RR: %w", err)
	}

	resp, err := t.readFrameLocked(astroFrameTimeout)
	if err != nil {
		return nil, fmt.Errorf("NCO_RR response: %w", err)
	}

	if IsErrorResponse(resp) {
		return nil, fmt.Errorf("NCO_RR not available")
	}

	// Response: [delay:4 LE]
	if len(resp.Payload) < 4 {
		return nil, fmt.Errorf("NCO_RR payload too short: %d bytes", len(resp.Payload))
	}

	delay := binary.LittleEndian.Uint32(resp.Payload[:4])

	return &AstrocastNextContact{Delay: delay}, nil
}

// GetModuleState returns the module's internal state (TLV-encoded MST_RR response).
func (t *DirectAstrocastTransport) GetModuleState(ctx context.Context) (*AstrocastModuleState, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureConnectedLocked(ctx); err != nil {
		return nil, err
	}

	frame := EncodeAstroFrame(AstroCmdMstRR, nil)
	if _, err := t.file.Write(frame); err != nil {
		t.disconnectLocked()
		return nil, fmt.Errorf("write MST_RR: %w", err)
	}

	resp, err := t.readFrameLocked(astroFrameTimeout)
	if err != nil {
		return nil, fmt.Errorf("MST_RR response: %w", err)
	}

	if IsErrorResponse(resp) {
		return nil, fmt.Errorf("MST_RR not available")
	}

	tlv, err := parseTLV(resp.Payload)
	if err != nil {
		return nil, fmt.Errorf("MST_RR TLV parse: %w", err)
	}

	return &AstrocastModuleState{
		MsgQueued:       tlvUint8(tlv, AstroTLVMsgQueued),
		AckMsgQueued:    tlvUint8(tlv, AstroTLVAckMsgQueued),
		LastResetReason: tlvUint8(tlv, AstroTLVLastResetReason),
		Uptime:          tlvUint32LE(tlv, AstroTLVUptime),
	}, nil
}

// GetLastContact returns details about the last satellite contact (TLV-encoded LCD_RR response).
func (t *DirectAstrocastTransport) GetLastContact(ctx context.Context) (*AstrocastLastContact, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureConnectedLocked(ctx); err != nil {
		return nil, err
	}

	frame := EncodeAstroFrame(AstroCmdLcdRR, nil)
	if _, err := t.file.Write(frame); err != nil {
		t.disconnectLocked()
		return nil, fmt.Errorf("write LCD_RR: %w", err)
	}

	resp, err := t.readFrameLocked(astroFrameTimeout)
	if err != nil {
		return nil, fmt.Errorf("LCD_RR response: %w", err)
	}

	if IsErrorResponse(resp) {
		return nil, fmt.Errorf("LCD_RR not available")
	}

	tlv, err := parseTLV(resp.Payload)
	if err != nil {
		return nil, fmt.Errorf("LCD_RR TLV parse: %w", err)
	}

	return &AstrocastLastContact{
		StartTime: tlvUint32LE(tlv, AstroTLVStartTime),
		EndTime:   tlvUint32LE(tlv, AstroTLVEndTime),
		PeakRSSI:  tlvUint8(tlv, AstroTLVPeakRSSI),
		PeakTime:  tlvUint32LE(tlv, AstroTLVPeakTime),
	}, nil
}

// GetEnvironment returns signal environment details (TLV-encoded END_RR response).
func (t *DirectAstrocastTransport) GetEnvironment(ctx context.Context) (*AstrocastEnvironment, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureConnectedLocked(ctx); err != nil {
		return nil, err
	}

	frame := EncodeAstroFrame(AstroCmdEndRR, nil)
	if _, err := t.file.Write(frame); err != nil {
		t.disconnectLocked()
		return nil, fmt.Errorf("write END_RR: %w", err)
	}

	resp, err := t.readFrameLocked(astroFrameTimeout)
	if err != nil {
		return nil, fmt.Errorf("END_RR response: %w", err)
	}

	if IsErrorResponse(resp) {
		return nil, fmt.Errorf("END_RR not available")
	}

	tlv, err := parseTLV(resp.Payload)
	if err != nil {
		return nil, fmt.Errorf("END_RR TLV parse: %w", err)
	}

	return &AstrocastEnvironment{
		LastMACResult:   tlvUint8(tlv, AstroTLVLastMACResult),
		LastRSSI:        tlvUint8(tlv, AstroTLVLastRSSI),
		TimeSinceSatDet: tlvUint32LE(tlv, AstroTLVTimeSinceSatDet),
	}, nil
}

// GetPerformance returns performance counters (TLV-encoded PER_RR response, 14 counters).
func (t *DirectAstrocastTransport) GetPerformance(ctx context.Context) (*AstrocastPerformance, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureConnectedLocked(ctx); err != nil {
		return nil, err
	}

	frame := EncodeAstroFrame(AstroCmdPerRR, nil)
	if _, err := t.file.Write(frame); err != nil {
		t.disconnectLocked()
		return nil, fmt.Errorf("write PER_RR: %w", err)
	}

	resp, err := t.readFrameLocked(astroFrameTimeout)
	if err != nil {
		return nil, fmt.Errorf("PER_RR response: %w", err)
	}

	if IsErrorResponse(resp) {
		return nil, fmt.Errorf("PER_RR not available")
	}

	tlv, err := parseTLV(resp.Payload)
	if err != nil {
		return nil, fmt.Errorf("PER_RR TLV parse: %w", err)
	}

	return &AstrocastPerformance{
		SatSearchPhasesCnt:   tlvUint32LE(tlv, 0x01),
		SatDetectOpCnt:       tlvUint32LE(tlv, 0x02),
		SatDetectCnt:         tlvUint32LE(tlv, 0x03),
		SignalDemodPhasesCnt: tlvUint32LE(tlv, 0x04),
		SignalDemodAttempts:  tlvUint32LE(tlv, 0x05),
		SignalDemodSuccess:   tlvUint32LE(tlv, 0x06),
		AckDemodAttempts:     tlvUint32LE(tlv, 0x07),
		AckDemodSuccess:      tlvUint32LE(tlv, 0x08),
		Queued:               tlvUint32LE(tlv, 0x09),
		Dequeued:             tlvUint32LE(tlv, 0x0A),
		AckReceived:          tlvUint32LE(tlv, 0x0B),
		MsgTransmitted:       tlvUint32LE(tlv, 0x0C),
		MsgAcknowledged:      tlvUint32LE(tlv, 0x0D),
		MsgTransmitFailed:    tlvUint32LE(tlv, 0x0E),
	}, nil
}

// ReadConfig reads the module configuration via CFG_RR.
func (t *DirectAstrocastTransport) ReadConfig(ctx context.Context) (*AstrocastConfig, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureConnectedLocked(ctx); err != nil {
		return nil, err
	}

	frame := EncodeAstroFrame(AstroCmdCfgRR, nil)
	if _, err := t.file.Write(frame); err != nil {
		t.disconnectLocked()
		return nil, fmt.Errorf("write CFG_RR: %w", err)
	}

	resp, err := t.readFrameLocked(astroFrameTimeout)
	if err != nil {
		return nil, fmt.Errorf("CFG_RR response: %w", err)
	}

	if IsErrorResponse(resp) {
		errCode := ParseErrorCode(resp.Payload)
		return nil, fmt.Errorf("CFG_RR rejected (error code 0x%04x)", errCode)
	}

	// Parse configuration response (fields vary by firmware version)
	cfg := &AstrocastConfig{}
	if len(resp.Payload) >= 1 {
		cfg.ProductID = resp.Payload[0]
	}
	if len(resp.Payload) >= 2 {
		cfg.HardwareRev = resp.Payload[1]
	}
	if len(resp.Payload) >= 3 {
		cfg.FirmwareMajor = resp.Payload[2]
	}
	if len(resp.Payload) >= 4 {
		cfg.FirmwareMinor = resp.Payload[3]
	}
	if len(resp.Payload) >= 5 {
		cfg.FirmwarePatch = resp.Payload[4]
	}
	if len(resp.Payload) >= 6 {
		flags := resp.Payload[5]
		cfg.WithPLD = flags&0x01 != 0
		cfg.WithGeo = flags&0x02 != 0
		cfg.WithEphemeris = flags&0x04 != 0
		cfg.WithDeepSleep = flags&0x08 != 0
		cfg.WithAckEvent = flags&0x10 != 0
		cfg.WithResetEvent = flags&0x20 != 0
		cfg.WithCmdEvent = flags&0x40 != 0
		cfg.WithTxPend = flags&0x80 != 0
	}

	return cfg, nil
}

// SaveConfig saves the current configuration to flash via CFG_SR.
func (t *DirectAstrocastTransport) SaveConfig(ctx context.Context) error {
	return t.sendSimpleCommand(ctx, AstroCmdCfgSR, "CFG_SR")
}

// FactoryReset resets the module to factory defaults via CFG_FR.
func (t *DirectAstrocastTransport) FactoryReset(ctx context.Context) error {
	return t.sendSimpleCommand(ctx, AstroCmdCfgFR, "CFG_FR")
}

// ReadRTC reads the RTC time from the module (unix timestamp).
func (t *DirectAstrocastTransport) ReadRTC(ctx context.Context) (uint32, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureConnectedLocked(ctx); err != nil {
		return 0, err
	}

	frame := EncodeAstroFrame(AstroCmdRtcRR, nil)
	if _, err := t.file.Write(frame); err != nil {
		t.disconnectLocked()
		return 0, fmt.Errorf("write RTC_RR: %w", err)
	}

	resp, err := t.readFrameLocked(astroFrameTimeout)
	if err != nil {
		return 0, fmt.Errorf("RTC_RR response: %w", err)
	}

	if IsErrorResponse(resp) {
		return 0, fmt.Errorf("RTC_RR not available")
	}

	if len(resp.Payload) < 4 {
		return 0, fmt.Errorf("RTC_RR payload too short: %d bytes", len(resp.Payload))
	}

	return binary.LittleEndian.Uint32(resp.Payload[:4]), nil
}

// ReadGUID reads the module GUID.
func (t *DirectAstrocastTransport) ReadGUID(ctx context.Context) (string, error) {
	return t.readStringCommand(ctx, AstroCmdMgiRR, "MGI_RR")
}

// ReadSerialNumber reads the module serial number.
func (t *DirectAstrocastTransport) ReadSerialNumber(ctx context.Context) (string, error) {
	return t.readStringCommand(ctx, AstroCmdMsnRR, "MSN_RR")
}

// ReadProductNumber reads the module product number.
func (t *DirectAstrocastTransport) ReadProductNumber(ctx context.Context) (string, error) {
	return t.readStringCommand(ctx, AstroCmdMpnRR, "MPN_RR")
}

// SaveContext saves the module context via CTX_SR.
func (t *DirectAstrocastTransport) SaveContext(ctx context.Context) error {
	return t.sendSimpleCommand(ctx, AstroCmdCtxSR, "CTX_SR")
}

// ClearPerformance clears the performance counters via PER_CR.
func (t *DirectAstrocastTransport) ClearPerformance(ctx context.Context) error {
	return t.sendSimpleCommand(ctx, AstroCmdPerCR, "PER_CR")
}

// ============================================================================
// Internal: generic command helpers
// ============================================================================

// sendSimpleCommand sends a command with no payload and expects a simple ACK/NACK.
func (t *DirectAstrocastTransport) sendSimpleCommand(ctx context.Context, cmd uint8, name string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureConnectedLocked(ctx); err != nil {
		return err
	}

	frame := EncodeAstroFrame(cmd, nil)
	if _, err := t.file.Write(frame); err != nil {
		t.disconnectLocked()
		return fmt.Errorf("write %s: %w", name, err)
	}

	resp, err := t.readFrameLocked(astroFrameTimeout)
	if err != nil {
		return fmt.Errorf("%s response: %w", name, err)
	}

	if IsErrorResponse(resp) {
		errCode := ParseErrorCode(resp.Payload)
		return fmt.Errorf("%s rejected (error code 0x%04x)", name, errCode)
	}

	return nil
}

// readStringCommand sends a command and returns the response payload as a hex-encoded or ASCII string.
func (t *DirectAstrocastTransport) readStringCommand(ctx context.Context, cmd uint8, name string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureConnectedLocked(ctx); err != nil {
		return "", err
	}

	frame := EncodeAstroFrame(cmd, nil)
	if _, err := t.file.Write(frame); err != nil {
		t.disconnectLocked()
		return "", fmt.Errorf("write %s: %w", name, err)
	}

	resp, err := t.readFrameLocked(astroFrameTimeout)
	if err != nil {
		return "", fmt.Errorf("%s response: %w", name, err)
	}

	if IsErrorResponse(resp) {
		errCode := ParseErrorCode(resp.Payload)
		return "", fmt.Errorf("%s rejected (error code 0x%04x)", name, errCode)
	}

	if len(resp.Payload) == 0 {
		return "", nil
	}

	// Return as hex-encoded string for binary identifiers
	return hex.EncodeToString(resp.Payload), nil
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

	if IsErrorResponse(resp) || len(resp.Payload) < 1 {
		return
	}

	evtReg := resp.Payload[0]

	if evtReg&AstroEvtSAKAvail != 0 {
		t.emitEvent(AstrocastEvent{
			Type:    "sak_available",
			Message: "Satellite acknowledged a queued message",
		})
	}
	if evtReg&AstroEvtReset != 0 {
		t.emitEvent(AstrocastEvent{
			Type:    "reset",
			Message: "Module reset detected",
		})
	}
	if evtReg&AstroEvtCmdAvail != 0 {
		t.emitEvent(AstrocastEvent{
			Type:    "cmd_available",
			Message: "Downlink command available",
		})
	}
	if evtReg&AstroEvtBusy != 0 {
		t.emitEvent(AstrocastEvent{
			Type:    "busy",
			Message: "Module communicating with satellite",
		})
	}
}

// ============================================================================
// Internal: ASCII hex frame I/O
// ============================================================================

// readFrameLocked reads a complete Astronode ASCII hex frame from the serial port.
// Frame format: [STX 0x02] [hex data] [ETX 0x03]
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

// tryParseFrame scans for a valid STX..ETX ASCII hex frame in the buffer.
func (t *DirectAstrocastTransport) tryParseFrame(data []byte) *AstroFrame {
	// Find STX (0x02)
	stxIdx := bytes.IndexByte(data, 0x02)
	if stxIdx < 0 {
		return nil
	}

	// Find ETX (0x03) after STX
	remaining := data[stxIdx:]
	etxIdx := bytes.IndexByte(remaining[1:], 0x03)
	if etxIdx < 0 {
		return nil // no ETX yet, need more data
	}
	etxIdx += 1 // adjust for the offset from remaining[1:]

	// Extract the complete frame [STX...ETX]
	frameData := remaining[:etxIdx+1]

	// Minimum frame: STX + opcode(2) + CRC(4) + ETX = 8 bytes
	if len(frameData) < 8 {
		return nil
	}

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
// Two-pass strategy: VID:PID match -> protocol probe.
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

	// Pass 2: protocol probe on unclaimed FTDI/CP210x ports
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

	// Send EVT_RR (read event register) — benign probe using ASCII hex framing
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
			// Check for valid STX...ETX ASCII hex frame
			stxIdx := bytes.IndexByte(accum, 0x02)
			if stxIdx >= 0 {
				etxIdx := bytes.IndexByte(accum[stxIdx+1:], 0x03)
				if etxIdx >= 0 {
					frameData := accum[stxIdx : stxIdx+1+etxIdx+1]
					if len(frameData) >= 8 { // STX + opcode(2) + CRC(4) + ETX
						_, decErr := DecodeAstroFrame(frameData)
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
