package gateway

import (
	"encoding/json"
	"fmt"
)

// ZigBeeConfig holds the configuration for a ZigBee 3.0 gateway.
type ZigBeeConfig struct {
	SerialPort      string `json:"serial_port,omitempty"`      // "auto" or explicit path (e.g., "/dev/ttyUSB0")
	InboundChannel  int    `json:"inbound_channel"`            // mesh channel for inbound ZigBee data (default 0)
	InboundDest     string `json:"inbound_dest,omitempty"`     // target node for inbound data (empty = broadcast)
	ForwardAll      bool   `json:"forward_all"`                // forward all mesh messages to ZigBee network
	ForwardPortnums []int  `json:"forward_portnums,omitempty"` // specific portnums to forward (empty + ForwardAll = all)
	DefaultDstAddr  uint16 `json:"default_dst_addr"`           // default ZigBee destination (0xFFFF = broadcast)
	DefaultDstEP    byte   `json:"default_dst_ep"`             // default destination endpoint (1)
	DefaultCluster  uint16 `json:"default_cluster"`            // default cluster ID for outbound (0x0006 = On/Off)
}

// DefaultZigBeeConfig returns sensible defaults.
func DefaultZigBeeConfig() ZigBeeConfig {
	return ZigBeeConfig{
		SerialPort:     "auto",
		InboundChannel: 0,
		ForwardAll:     false,
		DefaultDstAddr: 0xFFFF, // broadcast
		DefaultDstEP:   1,
		DefaultCluster: 0x0006, // On/Off cluster
	}
}

// ParseZigBeeConfig parses JSON config into ZigBeeConfig.
func ParseZigBeeConfig(data string) (*ZigBeeConfig, error) {
	cfg := DefaultZigBeeConfig()
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		return nil, fmt.Errorf("parse zigbee config: %w", err)
	}
	return &cfg, nil
}

// Validate checks required fields.
func (c *ZigBeeConfig) Validate() error {
	if c.DefaultDstEP == 0 {
		c.DefaultDstEP = 1
	}
	return nil
}

// Redacted returns a copy (ZigBee has no secrets to redact).
func (c ZigBeeConfig) Redacted() ZigBeeConfig {
	return c
}
