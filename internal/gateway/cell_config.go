package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
)

// CellAddrPrefix qualifies a phone on the MeshSat 2G cell: "cell:1001".
// A cell MSISDN is a short number, not E.164, so the prefix keeps 1001 on
// the cell apart from sms_0's carrier numbers in the ledger, the rules,
// the message table and the directory. [MESHSAT-1557]
const CellAddrPrefix = "cell:"

// CellAddress gives a cell MSISDN its channel-qualified form.
func CellAddress(msisdn string) string { return CellAddrPrefix + msisdn }

// ParseCellAddress reads "cell:1001" or a bare "1001" (digits, an optional
// leading +) into the MSISDN the SMPP server knows. ok is false for
// anything else.
func ParseCellAddress(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) >= len(CellAddrPrefix) && strings.EqualFold(s[:len(CellAddrPrefix)], CellAddrPrefix) {
		s = s[len(CellAddrPrefix):]
	}
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 20 {
		return "", false
	}
	for i, r := range s {
		if r == '+' && i == 0 && len(s) > 1 {
			continue
		}
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return s, true
}

// CellConfig configures the gateway to the MeshSat 2G cell: one SMPP 3.4
// transceiver session to osmo-msc. [MESHSAT-1557]
type CellConfig struct {
	// Host is the SMPP server (the host that runs the cell's core); empty
	// means the gateway is not configured and will not start.
	Host string `json:"host"`
	// Port is the SMPP port (2775).
	Port int `json:"port"`
	// SystemID is the ESME name the cell knows (at most 15 characters).
	SystemID string `json:"system_id"`
	// Password is the ESME password (at most 8 characters). Empty here
	// means the environment supplies it (CELL_SMPP_PASSWORD) so the secret
	// need not live in the database.
	Password string `json:"password,omitempty"`
	// ShortCode is the kit's own number on the cell: texts to it are
	// relayed, and replies go out from it.
	ShortCode string `json:"short_code"`
	// Destinations are the phones a rule with no cell_destinations of its
	// own sends to.
	Destinations []string `json:"destinations,omitempty"`
	// ReplyWindowHours: when neither the rule nor Destinations name a
	// phone, every phone that texted the short code this recently gets
	// the text. 0 switches that off.
	ReplyWindowHours int `json:"reply_window_hours"`
	// DeliveryReceipts asks the cell for a delivery receipt on every text
	// (registered_delivery). In forward mode the submit response already
	// says the phone took it, so this is off unless the receipt path is
	// under test.
	DeliveryReceipts bool `json:"delivery_receipts"`
	// AcceptAnyDestination relays a phone's text whatever number it
	// dialled. Off, only texts to ShortCode are relayed and the rest are
	// refused with "number unassigned".
	AcceptAnyDestination bool `json:"accept_any_destination"`
	// SMSPrefix opens a relayed mesh text ("MeshSat <sender>: ...").
	SMSPrefix string `json:"sms_prefix"`
}

// DefaultCellConfig returns the defaults; Host stays empty.
func DefaultCellConfig() CellConfig {
	return CellConfig{Port: 2775, SystemID: "meshsat-bridge", ShortCode: "1000", ReplyWindowHours: 24, SMSPrefix: "MeshSat"}
}

// ParseCellConfig parses the gateway_config JSON.
func ParseCellConfig(data string) (*CellConfig, error) {
	cfg := DefaultCellConfig()
	if data != "" {
		if err := json.Unmarshal([]byte(data), &cfg); err != nil {
			return nil, fmt.Errorf("parse cell config: %w", err)
		}
	}
	return &cfg, nil
}

// Validate checks the limits SMPP 3.4 and the cell impose and normalises
// the phone lists to bare MSISDNs.
func (c *CellConfig) Validate() error {
	c.Host = strings.TrimSpace(c.Host)
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("cell: port %d out of range", c.Port)
	}
	c.SystemID = strings.TrimSpace(c.SystemID)
	if c.SystemID == "" {
		return fmt.Errorf("cell: system_id is required")
	}
	if len(c.SystemID) > 15 {
		return fmt.Errorf("cell: system_id %q is longer than 15 characters (SMPP 3.4)", c.SystemID)
	}
	if len(c.Password) > 8 {
		return fmt.Errorf("cell: the password is longer than 8 characters (SMPP 3.4; osmo-msc refuses its whole config over it)")
	}
	sc, ok := ParseCellAddress(c.ShortCode)
	if !ok {
		return fmt.Errorf("cell: short_code %q is not a number", c.ShortCode)
	}
	c.ShortCode = sc
	dests := make([]string, 0, len(c.Destinations))
	for _, d := range c.Destinations {
		m, ok := ParseCellAddress(d)
		if !ok {
			return fmt.Errorf("cell: destination %q is not a phone on the cell (digits, or cell:<digits>)", d)
		}
		dests = append(dests, m)
	}
	c.Destinations = dests
	if c.ReplyWindowHours < 0 {
		return fmt.Errorf("cell: reply_window_hours must be 0 or more")
	}
	if c.SMSPrefix == "" {
		c.SMSPrefix = "MeshSat"
	}
	return nil
}

// Redacted masks the password.
func (c CellConfig) Redacted() CellConfig {
	if c.Password != "" {
		c.Password = secretMask
	}
	return c
}
