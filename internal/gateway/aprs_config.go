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
}

// DefaultAPRSConfig returns sensible defaults for EU APRS.
func DefaultAPRSConfig() APRSConfig {
	return APRSConfig{
		KISSHost:     "localhost",
		KISSPort:     8001,
		SSID:         10, // -10 is conventional for igate
		APRSISServer: "euro.aprs2.net:14580",
		FrequencyMHz: 144.800, // EU APRS frequency
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
		c.KISSHost = "localhost"
	}
	if c.KISSPort <= 0 || c.KISSPort > 65535 {
		c.KISSPort = 8001
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
