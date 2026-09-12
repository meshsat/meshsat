package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// APRSConfig holds the configuration for the APRS gateway.
type APRSConfig struct {
	KISSHost     string  `json:"kiss_host"`
	KISSPort     int     `json:"kiss_port"`
	Callsign     string  `json:"callsign"`
	SSID         int     `json:"ssid"`
	APRSISEnable bool    `json:"aprs_is_enabled"`
	APRSISServer string  `json:"aprs_is_server"`
	APRSISPass   string  `json:"aprs_is_passcode"`
	FrequencyMHz float64 `json:"frequency_mhz"`

	// Bundled-Direwolf supervisor settings. [MESHSAT-516/517]
	// When ExternalDirewolf is true, MeshSat connects to a KISS server on
	// KISSHost:KISSPort managed outside the container (legacy host-side
	// systemd path). Default (false) starts direwolf inside the container
	// with the settings below.
	ExternalDirewolf bool   `json:"external_direwolf"`
	AudioCard        string `json:"audio_card"` // ALSA card name, e.g. "AllInOneCable"
	PTTDevice        string `json:"ptt_device"` // e.g. "/dev/ttyACM1"
	PTTLine          string `json:"ptt_line"`   // "RTS" or "DTR"
	ModemBaud        int    `json:"modem_baud"` // 1200 (AFSK) or 9600 (G3RUH)

	// Hardware KISS TNC over a serial port (PicoAPRS V4 on USB-C). When
	// KISSDevice is set the gateway opens it instead of dialling
	// KISSHost:KISSPort, no Direwolf is spawned and no sound card is
	// needed. Use the /dev/serial/by-id path so a re-enumeration keeps
	// the name. [MESHSAT-821]
	KISSDevice string `json:"kiss_device"`
	KISSBaud   int    `json:"kiss_baud"` // 0 = 115200

	// Direwolf channel timing, in Direwolf's own units (10 ms for the
	// delays, 0..255 for persist). Zero means Direwolf's default. The kit
	// handhelds need a longer preamble than the 300 ms default before
	// their squelch opens: the value is measured per chain with the link
	// test, not guessed. [MESHSAT-857]
	TXDelay  int `json:"tx_delay"`  // TXDELAY, default 30 (300 ms)
	TXTail   int `json:"tx_tail"`   // TXTAIL, default 10 (100 ms)
	Persist  int `json:"persist"`   // PERSIST p-persistence, default 63
	SlotTime int `json:"slot_time"` // SLOTTIME, default 10 (100 ms)

	// Status beacon: a plain, readable APRS status frame every BeaconSecs
	// (0 = off). It is the liveness signal the peer kit's receive watchdog
	// expects on a two-kit network and it lets any APRS receiver see the
	// kit. BeaconText defaults to "MeshSat <callsign> ok". [MESHSAT-857]
	BeaconSecs int    `json:"beacon_secs"`
	BeaconText string `json:"beacon_text"`

	// Transmit every outbound message TXRepeat times, TXRepeatGapMs apart
	// (0 or 1 = once, gap default 1500 ms). APRS UI frames carry no
	// acknowledgement, so a frame lost to a collision or a late squelch is
	// a lost message; a second copy turns a 5 percent loss into a quarter
	// of a percent and the far kit's payload dedup drops the duplicate.
	// Beacons get the same copies. [MESHSAT-857]
	TXRepeat      int `json:"tx_repeat"`
	TXRepeatGapMs int `json:"tx_repeat_gap_ms"`

	// BeaconRepeatGapMs spaces the copies of a BEACON. It must differ from
	// TXRepeatGapMs: when both pairs use the same gap they march in lockstep,
	// so a beacon pair that starts near a message pair swallows BOTH copies of
	// the message inside the receiver's own transmission, and a half-duplex
	// radio hears nothing while it keys. Measured 10 Sep 2026: of the lost
	// frames, 8 of 11 and 7 of 8 fell within 2 s of the receiver's own
	// transmit, and 1 to 3 messages in 20 lost both copies. Zero derives a
	// value from TXRepeatGapMs rather than sharing it. [MESHSAT-1021]
	BeaconRepeatGapMs int `json:"beacon_repeat_gap_ms"`
}

// Direwolf timing defaults (Direwolf's own), used when a field is zero.
const (
	direwolfDefaultTXDelay  = 30
	direwolfDefaultTXTail   = 10
	direwolfDefaultPersist  = 63
	direwolfDefaultSlotTime = 10
)

func orDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// SerialTNC reports whether the gateway talks to a hardware TNC over serial.
func (c APRSConfig) SerialTNC() bool { return c.KISSDevice != "" }

// DefaultAPRSConfig returns sensible defaults for EU APRS on an AIOC kit.
// PTT defaults to CM108 HID (AIOC's actual PTT path) — the ACM serial
// port on the AIOC is present but NOT wired to the radio's PTT line.
// Setting PTTLine to "RTS" or "DTR" opts into serial PTT for non-AIOC
// cables that do wire it that way.
func DefaultAPRSConfig() APRSConfig {
	// First-boot defaults for the serial TNC come from the environment so
	// a kit can be switched to a PicoAPRS from its compose file; the stored
	// gateway config wins once it carries the keys. [MESHSAT-821]
	kissBaud, _ := strconv.Atoi(os.Getenv("MESHSAT_APRS_KISS_BAUD"))
	return APRSConfig{
		KISSDevice:   os.Getenv("MESHSAT_APRS_KISS_DEVICE"),
		KISSBaud:     kissBaud,
		KISSHost:     "127.0.0.1",
		KISSPort:     8001,
		SSID:         10, // -10 is conventional for igate
		APRSISServer: "euro.aprs2.net:14580",
		FrequencyMHz: 144.800, // EU APRS frequency
		AudioCard:    "AllInOneCable",
		PTTDevice:    "",
		PTTLine:      "", // empty => PTT CM108 (auto HID discovery)
		ModemBaud:    1200,
	}
}

// ParseAPRSConfig parses JSON config into APRSConfig.
func ParseAPRSConfig(data string) (*APRSConfig, error) {
	cfg := DefaultAPRSConfig()
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		return nil, fmt.Errorf("parse aprs config: %w", err)
	}
	return &cfg, nil
}

// Validate checks required fields.
func (c *APRSConfig) Validate() error {
	if c.Callsign == "" {
		return fmt.Errorf("callsign is required for APRS")
	}
	if c.SSID < 0 || c.SSID > 15 {
		return fmt.Errorf("ssid must be 0-15")
	}
	if c.KISSHost == "" {
		c.KISSHost = "127.0.0.1"
	}
	if c.KISSPort <= 0 || c.KISSPort > 65535 {
		c.KISSPort = 8001
	}
	if c.KISSDevice != "" {
		// A hardware TNC replaces Direwolf entirely.
		c.ExternalDirewolf = true
		if c.KISSBaud <= 0 {
			c.KISSBaud = 115200
		}
	}
	if !c.ExternalDirewolf {
		if c.AudioCard == "" {
			c.AudioCard = "AllInOneCable"
		}
		// PTTDevice/PTTLine default to empty so the supervisor emits
		// `PTT CM108` (HID GPIO, what AIOC actually uses). Setting
		// PTTLine to RTS/DTR is an explicit opt-in for serial PTT.
		if c.ModemBaud == 0 {
			c.ModemBaud = 1200
		}
	}
	if c.APRSISEnable {
		if c.APRSISServer == "" {
			c.APRSISServer = "euro.aprs2.net:14580"
		}
		if c.APRSISPass == "" {
			return fmt.Errorf("aprs_is_passcode is required when APRS-IS is enabled")
		}
	}
	if c.FrequencyMHz == 0 {
		c.FrequencyMHz = 144.800
	}
	return nil
}

// Redacted returns a copy with secrets masked.
func (c APRSConfig) Redacted() APRSConfig {
	if c.APRSISPass != "" {
		c.APRSISPass = "****"
	}
	return c
}
