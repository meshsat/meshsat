package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"meshsat/internal/gateway"
)

// handleTAKEventsSSE streams CoT events via Server-Sent Events.
// @Summary TAK CoT event stream
// @Description Real-time stream of CoT events flowing through the TAK gateway
// @Tags tak
// @Produce text/event-stream
// @Param type query string false "Filter by CoT type prefix (e.g. 'a-f', 'b-t')"
// @Param callsign query string false "Filter by callsign substring"
// @Success 200 {string} string "SSE stream"
// @Router /api/tak/events [get]
func (s *Server) handleTAKEventsSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	typeFilter := r.URL.Query().Get("type")
	callsignFilter := r.URL.Query().Get("callsign")

	matchesFilter := func(evt gateway.TakCotEventRecord) bool {
		if typeFilter != "" && !strings.HasPrefix(evt.Type, typeFilter) {
			return false
		}
		if callsignFilter != "" && !strings.Contains(strings.ToLower(evt.Callsign), strings.ToLower(callsignFilter)) {
			return false
		}
		return true
	}

	// Replay recent events
	recent := gateway.GlobalTakEventBus.Recent(100)
	for _, evt := range recent {
		if !matchesFilter(evt) {
			continue
		}
		data, _ := json.Marshal(evt)
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	flusher.Flush()

	// Subscribe to live events
	ch, unsub := gateway.GlobalTakEventBus.Subscribe()
	defer unsub()

	for {
		select {
		case <-r.Context().Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			if !matchesFilter(evt) {
				continue
			}
			data, _ := json.Marshal(evt)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// handleTAKRecentEvents returns recent CoT events as JSON (non-SSE).
// @Summary Recent TAK CoT events
// @Description Returns the last 100 CoT events from the ring buffer
// @Tags tak
// @Produce json
// @Success 200 {array} gateway.TakCotEventRecord
// @Router /api/tak/events/recent [get]
func (s *Server) handleTAKRecentEvents(w http.ResponseWriter, r *http.Request) {
	events := gateway.GlobalTakEventBus.Recent(100)
	writeJSON(w, http.StatusOK, events)
}
