package gateway

import (
	"encoding/json"
	"fmt"
)

// WebhookConfig holds the configuration for the webhook gateway.
type WebhookConfig struct {
	OutboundURL     string            `json:"outbound_url,omitempty"`
	OutboundHeaders map[string]string `json:"outbound_headers,omitempty"`
	OutboundMethod  string            `json:"outbound_method,omitempty"` // POST or PUT (default POST)
	InboundEnabled  bool              `json:"inbound_enabled"`
	InboundSecret   string            `json:"inbound_secret,omitempty"`
	RetryCount      int               `json:"retry_count"` // default 5
	TimeoutSec      int               `json:"timeout_sec"` // default 10
}

// DefaultWebhookConfig returns sensible defaults.
func DefaultWebhookConfig() WebhookConfig {
	return WebhookConfig{
		OutboundMethod: "POST",
		RetryCount:     5,
		TimeoutSec:     10,
	}
}

// ParseWebhookConfig parses JSON config into WebhookConfig.
func ParseWebhookConfig(data string) (*WebhookConfig, error) {
	cfg := DefaultWebhookConfig()
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		return nil, fmt.Errorf("parse webhook config: %w", err)
	}
	return &cfg, nil
}

// Validate checks required fields.
func (c *WebhookConfig) Validate() error {
	if c.OutboundURL == "" && !c.InboundEnabled {
		return fmt.Errorf("at least one of outbound_url or inbound_enabled is required")
	}
	if c.OutboundMethod == "" {
		c.OutboundMethod = "POST"
	}
	if c.OutboundMethod != "POST" && c.OutboundMethod != "PUT" {
		return fmt.Errorf("outbound_method must be POST or PUT")
	}
	if c.RetryCount <= 0 {
		c.RetryCount = 5
	}
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = 10
	}
	return nil
}

// Redacted returns a copy with secrets masked.
func (c WebhookConfig) Redacted() WebhookConfig {
	if c.InboundSecret != "" {
		c.InboundSecret = "****"
	}
	for k := range c.OutboundHeaders {
		if k == "Authorization" || k == "X-API-Key" || k == "X-Webhook-Secret" {
			c.OutboundHeaders[k] = "****"
		}
	}
	return c
}
