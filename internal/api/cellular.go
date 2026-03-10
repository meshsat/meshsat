package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/database"
	"meshsat/internal/gateway"
)

func (s *Server) handleGetCellularSignal(w http.ResponseWriter, r *http.Request) {
	// Try live modem first
	if s.cellTransport != nil {
		signal, err := s.cellTransport.GetSignal(r.Context())
		if err == nil {
			writeJSON(w, http.StatusOK, signal)
			return
		}
	}
	// Fall back to latest DB reading
	point, err := s.db.GetLatestCellularSignal()
	if err != nil || point == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"bars": 0, "dbm": -113, "technology": "", "assessment": "none"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"bars":       point.Bars,
		"dbm":        point.DBm,
		"technology": point.Technology,
		"assessment": signalAssessment(point.Bars),
		"timestamp":  time.Unix(point.Timestamp, 0).UTC().Format(time.RFC3339),
	})
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
	// Try live modem first
	if s.cellTransport != nil {
		status, err := s.cellTransport.GetStatus(r.Context())
		if err == nil {
			writeJSON(w, http.StatusOK, status)
			return
		}
	}
	// Fall back to DB cell info for basic status
	ci, err := s.db.GetLatestCellInfo()
	if err == nil && ci != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"connected":    true,
			"network_type": ci.NetworkType,
			"sim_state":    "READY",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"connected": false,
		"sim_state": "UNKNOWN",
	})
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

	// Log inbound webhook
	_ = s.db.InsertWebhookLog("inbound", "/api/webhooks/cellular/inbound", "POST", 200, req.Text, "", "")

	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}

// --- SIM PIN Unlock ---

func (s *Server) handleSubmitCellularPIN(w http.ResponseWriter, r *http.Request) {
	if s.cellTransport == nil {
		writeError(w, http.StatusServiceUnavailable, "cellular transport not available")
		return
	}

	var req struct {
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if len(req.PIN) < 4 || len(req.PIN) > 8 {
		writeError(w, http.StatusBadRequest, "PIN must be 4-8 digits")
		return
	}

	if err := s.cellTransport.UnlockPIN(r.Context(), req.PIN); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "unlocked"})
}

// --- Cell Info ---

func (s *Server) handleGetCellInfo(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{}

	// Live cell info from modem
	if s.cellTransport != nil {
		info, err := s.cellTransport.GetCellInfo(r.Context())
		if err == nil && info != nil {
			resp["live"] = info
		}
	}

	// Latest persisted cell info from DB
	dbInfo, err := s.db.GetLatestCellInfo()
	if err == nil && dbInfo != nil {
		resp["latest"] = dbInfo
	}

	writeJSON(w, http.StatusOK, resp)
}

// --- Cell Broadcast Alerts ---

func (s *Server) handleGetCellBroadcasts(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 200 {
			limit = l
		}
	}
	unackedOnly := r.URL.Query().Get("unacked_only") == "true"

	alerts, err := s.db.GetCellBroadcasts(limit, unackedOnly)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if alerts == nil {
		alerts = []database.CellBroadcast{}
	}
	writeJSON(w, http.StatusOK, alerts)
}

func (s *Server) handleAckCellBroadcast(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.db.AckCellBroadcast(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "acknowledged"})
}

// --- SMS History ---

func (s *Server) handleGetSMSMessages(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 500 {
			limit = l
		}
	}
	offsetStr := r.URL.Query().Get("offset")
	offset := 0
	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}

	msgs, err := s.db.GetSMSMessages(limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if msgs == nil {
		msgs = []database.SMSMessageRecord{}
	}
	writeJSON(w, http.StatusOK, msgs)
}

// --- SMS Contacts ---

func (s *Server) handleGetSMSContacts(w http.ResponseWriter, r *http.Request) {
	contacts, err := s.db.GetSMSContacts()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if contacts == nil {
		contacts = []database.SMSContact{}
	}
	writeJSON(w, http.StatusOK, contacts)
}

func (s *Server) handleCreateSMSContact(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Phone   string `json:"phone"`
		Notes   string `json:"notes"`
		AutoFwd bool   `json:"auto_fwd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Name == "" || req.Phone == "" {
		writeError(w, http.StatusBadRequest, "name and phone are required")
		return
	}

	id, err := s.db.CreateSMSContact(req.Name, req.Phone, req.Notes, req.AutoFwd)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (s *Server) handleUpdateSMSContact(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var req struct {
		Name    string `json:"name"`
		Phone   string `json:"phone"`
		Notes   string `json:"notes"`
		AutoFwd bool   `json:"auto_fwd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Name == "" || req.Phone == "" {
		writeError(w, http.StatusBadRequest, "name and phone are required")
		return
	}

	if err := s.db.UpdateSMSContact(id, req.Name, req.Phone, req.Notes, req.AutoFwd); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteSMSContact(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	if err := s.db.DeleteSMSContact(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- Send SMS ---

func (s *Server) handleSendSMS(w http.ResponseWriter, r *http.Request) {
	if s.cellTransport == nil {
		writeError(w, http.StatusServiceUnavailable, "cellular transport not available")
		return
	}

	var req struct {
		To   string `json:"to"`
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.To == "" || req.Text == "" {
		writeError(w, http.StatusBadRequest, "to and text are required")
		return
	}

	if err := s.cellTransport.SendSMS(r.Context(), req.To, req.Text); err != nil {
		s.db.InsertSMSMessage("tx", req.To, req.Text, "failed", time.Now().Unix())
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.db.InsertSMSMessage("tx", req.To, req.Text, "sent", time.Now().Unix())
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// --- Webhook Log ---

func (s *Server) handleGetWebhookLog(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit := 100
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 1000 {
			limit = l
		}
	}

	entries, err := s.db.GetWebhookLog(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entries == nil {
		entries = []database.WebhookLogEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}
