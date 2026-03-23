package transport

// JSPR (JSON Serial Protocol for REST) implementation for RockBLOCK 9704.
// Ported from the MIT-licensed C library at github.com/rock7/RockBLOCK-9704.
//
// Protocol: line-based text over serial at 230400 baud.
//   Request:  "METHOD target {json}\r"
//   Response: "CODE target {json}\r"
// Unsolicited messages use code 299.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"go.bug.st/serial"
)

// JSPR constants
const (
	jsprBaud            = 230400
	jsprRxBufSize       = 8192
	jsprMaxTargetLen    = 30
	jsprMaxSegmentData  = 1446
	jsprIMTCRCSize      = 2
	jsprMaxPayload      = 100000 // 100 KB max message
	jsprMaxTopics       = 20
	jsprReadTimeout     = 50 * time.Millisecond // poll granularity
	jsprResponseTimeout = 10 * time.Second      // max wait for a response
	jsprMOTimeout       = 180 * time.Second     // max wait for MO final status

	// Well-known topic IDs
	jsprRawTopic    = 244
	jsprPurpleTopic = 313
	jsprPinkTopic   = 314
	jsprRedTopic    = 315
	jsprOrangeTopic = 316
	jsprYellowTopic = 317
)

// JSPR response codes
const (
	jsprCodeOK          = 200
	jsprCodeUnsolicited = 299
	jsprCodeNoAPI       = 400
	jsprCodeBadReqType  = 401
	jsprCodeAlreadySet  = 402
	jsprCodeCmdTooLong  = 403
	jsprCodeUnknown     = 404
	jsprCodeMalformed   = 405
	jsprCodeNotAllowed  = 406
	jsprCodeBadJSON     = 407
	jsprCodeFailed      = 408
	jsprCodeUnauth      = 409
	jsprCodeNoSIM       = 410
	jsprCodeSerialErr   = 500
)

// jsprResponse represents a parsed JSPR response from the modem.
type jsprResponse struct {
	Code   int
	Target string
	JSON   json.RawMessage
}

// pendingRequest represents an in-flight JSPR command awaiting a response.
type pendingRequest struct {
	target   string
	ch       chan jsprResponse // buffered, capacity 1
	deadline time.Time
}

// jsprConn manages the JSPR serial connection and protocol state.
type jsprConn struct {
	port serial.Port

	// Write mutex — protects serial writes only (held for microseconds)
	writeMu sync.Mutex

	// Pending request map — keyed by JSPR target name
	pendingMu sync.Mutex
	pending   map[string]*pendingRequest

	// Reader goroutine lifecycle
	readerDone chan struct{}
	readerStop chan struct{}

	// Unsolicited message buffer
	unsolMu   sync.Mutex
	unsol     []jsprResponse
	unsolCond *sync.Cond

	// MO state
	moRef int // request_reference counter (1-100)

	// Persistent line buffer for the reader goroutine.
	// Survives across readOneLine calls so partial reads accumulate correctly
	// when the modem pauses mid-line (common on the 9704 at 230400 baud).
	lineBuf strings.Builder
}

// newJSPRConn wraps an already-opened serial port for JSPR communication.
func newJSPRConn(port serial.Port) *jsprConn {
	c := &jsprConn{
		port:    port,
		moRef:   0,
		pending: make(map[string]*pendingRequest),
	}
	c.unsolCond = sync.NewCond(&c.unsolMu)
	return c
}

// startReader launches the background reader goroutine.
// Must be called after drainSerial() and before any sendRequest calls.
func (c *jsprConn) startReader() {
	c.readerStop = make(chan struct{})
	c.readerDone = make(chan struct{})
	go c.readerLoop()
}

// stopReader signals the reader goroutine to stop and waits for it to exit.
func (c *jsprConn) stopReader() {
	if c.readerStop != nil {
		close(c.readerStop)
	}
	if c.readerDone != nil {
		<-c.readerDone
	}
}

// readerLoop is the ONLY goroutine that reads from the serial port.
// It dispatches responses to pending callers and buffers unsolicited messages.
func (c *jsprConn) readerLoop() {
	defer close(c.readerDone)

	for {
		select {
		case <-c.readerStop:
			return
		default:
		}

		// Acquire writeMu during read to prevent concurrent R/W on the serial port.
		// The go-serial library may not support simultaneous Read() and Write() on
		// the same fd — concurrent access can cause the Read() to hang permanently.
		// writeMu is released between reads so sendRequest can acquire it to write.
		c.writeMu.Lock()
		resp, err := c.readOneLine(100 * time.Millisecond)
		c.writeMu.Unlock()
		if err != nil {
			log.Debug().Err(err).Msg("jspr: reader loop read error")
			continue
		}
		if resp == nil {
			continue // no data, loop back and check stop
		}

		log.Debug().Int("code", resp.Code).Str("target", resp.Target).Msg("jspr: reader got response")

		if resp.Code == jsprCodeUnsolicited {
			c.bufferUnsolicited(*resp)
			continue
		}

		// Dispatch to pending request
		c.pendingMu.Lock()
		if pr, ok := c.pending[resp.Target]; ok {
			delete(c.pending, resp.Target)
			c.pendingMu.Unlock()
			log.Debug().Int("code", resp.Code).Str("target", resp.Target).Msg("jspr: dispatched to caller")
			// Non-blocking send (channel is buffered with capacity 1)
			select {
			case pr.ch <- *resp:
			default:
			}
			continue
		}

		// Error responses (code >= 400) may use a generic target like "MALFORMED"
		// instead of echoing the requested target. Try any pending request.
		if resp.Code >= 400 {
			var anyPR *pendingRequest
			var anyKey string
			for k, pr := range c.pending {
				anyPR = pr
				anyKey = k
				break
			}
			if anyPR != nil {
				delete(c.pending, anyKey)
				c.pendingMu.Unlock()
				log.Debug().Int("code", resp.Code).Str("target", resp.Target).Str("expected", anyPR.target).Msg("jspr: delivering error response with mismatched target")
				select {
				case anyPR.ch <- *resp:
				default:
				}
				continue
			}
		}
		c.pendingMu.Unlock()

		log.Debug().Int("code", resp.Code).Str("target", resp.Target).Msg("jspr: discarding unmatched response")
	}
}

// waitForUnsolicited waits until a new unsolicited message is buffered, or timeout.
// Returns true if signalled, false on timeout.
func (c *jsprConn) waitForUnsolicited(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		c.unsolMu.Lock()
		c.unsolCond.Wait()
		c.unsolMu.Unlock()
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-time.After(timeout):
		// Unblock the waiting goroutine by broadcasting.
		// The goroutine will wake up, unlock, and exit.
		c.unsolCond.Broadcast()
		<-done
		return false
	}
}

// nextRef increments and returns the next request_reference (1-100, wrapping).
func (c *jsprConn) nextRef() int {
	c.moRef++
	if c.moRef > 100 {
		c.moRef = 1
	}
	return c.moRef
}

// sendRequest sends a JSPR request and waits for a matching response.
// Method is "GET" or "PUT". The write is protected by writeMu (held briefly).
// The response is delivered asynchronously by the reader goroutine.
func (c *jsprConn) sendRequest(method, target string, payload interface{}) (*jsprResponse, error) {
	return c.sendRequestWithTimeout(method, target, payload, jsprResponseTimeout)
}

// sendRequestWithTimeout is like sendRequest but with a custom timeout.
// Used for commands like constellationState where the official library
// uses a 1-second timeout instead of the default 10 seconds.
func (c *jsprConn) sendRequestWithTimeout(method, target string, payload interface{}, timeout time.Duration) (*jsprResponse, error) {
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("jspr: marshal payload: %w", err)
	}

	// The RockBLOCK 9704 firmware's JSON parser requires spaces after colons
	// and commas, matching the format used by the official C library (snprintf
	// with "key": value). Go's json.Marshal produces compact JSON without
	// spaces, which the modem rejects with 407 BAD_JSON.
	jsonStr := jsprSpacedJSON(jsonBytes)
	line := fmt.Sprintf("%s %s %s\r", method, target, jsonStr)

	// Register pending request before writing (avoid race with reader)
	pr := &pendingRequest{
		target:   target,
		ch:       make(chan jsprResponse, 1),
		deadline: time.Now().Add(timeout),
	}
	c.pendingMu.Lock()
	c.pending[target] = pr
	c.pendingMu.Unlock()

	// Write command — brief lock
	c.writeMu.Lock()
	n, writeErr := c.port.Write([]byte(line))
	c.writeMu.Unlock()
	if writeErr != nil {
		c.pendingMu.Lock()
		delete(c.pending, target)
		c.pendingMu.Unlock()
		return nil, fmt.Errorf("jspr: write: %w", writeErr)
	}
	log.Debug().Str("target", target).Int("bytes", n).Str("method", method).Msg("jspr: command written")

	// Wait for response from reader goroutine
	select {
	case resp := <-pr.ch:
		return &resp, nil
	case <-time.After(timeout):
		c.pendingMu.Lock()
		delete(c.pending, target)
		c.pendingMu.Unlock()
		return nil, fmt.Errorf("jspr: timeout waiting for %s response", target)
	}
}

// jsprSpacedJSON adds spaces after colons and commas in JSON to match the
// official RockBLOCK-9704 C library format. The modem firmware's parser
// rejects compact JSON (407 BAD_JSON). Safe for all JSPR payloads since
// values never contain bare colons or commas (strings are quoted, base64
// uses [A-Za-z0-9+/=]).
func jsprSpacedJSON(data []byte) string {
	s := string(data)
	// Only process non-empty objects — leave "{}" as-is
	if s == "{}" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	inString := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '"' && (i == 0 || s[i-1] != '\\') {
			inString = !inString
		}
		b.WriteByte(ch)
		if !inString {
			if ch == ':' || ch == ',' {
				b.WriteByte(' ')
			}
		}
	}
	return b.String()
}

// readOneLine reads one JSPR response line from serial.
// Uses c.lineBuf to persist partial reads across calls — this is critical because
// the 9704 modem at 230400 baud can pause mid-line (e.g., between code and JSON body),
// causing a single response to span multiple read windows.
// Returns nil, nil on read timeout (no data available and no partial line buffered).
func (c *jsprConn) readOneLine(maxWait time.Duration) (*jsprResponse, error) {
	deadline := time.Now().Add(maxWait)
	var buf [1]byte

	c.port.SetReadTimeout(jsprReadTimeout)

	for time.Now().Before(deadline) {
		n, err := c.port.Read(buf[:])
		if n == 0 && err == nil {
			if c.lineBuf.Len() == 0 {
				return nil, nil // no data at all, no partial line
			}
			continue // partial line in buffer, keep reading until deadline
		}
		if err != nil {
			if c.lineBuf.Len() == 0 {
				return nil, nil
			}
			// Read error with partial data — discard the partial line
			c.lineBuf.Reset()
			return nil, fmt.Errorf("jspr: read error: %w", err)
		}

		b := buf[0]

		// Strip non-printable chars before response code (modem may send DC1=0x11 on boot)
		if c.lineBuf.Len() == 0 && (b < 0x20 || b > 0x7E) && b != '\r' {
			continue
		}

		if b == '\r' {
			if c.lineBuf.Len() == 0 {
				continue // empty line
			}
			line := c.lineBuf.String()
			c.lineBuf.Reset()
			return parseJSPRLine(line)
		}

		if c.lineBuf.Len() >= jsprRxBufSize {
			c.lineBuf.Reset()
			return nil, fmt.Errorf("jspr: line too long, discarded")
		}

		c.lineBuf.WriteByte(b)
	}

	// Deadline reached. If we have partial data, DON'T discard it —
	// the next call will continue accumulating. Just return nil to let
	// the readerLoop check the stop channel and come back.
	return nil, nil
}

// bufferUnsolicited adds an unsolicited response to the queue and signals waiters.
func (c *jsprConn) bufferUnsolicited(resp jsprResponse) {
	c.unsolMu.Lock()
	c.unsol = append(c.unsol, resp)
	c.unsolMu.Unlock()
	c.unsolCond.Broadcast()
}

// takeUnsolicited removes and returns all unsolicited messages matching a target.
func (c *jsprConn) takeUnsolicited(target string) []jsprResponse {
	c.unsolMu.Lock()
	defer c.unsolMu.Unlock()

	var matched []jsprResponse
	var remaining []jsprResponse
	for _, r := range c.unsol {
		if r.Target == target {
			matched = append(matched, r)
		} else {
			remaining = append(remaining, r)
		}
	}
	c.unsol = remaining
	return matched
}

// takeFirstUnsolicited removes and returns the first unsolicited message matching a target.
func (c *jsprConn) takeFirstUnsolicited(target string) *jsprResponse {
	c.unsolMu.Lock()
	defer c.unsolMu.Unlock()

	for i, r := range c.unsol {
		if r.Target == target {
			c.unsol = append(c.unsol[:i], c.unsol[i+1:]...)
			return &r
		}
	}
	return nil
}

// drainSerial clears any pending data from the serial port.
func (c *jsprConn) drainSerial() {
	c.port.SetReadTimeout(100 * time.Millisecond)
	buf := make([]byte, 1024)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		n, _ := c.port.Read(buf)
		if n == 0 {
			break
		}
	}
}

// parseJSPRLine parses a JSPR response line: "CODE target {json}"
func parseJSPRLine(line string) (*jsprResponse, error) {
	if len(line) < 3 {
		return nil, fmt.Errorf("jspr: line too short: %q", line)
	}

	// Response code: first 3 chars
	codeStr := line[:3]
	code := 0
	for _, ch := range codeStr {
		if ch < '0' || ch > '9' {
			return nil, fmt.Errorf("jspr: invalid response code: %q", codeStr)
		}
		code = code*10 + int(ch-'0')
	}

	rest := line[3:]
	if len(rest) == 0 || rest[0] != ' ' {
		return nil, fmt.Errorf("jspr: missing space after code: %q", line)
	}
	rest = rest[1:]

	// Target: up to next space
	spaceIdx := strings.IndexByte(rest, ' ')
	if spaceIdx < 0 {
		// No JSON payload
		return &jsprResponse{Code: code, Target: rest, JSON: json.RawMessage("{}")}, nil
	}

	target := rest[:spaceIdx]
	jsonPart := rest[spaceIdx+1:]

	// Find JSON (from first '{' to end)
	braceIdx := strings.IndexByte(jsonPart, '{')
	if braceIdx < 0 {
		return &jsprResponse{Code: code, Target: target, JSON: json.RawMessage("{}")}, nil
	}

	return &jsprResponse{
		Code:   code,
		Target: target,
		JSON:   json.RawMessage(jsonPart[braceIdx:]),
	}, nil
}

// ============================================================================
// CRC-16/CCITT (XModem) — polynomial 0x1021, init 0x0000
// ============================================================================

var crc16CCITTTable [256]uint16

func init() {
	for i := 0; i < 256; i++ {
		crc := uint16(i) << 8
		for j := 0; j < 8; j++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
		crc16CCITTTable[i] = crc
	}
}

// crc16CCITT computes CRC-16/CCITT (XModem) over the given data.
func crc16CCITT(data []byte) uint16 {
	crc := uint16(0x0000)
	for _, b := range data {
		crc = (crc << 8) ^ crc16CCITTTable[byte(crc>>8)^b]
	}
	return crc
}

// appendIMTCRC appends 2-byte CRC-16/CCITT (big-endian) to payload.
func appendIMTCRC(data []byte) []byte {
	crc := crc16CCITT(data)
	return append(data, byte(crc>>8), byte(crc&0xFF))
}

// verifyIMTCRC checks that the last 2 bytes are a valid CRC of the preceding data.
// Returns the payload without CRC, or error if invalid.
func verifyIMTCRC(data []byte) ([]byte, error) {
	if len(data) < jsprIMTCRCSize {
		return nil, fmt.Errorf("data too short for CRC: %d bytes", len(data))
	}
	payload := data[:len(data)-jsprIMTCRCSize]
	expected := crc16CCITT(payload)
	got := uint16(data[len(data)-2])<<8 | uint16(data[len(data)-1])
	if got != expected {
		return nil, fmt.Errorf("CRC mismatch: expected 0x%04X, got 0x%04X", expected, got)
	}
	return payload, nil
}

// ============================================================================
// JSPR high-level operations
// ============================================================================

// jsprAPIVersion represents the API version structure.
type jsprAPIVersion struct {
	Major int `json:"major"`
	Minor int `json:"minor"`
	Patch int `json:"patch"`
}

type jsprAPIVersionResponse struct {
	SupportedVersions []jsprAPIVersion `json:"supported_versions"`
	ActiveVersion     *jsprAPIVersion  `json:"active_version,omitempty"`
}

type jsprAPIVersionPut struct {
	ActiveVersion jsprAPIVersion `json:"active_version"`
}

type jsprSimConfigResponse struct {
	Interface string `json:"interface"`
}

type jsprSimConfigPut struct {
	Interface string `json:"interface"`
}

type jsprOperationalStateResponse struct {
	State  string `json:"state"`
	Reason int    `json:"reason"`
}

type jsprOperationalStatePut struct {
	State string `json:"state"`
}

type jsprConstellationState struct {
	ConstellationVisible bool `json:"constellation_visible"`
	SignalBars           int  `json:"signal_bars"`
	SignalLevel          int  `json:"signal_level"`
}

type jsprHWInfo struct {
	HWVersion    string `json:"hw_version"`
	SerialNumber string `json:"serial_number"`
	IMEI         string `json:"imei"`
	BoardTemp    int    `json:"board_temp"`
}

type jsprSimStatus struct {
	CardPresent  bool   `json:"card_present"`
	SIMConnected bool   `json:"sim_connected"`
	ICCID        string `json:"iccid"`
}

type jsprFirmwareGet struct {
	Slot string `json:"slot"`
}

type jsprFirmwareResponse struct {
	Slot     string         `json:"slot"`
	Validity int            `json:"validity"`
	Version  jsprAPIVersion `json:"version"`
	Hash     string         `json:"hash"`
}

type jsprMORequest struct {
	TopicID          int `json:"topic_id"`
	MessageLength    int `json:"message_length"`
	RequestReference int `json:"request_reference"`
}

type jsprMOResponse struct {
	TopicID          int    `json:"topic_id"`
	RequestReference int    `json:"request_reference"`
	MessageResponse  string `json:"message_response"`
	MessageID        int    `json:"message_id"`
}

type jsprMOSegmentRequest struct {
	TopicID       int `json:"topic_id"`
	MessageID     int `json:"message_id"`
	SegmentLength int `json:"segment_length"`
	SegmentStart  int `json:"segment_start"`
}

type jsprMOSegmentPut struct {
	TopicID       int    `json:"topic_id"`
	MessageID     int    `json:"message_id"`
	SegmentLength int    `json:"segment_length"`
	SegmentStart  int    `json:"segment_start"`
	Data          string `json:"data"` // base64
}

type jsprMOStatus struct {
	TopicID       int    `json:"topic_id"`
	MessageID     int    `json:"message_id"`
	FinalMOStatus string `json:"final_mo_status"`
}

type jsprMTAnnounce struct {
	TopicID          int `json:"topic_id"`
	MessageID        int `json:"message_id"`
	MessageLengthMax int `json:"message_length_max"`
}

type jsprMTSegment struct {
	TopicID       int    `json:"topic_id"`
	MessageID     int    `json:"message_id"`
	SegmentLength int    `json:"segment_length"`
	SegmentStart  int    `json:"segment_start"`
	Data          string `json:"data"` // base64
}

type jsprMTStatus struct {
	TopicID       int    `json:"topic_id"`
	MessageID     int    `json:"message_id"`
	FinalMTStatus string `json:"final_mt_status"`
}

type jsprProvisioningTopic struct {
	TopicID            int    `json:"topic_id"`
	TopicName          string `json:"topic_name"`
	Priority           string `json:"priority"`
	DiscardTimeSeconds int    `json:"discard_time_seconds"`
	MaxQueueDepth      int    `json:"max_queue_depth"`
}

type jsprProvisioningResponse struct {
	Provisioning []jsprProvisioningTopic `json:"provisioning"`
}

// ============================================================================
// High-level JSPR operations (used by DirectIMTTransport)
// ============================================================================

// jsprBegin initialises the modem: negotiates API version, configures SIM, sets active state.
// Matches the handshake sequence from the official RockBLOCK-9704 C library.
func (c *jsprConn) jsprBegin() error {
	// drainSerial() is called by the caller before startReader(), so serial
	// is already clean at this point. Just add the settling delay.
	time.Sleep(5 * time.Millisecond) // settling time after drain (per official library)

	// 1. Negotiate API version — retry up to 3 times with delay (official library does 2).
	//    The modem may respond with "403 MALFORMED" on the first attempt if it is
	//    still processing bootInfo or has stale data from a prior probe session.
	var apiResp jsprAPIVersionResponse
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(5 * time.Millisecond)
			c.drainSerial() // clear any stale data between retries
		}

		resp, err := c.sendRequest("GET", "apiVersion", struct{}{})
		if err != nil {
			lastErr = fmt.Errorf("attempt %d: %w", attempt+1, err)
			log.Debug().Err(err).Int("attempt", attempt+1).Msg("jspr: GET apiVersion failed, retrying")
			continue
		}
		if resp.Code != jsprCodeOK {
			lastErr = fmt.Errorf("attempt %d: code %d (target=%s)", attempt+1, resp.Code, resp.Target)
			log.Debug().Int("code", resp.Code).Str("target", resp.Target).Int("attempt", attempt+1).Msg("jspr: GET apiVersion error, retrying")
			continue
		}

		if err := json.Unmarshal(resp.JSON, &apiResp); err != nil {
			lastErr = fmt.Errorf("attempt %d: parse: %w", attempt+1, err)
			continue
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		return fmt.Errorf("jspr begin: get apiVersion: %w", lastErr)
	}

	if apiResp.ActiveVersion == nil {
		// Select the first supported version (matching the official C library which
		// uses supportedVersions[0]). Fall back to v1.0.0 if the list is empty.
		ver := jsprAPIVersion{Major: 1, Minor: 0, Patch: 0}
		if len(apiResp.SupportedVersions) > 0 {
			ver = apiResp.SupportedVersions[0]
		}
		log.Debug().Int("major", ver.Major).Int("minor", ver.Minor).Int("patch", ver.Patch).Msg("jspr: setting API version")
		putResp, err := c.sendRequest("PUT", "apiVersion", jsprAPIVersionPut{
			ActiveVersion: ver,
		})
		if err != nil {
			return fmt.Errorf("jspr begin: set apiVersion: %w", err)
		}
		if putResp.Code != jsprCodeOK && putResp.Code != jsprCodeAlreadySet {
			return fmt.Errorf("jspr begin: set apiVersion returned code %d", putResp.Code)
		}
	}

	// 2. Configure SIM
	simResp, err := c.sendRequest("GET", "simConfig", struct{}{})
	if err != nil {
		return fmt.Errorf("jspr begin: get simConfig: %w", err)
	}

	var simCfg jsprSimConfigResponse
	if err := json.Unmarshal(simResp.JSON, &simCfg); err != nil {
		return fmt.Errorf("jspr begin: parse simConfig: %w", err)
	}

	if simCfg.Interface != "internal" {
		putResp, err := c.sendRequest("PUT", "simConfig", jsprSimConfigPut{Interface: "internal"})
		if err != nil {
			return fmt.Errorf("jspr begin: set simConfig: %w", err)
		}
		if putResp.Code != jsprCodeOK {
			return fmt.Errorf("jspr begin: set simConfig returned code %d", putResp.Code)
		}
		// Wait for simStatus unsolicited (reader goroutine buffers it)
		time.Sleep(500 * time.Millisecond)
	}

	// 3. Set operational state to active
	stateResp, err := c.sendRequest("GET", "operationalState", struct{}{})
	if err != nil {
		return fmt.Errorf("jspr begin: get operationalState: %w", err)
	}

	var opState jsprOperationalStateResponse
	if err := json.Unmarshal(stateResp.JSON, &opState); err != nil {
		return fmt.Errorf("jspr begin: parse operationalState: %w", err)
	}

	if opState.State != "active" {
		// May need to go through inactive first
		if opState.State != "inactive" {
			c.sendRequest("PUT", "operationalState", jsprOperationalStatePut{State: "inactive"})
			time.Sleep(200 * time.Millisecond)
		}
		putResp, err := c.sendRequest("PUT", "operationalState", jsprOperationalStatePut{State: "active"})
		if err != nil {
			return fmt.Errorf("jspr begin: set active: %w", err)
		}
		if putResp.Code != jsprCodeOK {
			return fmt.Errorf("jspr begin: set active returned code %d", putResp.Code)
		}
	}

	time.Sleep(100 * time.Millisecond) // modem needs settling time after begin
	return nil
}

// jsprGetHWInfo queries hardware info (IMEI, serial, firmware, board temp).
func (c *jsprConn) jsprGetHWInfo() (*jsprHWInfo, error) {
	resp, err := c.sendRequest("GET", "hwInfo", struct{}{})
	if err != nil {
		return nil, err
	}
	if resp.Code != jsprCodeOK {
		return nil, fmt.Errorf("hwInfo returned code %d", resp.Code)
	}
	var info jsprHWInfo
	if err := json.Unmarshal(resp.JSON, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// jsprGetSignal queries constellation/signal state.
// Uses a 2-second timeout matching the official C library's 1-second timeout
// for this command. The modem may not respond when no constellation is visible.
func (c *jsprConn) jsprGetSignal() (*jsprConstellationState, error) {
	resp, err := c.sendRequestWithTimeout("GET", "constellationState", struct{}{}, 2*time.Second)
	if err != nil {
		return nil, err
	}
	if resp.Code != jsprCodeOK {
		return nil, fmt.Errorf("constellationState returned code %d", resp.Code)
	}
	var sig jsprConstellationState
	if err := json.Unmarshal(resp.JSON, &sig); err != nil {
		return nil, err
	}
	return &sig, nil
}

// jsprGetSIMStatus queries SIM status.
func (c *jsprConn) jsprGetSIMStatus() (*jsprSimStatus, error) {
	resp, err := c.sendRequest("GET", "simStatus", struct{}{})
	if err != nil {
		return nil, err
	}
	if resp.Code != jsprCodeOK {
		return nil, fmt.Errorf("simStatus returned code %d", resp.Code)
	}
	var sim jsprSimStatus
	if err := json.Unmarshal(resp.JSON, &sim); err != nil {
		return nil, err
	}
	return &sim, nil
}

// jsprCheckProvisioning queries the modem for its provisioned topics.
// Returns the list of topics. An empty list means the modem is not yet
// provisioned on the Iridium network (needs 10-30 min with sky view on first activation).
func (c *jsprConn) jsprCheckProvisioning() ([]jsprProvisioningTopic, error) {
	resp, err := c.sendRequest("GET", "messageProvisioning", struct{}{})
	if err != nil {
		return nil, fmt.Errorf("messageProvisioning: %w", err)
	}
	if resp.Code != jsprCodeOK {
		return nil, fmt.Errorf("checkProvisioning returned code %d", resp.Code)
	}

	var prov jsprProvisioningResponse
	if err := json.Unmarshal(resp.JSON, &prov); err != nil {
		return nil, fmt.Errorf("parse messageProvisioning: %w", err)
	}
	return prov.Provisioning, nil
}

// jsprSendMO sends a Mobile Originated message via JSPR.
// Handles the full flow: messageOriginate → segment responses → final status.
// Returns final MO status string and any error.
func (c *jsprConn) jsprSendMO(topicID int, payload []byte) (string, error) {
	if len(payload) > jsprMaxPayload {
		return "", fmt.Errorf("payload too large: %d bytes (max %d)", len(payload), jsprMaxPayload)
	}

	// Append CRC to payload
	dataWithCRC := appendIMTCRC(payload)
	ref := c.nextRef()

	// 1. Initiate MO
	resp, err := c.sendRequest("PUT", "messageOriginate", jsprMORequest{
		TopicID:          topicID,
		MessageLength:    len(dataWithCRC),
		RequestReference: ref,
	})
	if err != nil {
		return "", fmt.Errorf("messageOriginate: %w", err)
	}
	if resp.Code != jsprCodeOK {
		return "", fmt.Errorf("messageOriginate returned code %d", resp.Code)
	}

	var moResp jsprMOResponse
	if err := json.Unmarshal(resp.JSON, &moResp); err != nil {
		return "", fmt.Errorf("parse messageOriginate response: %w", err)
	}
	if moResp.MessageResponse != "message_accepted" {
		return moResp.MessageResponse, fmt.Errorf("message not accepted: %s", moResp.MessageResponse)
	}

	msgID := moResp.MessageID

	// 2. Wait for segment requests and final status via unsolicited buffer.
	// The reader goroutine handles all serial reads; we just consume the buffer.
	deadline := time.Now().Add(jsprMOTimeout)
	for time.Now().Before(deadline) {
		// Check for segment requests
		segReqs := c.takeUnsolicited("messageOriginateSegment")
		for _, segReq := range segReqs {
			var seg jsprMOSegmentRequest
			if err := json.Unmarshal(segReq.JSON, &seg); err != nil {
				log.Warn().Err(err).Msg("jspr: failed to parse segment request")
				continue
			}
			if seg.MessageID != msgID {
				continue
			}

			// Supply the requested segment via sendRequest (async)
			start := seg.SegmentStart
			end := start + seg.SegmentLength
			if end > len(dataWithCRC) {
				end = len(dataWithCRC)
			}

			segData := base64.StdEncoding.EncodeToString(dataWithCRC[start:end])
			segResp, err := c.sendRequestWithTimeout("PUT", "messageOriginateSegment", jsprMOSegmentPut{
				TopicID:       topicID,
				MessageID:     msgID,
				SegmentLength: end - start,
				SegmentStart:  start,
				Data:          segData,
			}, 5*time.Second)
			if err != nil {
				log.Warn().Err(err).Msg("jspr: segment PUT response error")
			} else if segResp.Code != jsprCodeOK {
				log.Warn().Int("code", segResp.Code).Msg("jspr: segment PUT non-200")
			}
		}

		// Check for final MO status
		statusMsgs := c.takeUnsolicited("messageOriginateStatus")
		for _, statusMsg := range statusMsgs {
			var status jsprMOStatus
			if err := json.Unmarshal(statusMsg.JSON, &status); err != nil {
				continue
			}
			if status.MessageID == msgID {
				return status.FinalMOStatus, nil
			}
		}

		// Wait for the reader goroutine to deliver more unsolicited messages
		c.waitForUnsolicited(50 * time.Millisecond)
	}

	return "", fmt.Errorf("MO timeout after %s", jsprMOTimeout)
}

// jsprReceiveMT processes a buffered MT message announcement.
// Returns the reassembled payload (CRC-stripped) and topic ID.
func (c *jsprConn) jsprReceiveMT(announce jsprMTAnnounce) ([]byte, int, error) {
	maxLen := announce.MessageLengthMax
	buf := make([]byte, maxLen)
	received := 0

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		// Collect segments from unsolicited buffer (reader goroutine fills it)
		segs := c.takeUnsolicited("messageTerminateSegment")
		for _, seg := range segs {
			var s jsprMTSegment
			if err := json.Unmarshal(seg.JSON, &s); err != nil {
				continue
			}
			if s.MessageID != announce.MessageID {
				continue
			}

			decoded, err := base64.StdEncoding.DecodeString(s.Data)
			if err != nil {
				log.Warn().Err(err).Msg("jspr: MT segment base64 decode failed")
				continue
			}

			end := s.SegmentStart + len(decoded)
			if end > maxLen {
				end = maxLen
			}
			copy(buf[s.SegmentStart:end], decoded)
			received += len(decoded)
		}

		// Check for final status
		statuses := c.takeUnsolicited("messageTerminateStatus")
		for _, st := range statuses {
			var status jsprMTStatus
			if err := json.Unmarshal(st.JSON, &status); err != nil {
				continue
			}
			if status.MessageID == announce.MessageID {
				if status.FinalMTStatus != "complete" {
					return nil, announce.TopicID, fmt.Errorf("MT failed: %s", status.FinalMTStatus)
				}
				// Strip CRC
				payload, err := verifyIMTCRC(buf[:received])
				if err != nil {
					// Try without CRC verification — data may still be usable
					log.Warn().Err(err).Msg("jspr: MT CRC verification failed, using raw data")
					if received > jsprIMTCRCSize {
						return buf[:received-jsprIMTCRCSize], announce.TopicID, nil
					}
					return nil, announce.TopicID, err
				}
				return payload, announce.TopicID, nil
			}
		}

		// Wait for the reader goroutine to deliver more unsolicited messages
		c.waitForUnsolicited(50 * time.Millisecond)
	}

	return nil, announce.TopicID, fmt.Errorf("MT receive timeout for message %d", announce.MessageID)
}

// mustMarshal marshals to JSON, panicking on error (only for internal protocol structs).
func mustMarshal(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("jspr: marshal failed: %v", err))
	}
	return string(b)
}

// probeJSPR sends a GET apiVersion to check if a port is a JSPR modem at 230400 baud.
// Retries up to 3 times with drain+settle between attempts, matching the official
// RockBLOCK-9704 C library handshake sequence. The modem often responds with
// "403 MALFORMED" on the first attempt after cold boot or port re-open.
func probeJSPR(portPath string) bool {
	port, err := openSerial(portPath, jsprBaud)
	if err != nil {
		log.Debug().Err(err).Str("port", portPath).Msg("jspr probe: open failed")
		return false
	}
	defer port.Close()

	for attempt := 0; attempt < 3; attempt++ {
		// Drain any pending data
		port.SetReadTimeout(100 * time.Millisecond)
		buf := make([]byte, 1024)
		for {
			n, _ := port.Read(buf)
			if n == 0 {
				break
			}
		}
		time.Sleep(5 * time.Millisecond) // settling time after drain (per official library)

		// Send GET apiVersion
		if _, err := port.Write([]byte("GET apiVersion {}\r")); err != nil {
			log.Debug().Err(err).Str("port", portPath).Int("attempt", attempt+1).Msg("jspr probe: write failed")
			return false
		}

		// Read response with timeout
		port.SetReadTimeout(jsprReadTimeout)
		deadline := time.Now().Add(3 * time.Second)
		var line strings.Builder
		var b [1]byte
		gotResponse := false

		for time.Now().Before(deadline) {
			n, err := port.Read(b[:])
			if n == 0 && err == nil {
				continue
			}
			if err != nil {
				break
			}
			if b[0] == '\r' {
				if line.Len() >= 3 {
					resp, err := parseJSPRLine(line.String())
					if err == nil && resp.Code > 0 {
						// Any valid JSPR response (200, 403, etc.) proves this is a 9704
						return true
					}
					log.Debug().Str("line", line.String()).Err(err).Int("attempt", attempt+1).Msg("jspr probe: unparseable response")
				}
				gotResponse = true
				line.Reset()
				continue
			}
			// Skip non-printable
			if b[0] < 0x20 || b[0] > 0x7E {
				continue
			}
			line.WriteByte(b[0])
		}

		if !gotResponse {
			log.Debug().Str("port", portPath).Int("attempt", attempt+1).Msg("jspr probe: no response (timeout)")
		}
	}

	return false
}
