package gateway

import "encoding/json"

// IridiumConfig holds the configuration for an Iridium satellite gateway.
type IridiumConfig struct {
	ForwardPortnums []int  `json:"forward_portnums,omitempty"` // portnums to forward (empty = use ForwardAll)
	ForwardAll      bool   `json:"forward_all"`                // forward all message types
	Compression     string `json:"compression"`                // "none" or "compact"
	AutoReceive     bool   `json:"auto_receive"`               // auto-receive on ring alerts
	PollInterval    int    `json:"poll_interval"`              // seconds, 0 = no polling
	MaxTextLength   int    `json:"max_text_length"`            // max text bytes in SBD (default 320)
	IncludePosition bool   `json:"include_position"`           // include GPS coords in compact encoding
	DLQMaxRetries   int    `json:"dlq_max_retries"`            // max retry attempts for failed sends (default 3)
	DLQRetryBase    int    `json:"dlq_retry_base_secs"`        // base retry interval in seconds (default 120, exponential backoff)
}

// DefaultIridiumConfig returns sensible defaults.
func DefaultIridiumConfig() IridiumConfig {
	return IridiumConfig{
		ForwardAll:      false,
		ForwardPortnums: []int{1}, // TEXT_MESSAGE only by default
		Compression:     "compact",
		AutoReceive:     true,
		PollInterval:    0,
		MaxTextLength:   320,
		IncludePosition: true,
		DLQMaxRetries:   3,
		DLQRetryBase:    120,
	}
}

// ParseIridiumConfig parses JSON config into IridiumConfig.
func ParseIridiumConfig(data string) (*IridiumConfig, error) {
	cfg := DefaultIridiumConfig()
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		return nil, err
	}
	if cfg.MaxTextLength <= 0 || cfg.MaxTextLength > 320 {
		cfg.MaxTextLength = 320
	}
	return &cfg, nil
}
