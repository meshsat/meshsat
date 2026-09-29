package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"meshsat/internal/transport"
)

// The node over Bluetooth: what the MeshSat Linux app shows and asks under
// Setup > Node when this Bridge's mesh port is `ble`. Scanning, pairing and
// connecting are the transport's; the app never touches BlueZ. [MESHSAT-1390]
type bleNodeManager interface {
	BLEScan(ctx context.Context, seconds int) ([]transport.BLEDevice, error)
	BLEConnect(ctx context.Context, address string) error
	BLEPair(pin string) error
	BLEStatus() transport.BLEStatus
	BLEForget(ctx context.Context, removeBond bool) error
}

func (s *Server) bleManager(w http.ResponseWriter) (bleNodeManager, bool) {
	m, ok := s.mesh.(bleNodeManager)
	if !ok || s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return nil, false
	}
	if m.BLEStatus().Mode == "off" {
		writeError(w, http.StatusConflict, transport.ErrMeshNotBLE.Error())
		return nil, false
	}
	return m, true
}

// handleBLEScan lists the Meshtastic nodes in Bluetooth range.
// @Summary Scan for Meshtastic nodes over Bluetooth
// @Description Scans for the seconds given (default 8, at most 30) and lists the nodes advertising Meshtastic's service, strongest first, with the chosen one marked
// @Tags mesh
// @Produce json
// @Param seconds query int false "How long to scan"
// @Success 200 {object} map[string]interface{}
// @Failure 409 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/mesh/ble/scan [get]
func (s *Server) handleBLEScan(w http.ResponseWriter, r *http.Request) {
	m, ok := s.bleManager(w)
	if !ok {
		return
	}
	seconds, _ := strconv.Atoi(r.URL.Query().Get("seconds"))
	devices, err := m.BLEScan(r.Context(), seconds)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "scan failed: "+err.Error())
		return
	}
	if devices == nil {
		devices = []transport.BLEDevice{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"devices": devices, "count": len(devices)})
}

// handleBLEConnect chooses the node and brings the link up.
// @Summary Connect a Meshtastic node over Bluetooth
// @Description Makes the address the Bridge's node, remembered across restarts; pairs first when the node is new (the node then shows a PIN: POST /api/mesh/ble/pair), and connects. Answers once the previous node's link is ended (a satellite session of this Bridge in flight on its modem ends with its answer first, up to about 90 s), without waiting for the new link; GET /api/mesh/ble/status follows it
// @Tags mesh
// @Accept json
// @Produce json
// @Param body body object{address=string} true "The node's Bluetooth address"
// @Success 202 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/mesh/ble/connect [post]
func (s *Server) handleBLEConnect(w http.ResponseWriter, r *http.Request) {
	m, ok := s.bleManager(w)
	if !ok {
		return
	}
	var req struct {
		Address string `json:"address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Address) == "" {
		writeError(w, http.StatusBadRequest, "address is required")
		return
	}
	if err := m.BLEConnect(r.Context(), req.Address); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "connecting", "address": strings.ToUpper(strings.TrimSpace(req.Address))})
}

// handleBLEPair answers the node's PIN prompt.
// @Summary Enter the PIN the node shows
// @Description Hands the six-digit PIN to the pairing that is waiting for it (status pairing_pending)
// @Tags mesh
// @Accept json
// @Produce json
// @Param body body object{pin=string} true "The PIN shown on the node"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/mesh/ble/pair [post]
func (s *Server) handleBLEPair(w http.ResponseWriter, r *http.Request) {
	m, ok := s.bleManager(w)
	if !ok {
		return
	}
	var req struct {
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	pin := strings.TrimSpace(req.PIN)
	if len(pin) != 6 || strings.Trim(pin, "0123456789") != "" {
		writeError(w, http.StatusBadRequest, "the PIN is six digits")
		return
	}
	if err := m.BLEPair(pin); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "pin entered"})
}

// handleBLEStatus is the state of the node over Bluetooth.
// @Summary The node over Bluetooth
// @Description mode off|idle|scanning|pairing|connecting|ready|lost, the chosen node, whether a pairing waits for a PIN, whether the node carries the MeshSat satellite modem pipe; satellite_enabled (the switch "Use the node's modem", default true), satellite_owner (who holds the node's modem by its STATUS: none, phone for this Bridge, node, or "" when unknown or no pipe) and satellite_link_broken (writes to the node's modem stopped getting through)
// @Tags mesh
// @Produce json
// @Success 200 {object} transport.BLEStatus
// @Failure 503 {object} map[string]string
// @Router /api/mesh/ble/status [get]
func (s *Server) handleBLEStatus(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mesh.(bleNodeManager)
	if !ok || s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}
	writeJSON(w, http.StatusOK, m.BLEStatus())
}

// bleSatelliteManager is the RockBLOCK behind the node over Bluetooth: the
// switch that lends it to this Bridge and the node's own report on it.
// [MESHSAT-1391]
type bleSatelliteManager interface {
	BLESetSatellite(enabled bool) error
	BLESatelliteStats(ctx context.Context) (*transport.PipeStatsReading, error)
}

// errNoSatelliteModem is answered, 404 on both the switch and the report,
// when the mesh transport carries no satellite modem at all. [MESHSAT-1391]
const errNoSatelliteModem = "this mesh transport carries no satellite modem"

// handleBLESetSatellite sets the switch "Use the node's modem".
// @Summary Use the Bluetooth node's satellite modem, or leave it to the node
// @Description Keeps the switch with the node (default on) and acts at once: on claims the node's RockBLOCK for this Bridge (its SBD gateway iridium_0 when MESHSAT_IRIDIUM_PORT=ble); off starts no further satellite session and hands the modem back to the node for its own routing as soon as the session in flight, if any, has ended with its answer (so satellite_owner can still read phone right after). Answers the node's status (satellite_enabled, satellite_owner, satellite_link_broken). 404 when this mesh transport carries no satellite modem, as GET /api/mesh/ble/satellite/stats answers it
// @Tags mesh
// @Accept json
// @Produce json
// @Param body body object{enabled=bool} true "Use the node's modem"
// @Success 200 {object} transport.BLEStatus
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/mesh/ble/satellite [put]
func (s *Server) handleBLESetSatellite(w http.ResponseWriter, r *http.Request) {
	m, ok := s.bleManager(w)
	if !ok {
		return
	}
	sm, ok := m.(bleSatelliteManager)
	if !ok {
		writeError(w, http.StatusNotFound, errNoSatelliteModem)
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Enabled == nil {
		writeError(w, http.StatusBadRequest, `"enabled" (true or false) is required`)
		return
	}
	if err := sm.BLESetSatellite(*req.Enabled); err != nil {
		if errors.Is(err, transport.ErrMeshNotBLE) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m.BLEStatus())
}

// handleBLESatelliteStats is the node's own report on its modem.
// @Summary The Bluetooth node's report on its satellite modem
// @Description The node's STATS (MeshSat node contract v2), whoever holds the modem: owner, flags (session_in_flight, message_waiting, modem_answers, buffer_congested), csq (0-5 as the modem last reported it) and csq_age_s, sessions since boot, last_mo_status, last_momsn, last_mt_status, last_mt_queued, last_session_age_s, uptime_s, watchdog_reboots, client_bytes_dropped, the node's own routing (node_sessions, node_sent, node_received, day_sessions_used, day_sessions_cap); null where the node says unknown. Read again from the node when older than 10 s; read_at is when it was read. 404 for a node without STATS, no MeshSat node connected, or a mesh transport that carries no satellite modem (as PUT /api/mesh/ble/satellite answers it)
// @Tags mesh
// @Produce json
// @Success 200 {object} transport.PipeStatsReading
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/mesh/ble/satellite/stats [get]
func (s *Server) handleBLESatelliteStats(w http.ResponseWriter, r *http.Request) {
	m, ok := s.bleManager(w)
	if !ok {
		return
	}
	sm, ok := m.(bleSatelliteManager)
	if !ok {
		writeError(w, http.StatusNotFound, errNoSatelliteModem)
		return
	}
	reading, err := sm.BLESatelliteStats(r.Context())
	switch {
	case errors.Is(err, transport.ErrNoPipeStats):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, transport.ErrMeshNotBLE):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		writeJSON(w, http.StatusOK, reading)
	}
}

// handleBLEForget drops the node and the memory of it.
// @Summary Disconnect from the node over Bluetooth
// @Description Disconnects and forgets the address (the bond stays, as Android's Disconnect leaves it; bond=1 removes it too, which clears a stale one); the Bridge waits for a node to be chosen again. A satellite session of this Bridge in flight on the node's modem ends with its answer first (up to about 90 s), so it is never cut
// @Tags mesh
// @Produce json
// @Param bond query int false "1 removes the bond as well"
// @Success 200 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/mesh/ble [delete]
func (s *Server) handleBLEForget(w http.ResponseWriter, r *http.Request) {
	m, ok := s.bleManager(w)
	if !ok {
		return
	}
	if err := m.BLEForget(r.Context(), r.URL.Query().Get("bond") == "1"); err != nil {
		if errors.Is(err, transport.ErrMeshNotBLE) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "forgotten"})
}
