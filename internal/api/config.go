package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/transport"
)

// handleSetRadioConfig forwards a radio config change to the mesh transport.
// @Summary Set radio configuration
// @Description Sends a radio configuration update to the Meshtastic device via HAL
// @Tags config
// @Accept json
// @Param body body object{section=string,config=object} true "Radio config section and data"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/config/radio [post]
func (s *Server) handleSetRadioConfig(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	var req struct {
		Section string          `json:"section"`
		Config  json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Section == "" {
		writeError(w, http.StatusBadRequest, "section is required")
		return
	}

	if err := s.mesh.SetRadioConfig(r.Context(), req.Section, req.Config); err != nil {
		writeError(w, http.StatusInternalServerError, "set radio config failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "radio config updated"})
}

// handleSetModuleConfig forwards a module config change to the mesh transport.
// @Summary Set module configuration
// @Description Sends a module configuration update to the Meshtastic device via HAL
// @Tags config
// @Accept json
// @Param body body object{section=string,config=object} true "Module config section and data"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/config/module [post]
func (s *Server) handleSetModuleConfig(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	var req struct {
		Section string          `json:"section"`
		Config  json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Section == "" {
		writeError(w, http.StatusBadRequest, "section is required")
		return
	}

	if err := s.mesh.SetModuleConfig(r.Context(), req.Section, req.Config); err != nil {
		writeError(w, http.StatusInternalServerError, "set module config failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "module config updated"})
}

// handleGetConfig returns the full device configuration from HAL.
// @Summary Get device configuration
// @Description Retrieves the full Meshtastic device configuration via HAL
// @Tags config
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 503 {object} map[string]string
// @Router /api/config [get]
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
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
// @Description Requests a specific radio config section from the device (response arrives via config_complete SSE event)
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
// @Description Requests a specific module config section (response arrives via config_complete SSE event)
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

// handleSetChannel configures a radio channel.
// @Summary Set channel configuration
// @Description Configures a Meshtastic radio channel via HAL
// @Tags config
// @Accept json
// @Param body body transport.ChannelRequest true "Channel configuration"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
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
		writeError(w, http.StatusInternalServerError, "set channel failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "channel updated"})
}
