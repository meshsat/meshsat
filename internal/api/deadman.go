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
