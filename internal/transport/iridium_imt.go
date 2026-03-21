package transport

// IMT (Iridium Messaging Transport) auto-detection and USB device identification
// for the RockBLOCK 9704. Complements direct_imt.go (SatTransport implementation)
// and jspr.go (JSPR protocol layer).
//
// The 9704 uses an FTDI FT234XD USB-serial chip (VID:PID 0403:6015), which is
// shared with generic FTDI X-series devices and some Iridium dev kits. Detection
// uses a two-pass strategy: VID:PID match followed by JSPR handshake probe at
// 230400 baud to distinguish from AT-command devices (9603N) on the same VID:PID.

import (
	"path/filepath"

	"github.com/rs/zerolog/log"
)

// Known RockBLOCK 9704 USB VID:PID (FTDI FT234XD).
// 0403:6015 is shared with generic FTDI X-series and some Iridium dev kits,
// so a JSPR probe is required to confirm.
var knownIMTVIDPIDs = map[string]bool{
	"0403:6015": true, // FTDI X series (RockBLOCK 9704, also 9603N dev kits)
}

// autoDetectIMT scans serial ports for a RockBLOCK 9704 using two-pass detection.
// Pass 1: match by known VID:PID + verify with JSPR handshake at 230400 baud.
// Pass 2: JSPR probe on remaining unrecognised ports.
// excludePorts lists port paths that are already claimed by other transports.
func autoDetectIMT(excludePorts []string) string {
	excluded := make(map[string]bool, len(excludePorts))
	for _, p := range excludePorts {
		excluded[p] = true
	}

	var ports []string
	if matches, err := filepath.Glob("/dev/ttyUSB*"); err == nil {
		ports = append(ports, matches...)
	}
	if matches, err := filepath.Glob("/dev/ttyACM*"); err == nil {
		ports = append(ports, matches...)
	}

	// Pass 1: VID:PID match + JSPR probe.
	// The 0403:6015 VID:PID is shared with Iridium 9603N dev kits (AT at 19200)
	// and generic FTDI adapters, so we verify with a JSPR handshake at 230400.
	for _, port := range ports {
		if excluded[port] {
			continue
		}
		vidpid := findUSBVIDPID(port)
		if knownIMTVIDPIDs[vidpid] {
			if probeJSPR(port) {
				log.Info().Str("port", port).Str("vidpid", vidpid).Msg("imt: 9704 auto-detected by VID:PID + JSPR probe")
				return port
			}
		}
	}

	// Pass 2: JSPR probe on unknown ports (skip all recognised device types).
	for _, port := range ports {
		if excluded[port] {
			continue
		}
		vidpid := findUSBVIDPID(port)
		if knownMeshtasticVIDPIDs[vidpid] || gpsVIDPIDs[vidpid] ||
			knownCellularVIDPIDs[vidpid] || knownAstrocastVIDPIDs[vidpid] ||
			knownZigBeeOnlyVIDPIDs[vidpid] || knownIridiumVIDPIDs[vidpid] {
			continue
		}
		if probeJSPR(port) {
			log.Info().Str("port", port).Msg("imt: 9704 auto-detected by JSPR probe")
			return port
		}
	}

	return ""
}
