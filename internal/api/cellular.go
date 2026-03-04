package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/gateway"
)

func (s *Server) handleGetCellularSignal(w http.ResponseWriter, r *http.Request) {
	if s.cellTransport == nil {
		writeError(w, http.StatusServiceUnavailable, "cellular transport not available")
		return
	}
	signal, err := s.cellTransport.GetSignal(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, signal)
}

func (s *Server) handleGetCellularSignalHistory(w http.ResponseWriter, r *http.Request) {
	hoursStr := r.URL.Query().Get("hours")
	hours := 24
	if hoursStr != "" {
		if h, err := strconv.Atoi(hoursStr); err == nil && h > 0 && h <= 720 {
			hours = h
		}
	}

	limitStr := r.URL.Query().Get("limit")
	limit := 500
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	to := time.Now().Unix()
	from := to - int64(hours*3600)

	points, err := s.db.GetCellularSignalHistory(from, to, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if points == nil {
		points = []database.CellularSignalPoint{}
	}
	writeJSON(w, http.StatusOK, points)
}

func (s *Server) handleGetCellularStatus(w http.ResponseWriter, r *http.Request) {
	if s.cellTransport == nil {
		writeError(w, http.StatusServiceUnavailable, "cellular transport not available")
		return
	}
	status, err := s.cellTransport.GetStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleCellularDataConnect(w http.ResponseWriter, r *http.Request) {
	if s.gwManager == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway manager not available")
		return
	}

	var req struct {
		APN string `json:"apn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.APN == "" {
		writeError(w, http.StatusBadRequest, "apn is required")
		return
	}

	if err := s.gwManager.ConnectCellularData(r.Context(), req.APN); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "connected"})
}

func (s *Server) handleCellularDataDisconnect(w http.ResponseWriter, r *http.Request) {
	if s.gwManager == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway manager not available")
		return
	}
	if err := s.gwManager.DisconnectCellularData(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "disconnected"})
}

func (s *Server) handleCellularDataStatus(w http.ResponseWriter, r *http.Request) {
	if s.gwManager == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway manager not available")
		return
	}
	status, err := s.gwManager.GetCellularDataStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleGetDynDNSStatus(w http.ResponseWriter, r *http.Request) {
	if s.gwManager == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway manager not available")
		return
	}
	updater := s.gwManager.GetDynDNSUpdater()
	if updater == nil {
		writeJSON(w, http.StatusOK, gateway.DynDNSStatus{Enabled: false})
		return
	}
	writeJSON(w, http.StatusOK, updater.Status())
}

func (s *Server) handleDynDNSForceUpdate(w http.ResponseWriter, r *http.Request) {
	if s.gwManager == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway manager not available")
		return
	}
	updater := s.gwManager.GetDynDNSUpdater()
	if updater == nil {
		writeError(w, http.StatusNotFound, "DynDNS not enabled")
		return
	}
	if err := updater.ForceUpdate(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updater.Status())
}

func (s *Server) handleWebhookCellularInbound(w http.ResponseWriter, r *http.Request) {
	if s.gwManager == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway manager not available")
		return
	}

	cgw := s.gwManager.GetCellularGateway()
	if cgw == nil {
		writeError(w, http.StatusNotFound, "cellular gateway not running")
		return
	}

	// Validate webhook secret if configured
	cfg := cgw.Config()
	if cfg.WebhookInSecret != "" {
		secret := r.Header.Get("X-Webhook-Secret")
		if secret != cfg.WebhookInSecret {
			writeError(w, http.StatusUnauthorized, "invalid webhook secret")
			return
		}
	}

	var req struct {
		Text    string `json:"text"`
		To      string `json:"to,omitempty"`
		Channel int    `json:"channel,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}

	cgw.ForwardWebhookInbound(gateway.InboundMessage{
		Text:    req.Text,
		To:      req.To,
		Channel: req.Channel,
		Source:  "cellular",
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}
