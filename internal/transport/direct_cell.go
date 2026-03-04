package transport

// DirectCellTransport implements CellTransport with direct serial access to a SIM7600 4G/LTE modem.
// Follows the same patterns as DirectSatTransport — mutex-protected AT commands, background monitors.

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
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

	mu        sync.Mutex
	file      *os.File
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
	excludePorts []string
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

// Subscribe opens the serial connection and starts SMS monitor + signal polling.
func (t *DirectCellTransport) Subscribe(ctx context.Context) (<-chan CellEvent, error) {
	t.mu.Lock()
	if !t.connected {
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
	port := t.port
	if port == "" || port == "auto" {
		port = autoDetectCellular(t.excludePorts)
		if port == "" {
			return fmt.Errorf("no cellular modem found")
		}
	}

	file, err := openSerial(port, cellBaud)
	if err != nil {
		return err
	}

	t.file = file
	t.port = port

	// Initialize modem
	// ATE0 — disable echo
	sendAT(file, "ATE0", cellATTimeout)
	// AT&K0 — disable flow control
	sendAT(file, "AT&K0", cellATTimeout)
	// AT — basic check
	resp, err := sendAT(file, "AT", cellATTimeout)
	if err != nil || !strings.Contains(resp, "OK") {
		file.Close()
		return fmt.Errorf("AT check failed")
	}
	// AT+CGSN — get IMEI
	resp, err = sendAT(file, "AT+CGSN", cellATTimeout)
	if err == nil {
		t.imei = parseATValue(resp)
	}
	// AT+CGMM — get model
	resp, err = sendAT(file, "AT+CGMM", cellATTimeout)
	if err == nil {
		t.model = parseATValue(resp)
	}
	// AT+CMGF=1 — text mode SMS
	resp, err = sendAT(file, "AT+CMGF=1", cellATTimeout)
	if err != nil || strings.Contains(resp, "ERROR") {
		log.Warn().Msg("cellular: SMS text mode not supported, SMS will not work")
	}
	// AT+CNMI=2,1,0,0,0 — SMS notification (unsolicited +CMTI)
	sendAT(file, "AT+CNMI=2,1,0,0,0", cellATTimeout)
	// AT+CPIN? — SIM status
	resp, _ = sendAT(file, "AT+CPIN?", cellATTimeout)
	t.simState = parseCPIN(resp)
	// AT+CREG? — registration
	resp, _ = sendAT(file, "AT+CREG?", cellATTimeout)
	t.regStatus = parseCREG(resp)
	// AT+COPS? — operator
	resp, _ = sendAT(file, "AT+COPS?", cellATTimeout)
	t.operator = parseCOPS(resp)

	t.connected = true
	log.Info().Str("port", port).Str("imei", t.imei).Str("model", t.model).
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

		t.file.SetDeadline(time.Now().Add(100 * time.Millisecond))
		n, err := t.file.Read(buf)
		t.file.SetDeadline(time.Time{})
		t.mu.Unlock()

		if err != nil {
			if isTimeoutError(err) {
				continue
			}
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
				// +CMTI: "SM",3 — new SMS at index 3
				if strings.HasPrefix(s, "+CMTI:") {
					idx := parseCMTI(s)
					if idx >= 0 {
						go t.readAndEmitSMS(idx)
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

// readAndEmitSMS reads an SMS by index and emits it as an event.
func (t *DirectCellTransport) readAndEmitSMS(index int) {
	t.mu.Lock()
	if !t.connected || t.file == nil {
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
	t.emitEvent(CellEvent{
		Type:    "sms_received",
		Message: sms.Text,
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
			if !t.connected || t.file == nil {
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
	if !t.connected || t.file == nil {
		return fmt.Errorf("not connected")
	}

	// AT+CMGS="number"
	cmd := fmt.Sprintf("AT+CMGS=\"%s\"", to)
	drainPort(t.file)
	if _, err := t.file.WriteString(cmd + "\r"); err != nil {
		return fmt.Errorf("write CMGS failed: %w", err)
	}

	// Wait for ">" prompt
	deadline := time.Now().Add(5 * time.Second)
	t.file.SetDeadline(deadline)
	buf := make([]byte, 256)
	var resp strings.Builder
	for {
		if time.Now().After(deadline) {
			t.file.SetDeadline(time.Time{})
			return fmt.Errorf("timeout waiting for > prompt")
		}
		n, err := t.file.Read(buf)
		if n > 0 {
			resp.Write(buf[:n])
			if strings.Contains(resp.String(), ">") {
				break
			}
		}
		if err != nil && !isTimeoutError(err) {
			t.file.SetDeadline(time.Time{})
			return fmt.Errorf("read failed: %w", err)
		}
	}
	t.file.SetDeadline(time.Time{})

	// Send text + Ctrl+Z
	if _, err := t.file.WriteString(text + "\x1A"); err != nil {
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
	if !t.connected || t.file == nil {
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
	t.mu.Lock()
	defer t.mu.Unlock()
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
	if !t.connected || t.file == nil {
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
	if !t.connected || t.file == nil {
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
	}

	t.connected = false
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

// findSerialPorts is a glob helper for serial port detection.
func findSerialPorts(pattern string) ([]string, error) {
	return filepath.Glob(pattern)
}
