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
	Protocol        string `json:"protocol"` // "xml" (default) or "protobuf"
	CertFile        string `json:"cert_file,omitempty"`
	KeyFile         string `json:"key_file,omitempty"`
	CAFile          string `json:"ca_file,omitempty"`
	CredentialID    string `json:"credential_id,omitempty"` // DB credential ID (takes precedence over file paths)
	CallsignPrefix  string `json:"callsign_prefix"`
	CotStaleSec     int    `json:"cot_stale_seconds"`
	CoalesceSeconds int    `json:"coalesce_seconds"` // min seconds between PLI per device

	// Auto-enrollment: if set and no cert exists, enroll on startup via TAK Server port 8446
	EnrollURL      string `json:"enroll_url,omitempty"` // e.g., https://tak-server:8446
	EnrollUsername string `json:"enroll_username,omitempty"`
	EnrollPassword string `json:"enroll_password,omitempty"`
	AutoEnroll     bool   `json:"auto_enroll,omitempty"` // enable auto-enrollment on startup
	Multicast      bool   `json:"multicast"`             // enable UDP multicast SA (239.2.3.1:6969)
	MulticastIface string `json:"multicast_iface"`       // network interface for multicast (empty = all)
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
		// SSL requires one of: credential_id, cert files, or auto-enroll
		hasCred := c.CredentialID != ""
		hasFiles := c.CertFile != "" && c.KeyFile != ""
		hasEnroll := c.AutoEnroll && c.EnrollURL != "" && c.EnrollUsername != "" && c.EnrollPassword != ""
		if !hasCred && !hasFiles && !hasEnroll {
			return fmt.Errorf("SSL requires cert_file+key_file, credential_id, or auto_enroll with enroll_url/username/password")
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

// HasEnrollmentConfig returns true if auto-enrollment credentials are configured.
func (c *TAKConfig) HasEnrollmentConfig() bool {
	return c.AutoEnroll && c.EnrollURL != "" && c.EnrollUsername != "" && c.EnrollPassword != ""
}

// Redacted returns a copy with sensitive fields masked.
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
	if c.EnrollPassword != "" {
		c.EnrollPassword = "****"
	}
	return c
}
