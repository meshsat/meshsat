// host-ops: allowed-in-standalone
//
// MESHSAT-630 — WiFi peer-link modes (IBSS / ad-hoc today; AP + P2P
// tracked as follow-ups once we have chipset-cap data from real field
// kits). Lets two MeshSat kits establish a direct WiFi link without
// any infrastructure AP, carrying Reticulum's tcp_0 over the resulting
// IP link.
//
// Ported patterns from cubeos/hal handlers/network.go — same pragma
// gate + exec/validate helpers as the client-mode WiFi handlers in
// system_wifi.go. Field-kit bridge runs trusted, so shelling out to
// `iw` is acceptable here.

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// WiFiCapabilities lists the per-phy modes the local WiFi hardware
// can enter. Populated from `iw phy <phy> info` — the
// "Supported interface modes" block.
type WiFiCapabilities struct {
	PHY   string   `json:"phy"`
	Modes []string `json:"modes"`
	// Convenience flags mirror the Modes list.
	AP       bool `json:"ap"`
	IBSS     bool `json:"ibss"`
	P2P      bool `json:"p2p"`
	MeshMode bool `json:"mesh_point"`
}

// @Summary Probe WiFi hardware for supported modes
// @Description Parses `iw phy` supported-interface-modes and returns a JSON cap summary. Used by the UI to hide toggles the hardware cant satisfy. [MESHSAT-630]
// @Tags system
// @Produce json
// @Param iface path string false "WiFi interface (default: wlan0)"
// @Success 200 {object} WiFiCapabilities
// @Router /api/system/wifi/capabilities/{iface} [get]
func (s *Server) handleWiFiCapabilities(w http.ResponseWriter, r *http.Request) {
	iface := chi.URLParam(r, "iface")
	if iface == "" {
		iface = defaultWiFiInterface()
	}
	if err := validateInterfaceName(iface); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	caps, err := probeWiFiCapabilities(r.Context(), iface)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, sanitizeExecError("capabilities", err))
		return
	}
	writeJSON(w, http.StatusOK, caps)
}

// probeWiFiCapabilities: resolve iface → phy, then `iw phy <phy> info`
// and scan the indented "Supported interface modes" block until the
// next outdent. Returns a populated capability struct.
func probeWiFiCapabilities(ctx context.Context, iface string) (WiFiCapabilities, error) {
	var caps WiFiCapabilities
	// iface → phy
	info, err := execWithTimeout(ctx, "iw", "dev", iface, "info")
	if err != nil {
		return caps, fmt.Errorf("iw dev info: %w", err)
	}
	for _, line := range strings.Split(info, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "wiphy ") {
			if id := strings.TrimPrefix(trimmed, "wiphy "); id != "" {
				caps.PHY = "phy" + id
			}
			break
		}
	}
	if caps.PHY == "" {
		return caps, fmt.Errorf("could not resolve phy for %s", iface)
	}
	phyInfo, err := execWithTimeout(ctx, "iw", "phy", caps.PHY, "info")
	if err != nil {
		return caps, fmt.Errorf("iw phy info: %w", err)
	}
	inModes := false
	for _, line := range strings.Split(phyInfo, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "Supported interface modes:") {
			inModes = true
			continue
		}
		if inModes {
			if !strings.HasPrefix(line, "\t\t") && !strings.HasPrefix(line, "    ") {
				// dedented — modes block over
				break
			}
			// Lines look like "		 * IBSS".
			if idx := strings.LastIndex(trimmed, "* "); idx >= 0 {
				mode := strings.TrimSpace(trimmed[idx+2:])
				if mode != "" {
					caps.Modes = append(caps.Modes, mode)
				}
			}
		}
	}
	for _, m := range caps.Modes {
		switch m {
		case "AP":
			caps.AP = true
		case "IBSS":
			caps.IBSS = true
		case "P2P-client", "P2P-GO":
			caps.P2P = true
		case "mesh point":
			caps.MeshMode = true
		}
	}
	return caps, nil
}

// wifiIBSSJoinRequest is the POST body for IBSS join.
type wifiIBSSJoinRequest struct {
	Interface string `json:"interface"`
	SSID      string `json:"ssid"`
	Freq      int    `json:"freq"` // MHz, e.g. 2412 (ch 1), 2437 (ch 6), 2462 (ch 11)
}

// @Summary Join an IBSS (ad-hoc) cell — kit-to-kit WiFi without an AP
// @Description Brings the interface DOWN, switches mode to ibss, brings UP, joins the named cell on the given frequency. Two kits using the same SSID + freq form a direct link. [MESHSAT-630]
// @Tags system
// @Accept json
// @Produce json
// @Param body body wifiIBSSJoinRequest true "IBSS parameters"
// @Success 200 {object} map[string]interface{}
// @Router /api/system/wifi/ibss/join [post]
func (s *Server) handleWiFiIBSSJoin(w http.ResponseWriter, r *http.Request) {
	r = limitBody(r, 1<<20)
	var req wifiIBSSJoinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Interface == "" {
		req.Interface = defaultWiFiInterface()
	}
	if err := validateInterfaceName(req.Interface); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateSSID(req.SSID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Freq < 2412 || req.Freq > 5825 {
		writeError(w, http.StatusBadRequest, "freq out of range (2412-5825 MHz)")
		return
	}
	// Chipset capability gate — don't burn time switching modes we
	// can't enter.
	caps, _ := probeWiFiCapabilities(r.Context(), req.Interface)
	if !caps.IBSS {
		writeError(w, http.StatusBadRequest, "IBSS not supported by this chipset")
		return
	}
	// Sequence: stop wpa_supplicant on this iface (otherwise it'll
	// fight us), link down, set type ibss, link up, ibss join.
	// All via nsenter so we touch the host network namespace.
	// Best-effort stops — ok if they aren't running.
	_, _ = execWithTimeout(r.Context(), "nsenter", "-t", "1", "-m", "-n", "--",
		"wpa_cli", "-i", req.Interface, "terminate")
	steps := [][]string{
		{"ip", "link", "set", req.Interface, "down"},
		{"iw", "dev", req.Interface, "set", "type", "ibss"},
		{"ip", "link", "set", req.Interface, "up"},
		{"iw", "dev", req.Interface, "ibss", "join", req.SSID, strconv.Itoa(req.Freq)},
	}
	for _, step := range steps {
		args := append([]string{"-t", "1", "-m", "-n", "--"}, step...)
		if out, err := execWithTimeout(r.Context(), "nsenter", args...); err != nil {
			log.Error().Err(err).Str("cmd", strings.Join(step, " ")).Str("out", out).Msg("wifi-ibss: step failed")
			writeError(w, http.StatusInternalServerError, sanitizeExecError("IBSS step "+step[0], err))
			return
		}
	}
	log.Info().Str("iface", req.Interface).Str("ssid", req.SSID).Int("freq", req.Freq).
		Msg("wifi: joined IBSS")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"interface": req.Interface, "ssid": req.SSID, "freq": req.Freq, "status": "joined",
	})
}

// @Summary Leave the IBSS cell and return to managed (client) mode
// @Tags system
// @Produce json
// @Param iface path string false "WiFi interface (default: wlan0)"
// @Success 200 {object} map[string]interface{}
// @Router /api/system/wifi/ibss/leave/{iface} [post]
func (s *Server) handleWiFiIBSSLeave(w http.ResponseWriter, r *http.Request) {
	iface := chi.URLParam(r, "iface")
	if iface == "" {
		iface = defaultWiFiInterface()
	}
	if err := validateInterfaceName(iface); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	steps := [][]string{
		{"iw", "dev", iface, "ibss", "leave"},
		{"ip", "link", "set", iface, "down"},
		{"iw", "dev", iface, "set", "type", "managed"},
		{"ip", "link", "set", iface, "up"},
	}
	for _, step := range steps {
		args := append([]string{"-t", "1", "-m", "-n", "--"}, step...)
		// Best-effort on leave — if ibss leave fails the iface may
		// already be in managed mode; keep going.
		if _, err := execWithTimeout(r.Context(), "nsenter", args...); err != nil && step[2] != "ibss" {
			writeError(w, http.StatusInternalServerError, sanitizeExecError("IBSS leave step "+step[0], err))
			return
		}
	}
	log.Info().Str("iface", iface).Msg("wifi: left IBSS, back to managed")
	writeJSON(w, http.StatusOK, map[string]interface{}{"interface": iface, "status": "left"})
}
