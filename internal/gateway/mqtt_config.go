package gateway

import (
	"encoding/json"
	"fmt"
	"net/url"
)

// MQTTConfig holds the configuration for an MQTT gateway.
type MQTTConfig struct {
	BrokerURL   string `json:"broker_url"`
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	ClientID    string `json:"client_id,omitempty"`
	TopicPrefix string `json:"topic_prefix"`
	ChannelName string `json:"channel_name"`
	QoS         int    `json:"qos"`
	TLS         bool   `json:"tls"`
	KeepAlive   int    `json:"keep_alive"` // seconds
}

// DefaultMQTTConfig returns sensible defaults.
func DefaultMQTTConfig() MQTTConfig {
	return MQTTConfig{
		TopicPrefix: "msh/cubeos",
		ChannelName: "LongFast",
		QoS:         1,
		KeepAlive:   60,
		ClientID:    "meshsat",
	}
}

// Validate checks required fields.
func (c *MQTTConfig) Validate() error {
	if c.BrokerURL == "" {
		return fmt.Errorf("broker_url is required")
	}
	u, err := url.Parse(c.BrokerURL)
	if err != nil {
		return fmt.Errorf("invalid broker_url: %w", err)
	}
	if u.Scheme != "tcp" && u.Scheme != "ssl" && u.Scheme != "tls" && u.Scheme != "ws" && u.Scheme != "wss" {
		return fmt.Errorf("broker_url scheme must be tcp, ssl, tls, ws, or wss")
	}
	if c.QoS < 0 || c.QoS > 2 {
		return fmt.Errorf("qos must be 0, 1, or 2")
	}
	if c.TopicPrefix == "" {
		c.TopicPrefix = "msh/cubeos"
	}
	if c.ChannelName == "" {
		c.ChannelName = "LongFast"
	}
	if c.KeepAlive <= 0 {
		c.KeepAlive = 60
	}
	return nil
}

// ParseMQTTConfig parses JSON config into MQTTConfig.
func ParseMQTTConfig(data string) (*MQTTConfig, error) {
	cfg := DefaultMQTTConfig()
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		return nil, fmt.Errorf("parse mqtt config: %w", err)
	}
	return &cfg, nil
}

// Redacted returns a copy with password masked.
func (c MQTTConfig) Redacted() MQTTConfig {
	if c.Password != "" {
		c.Password = "****"
	}
	return c
}
