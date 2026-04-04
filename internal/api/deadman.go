package api

import (
	"encoding/json"
	"net/http"
	"time"
)

type deadmanConfigResponse struct {
	Enabled      bool  `json:"enabled"`
	TimeoutMin   int   `json:"timeout_min"`
	LastActivity int64 `json:"last_activity"` // unix timestamp
	Triggered    bool  `json:"triggered"`
}

type deadmanConfigRequest struct {
	Enabled    bool `json:"enabled"`
	TimeoutMin int  `json:"timeout_min"`
}

// handleGetDeadmanConfig returns the current dead man's switch configuration.
// @Summary Get dead man's switch config
// @Description Returns the dead man's switch status, timeout, last activity, and trigger state
// @Tags system
// @Produce json
// @Success 200 {object} deadmanConfigResponse
// @Router /api/deadman [get]
func (s *Server) handleGetDeadmanConfig(w http.ResponseWriter, r *http.Request) {
	if s.deadman == nil {
		writeJSON(w, http.StatusOK, deadmanConfigResponse{
			Enabled:    false,
			TimeoutMin: 240,
		})
		return
	}
	writeJSON(w, http.StatusOK, deadmanConfigResponse{
		Enabled:      s.deadman.IsEnabled(),
		TimeoutMin:   int(s.deadman.GetTimeout().Minutes()),
		LastActivity: s.deadman.LastActivity(),
		Triggered:    s.deadman.IsTriggered(),
	})
}

// handleSetDeadmanConfig updates the dead man's switch configuration.
// @Summary Set dead man's switch config
// @Description Updates the dead man's switch enabled state and timeout
// @Tags system
// @Accept json
// @Produce json
// @Param body body deadmanConfigRequest true "Config" example({"enabled":true,"timeout_min":240})
// @Success 200 {object} deadmanConfigResponse
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/deadman [post]
func (s *Server) handleSetDeadmanConfig(w http.ResponseWriter, r *http.Request) {
	if s.deadman == nil {
		writeError(w, http.StatusServiceUnavailable, "dead man's switch not available")
		return
	}
	var req deadmanConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.TimeoutMin < 1 {
		req.TimeoutMin = 240
	}
	s.deadman.SetEnabled(req.Enabled)
	s.deadman.SetTimeout(time.Duration(req.TimeoutMin) * time.Minute)
	s.deadman.Touch()

	writeJSON(w, http.StatusOK, deadmanConfigResponse{
		Enabled:      s.deadman.IsEnabled(),
		TimeoutMin:   int(s.deadman.GetTimeout().Minutes()),
		LastActivity: s.deadman.LastActivity(),
		Triggered:    s.deadman.IsTriggered(),
	})
}
