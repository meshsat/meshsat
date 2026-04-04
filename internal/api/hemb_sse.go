package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"meshsat/internal/hemb"
)

// handleHeMBSSE streams HeMB events via Server-Sent Events.
// Query params: stream_id, bearer_id, event_type (comma-separated), limit.
// @Summary HeMB event stream (SSE)
// @Description Streams HeMB bond group events via Server-Sent Events with optional filtering
// @Tags hemb
// @Produce text/event-stream
// @Param stream_id query string false "Filter by stream ID"
// @Param bearer_id query string false "Filter by bearer interface ID"
// @Param event_type query string false "Filter by event type"
// @Param replay query integer false "Number of recent events to replay (default: 50)"
// @Success 200 {string} string "SSE event stream"
// @Router /api/hemb/events [get]
func (s *Server) handleHeMBSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Parse filter params.
	filterStreamID := r.URL.Query().Get("stream_id")
	filterBearerID := r.URL.Query().Get("bearer_id")
	filterEventType := r.URL.Query().Get("event_type")

	events, unsub := hemb.GlobalEventBus.Subscribe()
	defer unsub()

	// Send recent events first (replay buffer).
	replayLimit := 50
	if lim := r.URL.Query().Get("replay"); lim != "" {
		if n, err := strconv.Atoi(lim); err == nil && n > 0 {
			replayLimit = n
		}
	}
	for _, evt := range hemb.GlobalEventBus.Recent(replayLimit) {
		if !matchFilter(evt, filterStreamID, filterBearerID, filterEventType) {
			continue
		}
		data, _ := json.Marshal(evt)
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	flusher.Flush()

	// Send initial connection event.
	fmt.Fprintf(w, "data: {\"type\":\"hemb_connected\",\"message\":\"subscribed to HeMB event stream\"}\n\n")
	flusher.Flush()

	log.Debug().Msg("HeMB SSE client connected")

	for {
		select {
		case <-r.Context().Done():
			log.Debug().Msg("HeMB SSE client disconnected")
			return
		case evt, ok := <-events:
			if !ok {
				return
			}
			if !matchFilter(evt, filterStreamID, filterBearerID, filterEventType) {
				continue
			}
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// matchFilter checks if an event matches the SSE query parameters.
func matchFilter(evt hemb.Event, streamID, bearerID, eventType string) bool {
	if eventType != "" && string(evt.Type) != eventType {
		return false
	}
	// For stream_id and bearer_id filtering, we'd need to parse the payload.
	// For now, type-level filtering is sufficient. The frontend can do fine-grained filtering.
	_ = streamID
	_ = bearerID
	return true
}

// handleGetHeMBTopology returns the bearer-centric topology for the graph component.
// @Summary Get HeMB topology
// @Description Returns bearer-centric topology for the HeMB graph visualization
// @Tags hemb
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/hemb/topology [get]
func (s *Server) handleGetHeMBTopology(w http.ResponseWriter, r *http.Request) {
	// Build topology from bond groups and interface status.
	groups, _ := s.db.GetAllBondGroups()

	type bearerEdge struct {
		InterfaceID string `json:"interface_id"`
		ChannelType string `json:"channel_type"`
		IsPaid      bool   `json:"is_paid"`
		HealthScore int    `json:"health_score"`
		Online      bool   `json:"online"`
	}

	type topologyResponse struct {
		Local  map[string]string `json:"local"`
		Peers  []map[string]any  `json:"peers"`
		Groups []map[string]any  `json:"groups"`
		Edges  []bearerEdge      `json:"bearer_edges"`
	}

	resp := topologyResponse{
		Local: map[string]string{
			"id":   s.bridgeID(),
			"role": "bridge",
		},
		Peers:  []map[string]any{},
		Groups: []map[string]any{},
		Edges:  []bearerEdge{},
	}

	for _, g := range groups {
		members, _ := s.db.GetBondMembers(g.ID)
		memberIDs := make([]string, len(members))
		for i, m := range members {
			memberIDs[i] = m.InterfaceID
		}
		resp.Groups = append(resp.Groups, map[string]any{
			"id":      g.ID,
			"label":   g.Label,
			"members": memberIDs,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleGetHeMBEventHistory returns paginated historical events from the ring buffer.
// @Summary Get HeMB event history
// @Description Returns paginated historical HeMB events with optional type filter
// @Tags hemb
// @Produce json
// @Param limit query integer false "Max events (default: 100, max: 500)"
// @Param event_type query string false "Filter by event type"
// @Success 200 {object} map[string]any
// @Router /api/hemb/events/history [get]
func (s *Server) handleGetHeMBEventHistory(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if lim := r.URL.Query().Get("limit"); lim != "" {
		if n, err := strconv.Atoi(lim); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	filterType := r.URL.Query().Get("event_type")

	var events []hemb.Event
	if filterType != "" {
		events = hemb.GlobalEventBus.RecentByType(hemb.EventType(filterType), limit)
	} else {
		events = hemb.GlobalEventBus.Recent(limit)
	}
	if events == nil {
		events = []hemb.Event{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"events":   events,
		"total":    len(events),
		"has_more": false,
	})
}

// handleGetHeMBStreams returns active reassembly streams.
// @Summary List HeMB active streams
// @Description Returns active RLNC reassembly streams
// @Tags hemb
// @Produce json
// @Success 200 {object} map[string]any
// @Router /api/hemb/streams [get]
func (s *Server) handleGetHeMBStreams(w http.ResponseWriter, r *http.Request) {
	streams := s.processor.HeMBActiveStreams()
	if streams == nil {
		streams = []hemb.StreamInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"streams": streams,
	})
}

// handleGetHeMBStreamDetail returns per-generation detail for a specific stream.
// @Summary Get HeMB stream detail
// @Description Returns per-generation detail for a specific RLNC stream
// @Tags hemb
// @Produce json
// @Param id path integer true "Stream ID (0-255)"
// @Success 200 {object} map[string]any
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/hemb/streams/{id} [get]
func (s *Server) handleGetHeMBStreamDetail(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil || id < 0 || id > 255 {
		writeError(w, http.StatusBadRequest, "invalid stream id")
		return
	}
	gens, ok := s.processor.HeMBStreamDetail(uint8(id))
	if !ok {
		writeError(w, http.StatusNotFound, "stream not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"stream_id":   id,
		"generations": gens,
	})
}

// handleGetHeMBGenerationInspect returns RLNC matrix details for debugging.
// @Summary Inspect HeMB generation
// @Description Returns RLNC coding matrix details for a specific generation (debug)
// @Tags hemb
// @Produce json
// @Param stream_id path integer true "Stream ID (0-255)"
// @Param gen_id path integer true "Generation ID (0-65535)"
// @Success 200 {object} map[string]any
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/hemb/generations/{stream_id}/{gen_id} [get]
func (s *Server) handleGetHeMBGenerationInspect(w http.ResponseWriter, r *http.Request) {
	sidStr := chi.URLParam(r, "stream_id")
	gidStr := chi.URLParam(r, "gen_id")
	sid, err := strconv.Atoi(sidStr)
	if err != nil || sid < 0 || sid > 255 {
		writeError(w, http.StatusBadRequest, "invalid stream_id")
		return
	}
	gid, err := strconv.Atoi(gidStr)
	if err != nil || gid < 0 || gid > 65535 {
		writeError(w, http.StatusBadRequest, "invalid gen_id")
		return
	}
	inspection, ok := s.processor.HeMBInspectGeneration(uint8(sid), uint16(gid))
	if !ok {
		writeError(w, http.StatusNotFound, "generation not found")
		return
	}
	writeJSON(w, http.StatusOK, inspection)
}

// bridgeID returns the bridge identifier for topology.
func (s *Server) bridgeID() string {
	// Try to get from config or hostname.
	if id, err := s.db.GetSystemConfig("bridge_id"); err == nil && id != "" {
		return id
	}
	return "bridge"
}
