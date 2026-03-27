package api

import (
	"encoding/json"
	"net/http"
	"time"
)

// handleAdminReboot sends a reboot command to a mesh node.
// @Summary Reboot a mesh node
// @Description Forwards a reboot command through HAL to a local or remote mesh node
// @Tags admin
// @Accept json
// @Param body body object{node_id=uint32,delay_secs=int} true "Target node and delay"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/admin/reboot [post]
func (s *Server) handleAdminReboot(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	var req struct {
		NodeID    uint32 `json:"node_id"`
		DelaySecs int    `json:"delay_secs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.DelaySecs <= 0 {
		req.DelaySecs = 5
	}

	if err := s.mesh.AdminReboot(r.Context(), req.NodeID, req.DelaySecs); err != nil {
		writeError(w, http.StatusInternalServerError, "reboot failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reboot command sent"})
}

// handleAdminFactoryReset sends a factory reset command.
// @Summary Factory reset a mesh node
// @Description Sends a factory reset command — all device state returned to defaults
// @Tags admin
// @Accept json
// @Param body body object{node_id=uint32} true "Target node"
// @Success 200 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/admin/factory_reset [post]
func (s *Server) handleAdminFactoryReset(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	var req struct {
		NodeID uint32 `json:"node_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := s.mesh.AdminFactoryReset(r.Context(), req.NodeID); err != nil {
		writeError(w, http.StatusInternalServerError, "factory reset failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "factory reset command sent"})
}

// handleTraceroute sends a traceroute request.
// @Summary Traceroute to a mesh node
// @Description Sends a traceroute to discover the path to a destination node
// @Tags admin
// @Accept json
// @Param body body object{node_id=uint32} true "Destination node"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/admin/traceroute [post]
func (s *Server) handleTraceroute(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	var req struct {
		NodeID uint32 `json:"node_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.NodeID == 0 {
		writeError(w, http.StatusBadRequest, "node_id is required")
		return
	}

	if err := s.mesh.Traceroute(r.Context(), req.NodeID); err != nil {
		writeError(w, http.StatusInternalServerError, "traceroute failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "traceroute request sent"})
}

// handleSystemRestart triggers a graceful bridge restart.
// Docker's restart policy brings the process back up.
// @Summary Restart the bridge process
// @Description Initiates a graceful shutdown. Docker restart policy restarts the container.
// @Tags system
// @Success 200 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/system/restart [post]
func (s *Server) handleSystemRestart(w http.ResponseWriter, r *http.Request) {
	if s.restartFn == nil {
		writeError(w, http.StatusServiceUnavailable, "restart not available")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restarting"})
	go func() {
		time.Sleep(500 * time.Millisecond)
		s.restartFn()
	}()
}
