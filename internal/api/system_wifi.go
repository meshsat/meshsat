// host-ops: allowed-in-standalone
//
// Field-kit standalone-mode pragma. Same rationale as
// system_bluetooth.go — meshsat is the sole trusted container on a
// dedicated Pi, so `nsenter -t 1 -m wpa_cli ...` (which HAL uses to
// share /tmp with host wpa_supplicant) is acceptable here.
package api

// WiFi system-management handlers — scan / connect / disconnect / saved.
// Ported from cubeos/hal/internal/handlers/network.go (ScanWiFi,
// ConnectWiFi, DisconnectWiFi, parseWifiScan, ensureWpaSupplicant,
// readSavedPSK). Kept the nsenter-into-PID-1 workaround because
// wpa_cli creates its reply socket in /tmp and needs to share /tmp
// with wpa_supplicant on the host. [MESHSAT-624]

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

func defaultWiFiInterface() string {
	if iface := os.Getenv("MESHSAT_WIFI_INTERFACE"); iface != "" {
		return iface
	}
	return "wlan0"
}

// execWpaCli runs wpa_cli via nsenter -t 1 -m so its control socket
// lands in the host's /tmp where wpa_supplicant is listening.
func execWpaCli(ctx context.Context, args ...string) (string, error) {
	nsArgs := append([]string{"-t", "1", "-m", "--", "wpa_cli"}, args...)
	return execWithTimeout(ctx, "nsenter", nsArgs...)
}

// ── scan ──────────────────────────────────────────────────────────────

// @Summary Scan WiFi networks
// @Description Runs `iw <iface> scan` and parses BSS / SSID / signal / security. Interface defaults to wlan0 or MESHSAT_WIFI_INTERFACE.
// @Tags system
// @Produce json
// @Param iface path string false "WiFi interface (default: wlan0)"
// @Success 200 {object} map[string]interface{}
// @Router /api/system/wifi/scan/{iface} [get]
func (s *Server) handleWiFiScan(w http.ResponseWriter, r *http.Request) {
	iface := chi.URLParam(r, "iface")
	if iface == "" {
		iface = defaultWiFiInterface()
	}
	if err := validateInterfaceName(iface); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	output, err := execWithTimeout(r.Context(), "iw", iface, "scan")
	if err != nil {
		log.Error().Err(err).Str("iface", iface).Msg("wifi scan failed")
		writeError(w, http.StatusInternalServerError, sanitizeExecError("WiFi scan", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"interface": iface,
		"networks":  parseWiFiScan(output),
	})
}

func parseWiFiScan(output string) []map[string]interface{} {
	var networks []map[string]interface{}
	var current map[string]interface{}
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "BSS ") {
			if current != nil {
				networks = append(networks, current)
			}
			current = map[string]interface{}{"security": "Open"}
			if parts := strings.Fields(trimmed); len(parts) >= 2 {
				current["bssid"] = strings.TrimSuffix(parts[1], "(on")
			}
			continue
		}
		if current == nil {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "SSID:"):
			current["ssid"] = strings.TrimSpace(strings.TrimPrefix(trimmed, "SSID:"))
		case strings.HasPrefix(trimmed, "signal:"):
			sig := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "signal: "), " dBm"))
			if v, err := strconv.ParseFloat(sig, 64); err == nil {
				current["signal"] = int(v)
			}
		case strings.HasPrefix(trimmed, "freq:"):
			if v, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(trimmed, "freq:"))); err == nil {
				current["frequency"] = v
				current["channel"] = freqToChannel(v)
			}
		case strings.Contains(trimmed, "RSN"):
			current["security"] = "WPA2"
		case strings.Contains(trimmed, "WPA"):
			if current["security"] != "WPA2" {
				current["security"] = "WPA"
			}
		case strings.Contains(trimmed, "WEP"):
			if current["security"] == "Open" {
				current["security"] = "WEP"
			}
		}
	}
	if current != nil {
		networks = append(networks, current)
	}
	return networks
}

func freqToChannel(freq int) int {
	switch {
	case freq >= 2412 && freq <= 2484:
		if freq == 2484 {
			return 14
		}
		return (freq - 2407) / 5
	case freq >= 5170 && freq <= 5825:
		return (freq - 5000) / 5
	case freq >= 5955 && freq <= 7115:
		return (freq - 5950) / 5
	default:
		return 0
	}
}

// ── connect ───────────────────────────────────────────────────────────

// ensureWpaSupplicant starts wpa_supplicant on the given interface if
// it's not already running. Ubuntu 24.04 + networkd can leave it off
// until netplan processes a wifis: section, and if we POST to
// /api/system/wifi/connect before that we'd get 'add_network' errors.
func ensureWpaSupplicant(ctx context.Context, iface string) error {
	if _, err := execWpaCli(ctx, "-i", iface, "status"); err == nil {
		return nil
	}
	log.Info().Str("iface", iface).Msg("wifi: wpa_supplicant not running, starting it")

	confPath := fmt.Sprintf("/etc/wpa_supplicant/wpa_supplicant-%s.conf", iface)
	checkCmd := fmt.Sprintf(`test -f %s || cat > %s << 'EOF'
ctrl_interface=DIR=/var/run/wpa_supplicant GROUP=netdev
update_config=1
EOF`, confPath, confPath)

	if _, err := execWithTimeout(ctx, "nsenter", "-t", "1", "-m", "--", "bash", "-c", checkCmd); err != nil {
		genericConf := "/etc/wpa_supplicant/wpa_supplicant.conf"
		checkGeneric := fmt.Sprintf(`test -f %s || cat > %s << 'EOF'
ctrl_interface=DIR=/var/run/wpa_supplicant GROUP=netdev
update_config=1
EOF`, genericConf, genericConf)
		_, _ = execWithTimeout(ctx, "nsenter", "-t", "1", "-m", "--", "bash", "-c", checkGeneric)
		confPath = genericConf
	}

	if _, err := execWithTimeout(ctx, "nsenter", "-t", "1", "-m", "-n", "--",
		"wpa_supplicant", "-B", "-D", "nl80211", "-i", iface, "-c", confPath); err != nil {
		return fmt.Errorf("failed to start wpa_supplicant on %s: %w", iface, err)
	}
	time.Sleep(500 * time.Millisecond)
	if _, err := execWpaCli(ctx, "-i", iface, "status"); err != nil {
		return fmt.Errorf("wpa_supplicant control socket not ready on %s", iface)
	}
	return nil
}

type wifiConnectRequest struct {
	SSID      string `json:"ssid"`
	Password  string `json:"password"`
	Interface string `json:"interface"`
}

// @Summary Connect to a WiFi network
// @Description Add/reuse a wpa_supplicant network block, select it, and save. If `password` is empty and an existing saved network matches `ssid`, reconnects using the stored credentials.
// @Tags system
// @Accept json
// @Produce json
// @Param body body wifiConnectRequest true "WiFi credentials"
// @Success 200 {object} map[string]interface{}
// @Router /api/system/wifi/connect [post]
func (s *Server) handleWiFiConnect(w http.ResponseWriter, r *http.Request) {
	r = limitBody(r, 1<<20)
	var req wifiConnectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validateSSID(req.SSID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Password != "" {
		if err := validateWiFiPassword(req.Password); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.Interface == "" {
		req.Interface = defaultWiFiInterface()
	}
	if err := validateInterfaceName(req.Interface); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := ensureWpaSupplicant(r.Context(), req.Interface); err != nil {
		log.Error().Err(err).Str("iface", req.Interface).Msg("wifi connect: wpa_supplicant not available")
		writeError(w, http.StatusInternalServerError,
			fmt.Sprintf("wpa_supplicant not available on %s", req.Interface))
		return
	}

	// Check for an existing saved network matching this SSID.
	existingID := ""
	if out, err := execWpaCli(r.Context(), "-i", req.Interface, "list_networks"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 && f[0] != "network" {
				if _, err := strconv.Atoi(f[0]); err == nil && f[1] == req.SSID {
					existingID = f[0]
					break
				}
			}
		}
	}

	var networkID string
	if existingID != "" && req.Password == "" {
		networkID = existingID
	} else {
		if existingID != "" {
			_, _ = execWpaCli(r.Context(), "-i", req.Interface, "remove_network", existingID)
		}
		out, err := execWpaCli(r.Context(), "-i", req.Interface, "add_network")
		if err != nil {
			log.Error().Err(err).Msg("wifi connect: add_network failed")
			writeError(w, http.StatusInternalServerError, "failed to add network")
			return
		}
		networkID = strings.TrimSpace(out)
		if _, err := execWpaCli(r.Context(), "-i", req.Interface, "set_network", networkID, "ssid",
			fmt.Sprintf("\"%s\"", req.SSID)); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to set SSID")
			return
		}
		if req.Password != "" {
			if _, err := execWpaCli(r.Context(), "-i", req.Interface, "set_network", networkID, "psk",
				fmt.Sprintf("\"%s\"", req.Password)); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to set password")
				return
			}
		} else {
			if _, err := execWpaCli(r.Context(), "-i", req.Interface, "set_network", networkID,
				"key_mgmt", "NONE"); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to set key management")
				return
			}
		}
	}
	if _, err := execWpaCli(r.Context(), "-i", req.Interface, "select_network", networkID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to select network")
		return
	}
	_, _ = execWpaCli(r.Context(), "-i", req.Interface, "save_config")

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":    true,
		"ssid":       req.SSID,
		"interface":  req.Interface,
		"network_id": networkID,
	})
}

// @Summary Disconnect from current WiFi
// @Tags system
// @Param iface path string false "WiFi interface (default: wlan0)"
// @Success 200 {object} map[string]string
// @Router /api/system/wifi/disconnect/{iface} [post]
func (s *Server) handleWiFiDisconnect(w http.ResponseWriter, r *http.Request) {
	iface := chi.URLParam(r, "iface")
	if iface == "" {
		iface = defaultWiFiInterface()
	}
	if err := validateInterfaceName(iface); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := execWpaCli(r.Context(), "-i", iface, "disconnect"); err != nil {
		log.Error().Err(err).Str("iface", iface).Msg("wifi disconnect failed")
		writeError(w, http.StatusInternalServerError, sanitizeExecError("WiFi disconnect", err))
		return
	}
	writeSuccess(w, fmt.Sprintf("disconnected %s", iface))
}

// @Summary Current WiFi status on an interface
// @Description Shortcut for `wpa_cli -i <iface> status` — returns SSID, BSSID, IP, mode.
// @Tags system
// @Produce json
// @Param iface path string false "WiFi interface (default: wlan0)"
// @Success 200 {object} map[string]interface{}
// @Router /api/system/wifi/status/{iface} [get]
func (s *Server) handleWiFiStatus(w http.ResponseWriter, r *http.Request) {
	iface := chi.URLParam(r, "iface")
	if iface == "" {
		iface = defaultWiFiInterface()
	}
	if err := validateInterfaceName(iface); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	output, err := execWpaCli(r.Context(), "-i", iface, "status")
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, sanitizeExecError("WiFi status", err))
		return
	}
	status := map[string]interface{}{"interface": iface}
	for _, line := range strings.Split(output, "\n") {
		if i := strings.IndexByte(line, '='); i > 0 {
			status[strings.TrimSpace(line[:i])] = strings.TrimSpace(line[i+1:])
		}
	}
	// Merge in signal strength — wpa_cli status doesn't expose it, but
	// `iw dev <iface> link` does. Best-effort; swallow errors so we
	// don't fail the whole status call just because iw isn't happy.
	// [MESHSAT-631]
	if linkOut, linkErr := execWithTimeout(r.Context(), "iw", "dev", iface, "link"); linkErr == nil {
		for _, line := range strings.Split(linkOut, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "signal:") {
				sig := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "signal:"), " dBm"))
				if v, perr := strconv.Atoi(sig); perr == nil {
					status["signal"] = v
				}
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, status)
}

// @Summary List saved WiFi networks
// @Tags system
// @Produce json
// @Param iface path string false "WiFi interface (default: wlan0)"
// @Success 200 {object} map[string]interface{}
// @Router /api/system/wifi/saved/{iface} [get]
func (s *Server) handleWiFiSaved(w http.ResponseWriter, r *http.Request) {
	iface := chi.URLParam(r, "iface")
	if iface == "" {
		iface = defaultWiFiInterface()
	}
	if err := validateInterfaceName(iface); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	output, err := execWpaCli(r.Context(), "-i", iface, "list_networks")
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, sanitizeExecError("list saved WiFi", err))
		return
	}
	type entry struct {
		ID    string `json:"id"`
		SSID  string `json:"ssid"`
		BSSID string `json:"bssid,omitempty"`
		Flags string `json:"flags,omitempty"`
	}
	var networks []entry
	for _, line := range strings.Split(output, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] == "network" {
			continue
		}
		if _, err := strconv.Atoi(f[0]); err != nil {
			continue
		}
		e := entry{ID: f[0], SSID: f[1]}
		if len(f) >= 3 {
			e.BSSID = f[2]
		}
		if len(f) >= 4 {
			e.Flags = f[3]
		}
		networks = append(networks, e)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"interface": iface,
		"networks":  networks,
	})
}
