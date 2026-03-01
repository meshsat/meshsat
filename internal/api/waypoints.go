package api

import (
	"encoding/json"
	"net/http"

	"meshsat/internal/transport"
)

// handleSendWaypoint sends a waypoint to the mesh network.
// @Summary Send a waypoint
// @Description Sends a waypoint to the mesh network via HAL
// @Tags waypoints
// @Accept json
// @Param body body transport.Waypoint true "Waypoint data"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/waypoints [post]
func (s *Server) handleSendWaypoint(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	var wp transport.Waypoint
	if err := json.NewDecoder(r.Body).Decode(&wp); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if wp.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	if err := s.mesh.SendWaypoint(r.Context(), wp); err != nil {
		writeError(w, http.StatusInternalServerError, "send waypoint failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "waypoint sent"})
}
