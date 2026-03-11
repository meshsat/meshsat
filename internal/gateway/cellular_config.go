package gateway

import (
	"encoding/json"
	"fmt"
)

// CellularConfig holds the configuration for a cellular SMS/data gateway.
type CellularConfig struct {
	DestinationNumbers []string          `json:"destination_numbers"`         // phone numbers to send SMS to
	AllowedSenders     []string          `json:"allowed_senders,omitempty"`   // phone numbers allowed to send inbound (empty = all)
	SMSPrefix          string            `json:"sms_prefix"`                  // prefix for outbound SMS (default "[MeshSat]")
	MaxSMSSegments     int               `json:"max_sms_segments"`            // max SMS segments per message (default 1 = 160 chars)
	ForwardPortnums    []int             `json:"forward_portnums,omitempty"`  // portnums to forward (empty = use ForwardAll)
	ForwardAll         bool              `json:"forward_all"`                 // forward all message types
	InboundChannel     int               `json:"inbound_channel"`             // mesh channel for inbound SMS (default 0)
	InboundDestNode    string            `json:"inbound_dest_node,omitempty"` // target node for inbound SMS (empty = broadcast)
	APN                string            `json:"apn,omitempty"`               // LTE data APN (primary)
	APNFailoverList    []string          `json:"apn_failover_list,omitempty"` // ordered APN failover list (tried if primary fails)
	AutoConnectData    bool              `json:"auto_connect_data"`           // auto-connect LTE data on start
	AutoReconnect      bool              `json:"auto_reconnect"`              // auto-reconnect on data drop
	WebhookOutURL      string            `json:"webhook_out_url,omitempty"`   // outbound webhook URL
	WebhookOutHeaders  map[string]string `json:"webhook_out_headers,omitempty"`
	WebhookInEnabled   bool              `json:"webhook_in_enabled"`          // enable inbound webhook endpoint
	WebhookInSecret    string            `json:"webhook_in_secret,omitempty"` // shared secret for inbound webhook auth
	DynDNS             DynDNSConfig      `json:"dyndns,omitempty"`
}

// DynDNSConfig holds dynamic DNS updater configuration.
type DynDNSConfig struct {
	Enabled   bool   `json:"enabled"`
	Provider  string `json:"provider"`   // "duckdns", "noip", "dynu", "cloudflare", "custom"
	Domain    string `json:"domain"`     // e.g., "mymeshsat" for DuckDNS, FQDN for Cloudflare
	Token     string `json:"token"`      // DuckDNS token, Cloudflare API token, or other API key
	Username  string `json:"username"`   // for DynDNS v2 providers (noip, dynu)
	Password  string `json:"password"`   // for DynDNS v2 providers (noip, dynu)
	CustomURL string `json:"custom_url"` // for "custom" provider: template with {ip} and {hostname}
	Interval  int    `json:"interval"`   // update check interval in seconds (default 300)
	ZoneID    string `json:"zone_id"`    // Cloudflare DNS Zone ID
	RecordID  string `json:"record_id"`  // Cloudflare DNS Record ID (auto-resolved if empty)
}

// DefaultCellularConfig returns sensible defaults.
func DefaultCellularConfig() CellularConfig {
	return CellularConfig{
		SMSPrefix:      "[MeshSat]",
		MaxSMSSegments: 1,
		ForwardAll:     false,
		InboundChannel: 0,
		DynDNS: DynDNSConfig{
			Interval: 300,
		},
	}
}

// ParseCellularConfig parses JSON config into CellularConfig.
func ParseCellularConfig(data string) (*CellularConfig, error) {
	cfg := DefaultCellularConfig()
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		return nil, fmt.Errorf("parse cellular config: %w", err)
	}
	return &cfg, nil
}

// Validate checks required fields.
func (c *CellularConfig) Validate() error {
	if len(c.DestinationNumbers) == 0 && !c.WebhookInEnabled {
		return fmt.Errorf("at least one destination_number or webhook_in_enabled is required")
	}
	if c.MaxSMSSegments <= 0 {
		c.MaxSMSSegments = 1
	}
	if c.SMSPrefix == "" {
		c.SMSPrefix = "[MeshSat]"
	}
	if c.DynDNS.Enabled {
		if c.DynDNS.Provider == "" {
			return fmt.Errorf("dyndns.provider is required when dyndns is enabled")
		}
		if c.DynDNS.Domain == "" {
			return fmt.Errorf("dyndns.domain is required when dyndns is enabled")
		}
		if c.DynDNS.Interval <= 0 {
			c.DynDNS.Interval = 300
		}
	}
	return nil
}

// Redacted returns a copy with secrets masked.
func (c CellularConfig) Redacted() CellularConfig {
	if c.WebhookInSecret != "" {
		c.WebhookInSecret = "****"
	}
	if c.DynDNS.Token != "" {
		c.DynDNS.Token = "****"
	}
	if c.DynDNS.Password != "" {
		c.DynDNS.Password = "****"
	}
	return c
}
