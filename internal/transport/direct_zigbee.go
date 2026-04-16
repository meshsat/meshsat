package transport

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

// Known ZigBee coordinator VID:PID pairs.
// Note: CP210x (10c4:ea60) and CH343 (1a86:55d4) overlap with Meshtastic —
// protocol probing is required to disambiguate.
var knownZigBeeVIDPIDs = map[string]bool{
	"10c4:ea60": true, // CP210x (SONOFF ZBDongle-P, CC2652P)
	"1a86:55d4": true, // CH9102 (SONOFF ZBDongle-E, EFR32MG21)
	"10c4:8a2a": true, // CP2102N (ConBee II, CC2538+CC2592)
	"0451:16a8": true, // TI CC2531 (older ZigBee stick)
	"1cf1:0030": true, // dresden elektronik ConBee/RaspBee
}

// ZigBeeDevice holds information about a paired ZigBee device.
type ZigBeeDevice struct {
	ShortAddr   uint16    `json:"short_addr"`
	IEEEAddr    string    `json:"ieee_addr"` // hex-encoded 8-byte IEEE address
	Endpoint    byte      `json:"endpoint"`
	LQI         byte      `json:"lqi"`
	LastSeen    time.Time `json:"last_seen"`
	Temperature *float64  `json:"temperature,omitempty"` // Celsius (from cluster 0x0402) [MESHSAT-511]
	Humidity    *float64  `json:"humidity,omitempty"`    // percent (from cluster 0x0405) [MESHSAT-511]
}

// ZigBeeEvent is emitted when data arrives from a ZigBee device.
type ZigBeeEvent struct {
	Type        string       `json:"type"` // "data", "join", "leave", "temperature", "humidity"
	Device      ZigBeeDevice `json:"device"`
	ClusterID   uint16       `json:"cluster_id"`
	Data        []byte       `json:"data"`
	Timestamp   time.Time    `json:"timestamp"`
	Temperature *float64     `json:"temperature,omitempty"` // decoded Celsius [MESHSAT-511]
	Humidity    *float64     `json:"humidity,omitempty"`    // decoded percent [MESHSAT-511]
}

// ZCL cluster IDs for sensor data [MESHSAT-511]
const (
	ZCLClusterTemperature = 0x0402
	ZCLClusterHumidity    = 0x0405
)

// DirectZigBeeTransport manages a CC2652P Z-Stack coordinator over serial.
type DirectZigBeeTransport struct {
	mu          sync.Mutex
	port        serial.Port
	portName    string
	running     bool
	cancelFn    context.CancelFunc
	devices     map[uint16]*ZigBeeDevice // shortAddr → device
	coordState  byte                     // ZNP device state
	transID     byte                     // incrementing transaction ID
	subscribers []chan ZigBeeEvent
	subMu       sync.RWMutex

	// Permit-join state
	permitJoinEnd time.Time // when permit-join expires (zero = not active)

	// Firmware info (populated after init)
	FirmwareVersion string
}

// NewDirectZigBeeTransport creates a new ZigBee transport.
func NewDirectZigBeeTransport() *DirectZigBeeTransport {
	return &DirectZigBeeTransport{
		devices: make(map[uint16]*ZigBeeDevice),
	}
}

// Subscribe returns a channel that receives ZigBee events.
func (z *DirectZigBeeTransport) Subscribe() chan ZigBeeEvent {
	z.subMu.Lock()
	defer z.subMu.Unlock()
	ch := make(chan ZigBeeEvent, 32)
	z.subscribers = append(z.subscribers, ch)
	return ch
}

// emit sends an event to all subscribers.
func (z *DirectZigBeeTransport) emit(evt ZigBeeEvent) {
	z.subMu.RLock()
	defer z.subMu.RUnlock()
	for _, ch := range z.subscribers {
		select {
		case ch <- evt:
		default:
		}
	}
}

// Start opens the serial port and initializes the Z-Stack coordinator.
func (z *DirectZigBeeTransport) Start(ctx context.Context, portName string) error {
	z.mu.Lock()
	defer z.mu.Unlock()

	if z.running {
		return fmt.Errorf("zigbee transport already running")
	}

	mode := &serial.Mode{
		BaudRate: 115200,
		DataBits: 8,
		StopBits: serial.OneStopBit,
		Parity:   serial.NoParity,
	}

	p, err := serial.Open(portName, mode)
	if err != nil {
		return fmt.Errorf("open zigbee serial %s: %w", portName, err)
	}

	// Drain stale data from serial buffer — ProbeZNP may have left residual
	// bytes from the identification probe. Without this drain, initCoordinator's
	// SYS_PING response may contain stale probe data mixed in. [MESHSAT-403]
	p.SetReadTimeout(200 * time.Millisecond)
	drain := make([]byte, 256)
	for {
		n, _ := p.Read(drain)
		if n == 0 {
			break
		}
	}
	// Settle delay — give the CC2652P time to finish processing any residual
	// probe data before we send the first real command. [MESHSAT-403]
	time.Sleep(100 * time.Millisecond)

	z.port = p
	z.portName = portName

	// Initialize coordinator
	if err := z.initCoordinator(); err != nil {
		p.Close()
		return fmt.Errorf("init coordinator: %w", err)
	}

	ctx, z.cancelFn = context.WithCancel(ctx)
	z.running = true

	go z.readLoop(ctx)

	log.Info().Str("port", portName).Str("firmware", z.FirmwareVersion).Msg("zigbee: coordinator started")
	return nil
}

// Stop shuts down the transport.
func (z *DirectZigBeeTransport) Stop() {
	z.mu.Lock()
	defer z.mu.Unlock()

	if !z.running {
		return
	}
	z.running = false
	if z.cancelFn != nil {
		z.cancelFn()
	}
	if z.port != nil {
		z.port.Close()
	}
	log.Info().Msg("zigbee: transport stopped")
}

// IsRunning returns true if the transport is active.
func (z *DirectZigBeeTransport) IsRunning() bool {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.running
}

// GetDevices returns all known paired devices.
func (z *DirectZigBeeTransport) GetDevices() []ZigBeeDevice {
	z.mu.Lock()
	defer z.mu.Unlock()
	devs := make([]ZigBeeDevice, 0, len(z.devices))
	for _, d := range z.devices {
		devs = append(devs, *d)
	}
	return devs
}

// Send sends data to a specific ZigBee device endpoint.
func (z *DirectZigBeeTransport) Send(dstAddr uint16, dstEP byte, clusterID uint16, data []byte) error {
	z.mu.Lock()
	defer z.mu.Unlock()

	if !z.running {
		return fmt.Errorf("zigbee transport not running")
	}

	z.transID++
	frame := BuildAFDataReq(dstAddr, dstEP, 1, clusterID, z.transID, data)
	return z.sendFrame(frame)
}

// PermitJoin sends ZDO_MGMT_PERMIT_JOIN_REQ to open the network for pairing.
// duration is clamped to 1-254 seconds. Use 0 to close the network.
func (z *DirectZigBeeTransport) PermitJoin(durationSec byte) error {
	z.mu.Lock()
	defer z.mu.Unlock()

	if !z.running {
		return fmt.Errorf("zigbee transport not running")
	}

	frame := BuildMgmtPermitJoinReq(durationSec)
	if err := z.sendFrame(frame); err != nil {
		return fmt.Errorf("permit join send: %w", err)
	}

	resp, err := z.readFrameTimeout(2 * time.Second)
	if err != nil {
		return fmt.Errorf("permit join response: %w", err)
	}
	if resp.IsCmd(CmdZDOMgmtPermitJoinRsp) && len(resp.Data) > 0 && resp.Data[0] != 0 {
		return fmt.Errorf("permit join failed: status=0x%02x", resp.Data[0])
	}

	if durationSec > 0 {
		z.permitJoinEnd = time.Now().Add(time.Duration(durationSec) * time.Second)
		log.Info().Uint8("duration_sec", durationSec).Msg("zigbee: permit join opened")
	} else {
		z.permitJoinEnd = time.Time{}
		log.Info().Msg("zigbee: permit join closed")
	}

	return nil
}

// PermitJoinRemaining returns the seconds remaining on the permit-join window.
// Returns 0 if permit-join is not active.
func (z *DirectZigBeeTransport) PermitJoinRemaining() int {
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.permitJoinEnd.IsZero() {
		return 0
	}
	rem := time.Until(z.permitJoinEnd)
	if rem <= 0 {
		z.permitJoinEnd = time.Time{}
		return 0
	}
	return int(rem.Seconds())
}

// ---- Internal ----

func (z *DirectZigBeeTransport) initCoordinator() error {
	// 1. Ping to verify ZNP is alive
	if err := z.sendFrame(BuildSysPing()); err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	resp, err := z.readFrameTimeout(2 * time.Second)
	if err != nil {
		return fmt.Errorf("ping response: %w", err)
	}
	if !resp.IsCmd(CmdSysPingRsp) {
		return fmt.Errorf("unexpected ping response: %s", resp)
	}
	log.Debug().Msg("zigbee: SYS_PING OK")

	// 2. Get firmware version
	if err := z.sendFrame(BuildSysVersion()); err != nil {
		return fmt.Errorf("version req: %w", err)
	}
	resp, err = z.readFrameTimeout(2 * time.Second)
	if err != nil {
		return fmt.Errorf("version response: %w", err)
	}
	if resp.IsCmd(CmdSysVersionRsp) {
		if info, err := ParseSysVersionRsp(resp.Data); err == nil {
			z.FirmwareVersion = fmt.Sprintf("Z-Stack %d.%d.%d (product=%d)",
				info.MajorRel, info.MinorRel, info.MaintRel, info.Product)
		}
	}

	// 3. Register AF endpoint 1 (Home Automation profile 0x0104)
	afReg := BuildAFRegister(1, 0x0104, 0x0005, // HA profile, configuration tool device
		[]uint16{0x0000, 0x0003, 0x0006, 0x0008, 0x0402, 0x0405}, // Basic, Identify, OnOff, Level, Temp, Humidity
		[]uint16{0x0000, 0x0003, 0x0006, 0x0008},
	)
	if err := z.sendFrame(afReg); err != nil {
		return fmt.Errorf("AF register: %w", err)
	}
	resp, err = z.readFrameTimeout(2 * time.Second)
	if err != nil {
		return fmt.Errorf("AF register response: %w", err)
	}
	if resp.IsCmd(CmdAFRegisterRsp) && len(resp.Data) > 0 && resp.Data[0] != 0 {
		log.Warn().Uint8("status", resp.Data[0]).Msg("zigbee: AF_REGISTER non-zero status (may be already registered)")
	}

	// 4. Start coordinator network
	if err := z.sendFrame(BuildZDOStartup()); err != nil {
		return fmt.Errorf("ZDO startup: %w", err)
	}
	resp, err = z.readFrameTimeout(5 * time.Second)
	if err != nil {
		return fmt.Errorf("ZDO startup response: %w", err)
	}
	if resp.IsCmd(CmdZDOStartupFromAppRsp) && len(resp.Data) > 0 {
		status := resp.Data[0]
		switch status {
		case 0:
			log.Info().Msg("zigbee: network restored from NV")
		case 1:
			log.Info().Msg("zigbee: new network started")
		case 2:
			log.Warn().Msg("zigbee: network startup failed")
			return fmt.Errorf("ZDO_STARTUP failed (status=2)")
		}
	}

	return nil
}

func (z *DirectZigBeeTransport) sendFrame(f ZNPFrame) error {
	encoded, err := EncodeZNP(f)
	if err != nil {
		return err
	}
	_, err = z.port.Write(encoded)
	return err
}

func (z *DirectZigBeeTransport) readFrameTimeout(timeout time.Duration) (ZNPFrame, error) {
	z.port.SetReadTimeout(timeout)
	buf := make([]byte, 256)
	var accumulated []byte

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		n, err := z.port.Read(buf)
		if n > 0 {
			accumulated = append(accumulated, buf[:n]...)
			frame, _, err := DecodeZNP(accumulated)
			if err == nil {
				return frame, nil
			}
		}
		if err != nil && len(accumulated) > 0 {
			continue
		}
	}
	return ZNPFrame{}, fmt.Errorf("read timeout after %v", timeout)
}

func (z *DirectZigBeeTransport) readLoop(ctx context.Context) {
	buf := make([]byte, 512)
	var accumulated []byte

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		z.port.SetReadTimeout(500 * time.Millisecond)
		n, err := z.port.Read(buf)
		if n > 0 {
			accumulated = append(accumulated, buf[:n]...)
			z.processAccumulated(&accumulated)
		}
		if err != nil {
			continue
		}
	}
}

func (z *DirectZigBeeTransport) processAccumulated(buf *[]byte) {
	for len(*buf) >= znpMinFrameLen {
		frame, consumed, err := DecodeZNP(*buf)
		if err != nil {
			if consumed > 0 {
				*buf = (*buf)[consumed:]
				continue
			}
			break // incomplete frame, wait for more data
		}
		*buf = (*buf)[consumed:]
		z.handleFrame(frame)
	}
}

func (z *DirectZigBeeTransport) handleFrame(f ZNPFrame) {
	switch {
	case f.IsCmd(CmdAFIncomingMsg):
		z.handleIncomingMsg(f)
	case f.IsCmd(CmdZDOStateChangeInd):
		if len(f.Data) > 0 {
			z.mu.Lock()
			z.coordState = f.Data[0]
			z.mu.Unlock()
			log.Info().Uint8("state", f.Data[0]).Msg("zigbee: coordinator state changed")
		}
	case f.IsCmd(CmdZDOEndDeviceAnnceInd):
		z.handleDeviceAnnounce(f)
	case f.IsCmd(CmdZDOPermitJoinInd):
		if len(f.Data) > 0 {
			dur := f.Data[0]
			log.Info().Uint8("duration", dur).Msg("zigbee: permit join indication")
			z.mu.Lock()
			if dur == 0 {
				z.permitJoinEnd = time.Time{}
			}
			z.mu.Unlock()
		}
	default:
		log.Debug().Str("frame", f.String()).Msg("zigbee: unhandled frame")
	}
}

func (z *DirectZigBeeTransport) handleIncomingMsg(f ZNPFrame) {
	msg, err := ParseAFIncomingMsg(f.Data)
	if err != nil {
		log.Warn().Err(err).Msg("zigbee: parse incoming msg failed")
		return
	}

	z.mu.Lock()
	dev, ok := z.devices[msg.SrcAddr]
	if !ok {
		dev = &ZigBeeDevice{ShortAddr: msg.SrcAddr, Endpoint: msg.SrcEP}
		z.devices[msg.SrcAddr] = dev
	}
	dev.LQI = msg.LQI
	dev.LastSeen = time.Now()

	// Decode ZCL Report Attributes for sensor clusters [MESHSAT-511]
	var temperature *float64
	var humidity *float64
	evtType := "data"

	switch msg.ClusterID {
	case ZCLClusterTemperature:
		if v, ok := decodeZCLInt16Attr(msg.Data); ok {
			t := float64(v) / 100.0 // ZCL temp is in 0.01 C
			temperature = &t
			dev.Temperature = &t
			evtType = "temperature"
			log.Info().Uint16("src", msg.SrcAddr).Float64("celsius", t).Msg("zigbee: temperature reading")
		}
	case ZCLClusterHumidity:
		if v, ok := decodeZCLUint16Attr(msg.Data); ok {
			h := float64(v) / 100.0 // ZCL humidity is in 0.01 %
			humidity = &h
			dev.Humidity = &h
			evtType = "humidity"
			log.Info().Uint16("src", msg.SrcAddr).Float64("percent", h).Msg("zigbee: humidity reading")
		}
	}
	z.mu.Unlock()

	log.Debug().
		Uint16("src", msg.SrcAddr).
		Uint16("cluster", msg.ClusterID).
		Uint8("lqi", msg.LQI).
		Int("len", len(msg.Data)).
		Msg("zigbee: incoming data")

	z.emit(ZigBeeEvent{
		Type:        evtType,
		Device:      *dev,
		ClusterID:   msg.ClusterID,
		Data:        msg.Data,
		Timestamp:   time.Now(),
		Temperature: temperature,
		Humidity:    humidity,
	})
}

// decodeZCLInt16Attr decodes a ZCL Report Attributes frame for a signed 16-bit value.
// Frame: frame_control(1) + seq(1) + attr_id(2) + data_type(1) + value(2)
func decodeZCLInt16Attr(data []byte) (int16, bool) {
	if len(data) < 7 {
		return 0, false
	}
	// Skip frame control (1), sequence (1), attr ID (2), data type (1) = 5 bytes
	val := int16(data[5]) | int16(data[6])<<8
	return val, true
}

// decodeZCLUint16Attr decodes a ZCL Report Attributes frame for an unsigned 16-bit value.
func decodeZCLUint16Attr(data []byte) (uint16, bool) {
	if len(data) < 7 {
		return 0, false
	}
	val := uint16(data[5]) | uint16(data[6])<<8
	return val, true
}

func (z *DirectZigBeeTransport) handleDeviceAnnounce(f ZNPFrame) {
	if len(f.Data) < 11 {
		return
	}
	srcAddr := uint16(f.Data[0]) | uint16(f.Data[1])<<8
	ieeeAddr := fmt.Sprintf("%02x%02x%02x%02x%02x%02x%02x%02x",
		f.Data[9], f.Data[8], f.Data[7], f.Data[6],
		f.Data[5], f.Data[4], f.Data[3], f.Data[2])

	z.mu.Lock()
	dev := &ZigBeeDevice{
		ShortAddr: srcAddr,
		IEEEAddr:  ieeeAddr,
		LastSeen:  time.Now(),
	}
	z.devices[srcAddr] = dev
	z.mu.Unlock()

	log.Info().
		Uint16("short_addr", srcAddr).
		Str("ieee", ieeeAddr).
		Msg("zigbee: device joined")

	z.emit(ZigBeeEvent{
		Type:      "join",
		Device:    *dev,
		Timestamp: time.Now(),
	})
}

// ProbeZNP checks if a serial port speaks Z-Stack ZNP protocol.
// Sends SYS_PING and checks for a valid response. Non-destructive.
func ProbeZNP(portName string) bool {
	mode := &serial.Mode{
		BaudRate: 115200,
		DataBits: 8,
		StopBits: serial.OneStopBit,
		Parity:   serial.NoParity,
	}

	p, err := serial.Open(portName, mode)
	if err != nil {
		return false
	}
	defer p.Close()

	// Clear DTR/RTS to prevent CC2652P auto-BSL reset circuit from triggering.
	// Many ZigBee dongles (SONOFF ZBDongle-P/E) have DTR+RTS wired to the BSL
	// circuit — asserting both enters bootloader mode and the coordinator won't
	// respond to ZNP commands until power-cycled. [MESHSAT-403]
	p.SetDTR(false)
	p.SetRTS(false)

	// Drain stale data and settle after potential DTR-triggered reset.
	// The CP210x kernel driver asserts DTR+RTS during open(), which triggers
	// the auto-BSL circuit on SONOFF ZBDongle-P/E and similar CC2652P boards.
	// After we clear DTR/RTS, the coordinator exits bootloader and starts the
	// Z-Stack firmware, which takes ~1-2s to initialize. [MESHSAT-403]
	p.SetReadTimeout(200 * time.Millisecond)
	drain := make([]byte, 256)
	for {
		n, _ := p.Read(drain)
		if n == 0 {
			break
		}
	}
	time.Sleep(1500 * time.Millisecond)

	// Try SYS_PING up to 3 times. The CC2652P may need additional time after
	// a DTR-triggered reset — the first SYS_PING may arrive while Z-Stack is
	// still initializing. Each retry drains and waits 500ms. [MESHSAT-403]
	frame := BuildSysPing()
	encoded, _ := EncodeZNP(frame)
	buf := make([]byte, 64)

	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			// Drain between retries
			p.SetReadTimeout(200 * time.Millisecond)
			for {
				n, _ := p.Read(buf)
				if n == 0 {
					break
				}
			}
			time.Sleep(500 * time.Millisecond)
		}

		if _, err := p.Write(encoded); err != nil {
			return false
		}

		p.SetReadTimeout(1 * time.Second)
		var accumulated []byte
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			n, _ := p.Read(buf)
			if n > 0 {
				accumulated = append(accumulated, buf[:n]...)
				resp, _, err := DecodeZNP(accumulated)
				if err == nil && resp.IsCmd(CmdSysPingRsp) {
					return true
				}
			}
		}
	}
	return false
}

// FindZigBeePort auto-detects a ZigBee coordinator dongle.
// Scans USB serial ports by VID:PID, then probes with ZNP SYS_PING.
func FindZigBeePort(excludePorts ...string) string {
	excludeSet := make(map[string]bool)
	for _, p := range excludePorts {
		excludeSet[p] = true
	}

	var candidates []string
	for _, pattern := range []string{"/dev/ttyUSB*", "/dev/ttyACM*"} {
		matches, _ := filepath.Glob(pattern)
		for _, port := range matches {
			if excludeSet[port] {
				continue
			}
			vidpid := findUSBVIDPID(port)
			if knownZigBeeVIDPIDs[strings.ToLower(vidpid)] {
				candidates = append(candidates, port)
			}
		}
	}

	// Protocol probe each candidate
	for _, port := range candidates {
		if ProbeZNP(port) {
			log.Info().Str("port", port).Msg("zigbee: coordinator detected via ZNP probe")
			return port
		}
	}

	return ""
}
