package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/rs/zerolog/log"
)

// handleSSE re-broadcasts processor events to web UI clients.
// @Summary Subscribe to mesh events (SSE)
// @Description Server-Sent Events stream of real-time mesh events
// @Tags events
// @Produce text/event-stream
// @Success 200 {string} string "SSE stream"
// @Router /api/events [get]
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	events, unsub := s.processor.Subscribe()
	defer unsub()

	// Send initial connection event
	fmt.Fprintf(w, "data: {\"type\":\"connected_to_stream\",\"message\":\"subscribed to MeshSat event stream\"}\n\n")
	flusher.Flush()

	log.Debug().Msg("SSE client connected")

	for {
		select {
		case <-r.Context().Done():
			log.Debug().Msg("SSE client disconnected")
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// handleHealth returns service health status.
// @Summary Health check
// @Description Returns MeshSat service health and database status
// @Tags system
// @Success 200 {object} map[string]string
// @Router /health [get]
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	dbOK := true
	if err := s.db.Ping(); err != nil {
		dbOK = false
	}

	status := "healthy"
	code := http.StatusOK
	if !dbOK {
		status = "degraded"
		code = http.StatusServiceUnavailable
	}

	writeJSON(w, code, map[string]interface{}{
		"status":   status,
		"service":  "meshsat",
		"database": dbOK,
	})
}
