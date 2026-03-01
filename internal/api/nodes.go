package api

import (
	"net/http"

	"meshsat/internal/transport"
)

// handleGetNodes returns all known mesh nodes from the radio.
// @Summary Get mesh nodes
// @Description Returns all known nodes from the Meshtastic radio's NodeDB
// @Tags nodes
// @Success 200 {object} map[string]interface{} "count, nodes"
// @Failure 503 {object} map[string]string "mesh transport unavailable"
// @Router /api/nodes [get]
func (s *Server) handleGetNodes(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	nodes, err := s.mesh.GetNodes(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to get nodes: "+err.Error())
		return
	}
	if nodes == nil {
		nodes = []transport.MeshNode{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"count": len(nodes),
		"nodes": nodes,
	})
}

// handleGetStatus returns the Meshtastic connection status.
// @Summary Get mesh status
// @Description Returns current Meshtastic device connection status
// @Tags nodes
// @Success 200 {object} transport.MeshStatus
// @Failure 503 {object} map[string]string "mesh transport unavailable"
// @Router /api/status [get]
func (s *Server) handleGetStatus(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	status, err := s.mesh.GetStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to get status: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}
