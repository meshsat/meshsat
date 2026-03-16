package api

import (
	"context"
	"net/http"
	"time"
)

type burstStatusResponse struct {
	Pending   int `json:"pending"`
	MaxSize   int `json:"max_size"`
	MaxAgeMin int `json:"max_age_min"`
}

// handleGetBurstStatus returns the current burst queue status.
func (s *Server) handleGetBurstStatus(w http.ResponseWriter, r *http.Request) {
	if s.burstQueue == nil {
		writeJSON(w, http.StatusOK, burstStatusResponse{
			Pending:   0,
			MaxSize:   10,
			MaxAgeMin: 30,
		})
		return
	}
	writeJSON(w, http.StatusOK, burstStatusResponse{
		Pending:   s.burstQueue.Pending(),
		MaxSize:   s.burstQueue.GetMaxSize(),
		MaxAgeMin: int(s.burstQueue.GetMaxAge().Minutes()),
	})
}

// handleFlushBurst forces a flush of the burst queue.
func (s *Server) handleFlushBurst(w http.ResponseWriter, r *http.Request) {
	if s.burstQueue == nil {
		writeError(w, http.StatusServiceUnavailable, "burst queue not available")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	payload, count, err := s.burstQueue.Flush(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "flush failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"flushed": count,
		"bytes":   len(payload),
		"pending": s.burstQueue.Pending(),
	})
}
