package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"meshsat/internal/transport"
)

// RadioSetupStatus represents the detected configuration state of the connected Meshtastic device.
type RadioSetupStatus struct {
	Connected    bool              `json:"connected"`
	Unconfigured bool              `json:"unconfigured"`
	NodeNum      uint32            `json:"node_num,omitempty"`
	LongName     string            `json:"long_name,omitempty"`
	ShortName    string            `json:"short_name,omitempty"`
	HWModel      string            `json:"hw_model,omitempty"`
	Region       string            `json:"region"`
	Issues       []RadioSetupIssue `json:"issues"`
}

// RadioSetupIssue describes a single configuration problem detected on the device.
type RadioSetupIssue struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // "error", "warning"
	Message  string `json:"message"`
}

// defaultNamePattern matches the factory-default Meshtastic node name "Meshtastic XXXX".
var defaultNamePattern = regexp.MustCompile(`(?i)^meshtastic[_ ][0-9a-f]{4}$`)

// handleGetRadioSetup detects whether the connected Meshtastic device is unconfigured.
// @Summary Detect radio setup status
// @Description Reads the device configuration and node info to determine if the radio needs initial setup
// @Tags radio-setup
// @Produce json
// @Success 200 {object} RadioSetupStatus
// @Failure 503 {object} map[string]string
// @Router /api/radio/setup [get]
func (s *Server) handleGetRadioSetup(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	ctx := r.Context()

	// Check connection status
	status, err := s.mesh.GetStatus(ctx)
	if err != nil || !status.Connected {
		writeJSON(w, http.StatusOK, RadioSetupStatus{
			Connected:    false,
			Unconfigured: true,
			Region:       "UNSET",
			Issues: []RadioSetupIssue{{
				Code:     "no_device",
				Severity: "error",
				Message:  "No Meshtastic device connected",
			}},
		})
		return
	}

	// Gather config and node data
	config, err := s.mesh.GetConfig(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read config: "+err.Error())
		return
	}

	nodes, err := s.mesh.GetNodes(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read nodes: "+err.Error())
		return
	}

	result := RadioSetupStatus{
		Connected: true,
		Region:    "UNSET",
	}

	// Find local node info
	for _, n := range nodes {
		if n.Num == uint32(nodeNumFromHex(status.NodeID)) {
			result.NodeNum = n.Num
			result.LongName = n.LongName
			result.ShortName = n.ShortName
			result.HWModel = n.HWModelName
			break
		}
	}

	// Detect LoRa region from config_6 (LoRa config, protobuf field 6 in Config oneof)
	// Region extraction lives in internal/transport: the spectrum monitor
	// checks its mesh band against the same value. [MESHSAT-1203]
	region := transport.LoraRegionCode(config)
	result.Region = transport.LoraRegionName(config)

	if region == 0 {
		result.Issues = append(result.Issues, RadioSetupIssue{
			Code:     "region_unset",
			Severity: "error",
			Message:  "LoRa region is not set — the device cannot transmit until a region is configured",
		})
	}

	// Detect default device name
	if result.LongName != "" && defaultNamePattern.MatchString(result.LongName) {
		result.Issues = append(result.Issues, RadioSetupIssue{
			Code:     "default_name",
			Severity: "warning",
			Message:  fmt.Sprintf("Device is using the factory default name %q", result.LongName),
		})
	}

	// Detect default channel (channel_0 with default PSK or name)
	if isDefaultChannel(config) {
		result.Issues = append(result.Issues, RadioSetupIssue{
			Code:     "default_channel",
			Severity: "warning",
			Message:  "Primary channel is using the factory default settings (LongFast with default PSK)",
		})
	}

	// Device is unconfigured if any error-severity issue exists
	for _, issue := range result.Issues {
		if issue.Severity == "error" {
			result.Unconfigured = true
			break
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// isDefaultChannel checks if channel_0 appears to have factory default settings.
// Default Meshtastic primary channel: role=PRIMARY, name="" (displayed as "LongFast"),
// PSK = AQ== (1 byte: 0x01, the default key selector).
func isDefaultChannel(config map[string]interface{}) bool {
	ch0Raw, ok := config["channel_0"]
	if !ok {
		// No channel data available — can't determine, assume not default
		return false
	}

	ch0Map, ok := ch0Raw.(map[string]interface{})
	if !ok {
		return false
	}

	// Channel message: field 2 = settings submessage
	settingsRaw, ok := ch0Map["2"]
	if !ok {
		// No settings submessage — channel has no custom settings, likely default
		return true
	}

	settingsMap, ok := settingsRaw.(map[string]interface{})
	if !ok {
		return true
	}

	// Settings field 1 = channel_num (0 = primary)
	// Settings field 2 = psk (bytes)
	// Settings field 3 = name (string)
	// Settings field 4 = id (fixed32)

	// Check name — empty or absent means default "LongFast"
	nameVal, hasName := settingsMap["3"]
	if hasName {
		if name, ok := nameVal.(string); ok && name != "" {
			// Custom name set — not default
			return false
		}
	}

	// Check PSK — default is 1 byte (0x01) or empty
	pskVal, hasPSK := settingsMap["2"]
	if hasPSK {
		switch v := pskVal.(type) {
		case []byte:
			if len(v) > 1 {
				return false // Custom PSK
			}
		case string:
			// May be stored as string representation
			if len(v) > 1 && v != "\x01" {
				return false
			}
		}
	}

	return true
}

// nodeNumFromHex converts a hex node ID string (e.g., "!aabbccdd") to uint32.
func nodeNumFromHex(id string) int64 {
	id = strings.TrimPrefix(id, "!")
	if id == "" {
		return 0
	}
	var num int64
	fmt.Sscanf(id, "%x", &num)
	return num
}
