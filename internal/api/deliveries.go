package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/database"
)

// GET /api/deliveries — list deliveries with optional filters
// @Summary List message deliveries
// @Description Returns message deliveries with optional channel, status, and message ref filters
// @Tags deliveries
// @Produce json
// @Param channel query string false "Filter by channel type"
// @Param status query string false "Filter by status (queued, sent, failed, dead)"
// @Param msg_ref query string false "Filter by message reference"
// @Param limit query integer false "Max results (default: 50, max: 500)"
// @Param offset query integer false "Pagination offset"
// @Success 200 {array} database.Delivery
// @Failure 500 {object} map[string]string
// @Router /api/deliveries [get]
func (s *Server) handleGetDeliveries(w http.ResponseWriter, r *http.Request) {
	filter := database.DeliveryFilter{
		Channel: r.URL.Query().Get("channel"),
		Status:  r.URL.Query().Get("status"),
		MsgRef:  r.URL.Query().Get("msg_ref"),
		Limit:   50,
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			filter.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			filter.Offset = n
		}
	}

	deliveries, err := s.db.GetDeliveries(filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list deliveries")
		return
	}
	writeJSON(w, http.StatusOK, deliveries)
}

// GET /api/deliveries/stats — delivery counts by channel and status
// @Summary Get delivery statistics
// @Description Returns delivery counts grouped by channel and status
// @Tags deliveries
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 500 {object} map[string]string
// @Router /api/deliveries/stats [get]
func (s *Server) handleGetDeliveryStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.db.DeliveryStatsAll()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get delivery stats")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// GET /api/deliveries/{id} — single delivery detail
// @Summary Get delivery detail
// @Description Returns a single delivery record by ID
// @Tags deliveries
// @Produce json
// @Param id path integer true "Delivery ID"
// @Success 200 {object} database.Delivery
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/deliveries/{id} [get]
func (s *Server) handleGetDelivery(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid delivery id")
		return
	}

	del, err := s.db.GetDelivery(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "delivery not found")
		return
	}
	writeJSON(w, http.StatusOK, del)
}

// POST /api/deliveries/{id}/cancel — cancel a queued/retry delivery
// @Summary Cancel delivery
// @Description Cancels a queued or retrying delivery
// @Tags deliveries
// @Produce json
// @Param id path integer true "Delivery ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/deliveries/{id}/cancel [post]
func (s *Server) handleCancelDelivery(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid delivery id")
		return
	}

	if err := s.db.CancelDelivery(id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to cancel delivery")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// POST /api/deliveries/{id}/retry — retry a failed/dead delivery
// @Summary Retry delivery
// @Description Re-queues a failed or dead-lettered delivery for another attempt
// @Tags deliveries
// @Produce json
// @Param id path integer true "Delivery ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/deliveries/{id}/retry [post]
func (s *Server) handleRetryDelivery(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid delivery id")
		return
	}

	if err := s.db.RetryDelivery(id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to retry delivery")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "requeued"})
}

// GET /api/deliveries/message/{ref} — all deliveries for a message
// @Summary Get deliveries for message
// @Description Returns all delivery attempts for a specific message reference
// @Tags deliveries
// @Produce json
// @Param ref path string true "Message reference ID"
// @Success 200 {array} database.Delivery
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/deliveries/message/{ref} [get]
func (s *Server) handleGetMessageDeliveries(w http.ResponseWriter, r *http.Request) {
	ref := chi.URLParam(r, "ref")
	if ref == "" {
		writeError(w, http.StatusBadRequest, "missing message ref")
		return
	}

	deliveries, err := s.db.GetDeliveriesByMessage(ref)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get deliveries")
		return
	}
	writeJSON(w, http.StatusOK, deliveries)
}

// handleGetLoopMetrics returns loop prevention counters.
// @Summary Get loop prevention metrics
// @Description Returns counters for hop limit drops, visited set drops, self-loop drops, and delivery dedups
// @Tags routing
// @Success 200 {object} map[string]int64
// @Router /api/loop-metrics [get]
func (s *Server) handleGetLoopMetrics(w http.ResponseWriter, r *http.Request) {
	if s.dispatcher == nil {
		writeJSON(w, http.StatusOK, map[string]int64{
			"hop_limit_drops":   0,
			"visited_set_drops": 0,
			"self_loop_drops":   0,
			"delivery_dedups":   0,
		})
		return
	}
	writeJSON(w, http.StatusOK, s.dispatcher.LoopMetrics().Snapshot())
}
