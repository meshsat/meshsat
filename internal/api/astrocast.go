package api

import (
	"net/http"
	"strconv"

	"meshsat/internal/engine"
)

// SetAstrocastTLEManager sets the Astrocast TLE manager for pass prediction.
func (s *Server) SetAstrocastTLEManager(m *engine.AstrocastTLEManager) {
	s.astroTleMgr = m
}

// handleGetAstrocastPasses returns Astrocast satellite passes for a ground location.
// @Summary Get Astrocast satellite passes
// @Description Predicts upcoming Astrocast satellite passes for a ground station location
// @Tags astrocast
// @Produce json
// @Param lat query number true "Ground station latitude"
// @Param lon query number true "Ground station longitude"
// @Param alt_m query number false "Ground station altitude in meters"
// @Param hours query integer false "Prediction window in hours (default: 24)"
// @Param min_elev query number false "Minimum elevation in degrees (default: 5.0)"
// @Param start query integer false "Start time (unix timestamp, default: now)"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/astrocast/passes [get]
func (s *Server) handleGetAstrocastPasses(w http.ResponseWriter, r *http.Request) {
	if s.astroTleMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "Astrocast TLE manager not initialized")
		return
	}

	lat, _ := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lon, _ := strconv.ParseFloat(r.URL.Query().Get("lon"), 64)
	altM, _ := strconv.ParseFloat(r.URL.Query().Get("alt_m"), 64)
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	minElev, _ := strconv.ParseFloat(r.URL.Query().Get("min_elev"), 64)
	startTime, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)

	if lat == 0 && lon == 0 {
		writeError(w, http.StatusBadRequest, "lat and lon are required")
		return
	}
	if hours <= 0 {
		hours = 24
	}
	if minElev <= 0 {
		minElev = 5.0
	}

	passes, err := s.astroTleMgr.GeneratePasses(lat, lon, altM/1000.0, hours, minElev, startTime)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	cacheAge, _ := s.astroTleMgr.CacheAge()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"passes":        passes,
		"cache_age_sec": cacheAge,
	})
}

// handleRefreshAstrocastTLEs triggers an immediate Astrocast TLE refresh.
// @Summary Refresh Astrocast TLE data
// @Description Triggers an immediate TLE refresh for Astrocast satellites
// @Tags astrocast
// @Produce json
// @Success 200 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/astrocast/passes/refresh [post]
func (s *Server) handleRefreshAstrocastTLEs(w http.ResponseWriter, r *http.Request) {
	if s.astroTleMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "Astrocast TLE manager not initialized")
		return
	}

	if err := s.astroTleMgr.RefreshTLEs(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "refreshed"})
}
