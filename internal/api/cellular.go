package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/gateway"
)

func (s *Server) handleGetCellularSignal(w http.ResponseWriter, r *http.Request) {
	// Prefer cached signal from background poller — instant, no serial contention.
	// Only fall through to live AT+CSQ if no cached data exists yet.
	if s.cellTransport != nil {
		if fast, err := s.cellTransport.GetSignalFast(r.Context()); err == nil {
			writeJSON(w, http.StatusOK, fast)
			return
		}
		// No cached data — try live modem with a short timeout
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		signal, err := s.cellTransport.GetSignal(ctx)
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

// handleGetCellularSignalFast returns cached signal from the last poll cycle.
// @Summary      Get cached cellular signal (non-blocking)
// @Description  Returns the last known signal reading without blocking on AT commands.
// @Tags         cellular
// @Produce      json
// @Success      200  {object}  transport.CellSignalInfo
// @Router       /cellular/signal/fast [get]
func (s *Server) handleGetCellularSignalFast(w http.ResponseWriter, r *http.Request) {
	if s.cellTransport != nil {
		signal, err := s.cellTransport.GetSignalFast(r.Context())
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
	// Fall back to DB: combine cell_info + signal_history for a rich status
	result := map[string]interface{}{
		"connected": false,
		"sim_state": "UNKNOWN",
	}
	ci, err := s.db.GetLatestCellInfo()
	if err == nil && ci != nil {
		result["connected"] = true
		result["sim_state"] = "READY"
		result["network_type"] = ci.NetworkType
		result["mcc"] = ci.MCC
		result["mnc"] = ci.MNC
		result["lac"] = ci.LAC
		result["cell_id"] = ci.CellID
		result["rsrp"] = ci.RSRP
		result["rsrq"] = ci.RSRQ
		// Construct operator from MCC+MNC
		if ci.MCC != "" && ci.MNC != "" {
			result["operator"] = ci.MCC + ci.MNC
		}
	}
	// Enrich with latest signal reading (has operator PLMN)
	sig, sigErr := s.db.GetLatestCellularSignal()
	if sigErr == nil && sig != nil {
		if sig.Operator != "" {
			result["operator"] = sig.Operator
		}
		if sig.Technology != "" {
			result["network_type"] = sig.Technology
		}
		result["connected"] = true
		result["sim_state"] = "READY"
	}
	writeJSON(w, http.StatusOK, result)
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

	// Live cell info from modem with short timeout
	if s.cellTransport != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		info, err := s.cellTransport.GetCellInfo(ctx)
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

	// Apply egress transforms (smaz2, encrypt, base64) if configured for cellular_0. [MESHSAT-447]
	outText := req.Text
	if s.transforms != nil {
		if iface, err := s.db.GetInterface("cellular_0"); err == nil && iface.EgressTransforms != "" && iface.EgressTransforms != "[]" {
			transformed, err := s.transforms.ApplyEgress([]byte(req.Text), iface.EgressTransforms)
			if err != nil {
				log.Warn().Err(err).Msg("sms: egress transform failed, sending plaintext")
			} else {
				outText = string(transformed)
				log.Info().Int("plain_len", len(req.Text)).Int("transformed_len", len(outText)).
					Msg("sms: egress transforms applied")
			}
		}
	}

	if err := s.cellTransport.SendSMS(r.Context(), req.To, outText); err != nil {
		s.db.InsertSMSMessage("tx", req.To, req.Text, "failed", time.Now().Unix())
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.db.InsertSMSMessage("tx", req.To, req.Text, "sent", time.Now().Unix())
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// --- Webhook Log ---

// handleCellularAT sends a raw AT command to the cellular modem. Debug only. [MESHSAT-448]
func (s *Server) handleCellularAT(w http.ResponseWriter, r *http.Request) {
	if s.cellTransport == nil {
		writeError(w, http.StatusServiceUnavailable, "cellular transport not available")
		return
	}
	var req struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"` // seconds, default 5
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Command == "" {
		writeError(w, http.StatusBadRequest, "command is required")
		return
	}
	timeout := 5 * time.Second
	if req.Timeout > 0 {
		timeout = time.Duration(req.Timeout) * time.Second
	}
	resp, err := s.cellTransport.ExecAT(r.Context(), req.Command, timeout)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"response": resp, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"response": resp})
}

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

// ─── SIM Card Management ─────────────────────────────────────────────────────

func (s *Server) handleGetSIMCards(w http.ResponseWriter, r *http.Request) {
	cards, err := s.db.GetSIMCards()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if cards == nil {
		cards = []database.SIMCard{}
	}
	writeJSON(w, http.StatusOK, cards)
}

func (s *Server) handleCreateSIMCard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ICCID string `json:"iccid"`
		Label string `json:"label"`
		Phone string `json:"phone"`
		PIN   string `json:"pin"`
		Notes string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.ICCID == "" {
		writeError(w, http.StatusBadRequest, "iccid is required")
		return
	}
	if req.Label == "" {
		req.Label = "SIM " + req.ICCID[len(req.ICCID)-4:]
	}
	id, err := s.db.CreateSIMCard(req.ICCID, req.Label, req.Phone, req.PIN, req.Notes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (s *Server) handleUpdateSIMCard(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		Label string `json:"label"`
		Phone string `json:"phone"`
		PIN   string `json:"pin"`
		Notes string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := s.db.UpdateSIMCard(id, req.Label, req.Phone, req.PIN, req.Notes); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteSIMCard(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.db.DeleteSIMCard(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleGetCurrentSIMICCID(w http.ResponseWriter, r *http.Request) {
	if s.cellTransport == nil {
		writeError(w, http.StatusServiceUnavailable, "cellular transport not available")
		return
	}
	status, err := s.cellTransport.GetStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"iccid":     status.ICCID,
		"sim_state": status.SIMState,
		"imei":      status.IMEI,
	})
}
