package gateway

import (
	"encoding/json"
	"fmt"
)

// AstrocastConfig holds the configuration for an Astrocast satellite gateway.
type AstrocastConfig struct {
	MaxUplinkBytes  int    `json:"max_uplink_bytes"`  // default 160
	PollIntervalSec int    `json:"poll_interval_sec"` // check for downlinks (default 300)
	FragmentEnabled bool   `json:"fragment_enabled"`  // auto-fragment messages >160 bytes (default true)
	GeolocEnabled   bool   `json:"geoloc_enabled"`    // include lat/lon in uplink metadata
	PowerMode       string `json:"power_mode"`        // "low_power", "balanced", "performance" (default "balanced")
}

// DefaultAstrocastConfig returns sensible defaults.
func DefaultAstrocastConfig() AstrocastConfig {
	return AstrocastConfig{
		MaxUplinkBytes:  160,
		PollIntervalSec: 300,
		FragmentEnabled: true,
		GeolocEnabled:   false,
		PowerMode:       "balanced",
	}
}

// ParseAstrocastConfig parses JSON config into AstrocastConfig.
func ParseAstrocastConfig(data string) (*AstrocastConfig, error) {
	cfg := DefaultAstrocastConfig()
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		return nil, fmt.Errorf("parse astrocast config: %w", err)
	}
	return &cfg, nil
}

// Validate checks required fields.
func (c *AstrocastConfig) Validate() error {
	if c.MaxUplinkBytes <= 0 || c.MaxUplinkBytes > 160 {
		c.MaxUplinkBytes = 160
	}
	if c.PollIntervalSec <= 0 {
		c.PollIntervalSec = 300
	}
	switch c.PowerMode {
	case "low_power", "balanced", "performance":
		// valid
	default:
		c.PowerMode = "balanced"
	}
	return nil
}
