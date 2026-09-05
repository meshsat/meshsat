package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/gateway"
)

// DeviceHealthResponse is the body of GET /api/devices/health.
type DeviceHealthResponse struct {
	Enabled bool                   `json:"enabled"`
	Targets []gateway.TargetStatus `json:"targets"`
}

// DeviceHealthHealRequest is the body of POST /api/devices/health/{target}/heal.
type DeviceHealthHealRequest struct {
	// Level is the reset level to run now: 1 soft, 2 device, 3 hard (VBUS cut).
	Level byte `json:"level"`
	// Confirm is required for a level above the target's automatic maximum
	// (the cellular VBUS cut is a modem power toggle).
	Confirm bool `json:"confirm"`
}

// handleGetDeviceHealth lists every device health target with its state,
// the last recovery step and the hard-reset budget. [MESHSAT-817]
// @Summary Device health watchdog status
// @Description State of every USB device probed by the device health watchdog (ok, degraded, healing, failed, paused, unknown), the last recovery rung, the grace window and the level-3 budget.
// @Tags devices
// @Produce json
// @Success 200 {object} DeviceHealthResponse
// @Router /api/devices/health [get]
func (s *Server) handleGetDeviceHealth(w http.ResponseWriter, r *http.Request) {
	if s.deviceHealth == nil {
		writeJSON(w, http.StatusOK, DeviceHealthResponse{Enabled: false, Targets: []gateway.TargetStatus{}})
		return
	}
	writeJSON(w, http.StatusOK, DeviceHealthResponse{Enabled: true, Targets: s.deviceHealth.Status()})
}

// handlePauseDeviceHealth stops the heal ladder for one target; probes continue.
// @Summary Pause the heal ladder for a device
// @Tags devices
// @Param target path string true "Target name (mesh, cellular, zigbee, rtl_sdr, gps, imt, iridium)"
// @Success 204
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/devices/health/{target}/pause [post]
func (s *Server) handlePauseDeviceHealth(w http.ResponseWriter, r *http.Request) {
	s.setDeviceHealthPaused(w, r, true)
}

// handleResumeDeviceHealth re-enables the heal ladder for one target.
// @Summary Resume the heal ladder for a device
// @Tags devices
// @Param target path string true "Target name"
// @Success 204
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/devices/health/{target}/resume [post]
func (s *Server) handleResumeDeviceHealth(w http.ResponseWriter, r *http.Request) {
	s.setDeviceHealthPaused(w, r, false)
}

func (s *Server) setDeviceHealthPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	if s.deviceHealth == nil {
		writeError(w, http.StatusServiceUnavailable, "device health watchdog disabled")
		return
	}
	target := chi.URLParam(r, "target")
	var err error
	if paused {
		err = s.deviceHealth.Pause(target)
	} else {
		err = s.deviceHealth.Resume(target)
	}
	if err != nil {
		writeDeviceHealthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleHealDevice runs one recovery rung now with the ladder's accounting.
// @Summary Run a recovery step on a device now
// @Description Runs the target's rung at the given level (1 soft reconnect, 2 the device's own reset, 3 hub-port VBUS cut) with the same grace, budget and audit as the automatic ladder. A level above the target's automatic maximum needs confirm=true.
// @Tags devices
// @Accept json
// @Produce json
// @Param target path string true "Target name"
// @Param body body DeviceHealthHealRequest true "Level and confirm"
// @Success 202 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/devices/health/{target}/heal [post]
func (s *Server) handleHealDevice(w http.ResponseWriter, r *http.Request) {
	if s.deviceHealth == nil {
		writeError(w, http.StatusServiceUnavailable, "device health watchdog disabled")
		return
	}
	target := chi.URLParam(r, "target")
	var req DeviceHealthHealRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Level < gateway.HealLevelSoft || req.Level > gateway.HealLevelHard {
		writeError(w, http.StatusBadRequest, "level must be 1, 2 or 3")
		return
	}
	if err := s.deviceHealth.Heal(r.Context(), target, req.Level, req.Confirm); err != nil {
		writeDeviceHealthError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "healing", "target": target})
}

func writeDeviceHealthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gateway.ErrHealthTargetUnknown):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, gateway.ErrHealthConfirm):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, gateway.ErrHealthTargetBusy), errors.Is(err, gateway.ErrHealthExternal), errors.Is(err, gateway.ErrHealthTargetPaused):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, gateway.ErrHealthNoStep):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusConflict, err.Error())
	}
}
