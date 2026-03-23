package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/gateway"
	"meshsat/internal/transport"
)

// handleGetMessages returns paginated message history.
// @Summary Get message history
// @Description Returns paginated mesh messages with optional filters
// @Tags messages
// @Param node query string false "Filter by node ID (!hex format)"
// @Param since query string false "Start time (RFC3339)"
// @Param until query string false "End time (RFC3339)"
// @Param portnum query int false "Filter by port number"
// @Param transport query string false "Filter by transport (radio, mqtt, satellite)"
// @Param direction query string false "Filter by direction (rx, tx)"
// @Param limit query int false "Results per page (default 50, max 1000)"
// @Param offset query int false "Offset for pagination"
// @Success 200 {object} map[string]interface{} "messages, total, limit, offset"
// @Router /api/messages [get]
func (s *Server) handleGetMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter := database.MessageFilter{
		Node:      q.Get("node"),
		Since:     q.Get("since"),
		Until:     q.Get("until"),
		Transport: q.Get("transport"),
		Direction: q.Get("direction"),
		Limit:     intParam(q.Get("limit"), 50),
		Offset:    intParam(q.Get("offset"), 0),
	}

	if v := q.Get("portnum"); v != "" {
		pn, err := strconv.Atoi(v)
		if err == nil {
			filter.PortNum = &pn
		}
	}

	msgs, total, err := s.db.GetMessages(filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to query messages: "+err.Error())
		return
	}
	if msgs == nil {
		msgs = []database.Message{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"messages": msgs,
		"total":    total,
		"limit":    filter.Limit,
		"offset":   filter.Offset,
	})
}

// handleGetMessageStats returns aggregate message statistics.
// @Summary Get message statistics
// @Description Returns message counts grouped by transport and port number
// @Tags messages
// @Success 200 {object} database.MessageStats
// @Router /api/messages/stats [get]
func (s *Server) handleGetMessageStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.db.GetMessageStats()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to get stats: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// handleSendMessage sends a text message via the mesh transport or a satellite gateway.
// @Summary Send a message
// @Description Sends a text message through the Meshtastic radio or a satellite gateway
// @Tags messages
// @Param body body transport.SendRequest true "Message to send"
// @Success 200 {object} map[string]string "success"
// @Failure 400 {object} map[string]string "error"
// @Router /api/messages/send [post]
func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	var req transport.SendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}

	// If a gateway is specified, route through the gateway manager.
	if req.Gateway != "" {
		if s.gwManager == nil {
			writeError(w, http.StatusServiceUnavailable, "gateway manager unavailable")
			return
		}
		var target gateway.Gateway
		for _, gw := range s.gwManager.Gateways() {
			if gw.Type() == req.Gateway {
				target = gw
				break
			}
		}
		if target == nil {
			writeError(w, http.StatusBadRequest, "unknown gateway: "+req.Gateway)
			return
		}
		st := target.Status()
		if !st.Connected {
			writeError(w, http.StatusServiceUnavailable, req.Gateway+" gateway not connected")
			return
		}
		msg := &transport.MeshMessage{
			DecodedText: req.Text,
			RxTime:      time.Now().Unix(),
		}
		if err := target.Forward(r.Context(), msg); err != nil {
			writeError(w, http.StatusInternalServerError, "gateway send failed: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "sent", "gateway": req.Gateway})
		return
	}

	// Default: send via Meshtastic mesh radio.
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}
	if err := s.mesh.SendMessage(r.Context(), req); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to send: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// handlePurgeMessages deletes messages older than a given timestamp.
func (s *Server) handlePurgeMessages(w http.ResponseWriter, r *http.Request) {
	before := r.URL.Query().Get("before")
	if before == "" {
		writeError(w, http.StatusBadRequest, "before parameter required (RFC3339 timestamp)")
		return
	}

	deleted, err := s.db.PurgeMessages(before)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to purge: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": deleted,
	})
}

func intParam(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return v
}
