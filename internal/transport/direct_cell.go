package transport

// DirectCellTransport implements CellTransport with direct serial access to a SIM7600 4G/LTE modem.
// Follows the same patterns as DirectSatTransport — mutex-protected AT commands, background monitors.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"go.bug.st/serial"
)

const (
	cellBaud           = 115200
	cellATTimeout      = 3 * time.Second
	cellSignalPoll     = 60 * time.Second
	cellSMSSendTimeout = 30 * time.Second
	maxSMSLength       = 160
)

// Known cellular modem VID:PID pairs.
var knownCellularVIDPIDs = map[string]bool{
	"1e0e:9001": true, // SimTech SIM7600
	"1e0e:9011": true, // SimTech SIM7600E-H
	"1e0e:9025": true, // SimTech SIM7000
	"2c7c:0125": true, // Quectel EC25
	"2c7c:0296": true, // Quectel BG96
	"2c7c:0512": true, // Quectel EG512R
	"12d1:15c1": true, // Huawei ME909s
}

// DirectCellTransport implements CellTransport via direct serial access to a 4G/LTE modem.
type DirectCellTransport struct {
	port string // "/dev/ttyUSB2" or "auto"

	mu   sync.Mutex // protects serial port (t.file) access only
	file serial.Port

	// Cached modem state — protected by stateMu (separate from serial mu).
	// This allows GetStatus to read state without contending with serial I/O.
	stateMu   sync.RWMutex
	connected bool
	imei      string
	model     string
	operator  string
	netType   string
	simState  string
	regStatus string

	// Data connection state
	dataMu     sync.RWMutex
	dataActive bool
	dataAPN    string
	dataIP     string
	dataIface  string

	// Signal state
	signalMu   sync.RWMutex
	lastSignal CellSignalInfo

	// Background goroutines
	stopMonitorCh chan struct{}
	monitorDone   chan struct{}
	stopSignalCh  chan struct{}
	signalDone    chan struct{}
	monitorActive bool

	// SSE subscribers
	eventMu   sync.RWMutex
	eventSubs map[uint64]chan CellEvent
	nextSubID uint64

	// Exclude ports (Meshtastic + Iridium)
	excludePorts   []string
	excludePortFns []func() string // dynamic resolvers (take precedence)
}

// NewDirectCellTransport creates a new direct serial cellular transport.
// Pass "auto" or "" for port to use auto-detection.
func NewDirectCellTransport(port string) *DirectCellTransport {
	return &DirectCellTransport{
		port:      port,
		eventSubs: make(map[uint64]chan CellEvent),
	}
}

// SetExcludePorts tells auto-detection to skip these ports (e.g., Meshtastic and Iridium ports).
func (t *DirectCellTransport) SetExcludePorts(ports []string) {
	t.excludePorts = ports
}

// SetExcludePortFuncs sets dynamic port resolvers for exclusion.
// Called at auto-detect time to get current ports of other transports.
func (t *DirectCellTransport) SetExcludePortFuncs(fns []func() string) {
	t.excludePortFns = fns
}

// Subscribe opens the serial connection and starts SMS monitor + signal polling.
func (t *DirectCellTransport) Subscribe(ctx context.Context) (<-chan CellEvent, error) {
	t.mu.Lock()
	if t.file == nil {
		if err := t.connectLocked(ctx); err != nil {
			t.mu.Unlock()
			return nil, fmt.Errorf("connect: %w", err)
		}
	}
	t.mu.Unlock()

	ch := make(chan CellEvent, 32)
	t.eventMu.Lock()
	id := t.nextSubID
	t.nextSubID++
	t.eventSubs[id] = ch
	t.eventMu.Unlock()

	go func() {
		<-ctx.Done()
		t.eventMu.Lock()
		delete(t.eventSubs, id)
		close(ch)
		t.eventMu.Unlock()
	}()

	return ch, nil
}

func (t *DirectCellTransport) connectLocked(_ context.Context) error {
	portPath := t.port
	if portPath == "" || portPath == "auto" {
		// Resolve dynamic exclude ports from other transports
		excludes := make([]string, 0, len(t.excludePorts)+len(t.excludePortFns))
		for _, fn := range t.excludePortFns {
			if resolved := fn(); resolved != "" && resolved != "auto" {
				excludes = append(excludes, resolved)
			}
		}
		excludes = append(excludes, t.excludePorts...)
		portPath = autoDetectCellular(excludes)
		if portPath == "" {
			return fmt.Errorf("no cellular modem found")
		}
	}

	log.Debug().Str("port", portPath).Msg("cellular: opening serial port")
	// openSerial can block on some USB serial drivers — run with timeout.
	type openResult struct {
		port serial.Port
		err  error
	}
	openCh := make(chan openResult, 1)
	go func() {
		p, e := openSerial(portPath, cellBaud)
		openCh <- openResult{p, e}
	}()
	var sp serial.Port
	select {
	case res := <-openCh:
		if res.err != nil {
			return res.err
		}
		sp = res.port
	case <-time.After(10 * time.Second):
		return fmt.Errorf("timeout opening %s (10s)", portPath)
	}
	log.Debug().Str("port", portPath).Msg("cellular: serial port opened")

	t.file = sp
	t.port = portPath

	// Initialize modem — each command has cellATTimeout (3s) as safety net.
	log.Debug().Msg("cellular: init ATE0")
	sendAT(sp, "ATE0", cellATTimeout)
	sendAT(sp, "AT&K0", cellATTimeout)
	resp, err := sendAT(sp, "AT", cellATTimeout)
	if err != nil || !strings.Contains(resp, "OK") {
		sp.Close()
		return fmt.Errorf("AT check failed")
	}
	log.Debug().Msg("cellular: init AT+CGSN")
	resp, err = sendAT(sp, "AT+CGSN", cellATTimeout)
	imei := ""
	if err == nil {
		imei = parseATValue(resp)
	}
	resp, err = sendAT(sp, "AT+CGMM", cellATTimeout)
	model := ""
	if err == nil {
		model = parseATValue(resp)
	}
	resp, err = sendAT(sp, "AT+CMGF=1", cellATTimeout)
	if err != nil || strings.Contains(resp, "ERROR") {
		log.Warn().Msg("cellular: SMS text mode not supported, SMS will not work")
	}
	sendAT(sp, "AT+CNMI=2,1,2,0,0", cellATTimeout)
	log.Debug().Msg("cellular: init AT+CPIN?")
	resp, _ = sendAT(sp, "AT+CPIN?", cellATTimeout)
	simState := parseCPIN(resp)
	log.Debug().Str("sim_state", simState).Msg("cellular: SIM state detected")

	var operator, netType, regStatus string
	// Only query network registration and operator when SIM is ready.
	// AT+COPS? on an unregistered modem (SIM locked) can hang or trigger
	// a slow network scan that exceeds the AT timeout.
	if simState == "READY" {
		sendAT(sp, "AT+CREG=2", cellATTimeout)
		resp, _ = sendAT(sp, "AT+CREG?", cellATTimeout)
		regStatus = parseCREG(resp)
		netType = parseCREGNetType(resp)
		log.Debug().Msg("cellular: init AT+COPS?")
		resp, _ = sendAT(sp, "AT+COPS?", cellATTimeout)
		operator = parseCOPS(resp)
		if netType == "" {
			netType = parseCOPSNetType(resp)
		}
		sendAT(sp, "AT+CSCB=0", cellATTimeout)
	}

	// Update cached state under stateMu (separate from serial mu).
	t.stateMu.Lock()
	t.imei = imei
	t.model = model
	t.simState = simState
	t.operator = operator
	t.netType = netType
	t.regStatus = regStatus
	t.connected = true
	t.stateMu.Unlock()
	log.Info().Str("port", portPath).Str("imei", t.imei).Str("model", t.model).
		Str("sim", t.simState).Str("operator", t.operator).Msg("cellular modem connected")

	t.emitEvent(CellEvent{
		Type:    "connected",
		Message: fmt.Sprintf("Connected to %s (IMEI: %s)", t.model, t.imei),
		Time:    time.Now().UTC().Format(time.RFC3339),
	})

	// Start background monitors
	t.startMonitors()

	return nil
}

func (t *DirectCellTransport) startMonitors() {
	if t.monitorActive {
		return
	}
	t.monitorActive = true

	t.stopMonitorCh = make(chan struct{})
	t.monitorDone = make(chan struct{})
	go t.smsMonitorLoop()

	t.stopSignalCh = make(chan struct{})
	t.signalDone = make(chan struct{})
	go t.cellSignalPollerLoop()
}

func (t *DirectCellTransport) stopMonitors() {
	if !t.monitorActive {
		return
	}
	t.monitorActive = false

	// Stop signal poller
	if t.stopSignalCh != nil {
		select {
		case <-t.signalDone:
		default:
			close(t.stopSignalCh)
			done := t.signalDone
			t.mu.Unlock()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				log.Warn().Msg("cellular: signal poller did not exit in time")
			}
			t.mu.Lock()
		}
		t.stopSignalCh = nil
		t.signalDone = nil
	}

	// Stop SMS monitor
	if t.stopMonitorCh != nil {
		select {
		case <-t.monitorDone:
		default:
			close(t.stopMonitorCh)
			done := t.monitorDone
			t.mu.Unlock()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				log.Warn().Msg("cellular: SMS monitor did not exit in time")
			}
			t.mu.Lock()
		}
		t.stopMonitorCh = nil
		t.monitorDone = nil
	}
}

// smsMonitorLoop reads serial for unsolicited +CMTI notifications (incoming SMS).
func (t *DirectCellTransport) smsMonitorLoop() {
	defer close(t.monitorDone)

	buf := make([]byte, 1)
	var line []byte

	for {
		select {
		case <-t.stopMonitorCh:
			return
		default:
		}

		t.mu.Lock()
		if t.file == nil {
			t.mu.Unlock()
			return
		}

		t.file.SetReadTimeout(100 * time.Millisecond)
		n, err := t.file.Read(buf)
		t.mu.Unlock()

		if n == 0 && err == nil {
			continue // read timeout, no data
		}
		if err != nil {
			select {
			case <-t.stopMonitorCh:
				return
			default:
			}
			log.Error().Err(err).Msg("cellular monitor serial error")
			t.emitEvent(CellEvent{
				Type:    "disconnected",
				Message: "Serial connection lost",
				Time:    time.Now().UTC().Format(time.RFC3339),
			})
			t.mu.Lock()
			if t.file != nil {
				t.file.Close()
				t.file = nil
			}
			t.mu.Unlock()
			t.stateMu.Lock()
			t.connected = false
			t.stateMu.Unlock()
			return
		}

		if n > 0 {
			line = append(line, buf[0])
			if buf[0] == '\n' {
				s := strings.TrimSpace(string(line))
				// +CMTI: "SM",3 — new SMS at index 3
				if strings.HasPrefix(s, "+CMTI:") {
					idx := parseCMTI(s)
					if idx >= 0 {
						go t.readAndEmitSMS(idx)
					}
				}
				// +CBM: <sn>,<mid>,<dcs>,<page>,<pages> — cell broadcast header
				if strings.HasPrefix(s, "+CBM:") {
					go t.readAndEmitCBS(s)
				}
				line = line[:0]
			}
			if len(line) > 512 {
				line = line[:0]
			}
		}
	}
}

// readAndEmitSMS reads an SMS by index and emits it as an event.
func (t *DirectCellTransport) readAndEmitSMS(index int) {
	t.mu.Lock()
	if t.file == nil {
		t.mu.Unlock()
		return
	}

	// AT+CMGR=index — read SMS
	resp, err := sendAT(t.file, fmt.Sprintf("AT+CMGR=%d", index), cellATTimeout)
	if err != nil {
		t.mu.Unlock()
		log.Warn().Err(err).Int("index", index).Msg("cellular: failed to read SMS")
		return
	}

	// Delete after reading
	sendAT(t.file, fmt.Sprintf("AT+CMGD=%d", index), cellATTimeout)
	t.mu.Unlock()

	sms := parseCMGR(resp)
	if sms == nil {
		return
	}

	log.Info().Str("sender", sms.Sender).Str("text", sms.Text).Msg("cellular: SMS received")
	smsJSON, _ := json.Marshal(sms)
	t.emitEvent(CellEvent{
		Type:    "sms_received",
		Message: sms.Text,
		Data:    smsJSON,
		Time:    time.Now().UTC().Format(time.RFC3339),
	})
}

// readAndEmitCBS reads the CBS message body after the +CBM header line.
func (t *DirectCellTransport) readAndEmitCBS(header string) {
	// Read the CBS body — it follows the +CBM header line
	t.mu.Lock()
	if t.file == nil {
		t.mu.Unlock()
		return
	}
	// Read body with short timeout (CBS data comes immediately after header)
	body, _ := readATResponse(t.file, 2*time.Second)
	t.mu.Unlock()

	cbs := parseCBM(header, body)
	if cbs == nil {
		return
	}

	log.Info().Int("mid", cbs.MessageID).Str("severity", cbs.Severity).
		Str("text", cbs.Text).Msg("cellular: CBS alert received")

	cbsJSON, _ := json.Marshal(cbs)
	t.emitEvent(CellEvent{
		Type:    "cbs_received",
		Message: cbs.Text,
		Data:    cbsJSON,
		Time:    time.Now().UTC().Format(time.RFC3339),
	})
}

// cellSignalPollerLoop polls AT+CSQ every 60s.
func (t *DirectCellTransport) cellSignalPollerLoop() {
	defer close(t.signalDone)
	ticker := time.NewTicker(cellSignalPoll)
	defer ticker.Stop()

	for {
		select {
		case <-t.stopSignalCh:
			return
		case <-ticker.C:
			t.mu.Lock()
			if t.file == nil {
				t.mu.Unlock()
				return
			}

			resp, err := sendAT(t.file, "AT+CSQ", 5*time.Second)
			t.mu.Unlock()

			if err != nil {
				log.Warn().Err(err).Msg("cellular signal poll failed")
				continue
			}

			info := parseCellCSQ(resp)
			if info == nil {
				continue
			}

			// Also query cell info for network type and RSRP/RSRQ.
			// Try AT+QENG (Quectel proprietary) first, fall back to AT+CREG?.
			var ci *CellInfo
			t.mu.Lock()
			cellResp, cellErr := sendAT(t.file, "AT+QENG=\"servingcell\"", 5*time.Second)
			if cellErr == nil && strings.Contains(cellResp, "+QENG:") {
				ci = parseQENG(cellResp)
			}
			if ci == nil {
				// Fallback: extended AT+CREG? (works on all modems)
				cregResp, cregErr := sendAT(t.file, "AT+CREG?", cellATTimeout)
				if cregErr == nil {
					ci = parseCREGExtended(cregResp)
				}
			}
			t.mu.Unlock()
			if ci != nil {
				if ci.NetworkType != "" {
					info.Technology = ci.NetworkType
					t.stateMu.Lock()
					t.netType = ci.NetworkType
					t.stateMu.Unlock()
				}
				// Emit cell info update
				ciJSON, _ := json.Marshal(ci)
				t.emitEvent(CellEvent{
					Type:    "cell_info_update",
					Message: fmt.Sprintf("Cell: %s/%s CID=%s", ci.MCC, ci.MNC, ci.CellID),
					Data:    ciJSON,
					Time:    time.Now().UTC().Format(time.RFC3339),
				})
			}

			t.signalMu.Lock()
			t.lastSignal = *info
			t.signalMu.Unlock()

			t.emitEvent(CellEvent{
				Type:    "signal",
				Message: fmt.Sprintf("Signal: %d bars (%d dBm)", info.Bars, info.DBm),
				Signal:  info.Bars,
				Time:    info.Timestamp,
			})
		}
	}
}

// SendSMS sends an SMS to the specified number.
func (t *DirectCellTransport) SendSMS(ctx context.Context, to string, text string) error {
	if len(text) > maxSMSLength {
		text = text[:maxSMSLength]
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file == nil {
		return fmt.Errorf("not connected")
	}

	// AT+CMGS="number"
	cmd := fmt.Sprintf("AT+CMGS=\"%s\"", to)
	drainPort(t.file)
	if _, err := t.file.Write([]byte(cmd + "\r")); err != nil {
		return fmt.Errorf("write CMGS failed: %w", err)
	}

	// Wait for ">" prompt
	deadline := time.Now().Add(5 * time.Second)
	t.file.SetReadTimeout(50 * time.Millisecond)
	buf := make([]byte, 256)
	var resp strings.Builder
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for > prompt")
		}
		n, err := t.file.Read(buf)
		if n > 0 {
			resp.Write(buf[:n])
			if strings.Contains(resp.String(), ">") {
				break
			}
		}
		if err != nil {
			return fmt.Errorf("read failed: %w", err)
		}
	}

	// Send text + Ctrl+Z
	if _, err := t.file.Write([]byte(text + "\x1A")); err != nil {
		return fmt.Errorf("write text failed: %w", err)
	}

	// Read response (wait for OK or ERROR)
	smsResp, err := readATResponse(t.file, cellSMSSendTimeout)
	if err != nil {
		return fmt.Errorf("SMS send failed: %w", err)
	}
	if strings.Contains(smsResp, "ERROR") {
		return fmt.Errorf("SMS send error: %s", strings.TrimSpace(smsResp))
	}

	log.Info().Str("to", to).Int("len", len(text)).Msg("cellular: SMS sent")
	return nil
}

// GetSignal returns current signal strength.
func (t *DirectCellTransport) GetSignal(_ context.Context) (*CellSignalInfo, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file == nil {
		return nil, fmt.Errorf("not connected")
	}

	resp, err := sendAT(t.file, "AT+CSQ", 5*time.Second)
	if err != nil {
		return nil, err
	}

	info := parseCellCSQ(resp)
	if info == nil {
		return nil, fmt.Errorf("failed to parse signal")
	}

	t.signalMu.Lock()
	t.lastSignal = *info
	t.signalMu.Unlock()

	return info, nil
}

// GetStatus returns modem connection status.
func (t *DirectCellTransport) GetStatus(_ context.Context) (*CellStatus, error) {
	t.stateMu.RLock()
	defer t.stateMu.RUnlock()
	return &CellStatus{
		Connected:    t.connected,
		Port:         t.port,
		IMEI:         t.imei,
		Model:        t.model,
		Operator:     t.operator,
		NetworkType:  t.netType,
		SIMState:     t.simState,
		Registration: t.regStatus,
	}, nil
}

// ConnectData brings up the LTE data connection.
func (t *DirectCellTransport) ConnectData(_ context.Context, apn string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file == nil {
		return fmt.Errorf("not connected")
	}

	// Set APN
	cmd := fmt.Sprintf("AT+CGDCONT=1,\"IP\",\"%s\"", apn)
	resp, err := sendAT(t.file, cmd, cellATTimeout)
	if err != nil || strings.Contains(resp, "ERROR") {
		return fmt.Errorf("set APN failed: %s", resp)
	}

	// Activate PDP context
	resp, err = sendAT(t.file, "AT+CGACT=1,1", 30*time.Second)
	if err != nil || strings.Contains(resp, "ERROR") {
		return fmt.Errorf("activate PDP failed: %s", resp)
	}

	// Query assigned IP
	resp, err = sendAT(t.file, "AT+CGPADDR=1", cellATTimeout)
	ip := ""
	if err == nil {
		ip = parseCGPADDR(resp)
	}

	t.dataMu.Lock()
	t.dataActive = true
	t.dataAPN = apn
	t.dataIP = ip
	t.dataIface = detectDataInterface()
	t.dataMu.Unlock()

	log.Info().Str("apn", apn).Str("ip", ip).Msg("cellular: data connected")
	return nil
}

// DisconnectData tears down the LTE data connection.
func (t *DirectCellTransport) DisconnectData(_ context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file == nil {
		return fmt.Errorf("not connected")
	}

	resp, err := sendAT(t.file, "AT+CGACT=0,1", 10*time.Second)
	if err != nil || strings.Contains(resp, "ERROR") {
		return fmt.Errorf("deactivate PDP failed: %s", resp)
	}

	t.dataMu.Lock()
	t.dataActive = false
	t.dataIP = ""
	t.dataMu.Unlock()

	log.Info().Msg("cellular: data disconnected")
	return nil
}

// GetDataStatus returns the current data connection state.
func (t *DirectCellTransport) GetDataStatus(_ context.Context) (*CellDataStatus, error) {
	t.dataMu.RLock()
	defer t.dataMu.RUnlock()
	return &CellDataStatus{
		Active:    t.dataActive,
		APN:       t.dataAPN,
		IPAddress: t.dataIP,
		Interface: t.dataIface,
	}, nil
}

// Close shuts down the transport.
func (t *DirectCellTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.monitorActive {
		close(t.stopMonitorCh)
		close(t.stopSignalCh)
		t.monitorActive = false
		t.mu.Unlock()
		// Wait for goroutines to finish before closing the fd
		<-t.monitorDone
		<-t.signalDone
		t.mu.Lock()
	}

	t.stateMu.Lock()
	t.connected = false
	t.stateMu.Unlock()
	if t.file != nil {
		t.file.Close()
		t.file = nil
	}
	return nil
}

func (t *DirectCellTransport) emitEvent(event CellEvent) {
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

// parseCellCSQ parses AT+CSQ response → CellSignalInfo.
// +CSQ: 18,99 → RSSI=18 (0-31 scale), BER=99
func parseCellCSQ(resp string) *CellSignalInfo {
	idx := strings.Index(resp, "+CSQ:")
	if idx == -1 {
		return nil
	}
	remainder := strings.TrimSpace(resp[idx+5:])
	firstLine := strings.Split(remainder, "\n")[0]
	parts := strings.Split(strings.TrimSpace(firstLine), ",")
	if len(parts) < 1 {
		return nil
	}

	rssi, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || rssi < 0 || rssi > 31 {
		return nil
	}

	// Map 0-31 RSSI to dBm: dBm = -113 + (2 * rssi)
	dbm := -113 + (2 * rssi)
	bars := csqToBars(rssi)

	return &CellSignalInfo{
		Bars:       bars,
		DBm:        dbm,
		Technology: "LTE",
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Assessment: cellSignalAssessment(bars),
	}
}

// csqToBars maps AT+CSQ RSSI (0-31) to 0-5 bars.
func csqToBars(rssi int) int {
	switch {
	case rssi == 0 || rssi == 99:
		return 0
	case rssi <= 6:
		return 1
	case rssi <= 12:
		return 2
	case rssi <= 18:
		return 3
	case rssi <= 24:
		return 4
	default:
		return 5
	}
}

func cellSignalAssessment(bars int) string {
	switch bars {
	case 0:
		return "none"
	case 1:
		return "poor"
	case 2:
		return "fair"
	case 3:
		return "good"
	case 4:
		return "very good"
	default:
		return "excellent"
	}
}

// parseCPIN parses AT+CPIN? response → SIM state string.
func parseCPIN(resp string) string {
	if strings.Contains(resp, "+CPIN: READY") {
		return "READY"
	}
	if strings.Contains(resp, "SIM PIN") {
		return "PIN_REQUIRED"
	}
	if strings.Contains(resp, "SIM PUK") {
		return "PUK_REQUIRED"
	}
	if strings.Contains(resp, "ERROR") {
		return "NOT_INSERTED"
	}
	return "UNKNOWN"
}

// parseCREG parses AT+CREG? response → registration status string.
func parseCREG(resp string) string {
	idx := strings.Index(resp, "+CREG:")
	if idx == -1 {
		return "unknown"
	}
	remainder := strings.TrimSpace(resp[idx+6:])
	parts := strings.Split(strings.Split(remainder, "\n")[0], ",")
	if len(parts) < 2 {
		return "unknown"
	}
	stat, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
	switch stat {
	case 0:
		return "not_registered"
	case 1:
		return "registered_home"
	case 2:
		return "searching"
	case 3:
		return "denied"
	case 5:
		return "registered_roaming"
	default:
		return "unknown"
	}
}

// parseCOPS parses AT+COPS? response → operator name.
func parseCOPS(resp string) string {
	idx := strings.Index(resp, "+COPS:")
	if idx == -1 {
		return ""
	}
	remainder := resp[idx+6:]
	// Format: +COPS: 0,0,"OperatorName",7
	parts := strings.Split(remainder, "\"")
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

// parseCMTI parses +CMTI: "SM",3 → index 3.
func parseCMTI(line string) int {
	idx := strings.Index(line, "+CMTI:")
	if idx == -1 {
		return -1
	}
	parts := strings.Split(line[idx+6:], ",")
	if len(parts) < 2 {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return -1
	}
	return n
}

// parseCMGR parses AT+CMGR response → SMSMessage.
func parseCMGR(resp string) *SMSMessage {
	idx := strings.Index(resp, "+CMGR:")
	if idx == -1 {
		return nil
	}
	lines := strings.Split(resp[idx:], "\n")
	if len(lines) < 2 {
		return nil
	}
	// +CMGR: "REC UNREAD","+31612345678","","2026/03/04,12:00:00+04"
	header := lines[0]
	text := strings.TrimSpace(lines[1])
	if text == "OK" || text == "" {
		return nil
	}

	// Extract sender from header
	headerParts := strings.Split(header, "\"")
	sender := ""
	if len(headerParts) >= 4 {
		sender = headerParts[3]
	}

	return &SMSMessage{
		Sender: sender,
		Text:   text,
		Time:   time.Now().UTC().Format(time.RFC3339),
	}
}

// parseCGPADDR parses AT+CGPADDR=1 response → IP address.
func parseCGPADDR(resp string) string {
	idx := strings.Index(resp, "+CGPADDR:")
	if idx == -1 {
		return ""
	}
	remainder := strings.TrimSpace(resp[idx+9:])
	parts := strings.Split(strings.Split(remainder, "\n")[0], ",")
	if len(parts) < 2 {
		return ""
	}
	ip := strings.Trim(strings.TrimSpace(parts[1]), "\"")
	return ip
}

// detectDataInterface returns the cellular data network interface name.
func detectDataInterface() string {
	// Try common cellular interface names
	for _, name := range []string{"wwan0", "usb0", "eth1"} {
		iface, err := net.InterfaceByName(name)
		if err != nil {
			continue
		}
		if iface.Flags&net.FlagUp != 0 {
			return name
		}
	}
	return "wwan0" // default
}

// ============================================================================
// Auto-detection
// ============================================================================

// autoDetectCellular scans serial ports for a cellular modem.
// Uses VID:PID matching first, then AT+CPIN? probe to distinguish from Iridium.
func autoDetectCellular(excludePorts []string) string {
	excluded := make(map[string]bool)
	for _, p := range excludePorts {
		excluded[p] = true
	}

	var ports []string
	if matches, _ := findSerialPorts("/dev/ttyUSB*"); len(matches) > 0 {
		ports = append(ports, matches...)
	}
	if matches, _ := findSerialPorts("/dev/ttyACM*"); len(matches) > 0 {
		ports = append(ports, matches...)
	}

	// Pass 1: VID:PID match
	for _, port := range ports {
		if excluded[port] {
			continue
		}
		vidpid := findUSBVIDPID(port)
		if knownCellularVIDPIDs[vidpid] {
			log.Info().Str("port", port).Str("vidpid", vidpid).Msg("cellular auto-detected by VID:PID")
			return port
		}
	}

	// Pass 2: AT+CPIN? probe (distinguishes cellular from Iridium)
	for _, port := range ports {
		if excluded[port] {
			continue
		}
		// Skip known Meshtastic/GPS/Iridium devices
		vidpid := findUSBVIDPID(port)
		if knownMeshtasticVIDPIDs[vidpid] || gpsVIDPIDs[vidpid] {
			continue
		}
		if probeCellularAT(port) {
			log.Info().Str("port", port).Msg("cellular auto-detected by AT+CPIN? probe")
			return port
		}
	}

	return ""
}

// probeCellularAT probes a port with AT+CPIN? to check if it's a cellular modem.
// Cellular modems respond with "+CPIN: READY", Iridium modems give ERROR.
func probeCellularAT(port string) bool {
	file, err := openSerial(port, cellBaud)
	if err != nil {
		return false
	}
	defer file.Close()

	// Disable echo
	sendAT(file, "ATE0", 2*time.Second)

	resp, err := sendAT(file, "AT+CPIN?", 3*time.Second)
	if err != nil {
		return false
	}
	return strings.Contains(resp, "+CPIN:")
}

// GetPort returns the resolved serial port path.
func (t *DirectCellTransport) GetPort() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.port
}

// UnlockPIN submits the SIM PIN to unlock the modem.
func (t *DirectCellTransport) UnlockPIN(_ context.Context, pin string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file == nil {
		return fmt.Errorf("not connected")
	}

	cmd := fmt.Sprintf("AT+CPIN=\"%s\"", pin)
	resp, err := sendAT(t.file, cmd, 10*time.Second)
	if err != nil {
		return fmt.Errorf("PIN command failed: %w", err)
	}
	if strings.Contains(resp, "ERROR") {
		return fmt.Errorf("PIN rejected: %s", strings.TrimSpace(resp))
	}

	// Wait for modem to settle after PIN unlock
	time.Sleep(2 * time.Second)

	// Run post-unlock initialization: registration, operator, SMS, CBS
	sendAT(t.file, "AT+CREG=2", cellATTimeout)
	resp, _ = sendAT(t.file, "AT+CREG?", cellATTimeout)
	regStatus := parseCREG(resp)
	netType := parseCREGNetType(resp)

	resp, _ = sendAT(t.file, "AT+COPS?", cellATTimeout)
	operator := parseCOPS(resp)
	if netType == "" {
		netType = parseCOPSNetType(resp)
	}

	sendAT(t.file, "AT+CNMI=2,1,2,0,0", cellATTimeout)
	sendAT(t.file, "AT+CSCB=0", cellATTimeout)

	t.stateMu.Lock()
	t.simState = "READY"
	t.regStatus = regStatus
	t.netType = netType
	t.operator = operator
	t.stateMu.Unlock()

	log.Info().Str("sim", "READY").Str("reg", regStatus).Str("net", netType).
		Str("operator", operator).Msg("cellular: SIM PIN accepted, modem initialized")

	t.emitEvent(CellEvent{
		Type:    "network_change",
		Message: fmt.Sprintf("SIM unlocked, registered on %s (%s)", operator, netType),
		Time:    time.Now().UTC().Format(time.RFC3339),
	})

	return nil
}

// GetCellInfo queries cell tower information from the modem.
// Tries AT+QENG (Quectel proprietary) first, falls back to extended AT+CREG?.
func (t *DirectCellTransport) GetCellInfo(_ context.Context) (*CellInfo, error) {
	if !t.mu.TryLock() {
		return nil, fmt.Errorf("modem busy (initializing)")
	}
	defer t.mu.Unlock()
	if t.file == nil {
		return nil, fmt.Errorf("not connected")
	}

	// Try Quectel AT+QENG="servingcell"
	resp, err := sendAT(t.file, "AT+QENG=\"servingcell\"", 5*time.Second)
	if err == nil && strings.Contains(resp, "+QENG:") {
		info := parseQENG(resp)
		if info != nil {
			return info, nil
		}
	}

	// Fallback: extended AT+CREG? (if AT+CREG=2 was sent during init)
	resp, err = sendAT(t.file, "AT+CREG?", cellATTimeout)
	if err != nil {
		return nil, err
	}
	return parseCREGExtended(resp), nil
}

// parseQENG parses AT+QENG="servingcell" response (Quectel modems).
// LTE format: +QENG: "servingcell","NOCHANGE","LTE","FDD",262,01,1A2D003,148,100,1,5,5,9E3F,-109,-13,-80,16,38
func parseQENG(resp string) *CellInfo {
	idx := strings.Index(resp, "+QENG:")
	if idx == -1 {
		return nil
	}
	line := strings.Split(resp[idx:], "\n")[0]
	// Remove +QENG: prefix and split by comma
	parts := strings.Split(line[6:], ",")
	// Strip quotes from all parts
	for i := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(parts[i]), "\"")
	}

	if len(parts) < 5 {
		return nil
	}

	info := &CellInfo{}

	// Detect technology from parts[2]
	tech := ""
	if len(parts) > 2 {
		tech = strings.ToUpper(parts[2])
	}

	switch tech {
	case "LTE":
		info.NetworkType = "LTE"
		// +QENG: "servingcell","NOCHANGE","LTE","FDD",MCC,MNC,CellID,PCID,EARFCN,...,RSRP,RSRQ,RSSI,SINR,...
		if len(parts) >= 8 {
			info.MCC = parts[4]
			info.MNC = parts[5]
			info.CellID = parts[6]
		}
		if len(parts) >= 15 {
			if v, err := strconv.Atoi(parts[13]); err == nil {
				info.RSRP = &v
			}
			if v, err := strconv.Atoi(parts[14]); err == nil {
				info.RSRQ = &v
			}
		}
	case "WCDMA":
		info.NetworkType = "3G"
		if len(parts) >= 7 {
			info.MCC = parts[3]
			info.MNC = parts[4]
			info.LAC = parts[5]
			info.CellID = parts[6]
		}
	case "GSM":
		info.NetworkType = "2G"
		if len(parts) >= 7 {
			info.MCC = parts[3]
			info.MNC = parts[4]
			info.LAC = parts[5]
			info.CellID = parts[6]
		}
	default:
		info.NetworkType = tech
	}

	return info
}

// parseCREGExtended parses AT+CREG? extended response for LAC/CellID/act.
// Format: +CREG: 2,1,"1A2D","003E9E3F",7
func parseCREGExtended(resp string) *CellInfo {
	idx := strings.Index(resp, "+CREG:")
	if idx == -1 {
		return nil
	}
	remainder := strings.TrimSpace(resp[idx+6:])
	parts := strings.Split(strings.Split(remainder, "\n")[0], ",")
	info := &CellInfo{}

	if len(parts) >= 4 {
		info.LAC = strings.Trim(strings.TrimSpace(parts[2]), "\"")
		info.CellID = strings.Trim(strings.TrimSpace(parts[3]), "\"")
	}
	if len(parts) >= 5 {
		act, _ := strconv.Atoi(strings.TrimSpace(parts[4]))
		info.NetworkType = actToNetworkType(act)
	}
	return info
}

// parseCREGNetType extracts the access technology from AT+CREG? response.
func parseCREGNetType(resp string) string {
	idx := strings.Index(resp, "+CREG:")
	if idx == -1 {
		return ""
	}
	parts := strings.Split(strings.Split(strings.TrimSpace(resp[idx+6:]), "\n")[0], ",")
	if len(parts) >= 5 {
		act, _ := strconv.Atoi(strings.TrimSpace(parts[4]))
		return actToNetworkType(act)
	}
	// Fallback: parse from stat field only (2 fields = basic mode)
	return ""
}

// parseCOPSNetType extracts network type from AT+COPS? response.
// Format: +COPS: 0,0,"OperatorName",7
func parseCOPSNetType(resp string) string {
	idx := strings.Index(resp, "+COPS:")
	if idx == -1 {
		return ""
	}
	parts := strings.Split(resp[idx+6:], ",")
	if len(parts) >= 4 {
		act, _ := strconv.Atoi(strings.TrimSpace(strings.Split(parts[3], "\n")[0]))
		return actToNetworkType(act)
	}
	return ""
}

// actToNetworkType maps 3GPP access technology to human-readable name.
func actToNetworkType(act int) string {
	switch act {
	case 0:
		return "2G"
	case 1:
		return "2G" // GSM Compact
	case 2:
		return "3G"
	case 3:
		return "2G" // GSM w/ EGPRS
	case 4:
		return "3G" // UTRAN w/ HSDPA
	case 5:
		return "3G" // UTRAN w/ HSUPA
	case 6:
		return "3G" // UTRAN w/ HSDPA+HSUPA
	case 7:
		return "LTE"
	case 8:
		return "5G" // EC-GSM-IoT
	default:
		return ""
	}
}

// parseCBM parses a +CBM unsolicited response code (cell broadcast).
// Format: +CBM: <sn>,<mid>,<dcs>,<page>,<pages>\r\n<data>
func parseCBM(header, body string) *CellBroadcastMsg {
	idx := strings.Index(header, "+CBM:")
	if idx == -1 {
		return nil
	}
	parts := strings.Split(strings.TrimSpace(header[idx+5:]), ",")
	if len(parts) < 3 {
		return nil
	}

	sn, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
	mid, _ := strconv.Atoi(strings.TrimSpace(parts[1]))

	msg := &CellBroadcastMsg{
		SerialNumber: sn,
		MessageID:    mid,
		Channel:      mid, // CBS channel = message ID
		Text:         strings.TrimSpace(body),
		Severity:     cbsSeverity(mid),
	}
	return msg
}

// cbsSeverity maps CBS message IDs to severity levels.
// Based on ETSI TS 123 041 and 3GPP TS 23.041.
func cbsSeverity(mid int) string {
	switch {
	case mid >= 4370 && mid <= 4379:
		return "extreme" // EU-Alert Level 1 / CMAS Presidential
	case mid >= 4380 && mid <= 4389:
		return "severe" // EU-Alert Level 2 / CMAS Extreme
	case mid >= 4390 && mid <= 4395:
		return "amber" // AMBER Alert
	case mid == 4396 || mid == 4397:
		return "test" // Monthly test / exercise
	case mid >= 4398 && mid <= 4399:
		return "info" // EU-Alert Level 3 / CMAS Severe
	case mid == 919:
		return "test" // ETWS test
	case mid >= 4352 && mid <= 4359:
		return "extreme" // ETWS earthquake/tsunami
	default:
		return "unknown"
	}
}

// findSerialPorts is a glob helper for serial port detection.
func findSerialPorts(pattern string) ([]string, error) {
	return filepath.Glob(pattern)
}
