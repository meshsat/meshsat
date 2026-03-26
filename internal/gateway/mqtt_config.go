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
	BrokerURL    string `json:"broker_url"`
	Username     string `json:"username,omitempty"`
	Password     string `json:"password,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	TopicPrefix  string `json:"topic_prefix"`
	ChannelName  string `json:"channel_name"`
	QoS          int    `json:"qos"`
	TLS          bool   `json:"tls"`
	TLSCert      string `json:"tls_cert,omitempty"`      // path to client certificate (PEM)
	TLSKey       string `json:"tls_key,omitempty"`       // path to client private key (PEM)
	TLSCA        string `json:"tls_ca,omitempty"`        // path to CA certificate (PEM) for server verification
	TLSInsecure  bool   `json:"tls_insecure,omitempty"`  // skip server certificate verification
	KeepAlive    int    `json:"keep_alive"`              // seconds
	CredentialID string `json:"credential_id,omitempty"` // DB credential ID (takes precedence over file paths)
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

// CredentialLoader loads decrypted credential data from the credential cache.
// Returns JSON with ca_cert_pem, client_cert_pem, client_key_pem fields.
type CredentialLoader interface {
	LoadCredentialPEM(credentialID string) (caCertPEM, clientCertPEM, clientKeyPEM []byte, err error)
}

// credLoader is set by the manager when a credential loader is available.
var credLoader CredentialLoader

// SetCredentialLoader sets the global credential loader for TLS config building.
func SetCredentialLoader(cl CredentialLoader) {
	credLoader = cl
}

// buildTLSConfig constructs a *tls.Config from the MQTT config fields.
// If CredentialID is set and a credential loader is available, loads certs from DB.
// Otherwise falls back to filesystem paths. Returns nil if no TLS settings are configured.
func (c *MQTTConfig) buildTLSConfig() (*tls.Config, error) {
	// Try DB-backed credential first
	if c.CredentialID != "" && credLoader != nil {
		return c.buildTLSFromCredential()
	}

	// Fallback: filesystem paths (backward compat)
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

// buildTLSFromCredential loads TLS certs from the credential cache DB.
func (c *MQTTConfig) buildTLSFromCredential() (*tls.Config, error) {
	caCertPEM, clientCertPEM, clientKeyPEM, err := credLoader.LoadCredentialPEM(c.CredentialID)
	if err != nil {
		return nil, fmt.Errorf("load credential %s: %w", c.CredentialID, err)
	}

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	// Client certificate (mTLS) from DB
	if len(clientCertPEM) > 0 && len(clientKeyPEM) > 0 {
		cert, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
		if err != nil {
			return nil, fmt.Errorf("parse client certificate from DB: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	// CA certificate from DB
	if len(caCertPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCertPEM) {
			return nil, fmt.Errorf("DB CA certificate contains no valid PEM data")
		}
		tlsCfg.RootCAs = pool
	}

	if c.TLSInsecure {
		tlsCfg.InsecureSkipVerify = true
	}

	return tlsCfg, nil
}
