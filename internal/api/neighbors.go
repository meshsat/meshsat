package api

import (
	"net/http"
	"strconv"
)

// handleGetNeighborInfo returns neighbor info from live mesh and DB history.
// @Summary Get neighbor info
// @Description Returns neighbor info data from all nodes that have sent NeighborInfo packets
// @Tags neighbors
// @Param node query int false "Filter by node ID (decimal)"
// @Param limit query int false "Max records (default 100)"
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/neighbors [get]
func (s *Server) handleGetNeighborInfo(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// Live neighbor info from transport
	if s.mesh != nil {
		live, err := s.mesh.GetNeighborInfo(r.Context())
		if err == nil && len(live) > 0 {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"neighbors": live,
				"source":    "live",
			})
			return
		}
	}

	// Fall back to DB history
	var nodeID uint32
	if n := q.Get("node"); n != "" {
		v, _ := strconv.ParseUint(n, 10, 32)
		nodeID = uint32(v)
	}
	limit := intParam(q.Get("limit"), 100)

	records, err := s.db.GetNeighborInfo(nodeID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to query neighbor info: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"neighbors": records,
		"source":    "database",
	})
}
