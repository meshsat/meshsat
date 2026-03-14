package api

import (
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
