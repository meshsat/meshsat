package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
