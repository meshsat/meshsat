package gateway

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// HFConfig configures the 10 m HF gateway (hf_0): the receive side on the
// kit's RTL-SDR, and the licence-gated transmit side through a USB-audio
// radio keyed over CAT. [MESHSAT-1353]
type HFConfig struct {
	// FreqHz is the shout centre; 28.124 MHz is the codec's working centre.
	FreqHz int `json:"freq_hz"`
	// RTLTCPPort is the local port of the gateway's own rtl_tcp reader.
	RTLTCPPort int `json:"rtltcp_port"`
	// GainDB is the tuner gain in dB (snapped to the R828D steps by rtl_tcp).
	GainDB float64 `json:"gain_db"`
	// TXCallsign is the operator's callsign. Empty = transmit locked.
	TXCallsign string `json:"tx_callsign,omitempty"`
	// TXAudioDevice is the ALSA device of the radio's sound card (aplay -D).
	TXAudioDevice string `json:"tx_audio_device,omitempty"`
	// TXCATPort is the serial port for PTT over CAT (Kenwood set: TX; / RX;).
	TXCATPort string `json:"tx_cat_port,omitempty"`
	// TXAudioCentreHz is where the tones sit in the audio passband (1000 Hz
	// puts 28.124 MHz on a radio dialled to 28.123 MHz USB).
	TXAudioCentreHz float64 `json:"tx_audio_centre_hz"`
	// TXDestHash is the default LXMF delivery hash (32 hex) shouts are
	// addressed to when a message names none.
	TXDestHash string `json:"tx_dest_hash,omitempty"`
}

// DefaultHFConfig returns the defaults.
func DefaultHFConfig() HFConfig {
	return HFConfig{FreqHz: 28_124_000, RTLTCPPort: 6057, GainDB: 20, TXAudioCentreHz: 1000}
}

// ParseHFConfig parses the gateway_config JSON.
func ParseHFConfig(data string) (*HFConfig, error) {
	cfg := DefaultHFConfig()
	if data != "" {
		if err := json.Unmarshal([]byte(data), &cfg); err != nil {
			return nil, fmt.Errorf("parse hf config: %w", err)
		}
	}
	return &cfg, nil
}

// Validate checks the ranges.
func (c *HFConfig) Validate() error {
	if c.FreqHz < 28_000_000 || c.FreqHz > 29_700_000 {
		return fmt.Errorf("hf: freq_hz %d is outside the 10 m band", c.FreqHz)
	}
	if c.RTLTCPPort < 1024 || c.RTLTCPPort > 65535 {
		return fmt.Errorf("hf: rtltcp_port %d out of range", c.RTLTCPPort)
	}
	if c.GainDB < 0 || c.GainDB > 50 {
		return fmt.Errorf("hf: gain_db %.1f out of range", c.GainDB)
	}
	if c.TXDestHash != "" {
		if b, err := hex.DecodeString(c.TXDestHash); err != nil || len(b) != 16 {
			return fmt.Errorf("hf: tx_dest_hash must be 32 hex characters")
		}
	}
	if c.TXAudioCentreHz < 300 || c.TXAudioCentreHz > 2500 {
		return fmt.Errorf("hf: tx_audio_centre_hz %.0f out of the audio passband", c.TXAudioCentreHz)
	}
	return nil
}
