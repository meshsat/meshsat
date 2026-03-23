package gateway

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
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
	TLSCert     string `json:"tls_cert,omitempty"`     // path to client certificate (PEM)
	TLSKey      string `json:"tls_key,omitempty"`      // path to client private key (PEM)
	TLSCA       string `json:"tls_ca,omitempty"`       // path to CA certificate (PEM) for server verification
	TLSInsecure bool   `json:"tls_insecure,omitempty"` // skip server certificate verification
	KeepAlive   int    `json:"keep_alive"`             // seconds
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
	// Validate TLS client certificate pair
	if (c.TLSCert != "") != (c.TLSKey != "") {
		return fmt.Errorf("tls_cert and tls_key must both be set or both empty")
	}
	if c.TLSCert != "" {
		if _, err := os.Stat(c.TLSCert); err != nil {
			return fmt.Errorf("tls_cert file not accessible: %w", err)
		}
		if _, err := os.Stat(c.TLSKey); err != nil {
			return fmt.Errorf("tls_key file not accessible: %w", err)
		}
	}
	if c.TLSCA != "" {
		if _, err := os.Stat(c.TLSCA); err != nil {
			return fmt.Errorf("tls_ca file not accessible: %w", err)
		}
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

// buildTLSConfig constructs a *tls.Config from the MQTT config fields.
// Returns nil if no TLS settings are configured.
func (c *MQTTConfig) buildTLSConfig() (*tls.Config, error) {
	if c.TLSCert == "" && c.TLSCA == "" && !c.TLSInsecure {
		return nil, nil
	}

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	// Client certificate (mTLS)
	if c.TLSCert != "" && c.TLSKey != "" {
		cert, err := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey)
		if err != nil {
			return nil, fmt.Errorf("load client certificate: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	// Custom CA for server verification
	if c.TLSCA != "" {
		caCert, err := os.ReadFile(c.TLSCA)
		if err != nil {
			return nil, fmt.Errorf("read CA certificate: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("CA certificate contains no valid PEM data")
		}
		tlsCfg.RootCAs = pool
	}

	if c.TLSInsecure {
		tlsCfg.InsecureSkipVerify = true
	}

	return tlsCfg, nil
}
