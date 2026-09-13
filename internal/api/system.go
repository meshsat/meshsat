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

	"meshsat/internal/transport"
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
	// Charging is nil until the monitor has 30 min of mains history
	// (or when AC is unknown). [MESHSAT-794]
	Charging *bool `json:"charging,omitempty"`
	// InputInsufficient: mains is present but raw SOC fell >= 2 points
	// over 30 min, so the pack is draining on mains. [MESHSAT-794]
	InputInsufficient bool    `json:"input_insufficient,omitempty"`
	LastUpdate        float64 `json:"last_update"`
	Stale             bool    `json:"stale"`
}

// @Summary Get X1202 UPS battery status
// @Description Returns the latest voltage, state-of-charge, and AC-
// @Description present flag written by the host-side x1202-monitor
// @Description service (MAX17040 over I²C 0x36).  Field-kit only:
// @Description requires /run/x1202.json to be bind-mounted into the
// @Description container.  `charging` (omitted while unknown) is true
// @Description when mains is present and the pack is holding or
// @Description gaining charge, false on battery or while draining;
// @Description `input_insufficient` (omitted when false) is true when
// @Description mains is present but the pack has drained for 30 min.
// @Tags system
// @Produce json
// @Success 200 {object} batteryStatus
// @Failure 404 {object} map[string]string
// @Router /api/system/battery [get]
func (s *Server) handleGetBattery(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(X1202StatusPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "UPS not connected (no /run/x1202.json)")
		return
	}
	bs, err := parseBatteryStatus(data, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, bs)
}

// X1202StatusPath is where the host's x1202-monitor writes the pack state;
// the field kit compose file bind-mounts it read-only. [MESHSAT-549]
const X1202StatusPath = "/run/x1202.json"

// batteryLowPercent is the pack level below which a kit on battery reads
// as low, the same threshold the header's power chip turns red at.
const batteryLowPercent = 20

// parseBatteryStatus decodes the monitor's status file and marks a reading
// older than 60 s stale (the monitor polls every 10 s).
func parseBatteryStatus(data []byte, now time.Time) (*batteryStatus, error) {
	var bs batteryStatus
	if err := json.Unmarshal(data, &bs); err != nil {
		return nil, fmt.Errorf("parse x1202.json: %w", err)
	}
	bs.Stale = bs.LastUpdate > 0 && float64(now.Unix())-bs.LastUpdate > 60
	return &bs, nil
}

// batteryState names the pack state an operator acts on, with a short
// message. The level is left out of the key, so a reading that only moves a
// point or two emits nothing; charging is left out too, because it flips at
// the full-pack plateau. [MESHSAT-794]
func batteryState(bs *batteryStatus) (key, message string) {
	switch {
	case bs == nil:
		return "missing", "no UPS reading"
	case bs.Stale:
		return "stale", "UPS reading stale"
	case bs.InputInsufficient:
		return "draining", "input low: pack draining on mains"
	case bs.ACPresent != nil && *bs.ACPresent:
		return "mains", "mains connected"
	case bs.ACPresent != nil:
		level := ""
		if bs.SOCPercent != nil {
			level = fmt.Sprintf(", pack at %.0f %%", *bs.SOCPercent)
		}
		if bs.SOCPercent != nil && *bs.SOCPercent < batteryLowPercent {
			return "low", "pack low on battery" + level
		}
		return "battery", "on battery" + level
	}
	return "unknown", "UPS state unknown"
}

// batteryReadMisses is how many polls in a row must fail to read or parse
// the status file before it counts as gone: the monitor rewrites the file
// in place, so a single poll can catch it half written.
const batteryReadMisses = 3

// batteryWatch folds successive polls of the status file into state
// changes. [MESHSAT-794]
type batteryWatch struct {
	prev   string
	misses int
}

// observe takes one poll (nil when the file could not be read or parsed)
// and reports whether the pack state changed, from what and to what. The
// first state seen only sets the baseline.
func (w *batteryWatch) observe(bs *batteryStatus) (changed bool, prev, key, msg string) {
	if bs == nil && w.misses+1 < batteryReadMisses {
		w.misses++
		return false, w.prev, w.prev, ""
	}
	if bs != nil {
		w.misses = 0
	}
	prev = w.prev
	key, msg = batteryState(bs)
	w.prev = key
	return prev != "" && key != prev, prev, key, msg
}

// WatchBatteryEvents re-reads the X1202 monitor's status file every `every`
// and emits a "battery" event on /api/events whenever the pack state
// changes: mains lost or back, draining on mains, low on battery, reading
// stale. The dashboard tile and the booth header's power chip refresh on it
// instead of on their next poll, and the future SHORE/CHARGING panel lamps
// (MESHSAT-773) have one event to follow. The first reading only sets the
// baseline. Returns at once on a host without the file. [MESHSAT-794]
func WatchBatteryEvents(ctx context.Context, path string, every time.Duration, emit func(transport.MeshEvent)) {
	if _, err := os.Stat(path); err != nil {
		return
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	var w batteryWatch
	for {
		var bs *batteryStatus
		if data, err := os.ReadFile(path); err == nil {
			bs, _ = parseBatteryStatus(data, time.Now())
		}
		if changed, prev, key, msg := w.observe(bs); changed {
			payload, _ := json.Marshal(map[string]interface{}{"state": key, "previous": prev, "battery": bs})
			log.Info().Str("state", key).Str("previous", prev).Msg("battery: " + msg)
			emit(transport.MeshEvent{Type: "battery", Message: "battery: " + msg, Data: payload, Time: time.Now().UTC().Format(time.RFC3339)})
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
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
