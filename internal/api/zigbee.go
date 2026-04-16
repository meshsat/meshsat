package api

import (
	"encoding/json"
	"net/http"
)

// handleGetZigBeeDevices returns all paired ZigBee devices.
// @Summary List ZigBee devices
// @Description Returns all paired ZigBee devices with short address, IEEE address, LQI, and last seen time
// @Tags zigbee
// @Produce json
// @Success 200 {object} map[string]interface{} "devices, connected, firmware"
// @Router /api/zigbee/devices [get]
func (s *Server) handleGetZigBeeDevices(w http.ResponseWriter, r *http.Request) {
	zgw := s.gwManager.GetZigBeeGateway()
	if zgw == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"devices":   []interface{}{},
			"connected": false,
		})
		return
	}

	t := zgw.GetTransport()
	if t == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"devices":   []interface{}{},
			"connected": false,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"devices":   t.GetDevices(),
		"connected": t.IsRunning(),
		"firmware":  t.FirmwareVersion,
	})
}

// handleGetZigBeeStatus returns the ZigBee coordinator status.
// @Summary Get ZigBee coordinator status
// @Description Returns the ZigBee coordinator connection state, message counts, firmware version, and uptime
// @Tags zigbee
// @Produce json
// @Success 200 {object} map[string]interface{} "connected, running, messages_in, messages_out, errors, firmware, device_count"
// @Router /api/zigbee/status [get]
func (s *Server) handleGetZigBeeStatus(w http.ResponseWriter, r *http.Request) {
	zgw := s.gwManager.GetZigBeeGateway()
	if zgw == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"connected": false,
			"running":   false,
		})
		return
	}

	status := zgw.Status()
	t := zgw.GetTransport()

	resp := map[string]interface{}{
		"connected":    status.Connected,
		"running":      true,
		"messages_in":  status.MessagesIn,
		"messages_out": status.MessagesOut,
		"errors":       status.Errors,
	}

	if !status.LastActivity.IsZero() {
		resp["last_activity"] = status.LastActivity
	}
	if status.ConnectionUptime != "" {
		resp["uptime"] = status.ConnectionUptime
	}
	if t != nil {
		resp["firmware"] = t.FirmwareVersion
		resp["device_count"] = len(t.GetDevices())
	}

	writeJSON(w, http.StatusOK, resp)
}

// handlePostZigBeePermitJoin opens the ZigBee network for device pairing.
// @Summary Open ZigBee network for pairing
// @Description Sends ZDO_MGMT_PERMIT_JOIN_REQ to the coordinator to allow new devices to join
// @Tags zigbee
// @Accept json
// @Produce json
// @Param body body object true "duration_sec (1-254, 0 to close)"
// @Success 200 {object} map[string]interface{} "ok, duration_sec"
// @Failure 400 {object} map[string]string "error"
// @Failure 503 {object} map[string]string "error"
// @Router /api/zigbee/permit-join [post]
func (s *Server) handlePostZigBeePermitJoin(w http.ResponseWriter, r *http.Request) {
	zgw := s.gwManager.GetZigBeeGateway()
	if zgw == nil {
		writeError(w, http.StatusServiceUnavailable, "zigbee gateway not running")
		return
	}
	t := zgw.GetTransport()
	if t == nil {
		writeError(w, http.StatusServiceUnavailable, "zigbee transport not available")
		return
	}

	var req struct {
		DurationSec int `json:"duration_sec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.DurationSec < 0 || req.DurationSec > 254 {
		writeError(w, http.StatusBadRequest, "duration_sec must be 0-254")
		return
	}

	if err := t.PermitJoin(byte(req.DurationSec)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":           true,
		"duration_sec": req.DurationSec,
	})
}

// handleGetZigBeePermitJoin returns the current permit-join status.
// @Summary Get ZigBee permit-join status
// @Description Returns whether the network is open for pairing and the remaining duration
// @Tags zigbee
// @Produce json
// @Success 200 {object} map[string]interface{} "active, remaining_sec"
// @Router /api/zigbee/permit-join [get]
func (s *Server) handleGetZigBeePermitJoin(w http.ResponseWriter, r *http.Request) {
	zgw := s.gwManager.GetZigBeeGateway()
	if zgw == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"active":        false,
			"remaining_sec": 0,
		})
		return
	}
	t := zgw.GetTransport()
	if t == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"active":        false,
			"remaining_sec": 0,
		})
		return
	}

	rem := t.PermitJoinRemaining()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"active":        rem > 0,
		"remaining_sec": rem,
	})
}
