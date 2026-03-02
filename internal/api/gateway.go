package api

import (
	"encoding/json"
	"io"
	"net/http"

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
	writeJSON(w, http.StatusOK, sig)
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
