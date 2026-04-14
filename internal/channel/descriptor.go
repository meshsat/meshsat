package channel

import "time"

// ChannelDescriptor describes a transport channel's capabilities.
type ChannelDescriptor struct {
	ID            string        `json:"id"`
	Label         string        `json:"label"`
	IsPaid        bool          `json:"is_paid"`
	CanSend       bool          `json:"can_send"`
	CanReceive    bool          `json:"can_receive"`
	BinaryCapable bool          `json:"binary_capable"` // false = text-only (SMS, MQTT, webhook), needs base64 after encrypt/compress
	MaxPayload    int           `json:"max_payload"`    // 0 = unlimited
	DefaultTTL    time.Duration `json:"default_ttl"`    // 0 = no default TTL; applied when rule has no ttl_seconds
	IsSatellite   bool          `json:"is_satellite"`   // true for pass-scheduled channels (iridium)
	RetryConfig   RetryConfig   `json:"retry_config"`
	Options       []OptionField `json:"options,omitempty"`
}

// RetryConfig defines channel-specific retry behavior.
type RetryConfig struct {
	Enabled     bool          `json:"enabled"`
	InitialWait time.Duration `json:"initial_wait"`
	MaxWait     time.Duration `json:"max_wait"`
	MaxRetries  int           `json:"max_retries"`  // 0 = infinite
	BackoffFunc string        `json:"backoff_func"` // "exponential", "linear", "isu"
}

// OptionField describes a per-channel config field for the rule editor UI.
type OptionField struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Type    string   `json:"type"` // "text", "number", "select", "checkbox"
	Default string   `json:"default"`
	Options []string `json:"options,omitempty"` // for select type
}
