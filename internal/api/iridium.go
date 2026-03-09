package api

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// handleGetSignalHistory returns raw or aggregated signal history.
// Query params: source (default iridium), from, to (unix), interval (seconds).
func (s *Server) handleGetSignalHistory(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "iridium"
	}

	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	intervalStr := r.URL.Query().Get("interval")

	var from, to int64
	if fromStr != "" {
		from, _ = strconv.ParseInt(fromStr, 10, 64)
	}
	if toStr != "" {
		to, _ = strconv.ParseInt(toStr, 10, 64)
	}

	// Default: last 6 hours
	if from == 0 {
		from = now() - 6*3600
	}
	if to == 0 {
		to = now()
	}

	// Guard against clock skew: if the client's "from" is ahead of
	// the server's "now()" (e.g. server clock is behind), extend "to"
	// so the query window is valid.
	if from > to {
		to = from + 6*3600
	}

	if intervalStr != "" {
		interval, _ := strconv.Atoi(intervalStr)
		if interval > 0 {
			data, err := s.db.GetSignalHistoryAggregated(source, from, to, interval)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, data)
			return
		}
	}

	data, err := s.db.GetSignalHistoryRaw(source, from, to, 500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, data)
}

// handleGetCredits returns aggregated credit usage and budget limits.
func (s *Server) handleGetCredits(w http.ResponseWriter, r *http.Request) {
	summary, err := s.db.GetCreditSummary()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// handleSetCreditBudget sets daily and/or monthly credit budget limits.
func (s *Server) handleSetCreditBudget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DailyBudget   *int `json:"daily_budget"`
		MonthlyBudget *int `json:"monthly_budget"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.DailyBudget != nil {
		if err := s.db.SetSystemConfig("iridium_daily_budget", fmt.Sprintf("%d", *req.DailyBudget)); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if req.MonthlyBudget != nil {
		if err := s.db.SetSystemConfig("iridium_monthly_budget", fmt.Sprintf("%d", *req.MonthlyBudget)); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGetPasses returns satellite passes for a ground location.
// Query params: lat, lon, alt_m, hours, min_elev, start (unix timestamp, default now).
func (s *Server) handleGetPasses(w http.ResponseWriter, r *http.Request) {
	if s.tleMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "TLE manager not initialized")
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

	passes, err := s.tleMgr.GeneratePasses(lat, lon, altM/1000.0, hours, minElev, startTime)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	cacheAge, _ := s.tleMgr.CacheAge()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"passes":        passes,
		"cache_age_sec": cacheAge,
	})
}

// handleRefreshTLEs triggers an immediate TLE refresh from Celestrak.
func (s *Server) handleRefreshTLEs(w http.ResponseWriter, r *http.Request) {
	if s.tleMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "TLE manager not initialized")
		return
	}

	if err := s.tleMgr.RefreshTLEs(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "refreshed"})
}

// handleGetLocations returns all ground station locations.
func (s *Server) handleGetLocations(w http.ResponseWriter, r *http.Request) {
	locs, err := s.db.GetIridiumLocations()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, locs)
}

// handleCreateLocation adds a custom ground station location.
func (s *Server) handleCreateLocation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string  `json:"name"`
		Lat  float64 `json:"lat"`
		Lon  float64 `json:"lon"`
		AltM float64 `json:"alt_m"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	id, err := s.db.InsertIridiumLocation(req.Name, req.Lat, req.Lon, req.AltM)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

// handleDeleteLocation removes a custom ground station location.
func (s *Server) handleDeleteLocation(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid location id")
		return
	}

	if err := s.db.DeleteIridiumLocation(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleGetSchedulerStatus returns the current pass scheduler state.
func (s *Server) handleGetSchedulerStatus(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"enabled":   false,
			"mode":      "legacy",
			"mode_name": "Legacy",
		})
		return
	}

	sched := s.scheduler.Schedule()
	params := s.scheduler.GetTimingParams()

	resp := map[string]interface{}{
		"enabled":   true,
		"mode":      params.ModeName,
		"mode_name": params.Mode.DisplayName(),
	}

	if sched != nil {
		resp["location"] = sched.LocationName
		resp["computed_at"] = sched.ComputedAt
		resp["upcoming_passes_count"] = len(sched.UpcomingPasses)

		if !sched.NextTransition.IsZero() {
			resp["next_transition"] = sched.NextTransition
		}

		// Find next upcoming pass
		now := time.Now().Unix()
		for _, p := range sched.UpcomingPasses {
			if p.LOS >= now {
				resp["next_pass"] = map[string]interface{}{
					"satellite":     p.Satellite,
					"aos":           p.AOS,
					"los":           p.LOS,
					"peak_elev_deg": p.PeakElevDeg,
					"quality_score": p.QualityScore,
					"priority":      p.Priority,
					"is_active":     p.AOS <= now && p.LOS >= now,
				}
				break
			}
		}
	}

	resp["timing"] = map[string]interface{}{
		"poll_interval_sec":  int(params.PollInterval.Seconds()),
		"dlq_check_sec":      int(params.DLQCheckInterval.Seconds()),
		"dlq_retry_base_sec": int(params.DLQRetryBase.Seconds()),
	}

	if params.CurrentPass != nil {
		resp["current_pass"] = map[string]interface{}{
			"satellite":     params.CurrentPass.Satellite,
			"peak_elev_deg": params.CurrentPass.PeakElevDeg,
			"quality_score": params.CurrentPass.QualityScore,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleGetGeolocationSources returns the latest geolocation from each source
// (GPS, Iridium) plus the AUTO-resolved location.
func (s *Server) handleGetGeolocationSources(w http.ResponseWriter, r *http.Request) {
	sources, err := s.db.GetAllGeolocationSources()
	if err != nil {
		sources = nil
	}

	// Also include custom locations
	customLocs, _ := s.db.GetIridiumLocations()

	// AUTO resolution: GPS > Custom. Iridium geolocation is NOT used for
	// auto-resolution because it represents satellite sub-points (~200 km accuracy).
	var resolved *resolvedLocation
	for _, src := range sources {
		if src.Source == "gps" && src.Lat != 0 && src.Lon != 0 {
			resolved = &resolvedLocation{
				Source:     "gps",
				Lat:        src.Lat,
				Lon:        src.Lon,
				AltKm:      src.AltKm,
				AccuracyKm: src.AccuracyKm,
				Timestamp:  src.Timestamp,
			}
			break
		}
	}
	if resolved == nil && len(customLocs) > 0 {
		loc := customLocs[0]
		resolved = &resolvedLocation{
			Source:     "custom",
			Name:       loc.Name,
			Lat:        loc.Lat,
			Lon:        loc.Lon,
			AltKm:      loc.AltM / 1000.0,
			AccuracyKm: 0,
		}
	}

	resp := map[string]interface{}{
		"sources": sources,
	}
	if resolved != nil {
		resp["resolved"] = resolved
	}

	// Include live GPS metadata (satellite count, fix status) from GPS reader
	if s.gpsReader != nil {
		gs := s.gpsReader.GetStatus()
		resp["gps"] = map[string]interface{}{
			"fix":  gs.Fix,
			"sats": gs.Sats,
		}
	}

	// Include recent Iridium satellite sub-points for multi-pass visualization
	iridiumPoints, _ := s.db.GetRecentIridiumGeolocations(6)
	if len(iridiumPoints) > 0 {
		resp["iridium_passes"] = iridiumPoints

		// Compute centroid if 3+ points (multi-pass position estimate)
		if len(iridiumPoints) >= 3 {
			var latSum, lonSum float64
			for _, p := range iridiumPoints {
				latSum += p.Lat
				lonSum += p.Lon
			}
			n := float64(len(iridiumPoints))
			// Accuracy improves with more observations: ~200/sqrt(n) km
			accKm := 200.0 / math.Sqrt(n)
			resp["iridium_centroid"] = map[string]interface{}{
				"lat":         latSum / n,
				"lon":         lonSum / n,
				"accuracy_km": accKm,
				"points":      len(iridiumPoints),
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleGetIridiumGeolocation triggers an AT-MSGEO reading and stores the result.
// The response contains the satellite sub-point, not the modem position.
func (s *Server) handleGetIridiumGeolocation(w http.ResponseWriter, r *http.Request) {
	geo, err := s.gwManager.GetIridiumGeolocation(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	// Persist for multi-pass visualization
	ts, _ := time.Parse(time.RFC3339, geo.Timestamp)
	if err := s.db.InsertIridiumGeolocation(geo.Lat, geo.Lon, geo.Accuracy, "", ts.Unix()); err != nil {
		log.Warn().Err(err).Msg("failed to persist iridium geolocation")
	}

	writeJSON(w, http.StatusOK, geo)
}

// handleGetIridiumGeoHistory returns recent AT-MSGEO readings for multi-pass visualization.
func (s *Server) handleGetIridiumGeoHistory(w http.ResponseWriter, r *http.Request) {
	hoursStr := r.URL.Query().Get("hours")
	hours := 6
	if hoursStr != "" {
		if h, err := strconv.Atoi(hoursStr); err == nil && h > 0 && h <= 168 {
			hours = h
		}
	}

	points, err := s.db.GetRecentIridiumGeolocations(hours)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if points == nil {
		points = []database.IridiumGeoPoint{}
	}

	resp := map[string]interface{}{
		"points": points,
		"hours":  hours,
	}

	// Compute centroid if 3+ points
	if len(points) >= 3 {
		var latSum, lonSum float64
		for _, p := range points {
			latSum += p.Lat
			lonSum += p.Lon
		}
		n := float64(len(points))
		resp["centroid"] = map[string]interface{}{
			"lat":         latSum / n,
			"lon":         lonSum / n,
			"accuracy_km": 200.0 / math.Sqrt(n),
			"points":      len(points),
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

type resolvedLocation struct {
	Source     string  `json:"source"`
	Name       string  `json:"name,omitempty"`
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	AltKm      float64 `json:"alt_km"`
	AccuracyKm float64 `json:"accuracy_km"`
	Timestamp  int64   `json:"timestamp,omitempty"`
}

// handleManualMailboxCheck triggers a one-shot mailbox check.
func (s *Server) handleManualMailboxCheck(w http.ResponseWriter, r *http.Request) {
	if err := s.gwManager.ManualMailboxCheck(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "mailbox check triggered"})
}

func now() int64 {
	return time.Now().Unix()
}
