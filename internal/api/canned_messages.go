package api

import (
	"encoding/json"
	"net/http"
)

// handleGetCannedMessages requests canned messages from the device.
// @Summary Get canned messages
// @Description Requests the device to send back its canned messages via admin channel
// @Tags config
// @Success 200 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/canned-messages [get]
func (s *Server) handleGetCannedMessages(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	if err := s.mesh.GetCannedMessages(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "get canned messages failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "canned messages request sent (response will arrive via SSE)",
	})
}

// handleSetCannedMessages sets canned messages on the device.
// @Summary Set canned messages
// @Description Sets the canned messages on the Meshtastic device. Messages are pipe-separated.
// @Tags config
// @Accept json
// @Param body body object{messages=string} true "Pipe-separated canned messages (e.g. 'OK|Help|SOS')"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/canned-messages [post]
func (s *Server) handleSetCannedMessages(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	var req struct {
		Messages string `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Messages == "" {
		writeError(w, http.StatusBadRequest, "messages is required (pipe-separated, e.g. 'OK|Help|SOS')")
		return
	}

	if err := s.mesh.SetCannedMessages(r.Context(), req.Messages); err != nil {
		writeError(w, http.StatusInternalServerError, "set canned messages failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "canned messages updated"})
}
