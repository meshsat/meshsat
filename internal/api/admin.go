package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"meshsat/internal/transport"
)

// handleAdminReboot sends a reboot command to a mesh node.
// @Summary Reboot a mesh node
// @Description Forwards a reboot command through HAL to a local or remote mesh node
// @Tags admin
// @Accept json
// @Param body body object{node_id=uint32,delay_secs=int} true "Target node (0: the node this Bridge talks to) and delay"
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
	req.NodeID = s.ownNodeIfZero(req.NodeID)

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
// @Param body body object{node_id=uint32} true "Target node (0: the node this Bridge talks to)"
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

	req.NodeID = s.ownNodeIfZero(req.NodeID)
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

// ownNodeIfZero reads node_id 0 as the node this Bridge talks to: an admin
// message addressed to node 0 goes nowhere. [MESHSAT-1405]
func (s *Server) ownNodeIfZero(id uint32) uint32 {
	if own, ok := s.mesh.(interface{ MyNodeNum() uint32 }); ok && id == 0 {
		return own.MyNodeNum()
	}
	return id
}

// nodeAdmin is what the direct mesh transport offers for the node it talks
// to: Android's "Restart and reset" actions besides restart and factory
// reset. [MESHSAT-1405]
type nodeAdmin interface {
	AdminSetClock() error
	AdminShutdown(delay int) error
	AdminForgetNodes() error
}

func (s *Server) nodeAdmin(w http.ResponseWriter) nodeAdmin {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return nil
	}
	a, ok := s.mesh.(nodeAdmin)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "this mesh transport cannot do that")
		return nil
	}
	return a
}

// handleAdminSetClock sets the node's clock to this computer's.
// @Summary Set the node's clock
// @Description Sets the clock of the node this Bridge talks to to this computer's time (AdminMessage set_time_only); refused while this computer's clock is not known to be right [MESHSAT-1405]
// @Tags admin
// @Success 200 {object} map[string]string
// @Failure 409 {object} map[string]string "this computer's clock is not set yet"
// @Failure 503 {object} map[string]string
// @Router /api/admin/set_clock [post]
func (s *Server) handleAdminSetClock(w http.ResponseWriter, r *http.Request) {
	a := s.nodeAdmin(w)
	if a == nil {
		return
	}
	if err := a.AdminSetClock(); err != nil {
		if errors.Is(err, transport.ErrClockUntrusted) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeSettingsError(w, "set clock failed: ", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "clock sent"})
}

// handleAdminShutdown switches the node off.
// @Summary Switch the node off
// @Description Switches the node this Bridge talks to off after delay_secs (default 5; AdminMessage shutdown_seconds). It stays off until someone switches it on at the node [MESHSAT-1405]
// @Tags admin
// @Accept json
// @Param body body object{delay_secs=int} false "Delay in seconds"
// @Success 200 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/admin/shutdown [post]
func (s *Server) handleAdminShutdown(w http.ResponseWriter, r *http.Request) {
	a := s.nodeAdmin(w)
	if a == nil {
		return
	}
	var req struct {
		DelaySecs int `json:"delay_secs"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if err := a.AdminShutdown(req.DelaySecs); err != nil {
		writeSettingsError(w, "shutdown failed: ", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "shutdown command sent"})
}

// handleAdminNodeDBReset clears the node's list of heard nodes.
// @Summary Forget heard nodes
// @Description Clears the list of heard nodes of the node this Bridge talks to (AdminMessage nodedb_reset; favourites are kept). They come back as they transmit again [MESHSAT-1405]
// @Tags admin
// @Success 200 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/admin/nodedb_reset [post]
func (s *Server) handleAdminNodeDBReset(w http.ResponseWriter, r *http.Request) {
	a := s.nodeAdmin(w)
	if a == nil {
		return
	}
	if err := a.AdminForgetNodes(); err != nil {
		writeSettingsError(w, "nodedb reset failed: ", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "nodedb reset sent"})
}
