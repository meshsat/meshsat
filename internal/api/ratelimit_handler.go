package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/auth"
	"meshsat/internal/database"
)

// @Summary List satellite rate limit configs
// @Description Returns rate limit configuration for all devices.
// @Tags satellite-rate-limits
// @Produce json
// @Success 200 {array} database.SatelliteRateLimit
// @Router /api/satellite/rate-limits [get]
func (s *Server) handleGetSatelliteRateLimits(w http.ResponseWriter, r *http.Request) {
	limits, err := s.db.GetAllSatelliteRateLimits()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list rate limits")
		return
	}
	if limits == nil {
		limits = []database.SatelliteRateLimit{}
	}
	writeJSON(w, http.StatusOK, limits)
}

// @Summary Get satellite rate limit for a device
// @Description Returns rate limit config and current usage for a specific device.
// @Tags satellite-rate-limits
// @Produce json
// @Param device_id path int true "Device ID"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]string "invalid ID"
// @Failure 404 {object} map[string]string "not found"
// @Router /api/satellite/rate-limits/{device_id} [get]
func (s *Server) handleGetSatelliteRateLimit(w http.ResponseWriter, r *http.Request) {
	deviceID, err := strconv.ParseInt(chi.URLParam(r, "device_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device_id")
		return
	}

	rl, err := s.db.GetSatelliteRateLimit(deviceID)
	if err != nil {
		writeError(w, http.StatusNotFound, "no rate limit configured for this device")
		return
	}

	// Include current usage stats
	usage := map[string]interface{}{}
	if s.satRateLimiter != nil {
		usage = s.satRateLimiter.GetDeviceUsage(deviceID)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"config": rl,
		"usage":  usage,
	})
}

// @Summary Set satellite rate limit for a device
// @Description Creates or updates rate limit configuration for a device.
// @Tags satellite-rate-limits
// @Accept json
// @Produce json
// @Param device_id path int true "Device ID"
// @Param body body object true "Rate limit config"
// @Success 200 {object} database.SatelliteRateLimit
// @Failure 400 {object} map[string]string "invalid request"
// @Failure 404 {object} map[string]string "device not found"
// @Router /api/satellite/rate-limits/{device_id} [put]
func (s *Server) handleSetSatelliteRateLimit(w http.ResponseWriter, r *http.Request) {
	deviceID, err := strconv.ParseInt(chi.URLParam(r, "device_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device_id")
		return
	}

	// Verify device exists in this tenant
	tid := auth.TenantIDFromContext(r.Context())
	if _, err := s.db.GetDevice(deviceID, tid); err != nil {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}

	var req struct {
		DailyLimit         *int     `json:"daily_limit"`
		MonthlyLimit       *int     `json:"monthly_limit"`
		BurstSize          *int     `json:"burst_size"`
		RefillRate         *float64 `json:"refill_rate"`
		Enabled            *bool    `json:"enabled"`
		DailyCreditLimit   *int     `json:"daily_credit_limit"`
		MonthlyCreditLimit *int     `json:"monthly_credit_limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	rl := database.SatelliteRateLimit{
		DeviceID:     deviceID,
		DailyLimit:   50,
		MonthlyLimit: 1000,
		BurstSize:    10,
		RefillRate:   0.0167, // ~1 per minute
		Enabled:      true,
	}

	// Merge with existing config if present
	if existing, err := s.db.GetSatelliteRateLimit(deviceID); err == nil {
		rl = *existing
	}

	if req.DailyLimit != nil {
		rl.DailyLimit = *req.DailyLimit
	}
	if req.MonthlyLimit != nil {
		rl.MonthlyLimit = *req.MonthlyLimit
	}
	if req.BurstSize != nil {
		rl.BurstSize = *req.BurstSize
	}
	if req.RefillRate != nil {
		rl.RefillRate = *req.RefillRate
	}
	if req.Enabled != nil {
		rl.Enabled = *req.Enabled
	}
	if req.DailyCreditLimit != nil {
		rl.DailyCreditLimit = *req.DailyCreditLimit
	}
	if req.MonthlyCreditLimit != nil {
		rl.MonthlyCreditLimit = *req.MonthlyCreditLimit
	}

	if err := s.db.UpsertSatelliteRateLimit(rl); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save rate limit: "+err.Error())
		return
	}

	// Reload the limiter's in-memory state
	if s.satRateLimiter != nil {
		s.satRateLimiter.ReloadDevice(deviceID)
	}

	updated, _ := s.db.GetSatelliteRateLimit(deviceID)
	writeJSON(w, http.StatusOK, updated)
}

// @Summary Delete satellite rate limit for a device
// @Description Removes rate limit configuration, reverting to unlimited.
// @Tags satellite-rate-limits
// @Param device_id path int true "Device ID"
// @Success 204
// @Failure 400 {object} map[string]string "invalid ID"
// @Router /api/satellite/rate-limits/{device_id} [delete]
func (s *Server) handleDeleteSatelliteRateLimit(w http.ResponseWriter, r *http.Request) {
	deviceID, err := strconv.ParseInt(chi.URLParam(r, "device_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device_id")
		return
	}

	if err := s.db.DeleteSatelliteRateLimit(deviceID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete rate limit")
		return
	}

	if s.satRateLimiter != nil {
		s.satRateLimiter.ReloadDevice(deviceID)
	}

	w.WriteHeader(http.StatusNoContent)
}

// @Summary Admin override: temporarily bypass rate limit
// @Description Sets a temporary override that bypasses rate limiting for the specified duration.
// @Tags satellite-rate-limits
// @Accept json
// @Produce json
// @Param device_id path int true "Device ID"
// @Param body body object true "Override config" SchemaExample({"action":"bypass","duration_minutes":60})
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string "invalid request"
// @Router /api/satellite/rate-limits/{device_id}/override [post]
func (s *Server) handleSatelliteRateLimitOverride(w http.ResponseWriter, r *http.Request) {
	deviceID, err := strconv.ParseInt(chi.URLParam(r, "device_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device_id")
		return
	}

	var req struct {
		Action          string `json:"action"`           // "bypass" or "clear"
		DurationMinutes int    `json:"duration_minutes"` // for bypass
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	switch req.Action {
	case "bypass":
		if req.DurationMinutes <= 0 {
			req.DurationMinutes = 60
		}
		until := time.Now().UTC().Add(time.Duration(req.DurationMinutes) * time.Minute)
		if err := s.db.SetSatelliteRateLimitOverride(deviceID, until); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to set override")
			return
		}
		if s.satRateLimiter != nil {
			s.satRateLimiter.ReloadDevice(deviceID)
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status":         "override_active",
			"override_until": until.Format(time.RFC3339),
		})

	case "clear":
		if err := s.db.ClearSatelliteRateLimitOverride(deviceID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to clear override")
			return
		}
		if s.satRateLimiter != nil {
			s.satRateLimiter.ReloadDevice(deviceID)
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "override_cleared"})

	default:
		writeError(w, http.StatusBadRequest, "action must be 'bypass' or 'clear'")
	}
}

// @Summary Reset satellite usage counters
// @Description Clears all usage counters for a device (admin override).
// @Tags satellite-rate-limits
// @Param device_id path int true "Device ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string "invalid ID"
// @Router /api/satellite/rate-limits/{device_id}/reset [post]
func (s *Server) handleResetSatelliteUsage(w http.ResponseWriter, r *http.Request) {
	deviceID, err := strconv.ParseInt(chi.URLParam(r, "device_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device_id")
		return
	}

	if err := s.db.ResetSatelliteUsage(deviceID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reset usage")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "usage_reset"})
}

// @Summary Get satellite usage summary
// @Description Returns current usage stats for all devices with rate limits.
// @Tags satellite-rate-limits
// @Produce json
// @Success 200 {array} map[string]interface{}
// @Router /api/satellite/usage [get]
func (s *Server) handleGetSatelliteUsage(w http.ResponseWriter, r *http.Request) {
	limits, err := s.db.GetAllSatelliteRateLimits()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list rate limits")
		return
	}

	var result []map[string]interface{}
	for _, rl := range limits {
		usage := map[string]interface{}{"device_id": rl.DeviceID}
		if s.satRateLimiter != nil {
			usage = s.satRateLimiter.GetDeviceUsage(rl.DeviceID)
		}
		result = append(result, usage)
	}

	if result == nil {
		result = []map[string]interface{}{}
	}
	writeJSON(w, http.StatusOK, result)
}
