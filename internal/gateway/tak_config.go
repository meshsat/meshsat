package gateway

import (
	"encoding/json"
	"fmt"
)

// TAKConfig holds the configuration for the TAK/CoT gateway.
type TAKConfig struct {
	Host            string `json:"tak_host"`
	Port            int    `json:"tak_port"`
	SSL             bool   `json:"tak_ssl"`
	CertFile        string `json:"cert_file,omitempty"`
	KeyFile         string `json:"key_file,omitempty"`
	CAFile          string `json:"ca_file,omitempty"`
	CallsignPrefix  string `json:"callsign_prefix"`
	CotStaleSec     int    `json:"cot_stale_seconds"`
	CoalesceSeconds int    `json:"coalesce_seconds"` // min seconds between PLI per device
}

// DefaultTAKConfig returns sensible defaults.
func DefaultTAKConfig() TAKConfig {
	return TAKConfig{
		Port:            8087,
		CallsignPrefix:  "MESHSAT",
		CotStaleSec:     300,
		CoalesceSeconds: 30,
	}
}

// ParseTAKConfig parses JSON config into TAKConfig.
func ParseTAKConfig(data string) (*TAKConfig, error) {
	cfg := DefaultTAKConfig()
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		return nil, fmt.Errorf("parse tak config: %w", err)
	}
	return &cfg, nil
}

// Validate checks required fields.
func (c *TAKConfig) Validate() error {
	if c.Host == "" {
		return fmt.Errorf("tak_host is required")
	}
	if c.Port <= 0 || c.Port > 65535 {
		c.Port = 8087
	}
	if c.SSL {
		if c.CertFile == "" || c.KeyFile == "" {
			return fmt.Errorf("cert_file and key_file required when tak_ssl is true")
		}
	}
	if c.CallsignPrefix == "" {
		c.CallsignPrefix = "MESHSAT"
	}
	if c.CotStaleSec <= 0 {
		c.CotStaleSec = 300
	}
	if c.CoalesceSeconds <= 0 {
		c.CoalesceSeconds = 30
	}
	return nil
}

// Redacted returns a copy with file paths masked.
func (c TAKConfig) Redacted() TAKConfig {
	if c.CertFile != "" {
		c.CertFile = "****"
	}
	if c.KeyFile != "" {
		c.KeyFile = "****"
	}
	if c.CAFile != "" {
		c.CAFile = "****"
	}
	return c
}
