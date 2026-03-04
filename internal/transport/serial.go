package transport

// Serial port layer shared by Meshtastic and Iridium direct transports.
// Ported from HAL meshtastic_serial.go + iridium_driver.go.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
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

// openSerial opens a serial port and configures it via stty.
func openSerial(path string, baud int) (*os.File, error) {
	// Configure port via stty (raw mode, 8N1, no flow control)
	cmd := exec.Command("stty", "-F", path,
		fmt.Sprintf("%d", baud),
		"raw", "-echo", "-echoe", "-echok",
		"cs8", "-cstopb", "-parenb", "-crtscts", "-hupcl",
		"min", "1", "time", "1",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("stty %s: %s: %w", path, strings.TrimSpace(string(out)), err)
	}

	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	return file, nil
}

// wakeDevice sends the Meshtastic wake sequence (32 bytes of 0xC3).
func wakeDevice(file *os.File) error {
	wake := make([]byte, meshWakeLen)
	for i := range wake {
		wake[i] = meshStart2
	}
	if _, err := file.Write(wake); err != nil {
		return fmt.Errorf("wake write: %w", err)
	}
	time.Sleep(200 * time.Millisecond)
	return nil
}

// sendFrame sends a Meshtastic framed packet: [0x94][0xC3][len_msb][len_lsb][payload].
func sendFrame(file *os.File, payload []byte) error {
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
	_, err := file.Write(buf)
	return err
}

// meshFrameReader maintains a persistent accumulation buffer for extracting
// complete Meshtastic protobuf frames from a serial stream.
type meshFrameReader struct {
	file  *os.File
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

		// Set read deadline so file.Read() cannot block forever
		r.file.SetReadDeadline(time.Now().Add(meshReadTimeout))
		n, err := r.file.Read(buf)
		if err != nil {
			if os.IsTimeout(err) {
				continue
			}
			return nil, err
		}
		if n == 0 {
			continue
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

// sendAT sends an AT command and reads the response until OK/ERROR/READY.
// Used by Iridium direct transport. Caller must hold any relevant mutex.
func sendAT(file *os.File, command string, timeout time.Duration) (string, error) {
	// Drain pending data
	drainPort(file)

	// Send command with CR (no LF — Iridium protocol)
	if _, err := file.WriteString(command + "\r"); err != nil {
		return "", fmt.Errorf("write failed: %w", err)
	}

	return readATResponse(file, timeout)
}

// readATResponse reads until "OK" or "ERROR" is found, or timeout expires.
func readATResponse(file *os.File, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var resp strings.Builder
	buf := make([]byte, 256)

	for {
		if time.Now().After(deadline) {
			return resp.String(), fmt.Errorf("read timeout")
		}

		// Short slices for responsive timeout checking
		sliceEnd := time.Now().Add(50 * time.Millisecond)
		if sliceEnd.After(deadline) {
			sliceEnd = deadline
		}
		file.SetDeadline(sliceEnd)
		n, err := file.Read(buf)
		file.SetDeadline(time.Time{})

		if n > 0 {
			resp.Write(buf[:n])
			full := resp.String()

			if strings.Contains(full, "\r\nOK\r\n") ||
				strings.HasSuffix(strings.TrimSpace(full), "OK") ||
				strings.Contains(full, "\r\nERROR\r\n") ||
				strings.HasSuffix(strings.TrimSpace(full), "ERROR") ||
				strings.Contains(full, "READY") {
				return full, nil
			}
		}

		if err != nil && !isTimeoutError(err) {
			return resp.String(), err
		}
	}
}

// drainPort clears any pending data from the serial port.
func drainPort(file *os.File) {
	file.SetDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 1024)
	for {
		n, err := file.Read(buf)
		if n == 0 || err != nil {
			break
		}
	}
	file.SetDeadline(time.Time{})
}

// isTimeoutError checks if an error is a timeout.
func isTimeoutError(err error) bool {
	if os.IsTimeout(err) {
		return true
	}
	type timeouter interface {
		Timeout() bool
	}
	if te, ok := err.(timeouter); ok {
		return te.Timeout()
	}
	return false
}

// ============================================================================
// USB Device Detection (shared by Meshtastic + Iridium)
// ============================================================================

// Known Meshtastic VID:PID pairs
var knownMeshtasticVIDPIDs = map[string]bool{
	"303a:1001": true, // ESP32-S3 (Heltec V3, T-Beam S3)
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

	// Pass 1: VID:PID match (most reliable)
	for _, port := range allPorts {
		vidpid := findUSBVIDPID(port)
		if knownMeshtasticVIDPIDs[vidpid] {
			log.Info().Str("port", port).Str("vidpid", vidpid).Msg("meshtastic auto-detected by VID:PID")
			return port
		}
	}

	// Pass 2: ACM devices not recognized as GPS (ESP32-S3 native USB may lack sysfs VID)
	for _, port := range acmPorts {
		vidpid := findUSBVIDPID(port)
		if gpsVIDPIDs[vidpid] {
			continue
		}
		log.Info().Str("port", port).Msg("meshtastic auto-detected (ACM fallback)")
		return port
	}

	return ""
}

// autoDetectIridium scans serial ports for an Iridium modem using AT probe.
func autoDetectIridium(excludePort string) string {
	var ports []string
	if matches, err := filepath.Glob("/dev/ttyUSB*"); err == nil {
		ports = matches
	}
	if matches, err := filepath.Glob("/dev/ttyACM*"); err == nil {
		ports = append(ports, matches...)
	}

	for _, port := range ports {
		if port == excludePort {
			continue
		}
		// Skip known Meshtastic/GPS devices
		vidpid := findUSBVIDPID(port)
		if knownMeshtasticVIDPIDs[vidpid] || gpsVIDPIDs[vidpid] {
			continue
		}

		// AT probe
		if probeAT(port) {
			log.Info().Str("port", port).Msg("iridium auto-detected by AT probe")
			return port
		}
	}
	return ""
}

// probeAT sends a quick AT handshake to check if a port is an AT modem.
func probeAT(port string) bool {
	file, err := openSerial(port, 19200)
	if err != nil {
		return false
	}
	defer file.Close()

	// Try AT&K0 (disable flow control) then AT
	resp, err := sendAT(file, "AT&K0", 2*time.Second)
	if err == nil && strings.Contains(resp, "OK") {
		return true
	}

	resp, err = sendAT(file, "AT", 2*time.Second)
	return err == nil && strings.Contains(resp, "OK")
}
