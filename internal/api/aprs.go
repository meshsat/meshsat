package api

import "net/http"

// handleGetAPRSStatus returns aggregated APRS gateway status.
// @Summary Get APRS gateway status
// @Description Returns connection state, callsign, frequency, uptime, counters, and packet type breakdown
// @Tags aprs
// @Success 200 {object} map[string]interface{}
// @Router /api/aprs/status [get]
func (s *Server) handleGetAPRSStatus(w http.ResponseWriter, r *http.Request) {
	agw := s.gwManager.GetAPRSGateway()
	if agw == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"connected": false,
		})
		return
	}
	writeJSON(w, http.StatusOK, agw.GetAPRSStatus())
}

// handleGetAPRSHeard returns the heard station table.
// @Summary Get APRS heard stations
// @Description Returns all stations heard by the APRS gateway with last position and distance
// @Tags aprs
// @Success 200 {array} gateway.HeardStation
// @Router /api/aprs/heard [get]
func (s *Server) handleGetAPRSHeard(w http.ResponseWriter, r *http.Request) {
	agw := s.gwManager.GetAPRSGateway()
	if agw == nil {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}
	writeJSON(w, http.StatusOK, agw.Tracker().GetHeardStations())
}

// handleGetAPRSActivity returns RX/TX packets per minute for the last 30 minutes.
// @Summary Get APRS packet activity
// @Description Returns RX/TX packets per minute for the last 30 minutes
// @Tags aprs
// @Success 200 {object} map[string]interface{}
// @Router /api/aprs/activity [get]
func (s *Server) handleGetAPRSActivity(w http.ResponseWriter, r *http.Request) {
	agw := s.gwManager.GetAPRSGateway()
	if agw == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"buckets":      []interface{}{},
			"recent_paths": []interface{}{},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"buckets":      agw.Tracker().GetActivity(),
		"recent_paths": agw.Tracker().GetRecentPaths(),
	})
}
