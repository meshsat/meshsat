package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/database"
)

// GET /api/deliveries — list deliveries with optional filters
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
func (s *Server) handleGetDeliveryStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.db.DeliveryStatsAll()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get delivery stats")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// GET /api/deliveries/{id} — single delivery detail
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
