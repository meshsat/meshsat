package api

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// handleSpectrumStream streams per-scan power samples + state transitions
// from the RTL-SDR spectrum monitor via Server-Sent Events. The dashboard
// waterfall consumes this stream to render the 5-band realtime view and
// to trigger the sticky jamming-alert modal on transitions.
//
// Event format: each SSE message is a JSON-encoded spectrum.SpectrumEvent.
// Kind is either "scan" (per-bin powers) or "transition" (state change).
// Scan events arrive on every ScanInterval tick (~3s) per band; transition
// events are rare and also drive the dashboard popup + out-of-band alerts.
//
// Filtering: band=<name> narrows to a single band, useful when drilling
// into one waterfall panel. kind=transition filters to alerts only.
//
// @Summary Spectrum waterfall stream
// @Description Real-time RTL-SDR scan samples + jamming transitions
// @Tags spectrum
// @Produce text/event-stream
// @Param band query string false "Filter to a single band name"
// @Param kind query string false "Filter to 'scan' or 'transition'"
// @Success 200 {string} string "SSE stream"
// @Router /api/spectrum/stream [get]
func (s *Server) handleSpectrumStream(w http.ResponseWriter, r *http.Request) {
	if s.spectrumMon == nil || !s.spectrumMon.Enabled() {
		http.Error(w, "spectrum monitor disabled (rtl_power not available)", http.StatusServiceUnavailable)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	bandFilter := r.URL.Query().Get("band")
	kindFilter := r.URL.Query().Get("kind")

	ch, unsub := s.spectrumMon.Subscribe()
	defer unsub()

	// Flush the headers immediately so the client sees the connection is
	// open even before the first scan sample arrives (up to 3s later).
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			if bandFilter != "" && evt.Band != bandFilter {
				continue
			}
			if kindFilter != "" && string(evt.Kind) != kindFilter {
				continue
			}
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Kind, data)
			flusher.Flush()
		}
	}
}
