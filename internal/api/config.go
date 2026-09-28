package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/transport"
)

// handleSetRadioConfig writes one Config section of the node: the node's own
// copy with the fields of `config` laid over it (proto or JSON field names,
// enums as numbers or names). [MESHSAT-1405]
// @Summary Set radio configuration
// @Description Writes one Config section (device, position, power, network, display, lora, bluetooth, security) of the node: the settings the node reported, with only the fields named in config changed. Security writes never change the node's keys.
// @Tags config
// @Accept json
// @Param body body object{section=string,config=object} true "Radio config section and the fields to change"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string "unknown section or field, a value it cannot take"
// @Failure 409 {object} map[string]string "the node has not sent these settings yet"
// @Failure 503 {object} map[string]string "no node"
// @Router /api/config/radio [post]
func (s *Server) handleSetRadioConfig(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	section, config, err := readSettingsRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.mesh.SetRadioConfig(r.Context(), section, config); err != nil {
		writeSettingsError(w, "set radio config failed: ", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "radio config updated"})
}

// handleSetModuleConfig writes one ModuleConfig section of the node the same
// way. [MESHSAT-1405]
// @Summary Set module configuration
// @Description Writes one ModuleConfig section (mqtt, serial, external_notification, store_forward, range_test, telemetry, canned_message, audio, remote_hardware, neighbor_info, ambient_lighting, detection_sensor, paxcounter, statusmessage, traffic_management, tak) of the node: the settings the node reported, with only the fields named in config changed.
// @Tags config
// @Accept json
// @Param body body object{section=string,config=object} true "Module config section and the fields to change"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string "unknown section or field, a value it cannot take"
// @Failure 409 {object} map[string]string "the node has not sent these settings yet"
// @Failure 503 {object} map[string]string "no node"
// @Router /api/config/module [post]
func (s *Server) handleSetModuleConfig(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	section, config, err := readSettingsRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.mesh.SetModuleConfig(r.Context(), section, config); err != nil {
		writeSettingsError(w, "set module config failed: ", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "module config updated"})
}

// namedConfigReader is a mesh transport that keeps the node's settings typed.
// [MESHSAT-1405]
type namedConfigReader interface {
	NamedConfig() map[string]interface{}
}

// handleGetConfig returns the node's configuration.
// @Summary Get device configuration
// @Description The node's configuration as it reported it. By default keyed by protobuf field numbers (config_6 is the LoRa section), as the web dashboard reads it; with format=names, named sections and fields (enums as numbers), the channels with their key as a word (none, default, private, main) and never the key itself, the security section without the private key, the node's metadata and owner [MESHSAT-1405]
// @Tags config
// @Produce json
// @Param format query string false "names: named sections and fields"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/config [get]
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}
	switch r.URL.Query().Get("format") {
	case "":
	case "names":
		named, ok := s.mesh.(namedConfigReader)
		if !ok {
			writeError(w, http.StatusServiceUnavailable, "this mesh transport does not keep the node's settings by name")
			return
		}
		writeJSON(w, http.StatusOK, named.NamedConfig())
		return
	default:
		writeError(w, http.StatusBadRequest, "format is names or nothing")
		return
	}

	config, err := s.mesh.GetConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get config failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, config)
}

// handleGetConfigSection requests a specific config section from the device.
// @Summary Get config section
// @Description Asks the node for one Config section; its answer replaces the Bridge's copy (GET /api/config), usually within a second [MESHSAT-1405]
// @Tags config
// @Param section path string true "Config section: device, position, power, network, display, lora, bluetooth, security"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/config/{section} [get]
func (s *Server) handleGetConfigSection(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	section := chi.URLParam(r, "section")
	if section == "" {
		writeError(w, http.StatusBadRequest, "section is required")
		return
	}

	if err := s.mesh.GetConfigSection(r.Context(), section); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "config request sent for section: " + section,
	})
}

// handleGetModuleConfigSection requests a specific module config section from the device.
// @Summary Get module config section
// @Description Asks the node for one ModuleConfig section; its answer replaces the Bridge's copy (GET /api/config) [MESHSAT-1405]
// @Tags config
// @Param section path string true "Module section: mqtt, serial, external_notification, store_forward, range_test, telemetry, canned_message, audio, remote_hardware, neighbor_info"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/config/module/{section} [get]
func (s *Server) handleGetModuleConfigSection(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	section := chi.URLParam(r, "section")
	if section == "" {
		writeError(w, http.StatusBadRequest, "section is required")
		return
	}

	if err := s.mesh.GetModuleConfigSection(r.Context(), section); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "module config request sent for section: " + section,
	})
}

// handleSetChannel writes one channel over the node's own. [MESHSAT-1405]
// @Summary Set channel configuration
// @Description Writes one channel (index 0 to 7) over the channel the node reported: name and the MQTT switches as given, the key (psk, base64) only when given, the role (PRIMARY, SECONDARY, DISABLED) kept when empty; channel 0 is always PRIMARY and no other channel is
// @Tags config
// @Accept json
// @Param body body transport.ChannelRequest true "Channel configuration"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 409 {object} map[string]string "the node has not sent this channel yet"
// @Failure 503 {object} map[string]string
// @Router /api/channels [post]
func (s *Server) handleSetChannel(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	var req transport.ChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := s.mesh.SetChannel(r.Context(), req); err != nil {
		writeSettingsError(w, "set channel failed: ", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "channel updated"})
}

// handleSetOwner sets the device owner (long_name + short_name).
// @Summary Set device owner
// @Description Sets the Meshtastic device owner name via AdminMessage field 32; the node's own is_licensed goes back unchanged [MESHSAT-1405]
// @Tags config
// @Accept json
// @Param body body object{long_name=string,short_name=string} true "Owner names"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 409 {object} map[string]string "the node has not sent its own name yet"
// @Failure 503 {object} map[string]string
// @Router /api/config/owner [post]
func (s *Server) handleSetOwner(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	var req struct {
		LongName  string `json:"long_name"`
		ShortName string `json:"short_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.LongName == "" && req.ShortName == "" {
		writeError(w, http.StatusBadRequest, "at least one of long_name or short_name is required")
		return
	}

	if err := s.mesh.SetOwner(r.Context(), req.LongName, req.ShortName); err != nil {
		writeSettingsError(w, "set owner failed: ", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "owner updated"})
}

// handleRequestNodeInfo requests a NodeInfo update from a remote mesh node.
// @Summary Request NodeInfo
// @Description Sends a NodeInfo request to a remote Meshtastic node to refresh its name/info
// @Tags config
// @Accept json
// @Param body body object{node_num=integer} true "Target node number"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/nodes/request-info [post]
func (s *Server) handleRequestNodeInfo(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	var req struct {
		NodeNum uint32 `json:"node_num"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.NodeNum == 0 {
		writeError(w, http.StatusBadRequest, "node_num is required")
		return
	}

	if err := s.mesh.RequestNodeInfo(r.Context(), req.NodeNum); err != nil {
		switch {
		case errors.Is(err, transport.ErrNodeInfoSelf):
			// Would zero the radio's own NodeDB row. [MESHSAT-1102]
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, transport.ErrNodeNumUnknown), errors.Is(err, transport.ErrOwnUserUnknown):
			writeError(w, http.StatusServiceUnavailable, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "request nodeinfo failed: "+err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "nodeinfo request sent"})
}

// writeSettingsError answers a settings write the node could not be given:
// 400 for a request that cannot stand, 409 while the node has not sent the
// settings it would be laid over, 503 with no node. [MESHSAT-1405]
func writeSettingsError(w http.ResponseWriter, prefix string, err error) {
	var bad *transport.ConfigError
	switch {
	case errors.As(err, &bad):
		writeError(w, http.StatusBadRequest, bad.Msg)
	case errors.Is(err, transport.ErrConfigNotLoaded):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, transport.ErrNotConnected):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, prefix+err.Error())
	}
}

// readSettingsRequest reads {"section": ..., "config": {...}}, or the fields
// beside "section" when there is no "config" (the web dashboard's JSON
// editor sends that shape). [MESHSAT-1405]
func readSettingsRequest(r *http.Request) (string, json.RawMessage, error) {
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body == nil {
		return "", nil, errors.New("invalid request body")
	}
	var section string
	if raw, ok := body["section"]; !ok || json.Unmarshal(raw, &section) != nil || section == "" {
		return "", nil, errors.New("section is required")
	}
	if config, ok := body["config"]; ok {
		return section, config, nil
	}
	delete(body, "section")
	config, _ := json.Marshal(body)
	return section, config, nil
}
