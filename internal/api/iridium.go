package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
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

func now() int64 {
	return time.Now().Unix()
}
