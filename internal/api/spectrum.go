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
