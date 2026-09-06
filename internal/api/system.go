package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// Backlight control for the Pi Touch Display 2 (and any other
// sysfs-exposed backlight). [MESHSAT-556]
//
// The Linux kernel exposes every backlight device under
// /sys/class/backlight/<name>/ with a `max_brightness` + `brightness`
// pair. We pick the first device that exists and map the caller's
// 0-255 scale onto that device's own max range, so the API surface
// stays hardware-agnostic.
//
// Writes require the bridge process to have write permission on the
// sysfs file. Deployed field kits run in a container with
// `/sys/class/backlight` bind-mounted + CAP_SYS_ADMIN, or via a
// scoped sudoers entry for `tee`. If neither is available the
// handler returns 503 with a descriptive error instead of failing
// hard.

type backlightRequest struct {
	Value int `json:"value"` // 0-255, caller-normalised
}

type backlightResponse struct {
	Device        string `json:"device"`
	Value         int    `json:"value"`
	MaxBrightness int    `json:"max_brightness"`
	Raw           int    `json:"raw"`
}

// @Summary Set display backlight brightness
// @Description Writes `value` (0-255) to the first available
// @Description /sys/class/backlight device's `brightness` file,
// @Description proportionally mapped to that device's own
// @Description `max_brightness`. Used by the NVIS night-mode
// @Description switch to dim the Pi Touch Display 2 during
// @Description low-light operations. Returns 503 if no backlight
// @Description device is present or the sysfs path is not writable.
// @Tags system
// @Accept json
// @Produce json
// @Param body body backlightRequest true "Target brightness"
// @Success 200 {object} backlightResponse
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/system/backlight [post]
func (s *Server) handleBacklight(w http.ResponseWriter, r *http.Request) {
	var req backlightRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Value < 0 || req.Value > 255 {
		writeError(w, http.StatusBadRequest, "value must be in 0-255")
		return
	}

	dev, max, err := firstBacklightDevice()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	// Map 0-255 → 0-max proportionally.
	raw := (req.Value * max) / 255
	if err := writeBrightness(dev, raw); err != nil {
		writeError(w, http.StatusServiceUnavailable, "write brightness: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, backlightResponse{
		Device: dev, Value: req.Value, MaxBrightness: max, Raw: raw,
	})
}

func firstBacklightDevice() (string, int, error) {
	entries, err := os.ReadDir("/sys/class/backlight")
	if err != nil {
		return "", 0, fmt.Errorf("no backlight devices: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		maxBytes, err := os.ReadFile(filepath.Join("/sys/class/backlight", name, "max_brightness"))
		if err != nil {
			continue
		}
		max, err := strconv.Atoi(strings.TrimSpace(string(maxBytes)))
		if err != nil || max <= 0 {
			continue
		}
		return name, max, nil
	}
	return "", 0, fmt.Errorf("no usable backlight device under /sys/class/backlight")
}

func writeBrightness(device string, raw int) error {
	path := filepath.Join("/sys/class/backlight", device, "brightness")
	return os.WriteFile(path, []byte(strconv.Itoa(raw)), 0o644)
}

// ─── X1202 UPS battery status ─────────────────────────────────────
//
// The host-side x1202-monitor.py writes the latest voltage / SOC /
// AC-present state to /run/x1202.json on each I²C poll (10s).  The
// field kit compose file bind-mounts that file read-only into the
// bridge container so this handler can serve it without any I²C
// access from Go.  If the mount isn't present (non-field deploys,
// no UPS) the handler returns 404 with a hint — the frontend tile
// falls back to "UPS not connected". [MESHSAT-549]

type batteryStatus struct {
	Voltage    *float64 `json:"voltage"`
	SOCPercent *float64 `json:"soc_percent"`
	ACPresent  *bool    `json:"ac_present"`
	LastUpdate float64  `json:"last_update"`
	Stale      bool     `json:"stale"`
}

// @Summary Get X1202 UPS battery status
// @Description Returns the latest voltage, state-of-charge, and AC-
// @Description present flag written by the host-side x1202-monitor
// @Description service (MAX17040 over I²C 0x36).  Field-kit only:
// @Description requires /run/x1202.json to be bind-mounted into the
// @Description container.
// @Tags system
// @Produce json
// @Success 200 {object} batteryStatus
// @Failure 404 {object} map[string]string
// @Router /api/system/battery [get]
func (s *Server) handleGetBattery(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile("/run/x1202.json")
	if err != nil {
		writeError(w, http.StatusNotFound, "UPS not connected (no /run/x1202.json)")
		return
	}
	var bs batteryStatus
	if err := json.Unmarshal(data, &bs); err != nil {
		writeError(w, http.StatusInternalServerError, "parse x1202.json: "+err.Error())
		return
	}
	// Mark stale if last_update > 60s old (x1202-monitor polls every 10s).
	now := float64(time.Now().Unix())
	bs.Stale = bs.LastUpdate > 0 && (now-bs.LastUpdate) > 60
	writeJSON(w, http.StatusOK, bs)
}

// systemPowerRequest is the body of POST /api/system/power. [MESHSAT-831]
type systemPowerRequest struct {
	Action    string `json:"action"`               // "reboot" or "poweroff"
	DelaySecs int    `json:"delay_secs,omitempty"` // 1..300, default 5
	Confirm   bool   `json:"confirm"`              // must be true
}

// @Summary Reboot or power off the host
// @Description Asks the OOB host agent to reboot or halt the kit after a
// @Description short delay. Requires confirm=true. Field kits: after a halt
// @Description the X1202 UPS keeps its 5 V output up, so the box stays
// @Description powered until its button is long-pressed; the response
// @Description says so in `note`.
// @Tags system
// @Accept json
// @Produce json
// @Param body body systemPowerRequest true "Power action"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/system/power [post]
func (s *Server) handleSystemPower(w http.ResponseWriter, r *http.Request) {
	var req systemPowerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Action != "reboot" && req.Action != "poweroff" {
		writeError(w, http.StatusBadRequest, "action must be reboot or poweroff")
		return
	}
	if !req.Confirm {
		writeError(w, http.StatusBadRequest, "confirm must be true")
		return
	}
	if req.DelaySecs <= 0 {
		req.DelaySecs = 5
	}
	if req.DelaySecs > 300 {
		req.DelaySecs = 300
	}
	if s.host == nil || !s.host.Available() {
		writeError(w, http.StatusServiceUnavailable, "host agent not available on this kit")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	res, err := s.host.Call(ctx, req.Action, map[string]any{"delay": req.DelaySecs})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "host agent: "+err.Error())
		return
	}
	note := "the kit reboots; the panel may need a UPS button cycle if it comes back black"
	if req.Action == "poweroff" {
		note = "the kit halts; the UPS keeps it powered until its button is long-pressed"
	}
	log.Warn().Str("action", req.Action).Int("delay_secs", req.DelaySecs).Str("remote", r.RemoteAddr).Msg("system power action requested from the API")
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "scheduled", "action": req.Action, "delay_secs": req.DelaySecs, "note": note, "agent": res,
	})
}
