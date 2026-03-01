package api

import "net/http"

// handleGetGateways returns the status of all configured gateways.
// @Summary Get gateway status
// @Description Returns status of all gateways (MQTT, Iridium)
// @Tags gateways
// @Success 200 {object} map[string]interface{} "gateways"
// @Router /api/gateways [get]
func (s *Server) handleGetGateways(w http.ResponseWriter, r *http.Request) {
	// Phase 1: no gateways configured yet, return empty list
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"gateways": []interface{}{},
	})
}
