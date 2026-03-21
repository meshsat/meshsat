package api

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// handleGetUSBDevices returns the device supervisor's serial device inventory.
// @Summary List discovered USB serial devices
// @Tags devices
// @Produce json
// @Success 200 {array} transport.SerialDeviceEntry
// @Router /api/devices/usb [get]
func (s *Server) handleGetUSBDevices(w http.ResponseWriter, r *http.Request) {
	if s.devSupervisor == nil {
		writeError(w, http.StatusServiceUnavailable, "device supervisor not available (HAL mode)")
		return
	}
	devices := s.devSupervisor.Registry().ListAll()
	writeJSON(w, http.StatusOK, devices)
}

// handleUSBDeviceEvents streams device connect/disconnect events via SSE.
// @Summary Stream USB device events
// @Tags devices
// @Produce text/event-stream
// @Router /api/devices/usb/events [get]
func (s *Server) handleUSBDeviceEvents(w http.ResponseWriter, r *http.Request) {
	if s.devSupervisor == nil {
		writeError(w, http.StatusServiceUnavailable, "device supervisor not available")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	ch, unsub := s.devSupervisor.SubscribeEvents()
	defer unsub()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data)
			flusher.Flush()
		}
	}
}

// handleTriggerUSBScan forces an immediate USB device scan.
// @Summary Trigger USB device scan
// @Tags devices
// @Success 204
// @Router /api/devices/usb/scan [post]
func (s *Server) handleTriggerUSBScan(w http.ResponseWriter, r *http.Request) {
	if s.devSupervisor == nil {
		writeError(w, http.StatusServiceUnavailable, "device supervisor not available")
		return
	}
	s.devSupervisor.TriggerScan()
	w.WriteHeader(http.StatusNoContent)
}
