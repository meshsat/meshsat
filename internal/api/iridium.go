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
// Query params: source (sbd|imt|gss|iridium, empty=all satellite), from, to (unix), interval (seconds).
// When source is empty, returns combined sbd + imt + legacy "iridium" entries.
// @Summary Get satellite signal history
// @Description Returns raw or time-aggregated signal strength readings for satellite modems
// @Tags iridium
// @Produce json
// @Param source query string false "Signal source filter (sbd, imt, gss, iridium; empty=all satellite)"
// @Param from query integer false "Start time (unix timestamp, default: 6h ago)"
// @Param to query integer false "End time (unix timestamp, default: now)"
// @Param interval query integer false "Aggregation interval in seconds (omit for raw data)"
// @Success 200 {array} database.SignalHistoryAggregated
// @Failure 500 {object} map[string]string
// @Router /api/iridium/signal/history [get]
func (s *Server) handleGetSignalHistory(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")

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

	// Determine which sources to query.
	// Empty or "iridium" (legacy) → all satellite sources (sbd, imt, and legacy "iridium").
	// Specific source → just that source.
	useMulti := source == "" || source == "iridium"
	multiSources := []string{"sbd", "imt", "iridium"}

	if intervalStr != "" {
		interval, _ := strconv.Atoi(intervalStr)
		if interval > 0 {
			var data []database.SignalHistoryAggregated
			var err error
			if useMulti {
				data, err = s.db.GetSignalHistoryAggregatedMulti(multiSources, from, to, interval)
			} else {
				data, err = s.db.GetSignalHistoryAggregated(source, from, to, interval)
			}
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, data)
			return
		}
	}

	if useMulti {
		data, err := s.db.GetSignalHistoryRawMulti(multiSources, from, to, 500)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, data)
	} else {
		data, err := s.db.GetSignalHistoryRaw(source, from, to, 500)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, data)
	}
}

// handleGetCredits returns aggregated credit usage and budget limits.
// @Summary Get Iridium credit usage
// @Description Returns aggregated credit usage summary and budget limits
// @Tags iridium
// @Produce json
// @Success 200 {object} database.CreditSummary
// @Failure 500 {object} map[string]string
// @Router /api/iridium/credits [get]
func (s *Server) handleGetCredits(w http.ResponseWriter, r *http.Request) {
	summary, err := s.db.GetCreditSummary()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// handleSetCreditBudget sets daily and/or monthly credit budget limits.
// @Summary Set Iridium credit budget
// @Description Sets daily and/or monthly credit budget limits for satellite usage
// @Tags iridium
// @Accept json
// @Produce json
// @Param body body object true "Budget limits" example({"daily_budget":10,"monthly_budget":100})
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/iridium/credits/budget [post]
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
// @Summary Get Iridium satellite passes
// @Description Predicts upcoming Iridium satellite passes for a ground station location using SGP4/TLE
// @Tags iridium
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
// @Router /api/iridium/passes [get]
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
// @Summary Refresh Iridium TLE data
// @Description Triggers an immediate TLE (Two-Line Element) refresh from Celestrak
// @Tags iridium
// @Produce json
// @Success 200 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/iridium/passes/refresh [post]
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
// @Summary List ground station locations
// @Description Returns all configured ground station locations for pass prediction
// @Tags iridium
// @Produce json
// @Success 200 {array} database.IridiumLocation
// @Failure 500 {object} map[string]string
// @Router /api/iridium/locations [get]
func (s *Server) handleGetLocations(w http.ResponseWriter, r *http.Request) {
	locs, err := s.db.GetIridiumLocations()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, locs)
}

// handleCreateLocation adds a custom ground station location.
// @Summary Create ground station location
// @Description Adds a custom ground station location for pass prediction
// @Tags iridium
// @Accept json
// @Produce json
// @Param body body object true "Location" example({"name":"Home","lat":52.16,"lon":4.49,"alt_m":0})
// @Success 201 {object} map[string]int64
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/iridium/locations [post]
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
// @Summary Delete ground station location
// @Description Removes a custom ground station location
// @Tags iridium
// @Produce json
// @Param id path integer true "Location ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/iridium/locations/{id} [delete]
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
// @Summary Get pass scheduler status
// @Description Returns the current pass scheduler state, timing parameters, and next pass info
// @Tags iridium
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/iridium/scheduler [get]
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
// @Summary Get all geolocation sources
// @Description Returns latest geolocation from each source (GPS, Iridium, custom) plus the auto-resolved location
// @Tags location
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/locations/resolved [get]
func (s *Server) handleGetGeolocationSources(w http.ResponseWriter, r *http.Request) {
	sources, err := s.db.GetAllGeolocationSources()
	if err != nil {
		sources = nil
	}

	// Also include custom locations
	customLocs, _ := s.db.GetIridiumLocations()

	// AUTO resolution: GPS > Iridium centroid (3+ passes) > Custom.
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

	// Include cell tower info (if available)
	cellInfo, cellErr := s.db.GetLatestCellInfo()
	if cellErr == nil && cellInfo != nil && cellInfo.CellID != "" {
		resp["cell_info"] = cellInfo
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

// handleGetIridiumTime returns the Iridium network system time (AT-MSSTM).
// @Summary Get Iridium system time
// @Description Returns the Iridium network time via AT-MSSTM (90ms tick resolution)
// @Tags iridium
// @Success 200 {object} transport.IridiumTime
// @Failure 503 {object} map[string]string "unavailable"
// @Router /api/iridium/time [get]
func (s *Server) handleGetIridiumTime(w http.ResponseWriter, r *http.Request) {
	info, err := s.gwManager.GetIridiumTime(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleGetIridiumGeolocation triggers an AT-MSGEO reading and stores the result.
// The response contains the satellite sub-point, not the modem position.
// @Summary Get Iridium geolocation
// @Description Triggers an AT-MSGEO reading and returns the satellite sub-point coordinates
// @Tags iridium
// @Produce json
// @Success 200 {object} transport.IridiumGeolocation
// @Failure 503 {object} map[string]string
// @Router /api/iridium/geolocation [get]
func (s *Server) handleGetIridiumGeolocation(w http.ResponseWriter, r *http.Request) {
	geo, err := s.gwManager.GetIridiumGeolocation(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	// Persist for multi-pass visualization — use wall clock as observation time
	// (AT-MSGEO timestamp is satellite ephemeris age, not observation time)
	if err := s.db.InsertIridiumGeolocation(geo.Lat, geo.Lon, geo.Accuracy, "", time.Now().Unix()); err != nil {
		log.Warn().Err(err).Msg("failed to persist iridium geolocation")
	}

	writeJSON(w, http.StatusOK, geo)
}

// handleGetIridiumGeoHistory returns recent AT-MSGEO readings for multi-pass visualization.
// @Summary Get Iridium geolocation history
// @Description Returns recent AT-MSGEO satellite sub-point readings with optional centroid calculation
// @Tags iridium
// @Produce json
// @Param hours query integer false "History window in hours (default: 6, max: 168)"
// @Success 200 {object} map[string]interface{}
// @Failure 500 {object} map[string]string
// @Router /api/iridium/geolocation/history [get]
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
// @Summary Trigger manual mailbox check
// @Description Triggers a one-shot Iridium SBD mailbox check (SBDIX) to retrieve pending MT messages
// @Tags iridium
// @Produce json
// @Success 200 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/iridium/mailbox/check [post]
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
