package api

import (
	"net/http"
)

// handleGetSpectrumStatus returns the current state of all monitored frequency bands.
// @Summary Get spectrum monitoring status
// @Description Returns jamming/interference/clear state for each monitored band (LoRa 868MHz, APRS 144.8MHz)
// @Tags spectrum
// @Success 200 {object} map[string]interface{}
// @Router /api/spectrum/status [get]
func (s *Server) handleGetSpectrumStatus(w http.ResponseWriter, r *http.Request) {
	if s.spectrumMon == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"enabled": false,
			"bands":   []interface{}{},
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled": s.spectrumMon.Enabled(),
		"bands":   s.spectrumMon.Status(),
	})
}

// handleGetSpectrumHardware returns RTL-SDR dongle + scan-loop health.
// Operators use this to distinguish "dongle disconnected" from "scan
// process wedged" from "everything fine but quiet".
//
// @Summary  RTL-SDR hardware + scan-loop status
// @Tags     spectrum
// @Success  200 {object} spectrum.HardwareStatus
// @Router   /api/spectrum/hardware [get]
func (s *Server) handleGetSpectrumHardware(w http.ResponseWriter, r *http.Request) {
	if s.spectrumMon == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"available": false})
		return
	}
	writeJSON(w, http.StatusOK, s.spectrumMon.Hardware())
}

// handleGetSpectrumRelay returns per-destination MIJI/CoT relay health.
// Shows last success/failure timestamp + counts so the operator can
// tell at a glance whether downstream sinks (TAK server, hub MQTT)
// are receiving alerts.
//
// @Summary  MIJI/CoT relay per-destination status
// @Tags     spectrum
// @Success  200 {object} map[string]spectrum.RelayStatus
// @Router   /api/spectrum/relay-status [get]
func (s *Server) handleGetSpectrumRelay(w http.ResponseWriter, r *http.Request) {
	if s.spectrumMon == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{})
		return
	}
	writeJSON(w, http.StatusOK, s.spectrumMon.RelayTracker().Snapshot())
}
