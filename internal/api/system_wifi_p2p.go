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
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// cryptoRandRead is a tiny indirection so tests can stub RNG if ever needed.
var cryptoRandRead = cryptorand.Read

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
	// Ensure wpa_supplicant is actually managing this iface before we
	// ask wpa_cli to talk P2P to it — on USB dongles it usually isn't
	// by default (only wlan0 is bound via netplan's wifis: section).
	// Without this, wpa_cli -i <usb_iface> returns exit 255.
	if err := ensureWpaSupplicant(r.Context(), iface); err != nil {
		writeError(w, http.StatusServiceUnavailable, sanitizeExecError("ensureWpaSupplicant", err))
		return
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
	// Ensure wpa_supplicant is managing this iface — see handleWiFiP2PFind.
	if err := ensureWpaSupplicant(r.Context(), iface); err != nil {
		writeError(w, http.StatusServiceUnavailable, sanitizeExecError("ensureWpaSupplicant", err))
		return
	}
	// p2p_peers returns a newline-separated list of MACs. Retry the
	// call for a couple of seconds — on a fresh wpa_supplicant the
	// control socket briefly exists before the daemon is actually
	// listening on P2P, and the first call fails with exit 255 even
	// though everything is fine by the next tick.
	var out string
	var err error
	for i := 0; i < 4; i++ {
		out, err = execWpaCli(r.Context(), "-i", iface, "p2p_peers")
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
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

// wifiP2PConnectRequest matches the UI store's payload.  WPS-PIN is
// mandatory — PBC (push-button) is explicitly refused because it
// accepts pairing from any nearby device during its 120 s window.
// Operator supplies the PIN out-of-band (read off one kit's screen,
// enter on the other). Both kits send the same PIN. [MESHSAT-647 +
// MESHSAT-648]
type wifiP2PConnectRequest struct {
	Interface string `json:"interface"`
	PeerAddr  string `json:"peer_addr"`
	Pin       string `json:"pin"`
	// Role is the WPS config method this kit takes in the PIN exchange:
	//   "display" — this kit generated + showed the PIN; peer typed it
	//   "keypad"  — this kit received + typed the PIN; peer displays
	// When both kits advertise BOTH capabilities (common on mt7921u),
	// wpa_supplicant needs the role explicitly or negotiation stalls
	// and the virtual p2p-N iface comes up briefly then disappears
	// (observed live 2026-04-21). Empty → default to "keypad".
	Role     string `json:"role,omitempty"`
	GOIntent int    `json:"go_intent,omitempty"` // 0-15, default 7
}

// isValidWPSPIN accepts 4 or 8 numeric digits (4 = short unchecked,
// 8 = standard WPS with check digit).
func isValidWPSPIN(s string) bool {
	if len(s) != 4 && len(s) != 8 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
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
	// Authenticated pairing only — WPS-PIN mandatory, PBC refused.
	// [MESHSAT-647 hardening]. The real trust-anchor gate (peer MUST
	// have been pre-registered via BLE-pair trusted_peers) is
	// tracked as MESHSAT-648; PIN closes the immediate drive-by
	// window.
	if !isValidWPSPIN(req.Pin) {
		writeError(w, http.StatusBadRequest, "pin is required (4 or 8 digits). PBC is disabled — unauthenticated pairing is not permitted.")
		return
	}
	if err := ensureWpaSupplicant(r.Context(), req.Interface); err != nil {
		writeError(w, http.StatusServiceUnavailable, sanitizeExecError("ensureWpaSupplicant", err))
		return
	}
	role := req.Role
	switch role {
	case "display", "keypad":
		// ok
	case "":
		role = "keypad" // safest default — peer typed a PIN into us
	default:
		writeError(w, http.StatusBadRequest, "role must be display or keypad")
		return
	}
	// Tie the GO-intent to the role. If both kits fire with the same
	// intent (observed live 2026-04-21: both ran go_intent=7, tie-
	// breaker made parallax the GO but tesseract never fell back to
	// client, negotiation timed out after ~6 s) neither side can
	// complete the WPS exchange. Binding role → intent removes the
	// ambiguity: the kit DISPLAYING the PIN is always the Group
	// Owner; the kit TYPING the PIN is always the client. This
	// matches the usual WiFi-Direct idiom for kit-to-kit.
	goIntent := 15 // display → forced GO
	if role == "keypad" {
		goIntent = 0 // forced client
	}
	args := []string{"-i", req.Interface, "p2p_connect", req.PeerAddr, req.Pin, role, "go_intent=" + strconv.Itoa(goIntent)}
	out, err := execWpaCli(r.Context(), args...)
	if err != nil {
		log.Error().Err(err).Str("peer", req.PeerAddr).Msg("wifi-p2p: connect failed")
		writeError(w, http.StatusInternalServerError, sanitizeExecError("p2p_connect", err))
		return
	}
	log.Info().Str("iface", req.Interface).Str("peer", req.PeerAddr).Msg("wifi-p2p: PIN-authenticated connect initiated")

	// Close the gap: once the group comes up (async, ~2-8s), auto-add
	// the peer to our Reticulum TCP peer list and enrol tcp_0 in the
	// default HeMB bond. The operator shouldn't have to copy IPs
	// around. Runs in a goroutine so the HTTP response returns
	// immediately — the poll-and-wire happens out-of-band. [MESHSAT-647]
	go s.autoWireP2PPeer(req.PeerAddr)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"interface": req.Interface, "peer": req.PeerAddr, "method": "pin",
		"wpa_result": strings.TrimSpace(out),
	})
}

// autoWireP2PPeer polls the group status until active, derives the
// peer's IPv6 link-local from its MAC, and adds it as a Reticulum TCP
// peer + a member of the default HeMB bond group. Best-effort — every
// failure logs and moves on so a busted write doesn't leak to the
// primary connect flow.
func (s *Server) autoWireP2PPeer(peerMAC string) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	var groupIface string
	for i := 0; i < 24; i++ {
		st, _ := readP2PStatus(ctx, "")
		if st.Active && st.GroupIface != "" {
			groupIface = st.GroupIface
			break
		}
		time.Sleep(1 * time.Second)
	}
	if groupIface == "" {
		log.Warn().Str("peer", peerMAC).Msg("wifi-p2p: auto-wire gave up — group did not come up in 24 s")
		return
	}
	addr := derivePeerLinkLocalTCP(peerMAC, groupIface, 4242)
	if addr == "" {
		log.Warn().Str("peer", peerMAC).Msg("wifi-p2p: could not derive peer link-local")
		return
	}
	// Reticulum TCP peer
	if s.tcpIface != nil {
		if err := s.tcpIface.AddPeer(ctx, addr); err != nil {
			log.Warn().Err(err).Str("addr", addr).Msg("wifi-p2p: add TCP peer failed (maybe already present)")
		} else {
			log.Info().Str("addr", addr).Str("peer", peerMAC).Msg("wifi-p2p: added peer to Reticulum TCP interface")
			peers := s.loadPeers()
			seen := false
			for _, p := range peers {
				if p == addr {
					seen = true
					break
				}
			}
			if !seen {
				s.savePeers(append(peers, addr))
			}
		}
	}
	// HeMB bond membership — add tcp_0 to any existing bond group if
	// not already there. Leave alone if operator already enrolled it.
	// [MESHSAT-647 + MESHSAT-421]
	if s.db != nil {
		groups, err := s.db.GetAllBondGroups()
		if err == nil {
			for _, g := range groups {
				members, _ := s.db.GetBondMembers(g.ID)
				hasTCP := false
				for _, m := range members {
					if m.InterfaceID == "tcp_0" {
						hasTCP = true
						break
					}
				}
				if !hasTCP {
					_ = s.db.InsertBondMember(&database.BondMember{
						GroupID:     g.ID,
						InterfaceID: "tcp_0",
						Priority:    len(members),
					})
					log.Info().Str("bond", g.ID).Msg("wifi-p2p: enrolled tcp_0 in existing bond group")
				}
			}
		}
	}
}

// derivePeerLinkLocalTCP returns a Reticulum-consumable peer address
// string "[fe80::...%iface]:port" computed from the peer's P2P MAC
// using the non-EUI-64 form observed on mt7921u (no U/L bit flip).
// Confirmed 2026-04-21: parallax MAC 90:de:80:f3:a7:1e →
// fe80::90de:80ff:fef3:a71e%p2p-0 on tesseract.
func derivePeerLinkLocalTCP(mac, iface string, port int) string {
	b := parseMACBytes(mac)
	if b == nil {
		return ""
	}
	return fmt.Sprintf("[fe80::%02x%02x:%02xff:fe%02x:%02x%02x%%%s]:%d",
		b[0], b[1], b[2], b[3], b[4], b[5], iface, port)
}

// parseMACBytes returns the 6 octets of a colon-delim MAC, or nil.
func parseMACBytes(mac string) []byte {
	if !isValidMAC(mac) {
		return nil
	}
	out := make([]byte, 6)
	// strconv.ParseUint would work; doing it inline avoids another
	// import and is faster.
	hex := func(c byte) int {
		switch {
		case c >= '0' && c <= '9':
			return int(c - '0')
		case c >= 'a' && c <= 'f':
			return int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			return int(c-'A') + 10
		}
		return -1
	}
	for i := 0; i < 6; i++ {
		hi := hex(mac[i*3])
		lo := hex(mac[i*3+1])
		if hi < 0 || lo < 0 {
			return nil
		}
		out[i] = byte((hi << 4) | lo)
	}
	return out
}

// @Summary Generate a one-time 8-digit WPS PIN
// @Description crypto/rand 8 digits with the standard WPS check-digit. Caller reads the PIN off the screen, enters it on the peer kit. [MESHSAT-647 hardening]
// @Tags system
// @Produce json
// @Success 200 {object} map[string]string
// @Router /api/system/wifi/p2p/gen-pin [post]
func (s *Server) handleWiFiP2PGenPin(w http.ResponseWriter, r *http.Request) {
	pin, err := generateWPSPIN()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "pin gen failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"pin": pin})
}

// generateWPSPIN returns 8 random digits with the WPS check digit.
// Uses crypto/rand for the first 7 digits.
func generateWPSPIN() (string, error) {
	var buf [7]byte
	if _, err := cryptoRandRead(buf[:]); err != nil {
		return "", err
	}
	digits := [8]byte{}
	for i := 0; i < 7; i++ {
		digits[i] = '0' + (buf[i] % 10)
	}
	digits[7] = '0' + byte(wpsCheckDigit(string(digits[:7])))
	return string(digits[:]), nil
}

// wpsCheckDigit computes the 8th digit per WPS-PIN spec: weighted
// sum mod 10 of the 7-digit base number, subtracted from 10.
func wpsCheckDigit(base7 string) int {
	if len(base7) != 7 {
		return 0
	}
	acc := 0
	weights := []int{3, 1, 3, 1, 3, 1, 3}
	for i, c := range base7 {
		acc += int(c-'0') * weights[i]
	}
	d := (10 - (acc % 10)) % 10
	return d
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
	// Remove any persisted TCP peers whose address string points at
	// this group iface — the link-local is only valid while the
	// group is up, so leaving stale entries just wastes reconnect
	// attempts.
	s.removeP2PTCPPeersForGroup(st.GroupIface)

	if _, err := execWpaCli(r.Context(), "-i", parent, "p2p_group_remove", st.GroupIface); err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeExecError("p2p_group_remove", err))
		return
	}
	log.Info().Str("group_iface", st.GroupIface).Msg("wifi-p2p: group removed")
	writeSuccess(w, "p2p group removed")
}

// removeP2PTCPPeersForGroup purges any peer persisted in the
// reticulum_peers config whose address string mentions the given
// group iface (zone-id suffix e.g. "%p2p-0"). Matching the zone is a
// cheap proxy for "this peer reached us via the P2P link we're about
// to tear down". [MESHSAT-647]
func (s *Server) removeP2PTCPPeersForGroup(groupIface string) {
	if s.tcpIface == nil || groupIface == "" {
		return
	}
	peers := s.loadPeers()
	kept := peers[:0]
	for _, p := range peers {
		if strings.Contains(p, "%"+groupIface+"]") {
			s.tcpIface.RemovePeer(p)
			log.Info().Str("addr", p).Msg("wifi-p2p: removed TCP peer on group teardown")
			continue
		}
		kept = append(kept, p)
	}
	if len(kept) != len(peers) {
		s.savePeers(kept)
	}
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
