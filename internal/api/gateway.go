package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// handleGetIridiumSignalFast returns a cached Iridium signal reading (AT+CSQF, ~100ms).
// @Summary Get Iridium signal (fast)
// @Description Returns cached satellite signal bars using AT+CSQF (non-blocking)
// @Tags iridium
// @Success 200 {object} transport.SignalInfo
// @Failure 503 {object} map[string]string "unavailable"
// @Router /api/iridium/signal/fast [get]
func (s *Server) handleGetIridiumSignalFast(w http.ResponseWriter, r *http.Request) {
	if s.gwManager == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway manager not available")
		return
	}
	sig, err := s.gwManager.GetIridiumSignalFast(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	// When instantaneous reading is 0 (common between passes), fall back to
	// the most recent non-zero signal recorded in the last 10 minutes.
	if sig.Bars == 0 && s.db != nil {
		if latest, err := s.db.GetLatestSignal("iridium"); err == nil && latest != nil {
			sig.Bars = int(latest.Value)
			sig.Assessment = signalAssessment(sig.Bars)
		}
	}

	writeJSON(w, http.StatusOK, sig)
}

func signalAssessment(bars int) string {
	switch {
	case bars >= 4:
		return "good"
	case bars >= 2:
		return "fair"
	case bars >= 1:
		return "poor"
	default:
		return "none"
	}
}

// handleGetIridiumSignal returns a fresh Iridium signal reading (blocking AT+CSQ, up to 60s).
// @Summary Get Iridium signal (blocking)
// @Description Returns fresh satellite signal bars using AT+CSQ (blocks until modem responds)
// @Tags iridium
// @Success 200 {object} transport.SignalInfo
// @Failure 503 {object} map[string]string "unavailable"
// @Router /api/iridium/signal [get]
func (s *Server) handleGetIridiumSignal(w http.ResponseWriter, r *http.Request) {
	if s.gwManager == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway manager not available")
		return
	}
	sig, err := s.gwManager.GetIridiumSignal(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sig)
}

// handleGetGateways returns the status of all configured gateways.
// @Summary List all gateways
// @Description Returns status and config of all gateways (MQTT, Iridium)
// @Tags gateways
// @Success 200 {object} map[string]interface{} "gateways"
// @Router /api/gateways [get]
func (s *Server) handleGetGateways(w http.ResponseWriter, r *http.Request) {
	if s.gwManager == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"gateways": []interface{}{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"gateways": s.gwManager.GetStatus(),
	})
}

// handleGetGateway returns the status of a specific gateway.
// @Summary Get gateway status
// @Description Returns status and config for a specific gateway type
// @Tags gateways
// @Param type path string true "Gateway type (mqtt, iridium)"
// @Success 200 {object} map[string]interface{}
// @Failure 404 {object} map[string]string "not found"
// @Router /api/gateways/{type} [get]
func (s *Server) handleGetGateway(w http.ResponseWriter, r *http.Request) {
	gwType := chi.URLParam(r, "type")
	if s.gwManager == nil {
		writeError(w, http.StatusNotFound, "gateway manager not available")
		return
	}
	status, err := s.gwManager.GetSingleStatus(gwType)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// handlePutGateway creates or updates a gateway configuration.
// @Summary Configure gateway
// @Description Create or update gateway configuration
// @Tags gateways
// @Param type path string true "Gateway type (mqtt, iridium)"
// @Param body body object true "Gateway config with enabled flag"
// @Success 200 {object} map[string]string "ok"
// @Failure 400 {object} map[string]string "error"
// @Router /api/gateways/{type} [put]
func (s *Server) handlePutGateway(w http.ResponseWriter, r *http.Request) {
	gwType := chi.URLParam(r, "type")
	if s.gwManager == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway manager not available")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}

	var req struct {
		Enabled bool            `json:"enabled"`
		Config  json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "parse body: "+err.Error())
		return
	}

	configStr := "{}"
	if len(req.Config) > 0 {
		configStr = string(req.Config)
	}

	if err := s.gwManager.Configure(r.Context(), gwType, req.Enabled, configStr); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleDeleteGateway removes a gateway configuration.
// @Summary Delete gateway
// @Description Stop and remove a gateway configuration
// @Tags gateways
// @Param type path string true "Gateway type (mqtt, iridium)"
// @Success 200 {object} map[string]string "ok"
// @Failure 400 {object} map[string]string "error"
// @Router /api/gateways/{type} [delete]
func (s *Server) handleDeleteGateway(w http.ResponseWriter, r *http.Request) {
	gwType := chi.URLParam(r, "type")
	if s.gwManager == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway manager not available")
		return
	}

	if err := s.gwManager.Delete(gwType); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleStartGateway starts a configured gateway.
// @Summary Start gateway
// @Description Start a configured gateway
// @Tags gateways
// @Param type path string true "Gateway type (mqtt, iridium)"
// @Success 200 {object} map[string]string "ok"
// @Failure 400 {object} map[string]string "error"
// @Router /api/gateways/{type}/start [post]
func (s *Server) handleStartGateway(w http.ResponseWriter, r *http.Request) {
	gwType := chi.URLParam(r, "type")
	if s.gwManager == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway manager not available")
		return
	}

	if err := s.gwManager.StartGateway(r.Context(), gwType); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleStopGateway stops a running gateway.
// @Summary Stop gateway
// @Description Stop a running gateway
// @Tags gateways
// @Param type path string true "Gateway type (mqtt, iridium)"
// @Success 200 {object} map[string]string "ok"
// @Failure 400 {object} map[string]string "error"
// @Router /api/gateways/{type}/stop [post]
func (s *Server) handleStopGateway(w http.ResponseWriter, r *http.Request) {
	gwType := chi.URLParam(r, "type")
	if s.gwManager == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway manager not available")
		return
	}

	if err := s.gwManager.StopGateway(gwType); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleTestGateway tests connectivity for a gateway.
// @Summary Test gateway
// @Description Test connectivity for a configured gateway
// @Tags gateways
// @Param type path string true "Gateway type (mqtt, iridium)"
// @Success 200 {object} map[string]string "ok"
// @Failure 400 {object} map[string]string "error"
// @Router /api/gateways/{type}/test [post]
func (s *Server) handleTestGateway(w http.ResponseWriter, r *http.Request) {
	gwType := chi.URLParam(r, "type")
	if s.gwManager == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway manager not available")
		return
	}

	if err := s.gwManager.TestGateway(gwType); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"status": "failed",
			"error":  err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// =============================================================================
// Iridium Queue — offline compose and priority management
// =============================================================================

// handleGetIridiumQueue returns all pending/expired DLQ entries.
// @Summary Get Iridium outbound queue
// @Description Returns all non-sent, non-cancelled messages in the DLQ
// @Tags iridium
// @Success 200 {array} database.DeadLetter
// @Failure 503 {object} map[string]string "unavailable"
// @Router /api/iridium/queue [get]
func (s *Server) handleGetIridiumQueue(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	items, err := s.db.GetDeadLetterQueue()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"queue": items})
}

// handleEnqueueIridiumMessage adds a user-composed message to the DLQ for opportunistic send.
// @Summary Queue Iridium message for later send
// @Description Enqueues a message in the DLQ; sent when signal becomes available
// @Tags iridium
// @Param body body object true "message body: {message: string, priority: 0|1|2}"
// @Success 201 {object} map[string]string "queued"
// @Failure 400 {object} map[string]string "error"
// @Failure 503 {object} map[string]string "unavailable"
// @Router /api/iridium/queue [post]
func (s *Server) handleEnqueueIridiumMessage(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 340)) // Iridium SBD max payload
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}

	var req struct {
		Message  string `json:"message"`
		Priority int    `json:"priority"` // 0=critical, 1=normal, 2=low
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "parse body: "+err.Error())
		return
	}
	if req.Message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}
	if req.Priority < 0 || req.Priority > 2 {
		req.Priority = 1
	}
	if len(req.Message) > 340 {
		writeError(w, http.StatusBadRequest, "message exceeds 340-byte SBD limit")
		return
	}

	if err := s.db.InsertDirectDeadLetter([]byte(req.Message), req.Priority, 5, req.Message); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"status": "queued"})
}

// handleCancelQueueItem cancels a pending DLQ entry.
// @Summary Cancel a queued Iridium message
// @Description Marks the queued message as cancelled; it will not be retried
// @Tags iridium
// @Param id path int true "DLQ entry ID"
// @Success 200 {object} map[string]string "cancelled"
// @Failure 400 {object} map[string]string "error"
// @Failure 503 {object} map[string]string "unavailable"
// @Router /api/iridium/queue/{id}/cancel [post]
func (s *Server) handleCancelQueueItem(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.db.CancelDeadLetter(id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// handleDeleteQueueItem permanently deletes a DLQ entry.
// @Summary Delete a queued Iridium message
// @Description Permanently removes a dead letter entry from the database
// @Tags iridium
// @Param id path int true "DLQ entry ID"
// @Success 200 {object} map[string]string "deleted"
// @Failure 400 {object} map[string]string "error"
// @Failure 503 {object} map[string]string "unavailable"
// @Router /api/iridium/queue/{id} [delete]
func (s *Server) handleDeleteQueueItem(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.db.DeleteDeadLetter(id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleSetQueuePriority updates the priority of a pending DLQ entry.
// @Summary Set priority for a queued Iridium message
// @Description Changes the send priority for a pending queued message
// @Tags iridium
// @Param id path int true "DLQ entry ID"
// @Param body body object true "{priority: 0|1|2}"
// @Success 200 {object} map[string]string "ok"
// @Failure 400 {object} map[string]string "error"
// @Failure 503 {object} map[string]string "unavailable"
// @Router /api/iridium/queue/{id}/priority [post]
func (s *Server) handleSetQueuePriority(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var req struct {
		Priority int `json:"priority"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "parse body: "+err.Error())
		return
	}

	if err := s.db.SetDeadLetterPriority(id, req.Priority); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
