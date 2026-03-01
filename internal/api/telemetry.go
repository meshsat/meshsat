package api

import "net/http"

// handleGetTelemetry returns telemetry history for a node.
// @Summary Get telemetry history
// @Description Returns time-series telemetry data (battery, voltage, channel util, environment)
// @Tags telemetry
// @Param node query string false "Node ID (!hex format)"
// @Param since query string false "Start time (RFC3339)"
// @Param until query string false "End time (RFC3339)"
// @Param limit query int false "Max records (default 100, max 10000)"
// @Success 200 {object} map[string]interface{} "telemetry, node_id"
// @Router /api/telemetry [get]
func (s *Server) handleGetTelemetry(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	nodeID := q.Get("node")
	since := q.Get("since")
	until := q.Get("until")
	limit := intParam(q.Get("limit"), 100)

	records, err := s.db.GetTelemetry(nodeID, since, until, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to query telemetry: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"telemetry": records,
		"node_id":   nodeID,
	})
}
