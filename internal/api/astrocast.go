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
