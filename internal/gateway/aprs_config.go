package gateway

import (
	"encoding/json"
	"fmt"
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
}

// DefaultAPRSConfig returns sensible defaults for EU APRS on an AIOC kit.
// PTT defaults to CM108 HID (AIOC's actual PTT path) — the ACM serial
// port on the AIOC is present but NOT wired to the radio's PTT line.
// Setting PTTLine to "RTS" or "DTR" opts into serial PTT for non-AIOC
// cables that do wire it that way.
func DefaultAPRSConfig() APRSConfig {
	return APRSConfig{
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
