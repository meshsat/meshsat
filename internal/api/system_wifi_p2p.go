// host-ops: allowed-in-standalone
//
// WiFi-Direct (P2P) handlers — kit-to-kit link via mt7921u or any
// chipset that reports P2P-GO / P2P-client. This is the alternative
// to MESHSAT-630's IBSS path when the USB dongle doesn't support IBSS.
//
// All wpa_cli calls go via nsenter -t 1 -m (same pattern as
// system_wifi.go) so our bridge container shares the host's
// wpa_supplicant control socket in /var/run/wpa_supplicant. The
// network namespace stays ours — we only need to reach wpa_supplicant,
// not drive interfaces ourselves.
//
// [MESHSAT-647]

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// WiFiP2PPeer is one discovered P2P-capable device.
type WiFiP2PPeer struct {
	Address    string `json:"address"`                // MAC of the peer's P2P device
	DeviceName string `json:"device_name,omitempty"`  // advertised human-readable name
	DeviceType string `json:"device_type,omitempty"`  // category (e.g. "1-0050F204-1")
	PriDevType string `json:"pri_dev_type,omitempty"` // primary device type OUI
	WPSMethods string `json:"wps_methods,omitempty"`  // "0x0188" etc — bit mask of supported config methods
}

// WiFiP2PStatus is the current P2P group state.
type WiFiP2PStatus struct {
	Active     bool   `json:"active"`
	Role       string `json:"role,omitempty"`        // "go" | "client" | ""
	GroupIface string `json:"group_iface,omitempty"` // e.g. "p2p-wlx90de80f3a70b-0"
	PeerAddr   string `json:"peer_address,omitempty"`
	SSID       string `json:"ssid,omitempty"`
	IPAddress  string `json:"ip_address,omitempty"`
}

// @Summary Start WiFi-Direct peer discovery
// @Description wpa_cli p2p_find <timeout> on the given iface. Default 30 s. [MESHSAT-647]
// @Tags system
// @Produce json
// @Param iface query string false "WiFi interface (default: wlan0 / MESHSAT_WIFI_FIELD_IFACE)"
// @Param timeout query int false "discovery timeout seconds (1-120, default 30)"
// @Success 200 {object} map[string]interface{}
// @Router /api/system/wifi/p2p/find [post]
func (s *Server) handleWiFiP2PFind(w http.ResponseWriter, r *http.Request) {
	iface := r.URL.Query().Get("iface")
	if iface == "" {
		iface = defaultWiFiInterface()
	}
	if err := validateInterfaceName(iface); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	timeout := 30
	if t := r.URL.Query().Get("timeout"); t != "" {
		if n, err := strconv.Atoi(t); err == nil && n >= 1 && n <= 120 {
			timeout = n
		}
	}
	// Bring iface up before P2P — same auto-up gotcha as `iw scan`.
	if isIfaceDown(iface) {
		_, _ = execWithTimeout(r.Context(), "nsenter", "-t", "1", "-m", "-n", "--",
			"ip", "link", "set", iface, "up")
		time.Sleep(300 * time.Millisecond)
	}
	if _, err := execWpaCli(r.Context(), "-i", iface, "p2p_find", strconv.Itoa(timeout)); err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeExecError("p2p_find", err))
		return
	}
	log.Info().Str("iface", iface).Int("timeout", timeout).Msg("wifi-p2p: discovery started")
	writeJSON(w, http.StatusOK, map[string]interface{}{"interface": iface, "timeout": timeout, "status": "discovering"})
}

// @Summary Stop WiFi-Direct peer discovery
// @Tags system
// @Router /api/system/wifi/p2p/stop-find [post]
func (s *Server) handleWiFiP2PStopFind(w http.ResponseWriter, r *http.Request) {
	iface := r.URL.Query().Get("iface")
	if iface == "" {
		iface = defaultWiFiInterface()
	}
	if err := validateInterfaceName(iface); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	_, _ = execWpaCli(r.Context(), "-i", iface, "p2p_stop_find")
	writeSuccess(w, "discovery stopped")
}

// @Summary List discovered P2P peers
// @Tags system
// @Produce json
// @Param iface query string false "WiFi interface (default: wlan0)"
// @Success 200 {object} map[string]interface{}
// @Router /api/system/wifi/p2p/peers [get]
func (s *Server) handleWiFiP2PPeers(w http.ResponseWriter, r *http.Request) {
	iface := r.URL.Query().Get("iface")
	if iface == "" {
		iface = defaultWiFiInterface()
	}
	if err := validateInterfaceName(iface); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// p2p_peers returns a newline-separated list of MACs.
	out, err := execWpaCli(r.Context(), "-i", iface, "p2p_peers")
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeExecError("p2p_peers", err))
		return
	}
	var peers []WiFiP2PPeer
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		mac := strings.TrimSpace(line)
		if mac == "" || !isValidMAC(mac) {
			continue
		}
		// Fetch per-peer detail for device_name etc.
		peer := WiFiP2PPeer{Address: mac}
		if info, ierr := execWpaCli(r.Context(), "-i", iface, "p2p_peer", mac); ierr == nil {
			parseP2PPeerInfo(info, &peer)
		}
		peers = append(peers, peer)
	}
	if peers == nil {
		peers = []WiFiP2PPeer{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"interface": iface, "peers": peers})
}

// parseP2PPeerInfo extracts `device_name=`, `pri_dev_type=`, and
// `config_methods=` lines out of `wpa_cli p2p_peer <mac>` output.
func parseP2PPeerInfo(info string, p *WiFiP2PPeer) {
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "device_name="):
			p.DeviceName = strings.TrimPrefix(line, "device_name=")
		case strings.HasPrefix(line, "pri_dev_type="):
			p.PriDevType = strings.TrimPrefix(line, "pri_dev_type=")
		case strings.HasPrefix(line, "device_capability="):
			p.DeviceType = strings.TrimPrefix(line, "device_capability=")
		case strings.HasPrefix(line, "config_methods="):
			p.WPSMethods = strings.TrimPrefix(line, "config_methods=")
		}
	}
}

// wifiP2PConnectRequest matches the UI store's payload.
type wifiP2PConnectRequest struct {
	Interface string `json:"interface"`
	PeerAddr  string `json:"peer_addr"`
	Method    string `json:"method"` // "pbc" (push-button, default) or "pin"
	Pin       string `json:"pin,omitempty"`
	GOIntent  int    `json:"go_intent,omitempty"` // 0-15, default 7
}

// @Summary Connect to a P2P peer (WPS-PBC)
// @Description wpa_cli p2p_connect <peer> pbc. Push-button is the simplest and works for kit-to-kit since both ends are under our control. [MESHSAT-647]
// @Tags system
// @Accept json
// @Produce json
// @Param body body wifiP2PConnectRequest true "peer + method"
// @Success 200 {object} map[string]interface{}
// @Router /api/system/wifi/p2p/connect [post]
func (s *Server) handleWiFiP2PConnect(w http.ResponseWriter, r *http.Request) {
	r = limitBody(r, 1<<20)
	var req wifiP2PConnectRequest
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
	if !isValidMAC(req.PeerAddr) {
		writeError(w, http.StatusBadRequest, "invalid peer MAC")
		return
	}
	if req.Method == "" {
		req.Method = "pbc"
	}
	if req.Method != "pbc" && req.Method != "pin" {
		writeError(w, http.StatusBadRequest, "method must be pbc or pin")
		return
	}
	args := []string{"-i", req.Interface, "p2p_connect", req.PeerAddr, req.Method}
	if req.Method == "pin" && req.Pin != "" {
		args[len(args)-1] = req.Pin // replace the literal "pin" with the actual pin
	}
	if req.GOIntent > 0 && req.GOIntent <= 15 {
		args = append(args, "go_intent="+strconv.Itoa(req.GOIntent))
	}
	out, err := execWpaCli(r.Context(), args...)
	if err != nil {
		log.Error().Err(err).Str("peer", req.PeerAddr).Msg("wifi-p2p: connect failed")
		writeError(w, http.StatusInternalServerError, sanitizeExecError("p2p_connect", err))
		return
	}
	log.Info().Str("iface", req.Interface).Str("peer", req.PeerAddr).Str("method", req.Method).Msg("wifi-p2p: connect initiated")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"interface": req.Interface, "peer": req.PeerAddr, "method": req.Method,
		"wpa_result": strings.TrimSpace(out),
	})
}

// @Summary Tear down the active P2P group
// @Tags system
// @Router /api/system/wifi/p2p/disconnect [post]
func (s *Server) handleWiFiP2PDisconnect(w http.ResponseWriter, r *http.Request) {
	// Need the group iface — resolve from status.
	st, _ := readP2PStatus(r.Context(), "")
	if !st.Active {
		writeError(w, http.StatusBadRequest, "no active P2P group")
		return
	}
	// Use the parent iface (mt7921u exposes the group as p2p-<parent>-N).
	parent := parentIfaceFromP2PGroup(st.GroupIface)
	if parent == "" {
		parent = defaultWiFiInterface()
	}
	if _, err := execWpaCli(r.Context(), "-i", parent, "p2p_group_remove", st.GroupIface); err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeExecError("p2p_group_remove", err))
		return
	}
	log.Info().Str("group_iface", st.GroupIface).Msg("wifi-p2p: group removed")
	writeSuccess(w, "p2p group removed")
}

// @Summary Current WiFi-Direct group state
// @Tags system
// @Produce json
// @Param iface query string false "WiFi interface (default: wlan0)"
// @Success 200 {object} WiFiP2PStatus
// @Router /api/system/wifi/p2p/status [get]
func (s *Server) handleWiFiP2PStatus(w http.ResponseWriter, r *http.Request) {
	iface := r.URL.Query().Get("iface")
	st, err := readP2PStatus(r.Context(), iface)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeExecError("p2p_status", err))
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// readP2PStatus locates the active P2P group by listing wpa_cli
// interfaces and scanning for one whose name matches `p2p-<parent>-<n>`.
// For each candidate group iface we run `wpa_cli -i <group> status` to
// extract ssid + mode + ip.
func readP2PStatus(ctx context.Context, hintIface string) (WiFiP2PStatus, error) {
	var st WiFiP2PStatus
	raw, err := execWpaCli(ctx, "interface")
	if err != nil {
		return st, err
	}
	// `wpa_cli interface` output:
	//   Available interfaces:
	//    p2p-wlx90de80f3a70b-0
	//    wlx90de80f3a70b
	//    wlan0
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "p2p-") {
			continue
		}
		// Skip wpa_supplicant's P2P device-management interface —
		// it's always present when P2P is enabled and isn't an
		// active group. Real groups look like p2p-<parent>-<N>
		// where <N> is a numeric index.
		if strings.HasPrefix(line, "p2p-dev-") {
			continue
		}
		// Require a trailing -<numeric> to be confident we have an
		// actual group (the parentIfaceFromP2PGroup rule).
		if parentIfaceFromP2PGroup(line) == "" ||
			!isTrailingNumericIndex(line) {
			continue
		}
		st.Active = true
		st.GroupIface = line
		// Detail via group-iface status.
		if groupOut, gerr := execWpaCli(ctx, "-i", line, "status"); gerr == nil {
			for _, gl := range strings.Split(groupOut, "\n") {
				gl = strings.TrimSpace(gl)
				if idx := strings.IndexByte(gl, '='); idx > 0 {
					key, val := gl[:idx], gl[idx+1:]
					switch key {
					case "ssid":
						st.SSID = val
					case "mode":
						// mode=station → client; mode=P2P GO → go
						if strings.Contains(val, "P2P GO") || val == "AP" {
							st.Role = "go"
						} else if val == "station" {
							st.Role = "client"
						}
					case "ip_address":
						st.IPAddress = val
					}
				}
			}
		}
		break
	}
	return st, nil
}

// parentIfaceFromP2PGroup extracts the parent from a group-iface name.
// mt7921u format is "p2p-<parent>-<N>" where parent may itself contain
// hyphens (as in wlx90de80f3a70b). We split off the trailing "-N".
func parentIfaceFromP2PGroup(group string) string {
	if !strings.HasPrefix(group, "p2p-") {
		return ""
	}
	body := strings.TrimPrefix(group, "p2p-")
	if idx := strings.LastIndexByte(body, '-'); idx > 0 {
		// Confirm the suffix is numeric (group index).
		if _, err := strconv.Atoi(body[idx+1:]); err == nil {
			return body[:idx]
		}
	}
	return body
}

// isTrailingNumericIndex reports whether a group name ends in
// "-<digits>" — `wpa_cli interface` includes both "p2p-dev-wlan0"
// (device-mgmt, not a group) and "p2p-wlan0-0" (real group). Only
// the numeric-index form is an active group.
func isTrailingNumericIndex(name string) bool {
	idx := strings.LastIndexByte(name, '-')
	if idx < 0 || idx == len(name)-1 {
		return false
	}
	_, err := strconv.Atoi(name[idx+1:])
	return err == nil
}

// isValidMAC accepts standard 6-octet colon-delimited MAC.
func isValidMAC(s string) bool {
	if len(s) != 17 {
		return false
	}
	for i, c := range s {
		switch {
		case i%3 == 2:
			if c != ':' {
				return false
			}
		case (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'):
			// ok
		default:
			return false
		}
	}
	return true
}
