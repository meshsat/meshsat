package api

import (
	"encoding/json"
	"net/http"
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
