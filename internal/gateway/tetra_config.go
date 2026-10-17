package gateway

import (
	"encoding/json"
	"fmt"
	"strings"

	"meshsat/internal/tetra"
)

// TetraConfig is the TETRA gateway's settings (gateway type tetra, link
// tetra_0). Every field has a working default, so a radio plugged into a
// kit runs with "{}". [MESHSAT-1588]
type TetraConfig struct {
	// Port is the radio's AT port; empty is the radio the device
	// supervisor found (USB interface 0 of a Motorola 0cad:90xx).
	Port string `json:"port"`
	// Mode the gateway puts the radio in at start: dmo (Direct Mode, radio
	// to radio, no network: the default, a kit has no TETRA network), tmo
	// (a network) or keep (leave it as it is).
	Mode string `json:"mode"`
	// Destination is where a message with no address of its own goes: an
	// SSI (group or radio) or a full TSI. Empty is the radio's selected
	// group, the one its PTT talks to.
	Destination string `json:"destination"`
	// Prefix goes in front of a text relayed from the mesh, before the
	// sender's name. Empty sends "<sender>: <text>".
	Prefix string `json:"prefix"`
}

// DefaultTetraConfig is Direct Mode to the radio's own group.
func DefaultTetraConfig() TetraConfig {
	return TetraConfig{Mode: "dmo"}
}

// ParseTetraConfig reads a stored config over the defaults.
func ParseTetraConfig(raw string) (*TetraConfig, error) {
	cfg := DefaultTetraConfig()
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return nil, fmt.Errorf("tetra config: %w", err)
		}
	}
	cfg.Mode = strings.ToLower(strings.TrimSpace(cfg.Mode))
	if cfg.Mode == "" {
		cfg.Mode = "dmo"
	}
	cfg.Destination = strings.TrimSpace(cfg.Destination)
	cfg.Port = strings.TrimSpace(cfg.Port)
	return &cfg, nil
}

// Validate refuses settings the radio cannot take.
func (c *TetraConfig) Validate() error {
	switch c.Mode {
	case "dmo", "tmo", "keep":
	default:
		return fmt.Errorf("tetra: mode %q: dmo, tmo or keep", c.Mode)
	}
	if c.Destination != "" {
		if _, ok := tetra.ParseAddress(c.Destination); !ok {
			return fmt.Errorf("tetra: destination %q is not an SSI (1-16777214) or a TSI", c.Destination)
		}
	}
	if len(c.Prefix) > 20 {
		return fmt.Errorf("tetra: prefix longer than 20 characters (one SDS holds about 140)")
	}
	return nil
}
