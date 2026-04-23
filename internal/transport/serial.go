package transport

// Serial port layer shared by Meshtastic and Iridium direct transports.
// Ported from HAL meshtastic_serial.go + iridium_driver.go.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"go.bug.st/serial"
)

// Meshtastic serial framing constants
const (
	meshStart1      byte = 0x94
	meshStart2      byte = 0xC3
	meshMaxPayload       = 512
	meshWakeLen          = 32
	meshReadBufSize      = 1024
	meshReadTimeout      = 500 * time.Millisecond
)

// openSerial opens and configures a serial port using go.bug.st/serial (pure Go, no stty).
func openSerial(path string, baud int) (serial.Port, error) {
	mode := &serial.Mode{
		BaudRate: baud,
		DataBits: 8,
		StopBits: serial.OneStopBit,
		Parity:   serial.NoParity,
	}

	port, err := serial.Open(path, mode)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	// Default read timeout — callers override as needed
	port.SetReadTimeout(100 * time.Millisecond)

	return port, nil
}

// wakeDevice sends the Meshtastic wake sequence (32 bytes of 0xC3).
func wakeDevice(port serial.Port) error {
	wake := make([]byte, meshWakeLen)
	for i := range wake {
		wake[i] = meshStart2
	}
	if _, err := port.Write(wake); err != nil {
		return fmt.Errorf("wake write: %w", err)
	}
	time.Sleep(200 * time.Millisecond)
	return nil
}

// ProbeMeshtastic checks if a serial port speaks Meshtastic protocol.
// Sends the wake sequence and looks for the 0x94 0xC3 framing header in the response.
// Non-destructive — the device continues normal operation after probing.
//
// DTR guard: the port is opened with InitialStatusBits DTR=false and RTS=false
// (plus post-open defence in depth) so the probe does not pulse PWRKEY on a
// T-Call A7670E sharing the CH343 1a86:55d4 VID:PID. Without this guard the
// cascade could toggle the modem off during ProbeMeshtastic and the downstream
// ProbeAT would then time out against a mid-cold-boot modem, caching an
// "ambiguous" verdict for 30 min. [MESHSAT-646]
func ProbeMeshtastic(portName string) bool {
	mode := &serial.Mode{
		BaudRate: 115200,
		DataBits: 8,
		StopBits: serial.OneStopBit,
		Parity:   serial.NoParity,
		InitialStatusBits: &serial.ModemOutputBits{
			DTR: false,
			RTS: false,
		},
	}

	p, err := serial.Open(portName, mode)
	if err != nil {
		return false
	}
	defer p.Close()

	// Defence in depth — re-assert the clear in case a driver rewrote
	// the bits between open() and InitialStatusBits application.
	_ = p.SetDTR(false)
	_ = p.SetRTS(false)

	// Send Meshtastic wake sequence (32 bytes of 0xC3)
	wake := make([]byte, meshWakeLen)
	for i := range wake {
		wake[i] = meshStart2
	}
	if _, err := p.Write(wake); err != nil {
		return false
	}

	// Read response with timeout — Meshtastic devices respond with framed protobuf
	// packets (config, nodeinfo) that start with 0x94 0xC3.
	p.SetReadTimeout(500 * time.Millisecond)
	buf := make([]byte, 256)
	var accumulated []byte

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		n, _ := p.Read(buf)
		if n > 0 {
			accumulated = append(accumulated, buf[:n]...)
			if findStartMarker(accumulated) >= 0 {
				log.Debug().Str("port", portName).Int("bytes", len(accumulated)).Msg("meshtastic probe: framing header found")
				return true
			}
		}
		if n == 0 && len(accumulated) > 0 {
			break // read timeout with data but no marker
		}
	}

	return false
}

// sendFrame sends a Meshtastic framed packet: [0x94][0xC3][len_msb][len_lsb][payload].
func sendFrame(port serial.Port, payload []byte) error {
	if len(payload) > meshMaxPayload {
		return fmt.Errorf("payload too large (%d > %d)", len(payload), meshMaxPayload)
	}
	header := []byte{
		meshStart1,
		meshStart2,
		byte(len(payload) >> 8),
		byte(len(payload) & 0xFF),
	}
	buf := append(header, payload...)
	_, err := port.Write(buf)
	return err
}

// meshFrameReader maintains a persistent accumulation buffer for extracting
// complete Meshtastic protobuf frames from a serial stream.
type meshFrameReader struct {
	port  serial.Port
	accum []byte
}

// readFrame blocks until a complete FromRadio protobuf is received.
// Multi-frame OS reads are common during config download (~25 packets in burst).
func (r *meshFrameReader) readFrame(ctx context.Context) ([]byte, error) {
	buf := make([]byte, meshReadBufSize)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Check for a complete frame already in buffer
		if payload := r.extractFrame(); payload != nil {
			return payload, nil
		}

		// Read with timeout (set during port init) so we can check ctx
		n, err := r.port.Read(buf)
		if n == 0 && err == nil {
			continue // read timeout, no data
		}
		if err != nil {
			return nil, err
		}

		r.accum = append(r.accum, buf[:n]...)
	}
}

// extractFrame scans persistent accum buffer for a complete framed protobuf.
func (r *meshFrameReader) extractFrame() []byte {
	for {
		startIdx := findStartMarker(r.accum)
		if startIdx < 0 {
			if len(r.accum) > 1 {
				r.accum = r.accum[len(r.accum)-1:]
			}
			return nil
		}

		if startIdx > 0 {
			r.accum = r.accum[startIdx:]
		}

		if len(r.accum) < 4 {
			return nil
		}

		payloadLen := int(r.accum[2])<<8 | int(r.accum[3])

		if payloadLen > meshMaxPayload {
			log.Warn().Int("len", payloadLen).Msg("meshtastic: corrupted frame, skipping")
			r.accum = r.accum[2:]
			continue
		}
		if payloadLen == 0 {
			r.accum = r.accum[4:]
			continue
		}

		totalLen := 4 + payloadLen
		if len(r.accum) < totalLen {
			return nil
		}

		payload := make([]byte, payloadLen)
		copy(payload, r.accum[4:totalLen])
		r.accum = r.accum[totalLen:]
		return payload
	}
}

// findStartMarker finds index of 0x94 0xC3 start marker.
func findStartMarker(data []byte) int {
	for i := 0; i < len(data)-1; i++ {
		if data[i] == meshStart1 && data[i+1] == meshStart2 {
			return i
		}
	}
	return -1
}

// stripATDebugLines removes lines containing ANSI escape sequences from AT responses.
// The LILYGO T-Call A7670E with ATdebug firmware injects colored debug lines
// (e.g., "\x1b[34mDEBUG \x1b[0m| 22:58:49 453 [GPS] ...") into the serial stream
// between AT commands and their responses. These must be filtered out before
// checking for AT terminators (OK/ERROR) and before returning to parsers.
// The ESC byte (0x1B) never appears in legitimate AT responses.
func stripATDebugLines(raw string) string {
	if !strings.Contains(raw, "\x1b") {
		return raw // fast path: no ANSI codes
	}
	var clean strings.Builder
	clean.Grow(len(raw))
	for _, line := range strings.Split(raw, "\n") {
		if strings.Contains(line, "\x1b") {
			continue // drop lines with ANSI escape sequences
		}
		if clean.Len() > 0 {
			clean.WriteByte('\n')
		}
		clean.WriteString(line)
	}
	return clean.String()
}

// sendAT sends an AT command and reads the response until OK/ERROR/READY.
// Used by Iridium direct transport. Caller must hold any relevant mutex.
func sendAT(port serial.Port, command string, timeout time.Duration) (string, error) {
	// Drain pending data
	drainPort(port)

	// Send command with CR (no LF — Iridium protocol)
	if _, err := port.Write([]byte(command + "\r")); err != nil {
		return "", fmt.Errorf("write failed: %w", err)
	}

	return readATResponse(port, timeout)
}

// readATResponse reads until "OK" or "ERROR" is found, or timeout expires.
// Hard caps: timeout is enforced even if the modem sends continuous data,
// and response buffer is capped at 4KB to prevent runaway reads.
// Debug output from ATdebug firmware (ANSI escape lines) is stripped before
// checking terminators and before returning.
func readATResponse(port serial.Port, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var resp strings.Builder
	buf := make([]byte, 256)
	const maxResp = 4096

	// Use 50ms read slices for responsive timeout checking
	port.SetReadTimeout(50 * time.Millisecond)

	for {
		if time.Now().After(deadline) {
			return stripATDebugLines(resp.String()), fmt.Errorf("read timeout")
		}

		n, err := port.Read(buf)

		if n > 0 {
			resp.Write(buf[:n])

			// Strip ATdebug lines before checking terminators.
			// Raw buffer may exceed 4KB due to debug output, but the
			// cleaned response (what callers see) will be much smaller.
			clean := stripATDebugLines(resp.String())

			if strings.Contains(clean, "\r\nOK\r\n") ||
				strings.HasSuffix(strings.TrimSpace(clean), "OK") ||
				strings.Contains(clean, "\r\nERROR\r\n") ||
				strings.HasSuffix(strings.TrimSpace(clean), "ERROR") ||
				strings.Contains(clean, "READY") ||
				strings.Contains(clean, "+CMGS:") {
				return clean, nil
			}

			// Safety: stop reading if cleaned response is unreasonably large.
			// Use raw length for the cap since debug output is the concern.
			if resp.Len() > maxResp {
				return clean, fmt.Errorf("response too large (%d bytes)", resp.Len())
			}
		}

		if err != nil {
			return stripATDebugLines(resp.String()), err
		}
	}
}

// drainPort clears any pending data from the serial port.
func drainPort(port serial.Port) {
	port.SetReadTimeout(100 * time.Millisecond)
	buf := make([]byte, 1024)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		n, _ := port.Read(buf)
		if n == 0 {
			break
		}
	}
}

// ============================================================================
// USB Device Detection (shared by Meshtastic + Iridium)
// ============================================================================

// Known Meshtastic VID:PID pairs
var knownMeshtasticVIDPIDs = map[string]bool{
	"303a:1001": true, // ESP32-S3 (Heltec V3, T-Beam S3)
	"2886:0059": true, // Seeed XIAO ESP32-S3 (CDC-ACM, native USB)
	"1a86:55d4": true, // CH343 (T-Beam, Heltec V2)
	"1a86:7523": true, // CH340 (generic ESP32)
	"10c4:ea60": true, // CP2102/CP2104 (generic ESP32)
	"239a:8029": true, // RAK WisBlock (nRF52840)
	"239a:4405": true, // TTGO T-Echo (nRF52840)
	"1915:520f": true, // Nordic nRF52840 (RAK, T-Echo)
}

// GPS VID:PID pairs to exclude from radio scan
var gpsVIDPIDs = map[string]bool{
	"1546:01a6": true, // u-blox 7 older variant (ACM)
	"1546:01a7": true, // u-blox 7 (ACM)
	"1546:01a8": true, // u-blox 8 (ACM)
	"1546:01a9": true, // u-blox 9 (ACM)
	"1546:0502": true, // u-blox M8 (generic)
	"067b:23a3": true, // Prolific PL2303 (common GPS USB-serial)
	"067b:2303": true, // Prolific PL2303 legacy
}

// findUSBVIDPID walks up sysfs tree from tty device to find USB device's idVendor/idProduct.
func findUSBVIDPID(port string) string {
	devName := filepath.Base(port)
	sysPath := fmt.Sprintf("/sys/class/tty/%s/device", devName)
	current, err := filepath.EvalSymlinks(sysPath)
	if err != nil {
		return ""
	}

	// Walk up sysfs tree: ttyUSB=1 level, ttyACM CDC-ACM=2-3 levels
	for i := 0; i < 5; i++ {
		current = filepath.Dir(current)
		vidData, _ := os.ReadFile(filepath.Join(current, "idVendor"))
		pidData, _ := os.ReadFile(filepath.Join(current, "idProduct"))

		vid := strings.TrimSpace(string(vidData))
		pid := strings.TrimSpace(string(pidData))
		if vid != "" && pid != "" {
			return fmt.Sprintf("%s:%s", vid, pid)
		}
	}
	return ""
}

// findUSBInterfaceNum returns the USB interface number (e.g., "00", "01") for a serial port.
// Used to distinguish multi-interface modems like Huawei E220 where interface 0 is PPP/data
// and interface 1 is the AT command port.
func findUSBInterfaceNum(port string) string {
	devName := filepath.Base(port)
	sysPath := fmt.Sprintf("/sys/class/tty/%s/device", devName)
	current, err := filepath.EvalSymlinks(sysPath)
	if err != nil {
		return ""
	}

	// Walk up sysfs to find bInterfaceNumber
	for i := 0; i < 5; i++ {
		data, err := os.ReadFile(filepath.Join(current, "bInterfaceNumber"))
		if err == nil {
			return strings.TrimSpace(string(data))
		}
		current = filepath.Dir(current)
	}
	return ""
}

// autoDetectMeshtastic scans serial ports for a Meshtastic device.
// Three-pass strategy: VID:PID match → ACM fallback (excluding GPS).
func autoDetectMeshtastic() string {
	var acmPorts, usbPorts []string
	if matches, err := filepath.Glob("/dev/ttyACM*"); err == nil {
		acmPorts = matches
	}
	if matches, err := filepath.Glob("/dev/ttyUSB*"); err == nil {
		usbPorts = matches
	}
	allPorts := append(acmPorts, usbPorts...)

	// Pass 1: Unambiguous VID:PID match (Meshtastic-only, not shared with ZigBee/cellular)
	for _, port := range allPorts {
		vidpid := findUSBVIDPID(port)
		if knownMeshtasticVIDPIDs[vidpid] && !ambiguousZigBeeVIDPIDs[vidpid] && !knownCellularVIDPIDs[vidpid] {
			log.Info().Str("port", port).Str("vidpid", vidpid).Msg("meshtastic auto-detected by VID:PID")
			return port
		}
	}

	// Pass 2: Ambiguous VID:PID (shared with ZigBee) — skip cellular VID:PIDs entirely
	for _, port := range allPorts {
		vidpid := findUSBVIDPID(port)
		if knownMeshtasticVIDPIDs[vidpid] && ambiguousZigBeeVIDPIDs[vidpid] && !knownCellularVIDPIDs[vidpid] {
			// Skip if ZigBee responds on this port
			if ProbeZNP(port) {
				continue
			}
			log.Info().Str("port", port).Str("vidpid", vidpid).Msg("meshtastic auto-detected by VID:PID (ambiguous)")
			return port
		}
	}

	// Pass 3: ACM devices not recognized as GPS (ESP32-S3 native USB may lack sysfs VID).
	// Probe each candidate with a Meshtastic wake sequence to avoid false positives
	// on laptops and machines with non-Meshtastic ACM devices (MESHSAT-331).
	for _, port := range acmPorts {
		vidpid := findUSBVIDPID(port)
		if gpsVIDPIDs[vidpid] || knownCellularVIDPIDs[vidpid] {
			continue
		}
		if ProbeMeshtastic(port) {
			log.Info().Str("port", port).Msg("meshtastic auto-detected (ACM fallback, probe confirmed)")
			return port
		}
		log.Debug().Str("port", port).Msg("meshtastic: ACM port did not respond to probe, skipping")
	}

	return ""
}

// autoDetectIridium scans serial ports for an Iridium modem.
// First pass: prefer ports with known Iridium VID:PID (FTDI 0403:6001/6015).
// Second pass: AT probe on remaining unknown ports, skipping all other known device types.
func autoDetectIridium(excludePort string) string {
	var ports []string
	if matches, err := filepath.Glob("/dev/ttyUSB*"); err == nil {
		ports = matches
	}
	if matches, err := filepath.Glob("/dev/ttyACM*"); err == nil {
		ports = append(ports, matches...)
	}

	// First pass: match by known Iridium VID:PID (no AT probe needed)
	for _, port := range ports {
		if port == excludePort {
			continue
		}
		vidpid := findUSBVIDPID(port)
		if knownIridiumVIDPIDs[vidpid] {
			log.Info().Str("port", port).Str("vidpid", vidpid).Msg("iridium auto-detected by VID:PID")
			return port
		}
	}

	// Second pass: AT probe on unknown ports, skip all recognized device types
	for _, port := range ports {
		if port == excludePort {
			continue
		}
		vidpid := findUSBVIDPID(port)
		if knownMeshtasticVIDPIDs[vidpid] || gpsVIDPIDs[vidpid] ||
			knownCellularVIDPIDs[vidpid] ||
			knownZigBeeOnlyVIDPIDs[vidpid] {
			continue
		}

		// AT probe on truly unknown ports only
		if probeAT(port) {
			log.Info().Str("port", port).Msg("iridium auto-detected by AT probe")
			return port
		}
	}
	return ""
}

// probeAT sends a quick AT handshake to check if a port is an AT modem.
func probeAT(portPath string) bool {
	port, err := openSerial(portPath, 19200)
	if err != nil {
		return false
	}
	defer port.Close()

	// Try AT&K0 (disable flow control) then AT
	resp, err := sendAT(port, "AT&K0", 2*time.Second)
	if err == nil && strings.Contains(resp, "OK") {
		return true
	}

	resp, err = sendAT(port, "AT", 2*time.Second)
	return err == nil && strings.Contains(resp, "OK")
}

// Known Iridium VID:PID pairs
var knownIridiumVIDPIDs = map[string]bool{
	"0403:6001": true, // FTDI FT232R (Iridium 9603N)
	"0403:6015": true, // FTDI X series (Iridium dev kits)
}

// FindUSBDeviceID returns a stable hardware identifier for a USB tty port.
// Format is "VID:PID+SERIAL" if a USB serial number is available, or "VID:PID" otherwise.
// Returns empty string if the port is not a USB device.
func FindUSBDeviceID(port string) string {
	vidpid := findUSBVIDPID(port)
	if vidpid == "" {
		return ""
	}
	ser := FindUSBSerial(port)
	if ser != "" {
		return vidpid + "+" + ser
	}
	return vidpid
}

// FindUSBSerial reads the USB serial number from sysfs for a given tty port.
func FindUSBSerial(port string) string {
	devName := filepath.Base(port)
	sysPath := fmt.Sprintf("/sys/class/tty/%s/device", devName)
	current, err := filepath.EvalSymlinks(sysPath)
	if err != nil {
		return ""
	}

	for i := 0; i < 5; i++ {
		current = filepath.Dir(current)
		data, _ := os.ReadFile(filepath.Join(current, "serial"))
		ser := strings.TrimSpace(string(data))
		if ser != "" {
			return ser
		}
	}
	return ""
}

// findUSBProduct reads the USB product string from sysfs for a given tty port.
// Returns strings like "FT230X Basic UART" or "FT232R USB UART".
// Does NOT open the serial port — reads from sysfs only.
func findUSBProduct(port string) string {
	devName := filepath.Base(port)
	sysPath := fmt.Sprintf("/sys/class/tty/%s/device", devName)
	current, err := filepath.EvalSymlinks(sysPath)
	if err != nil {
		return ""
	}

	for i := 0; i < 5; i++ {
		current = filepath.Dir(current)
		data, _ := os.ReadFile(filepath.Join(current, "product"))
		prod := strings.TrimSpace(string(data))
		if prod != "" {
			return prod
		}
	}
	return ""
}

// ClassifyDevice returns the device type for a given VID:PID string.
// Returns one of: "meshtastic", "iridium", "cellular", "zigbee", "gps",
// "ambiguous", or "unknown".
//
// Some VID:PIDs (CP210x, CH343) are shared across multiple device classes.
// Previously this function silently returned the first class checked
// (meshtastic) which made the Bind dropdown mis-label cellular modems
// (T-Call A7670E on CH343 1a86:55d4) as meshtastic. Now we count how
// many classes claim the VID:PID and return "ambiguous" when more
// than one does — the Bind dropdown renders that in amber so the
// operator knows a probe is pending or a manual bind is needed.
func ClassifyDevice(vidpid string) string {
	inM := knownMeshtasticVIDPIDs[vidpid]
	inI := knownIridiumVIDPIDs[vidpid] || knownIMTVIDPIDs[vidpid]
	inC := knownCellularVIDPIDs[vidpid]
	inZ := knownZigBeeOnlyVIDPIDs[vidpid] || ambiguousZigBeeVIDPIDs[vidpid]
	inG := gpsVIDPIDs[vidpid]

	n := 0
	for _, b := range []bool{inM, inI, inC, inZ, inG} {
		if b {
			n++
		}
	}
	switch {
	case n == 0:
		return "unknown"
	case n > 1:
		return "ambiguous"
	case inM:
		return "meshtastic"
	case inI:
		return "iridium"
	case inC:
		return "cellular"
	case inZ:
		return "zigbee"
	case inG:
		return "gps"
	}
	return "unknown"
}

// ambiguousZigBeeVIDPIDs lists VID:PIDs shared between Meshtastic and ZigBee.
// ClassifyDeviceWithProbe uses ZNP protocol probing to disambiguate these.
var ambiguousZigBeeVIDPIDs = map[string]bool{
	"10c4:ea60": true, // CP210x — Meshtastic OR SONOFF ZBDongle-P (CC2652P)
	"1a86:55d4": true, // CH343 — Meshtastic OR SONOFF ZBDongle-E (EFR32MG21)
}

// probeCacheMu and probeCache prevent repeated ProbeZNP calls against the
// same port. [MESHSAT-510]
//
// The bridge's InterfaceManager runs scanDevices() every 5 seconds and
// calls ClassifyDeviceWithProbe on every serial port, including ports
// already claimed by a running gateway. Because the meshsat container runs
// with CAP_SYS_ADMIN, the TIOCEXCL lock the gateway holds is bypassed —
// the second open() succeeds, and on CP210x ZigBee dongles (SONOFF
// ZBDongle-P) the open asserts DTR/RTS, triggering the auto-BSL circuit
// and resetting the CC2652P Z-Stack firmware. On every reset the network
// goes back to DEV_HOLD and ZDO_MGMT_PERMIT_JOIN_REQ returns 0xC2
// (ZNwkInvalidRequest).
//
// The cache remembers the disambiguation result per port so we only pay
// the probe cost (and DTR/RTS risk) once per device appearance. The cache
// is invalidated by InvalidateProbeCache when a port vanishes, keyed by
// devPath — hot-swaps re-enter the probe path.
var (
	probeCacheMu sync.RWMutex
	probeCache   = map[string]probeCacheEntry{} // key: "vidpid|devPath"
)

type probeCacheEntry struct {
	result string
	at     time.Time
}

// probeCacheTTL caps how stale a cached classification can get in the
// worst case (e.g. if InvalidateProbeCache wasn't called on a hot-swap).
// 30 minutes is long enough that the periodic 5 s scanner never re-probes
// a healthy port but short enough that operator actions (like unplug +
// replug without triggering the supervisor) still converge.
const probeCacheTTL = 30 * time.Minute

// ClassifyDeviceWithProbe is like ClassifyDevice but runs a 3-way protocol
// probe (ZNP → Meshtastic → AT) for VID:PIDs shared between Meshtastic,
// ZigBee, and cellular. Results are cached per port under the same TTL to
// avoid repeated DTR-triggered resets on ZigBee dongles and T-Call boards.
// [MESHSAT-646]
//
// portPath is the serial device path (e.g. "/dev/ttyUSB3") needed for the
// probe. Order matters: ZNP first (cheap and safe for non-ZigBee), then
// Meshtastic (a 32-byte 0xC3 wake at 115200 is benign to the ZigBee stack
// we just ruled out), then AT last because on T-Call boards the open()
// syscall briefly asserts DTR regardless — we minimise that window via
// InitialStatusBits in ProbeAT.
//
// If all three probes miss, the base "ambiguous" verdict is cached and
// returned so /api/devices keeps rendering amber for operator attention.
func ClassifyDeviceWithProbe(vidpid, portPath string) string {
	base := ClassifyDevice(vidpid)
	// We only probe when the VID:PID is in the ambiguous-probe map
	// (shared with Meshtastic / ZigBee / Cellular). With the ambiguity-
	// aware ClassifyDevice, `base` is "ambiguous" for those entries —
	// accept "meshtastic" too so an already-cached result still
	// short-circuits the probe on legacy callers.
	if (base != "meshtastic" && base != "ambiguous") || !ambiguousZigBeeVIDPIDs[vidpid] || portPath == "" {
		return base
	}

	key := vidpid + "|" + portPath
	probeCacheMu.RLock()
	cached, ok := probeCache[key]
	probeCacheMu.RUnlock()
	if ok && time.Since(cached.at) < probeCacheTTL {
		return cached.result
	}

	result := base
	switch {
	case ProbeZNP(portPath):
		result = "zigbee"
	case ProbeMeshtastic(portPath):
		result = "meshtastic"
	case ProbeAT(portPath):
		result = "cellular"
	}

	probeCacheMu.Lock()
	probeCache[key] = probeCacheEntry{result: result, at: time.Now()}
	probeCacheMu.Unlock()
	return result
}

// ProbeAT checks if a serial port speaks AT command protocol at 115200
// baud (cellular default). Used by ClassifyDeviceWithProbe to resolve
// VID:PIDs shared with cellular modems (e.g. T-Call A7670E on CH343
// 1a86:55d4) after ZNP and Meshtastic probes have been ruled out.
// [MESHSAT-646]
//
// DTR guard: the port is opened with InitialStatusBits DTR=false and
// RTS=false so the serial library scrubs both lines immediately after
// open() and before any further I/O. On CH343 the kernel driver itself
// briefly asserts DTR during open() — which we can't prevent from
// userspace — but we keep the assertion window as small as the library
// allows. DTR must not stay asserted for the duration of the probe, as
// that would keep the ESP32 passthrough chip held in reset (T-Call) or
// pulse PWRKEY on every probe cycle.
func ProbeAT(portName string) bool {
	mode := &serial.Mode{
		BaudRate: 115200,
		DataBits: 8,
		StopBits: serial.OneStopBit,
		Parity:   serial.NoParity,
		InitialStatusBits: &serial.ModemOutputBits{
			DTR: false,
			RTS: false,
		},
	}

	p, err := serial.Open(portName, mode)
	if err != nil {
		return false
	}
	defer p.Close()

	// Defence in depth — re-assert the clear in case a driver rewrote
	// the bits between open() and InitialStatusBits application.
	_ = p.SetDTR(false)
	_ = p.SetRTS(false)

	resp, err := sendAT(p, "AT", 2*time.Second)
	return err == nil && strings.Contains(resp, "OK")
}

// InvalidateProbeCache drops cached classifications for a given port. Called
// by DeviceSupervisor when a port disappears, so the same /dev path being
// reassigned to a different device (hot-swap) will be re-classified.
func InvalidateProbeCache(portPath string) {
	if portPath == "" {
		return
	}
	probeCacheMu.Lock()
	defer probeCacheMu.Unlock()
	for k := range probeCache {
		if strings.HasSuffix(k, "|"+portPath) {
			delete(probeCache, k)
		}
	}
}

// ZigBee-only VID:PIDs (not shared with other device types).
var knownZigBeeOnlyVIDPIDs = map[string]bool{
	"0451:16a8": true, // TI CC2531 (ZigBee only)
	"1cf1:0030": true, // dresden elektronik ConBee/RaspBee
}
