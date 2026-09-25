package api

import (
	"net/http"
)

// handleGetHFStatus returns the 10 m HF gateway state.
// @Summary 10 m HF gateway status
// @Description Receive state of hf_0 (the kit's RTL-SDR is on loan to it while receiving, so every spectrum band is blind), decode counters, last heard shout, and whether transmit is enabled (an operator callsign is set).
// @Tags hf
// @Produce json
// @Success 200 {object} gateway.HFStatus
// @Router /api/hf/status [get]
func (s *Server) handleGetHFStatus(w http.ResponseWriter, r *http.Request) {
	if s.gwManager == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"receiving": false, "configured": false})
		return
	}
	g := s.gwManager.GetHFGateway()
	if g == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"receiving": false, "configured": false})
		return
	}
	writeJSON(w, http.StatusOK, g.HFStatus())
}
