package api

import "net/http"

// handleGetPositions returns GPS position history for a node.
// @Summary Get position history
// @Description Returns GPS track data for maps and position analysis
// @Tags positions
// @Param node query string false "Node ID (!hex format)"
// @Param since query string false "Start time (RFC3339)"
// @Param until query string false "End time (RFC3339)"
// @Param limit query int false "Max records (default 100, max 10000)"
// @Success 200 {object} map[string]interface{} "positions, node_id"
// @Router /api/positions [get]
func (s *Server) handleGetPositions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	nodeID := q.Get("node")
	since := q.Get("since")
	until := q.Get("until")
	limit := intParam(q.Get("limit"), 100)

	records, err := s.db.GetPositions(nodeID, since, until, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to query positions: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"positions": records,
		"node_id":   nodeID,
	})
}
