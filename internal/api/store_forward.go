package api

import (
	"encoding/json"
	"net/http"
)

// handleRequestStoreForward requests message history from a store & forward server node.
// @Summary Request store & forward history
// @Description Sends a CLIENT_HISTORY request to a Store & Forward server node to retrieve missed messages
// @Tags store_forward
// @Accept json
// @Param body body object{node_id=uint32,window=uint32} true "S&F server node and history window in seconds"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/store-forward/request [post]
func (s *Server) handleRequestStoreForward(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	var req struct {
		NodeID uint32 `json:"node_id"`
		Window uint32 `json:"window"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.NodeID == 0 {
		writeError(w, http.StatusBadRequest, "node_id is required (the S&F server node)")
		return
	}
	if req.Window == 0 {
		req.Window = 3600 // default 1 hour
	}

	if err := s.mesh.RequestStoreForward(r.Context(), req.NodeID, req.Window); err != nil {
		writeError(w, http.StatusInternalServerError, "store forward request failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "store forward history request sent",
	})
}
