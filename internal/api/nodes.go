package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

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

	// Wrap the mesh status in an envelope so the SPA's StatusStrip
	// picks up hub + directory health from one /api/status response
	// rather than fanning out into per-subsystem endpoints on every
	// 10s tick. All original MeshStatus fields stay at the top
	// level; new blocks are additive. [MESHSAT-614]
	envelope := map[string]interface{}{
		"connected":     status.Connected,
		"transport":     status.Transport,
		"address":       status.Address,
		"node_id":       status.NodeID,
		"node_name":     status.NodeName,
		"hw_model":      status.HWModel,
		"hw_model_name": status.HWModelName,
		"num_nodes":     status.NumNodes,
	}

	if s.db != nil {
		var hubVersion int64
		var lastSync string
		_ = s.db.QueryRow(`SELECT COALESCE(MAX(hub_version), 0) FROM directory_contacts`).Scan(&hubVersion)
		_ = s.db.QueryRow(`SELECT COALESCE(MAX(updated_at), '') FROM directory_contacts`).Scan(&lastSync)
		envelope["directory"] = map[string]interface{}{
			"hub_version":  hubVersion,
			"last_sync_at": lastSync,
		}
	}

	writeJSON(w, http.StatusOK, envelope)
}

// handleRemoveNode removes a node from the radio's NodeDB.
// @Summary Remove mesh node
// @Description Removes a node from the Meshtastic radio's NodeDB
// @Tags mesh
// @Produce json
// @Param num path integer true "Node number"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/nodes/{num} [delete]
func (s *Server) handleRemoveNode(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	numStr := chi.URLParam(r, "num")
	num, err := strconv.ParseUint(numStr, 10, 32)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid node number")
		return
	}

	if err := s.mesh.RemoveNode(r.Context(), uint32(num)); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to remove node: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}
