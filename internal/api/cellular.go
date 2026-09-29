package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/gateway"
)

// @Summary Get cellular signal strength
// @Description Returns live or cached cellular signal reading with fallback to DB
// @Tags cellular
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/cellular/signal [get]
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

// @Summary Get cellular signal history
// @Description Returns historical cellular signal strength readings
// @Tags cellular
// @Produce json
// @Param hours query integer false "History window in hours (default: 24, max: 720)"
// @Param limit query integer false "Max number of readings (default: 500)"
// @Success 200 {array} database.CellularSignalPoint
// @Failure 500 {object} map[string]string
// @Router /api/cellular/signal/history [get]
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

// @Summary Get cellular modem status
// @Description Returns cellular modem connection status, SIM state, operator, and network info, plus health_state and health_detail from the device health watchdog (connected is only the serial link) [MESHSAT-1064]
// @Tags cellular
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/cellular/status [get]
func (s *Server) handleGetCellularStatus(w http.ResponseWriter, r *http.Request) {
	// Try live modem first
	if s.cellTransport != nil {
		status, err := s.cellTransport.GetStatus(r.Context())
		if err == nil {
			if state, detail, ok := s.cellularHealth(); ok {
				status.HealthState, status.HealthDetail = state, detail
			}
			if s.smsBudget != nil {
				status.SMSBundle = s.smsBudget.Status() // [MESHSAT-1161]
			}
			writeJSON(w, http.StatusOK, status)
			return
		}
	}
	// Fall back to DB: combine cell_info + signal_history for a rich status
	result := map[string]interface{}{
		"connected": false,
		"sim_state": "UNKNOWN",
	}
	if s.smsBudget != nil {
		if st := s.smsBudget.Status(); st != nil {
			result["sms_bundle"] = st
		}
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
	if state, detail, ok := s.cellularHealth(); ok {
		result["health_state"] = state
		result["health_detail"] = detail
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

// @Summary Connect cellular data
// @Description Activates cellular data connection with the specified APN
// @Tags cellular
// @Accept json
// @Produce json
// @Param body body object true "APN config" example({"apn":"internet"})
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/cellular/data/connect [post]
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

// @Summary Disconnect cellular data
// @Description Deactivates cellular data connection
// @Tags cellular
// @Produce json
// @Success 200 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/cellular/data/disconnect [post]
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

// @Summary Get cellular data status
// @Description Returns current cellular data connection status (IP, APN, bytes transferred)
// @Tags cellular
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/cellular/data/status [get]
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

// @Summary Get DynDNS status
// @Description Returns the current DynDNS updater status including last update time and IP
// @Tags cellular
// @Produce json
// @Success 200 {object} gateway.DynDNSStatus
// @Failure 503 {object} map[string]string
// @Router /api/cellular/dyndns/status [get]
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

// @Summary Force DynDNS update
// @Description Triggers an immediate DynDNS record update
// @Tags cellular
// @Produce json
// @Success 200 {object} gateway.DynDNSStatus
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/cellular/dyndns/update [post]
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

// @Summary Cellular inbound webhook
// @Description Receives inbound messages via cellular webhook and forwards to mesh
// @Tags webhooks
// @Accept json
// @Produce json
// @Param body body object true "Inbound message" example({"text":"hello","to":"","channel":0})
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/webhooks/cellular/inbound [post]
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

// @Summary Submit SIM PIN
// @Description Unlocks the SIM card by submitting the PIN code
// @Tags cellular
// @Accept json
// @Produce json
// @Param body body object true "PIN" example({"pin":"1234"})
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/cellular/pin [post]
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

// @Summary Get cell tower info
// @Description Returns live and persisted cell tower information (MCC, MNC, LAC, Cell ID)
// @Tags cellular
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/cellular/info [get]
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

// @Summary List cell broadcast alerts
// @Description Returns cell broadcast emergency alerts (NL-Alert, EU-Alert)
// @Tags cellular
// @Produce json
// @Param limit query integer false "Max results (default: 50, max: 200)"
// @Param unacked_only query boolean false "Only unacknowledged alerts"
// @Success 200 {array} database.CellBroadcast
// @Failure 500 {object} map[string]string
// @Router /api/cellular/broadcasts [get]
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

// @Summary Acknowledge cell broadcast
// @Description Marks a cell broadcast alert as acknowledged
// @Tags cellular
// @Produce json
// @Param id path integer true "Broadcast alert ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/cellular/broadcasts/{id}/ack [post]
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

// @Summary List SMS messages
// @Description Returns SMS message history with pagination. text is the words: an SMS that came sealed is shown opened (by the sender's chat key, the wildcard's, or the SMS link's chain) and one that went sealed as typed; encrypted says which, for a chat bubble's lock.
// @Tags cellular
// @Produce json
// @Param limit query integer false "Max results (default: 50, max: 500)"
// @Param offset query integer false "Pagination offset (default: 0)"
// @Success 200 {array} database.SMSMessageRecord
// @Failure 500 {object} map[string]string
// @Router /api/cellular/sms [get]
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

// --- SMS Contacts (DEPRECATED — see MESHSAT-542) ---

// setSMSContactsDeprecationHeaders signals the upcoming retirement of
// the legacy /api/cellular/contacts* endpoints. Clients should move
// to /api/contacts (with ?kind=sms on the GET). Headers follow
// RFC 8594 (Sunset) and the IETF Deprecation header draft; clients
// that honour them surface a visible warning to operators. The
// legacy endpoints continue to function until the v50 migration
// drops the sms_contacts table. [MESHSAT-542]
func setSMSContactsDeprecationHeaders(w http.ResponseWriter) {
	w.Header().Set("Deprecation", "true")
	w.Header().Set("Sunset", "Wed, 01 Jul 2026 00:00:00 GMT")
	w.Header().Set("Link", `</api/contacts?kind=sms>; rel="successor-version"`)
	w.Header().Set("X-Meshsat-Deprecation", "Use /api/contacts?kind=sms (GET) and /api/contacts (POST/PUT/DELETE). Legacy /api/cellular/contacts* removed in v50 (MESHSAT-542).")
}

// @Summary List SMS contacts (deprecated)
// @Description DEPRECATED — use /api/contacts?kind=sms. Kept for one
// @Description release; removed in v50 (MESHSAT-542). Responses carry
// @Description Deprecation + Sunset + Link rel="successor-version"
// @Description headers.
// @Tags cellular
// @Produce json
// @Success 200 {array} database.SMSContact
// @Failure 500 {object} map[string]string
// @Router /api/cellular/contacts [get]
// @Deprecated
func (s *Server) handleGetSMSContacts(w http.ResponseWriter, r *http.Request) {
	setSMSContactsDeprecationHeaders(w)
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

// @Summary Create SMS contact (deprecated)
// @Description DEPRECATED — use POST /api/contacts. See MESHSAT-542.
// @Tags cellular
// @Accept json
// @Produce json
// @Param body body object true "Contact" example({"name":"Alice","phone":"+31612345678","notes":"","auto_fwd":false})
// @Success 201 {object} map[string]int64
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/cellular/contacts [post]
// @Deprecated
func (s *Server) handleCreateSMSContact(w http.ResponseWriter, r *http.Request) {
	setSMSContactsDeprecationHeaders(w)
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

// @Summary Update SMS contact (deprecated)
// @Description DEPRECATED — use PUT /api/contacts/{id}. See MESHSAT-542.
// @Tags cellular
// @Accept json
// @Produce json
// @Param id path integer true "Contact ID"
// @Param body body object true "Contact"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/cellular/contacts/{id} [put]
// @Deprecated
func (s *Server) handleUpdateSMSContact(w http.ResponseWriter, r *http.Request) {
	setSMSContactsDeprecationHeaders(w)
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

// @Summary Delete SMS contact (deprecated)
// @Description DEPRECATED — use DELETE /api/contacts/{id}. See MESHSAT-542.
// @Tags cellular
// @Produce json
// @Param id path integer true "Contact ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/cellular/contacts/{id} [delete]
// @Deprecated
func (s *Server) handleDeleteSMSContact(w http.ResponseWriter, r *http.Request) {
	setSMSContactsDeprecationHeaders(w)
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

// smsSendRequest is the body of POST /api/cellular/sms/send.
type smsSendRequest struct {
	To   string `json:"to"`
	Text string `json:"text"`
	// Plain sends the text exactly as given (not even trimmed): no chat key,
	// no transform chain (no encryption, compression or base64), no
	// attribution; the SMS history keeps it with encrypted false. For an SOS
	// or an alarm test to an emergency contact, which MeshSat Android sends
	// as plain text whatever its encryption settings. A kit's USB modem (AT
	// text mode) still gets the GSM clean-up, since it cannot carry every
	// character; ModemManager sends the text as it is. A plain text with a
	// control character other than a line break is refused (plainTextError).
	// Default false: as before.
	Plain bool `json:"plain"`
}

// plainTextError says why a plain text cannot go as given, nil when it can.
// A plain text skips the processing every other SMS gets, so whatever it
// holds reaches the modem: on a kit's USB modem, which takes the text in AT
// text mode, Ctrl-Z (0x1A) ends the SMS early and what follows runs as AT
// commands, and ESC (0x1B) drops the SMS while the modem still answers OK.
// Line breaks (\n, \r) are the only control characters a plain text keeps;
// the others (C0, DEL, C1) are refused with the first one named.
func plainTextError(text string) error {
	for i, r := range text {
		if r != '\n' && r != '\r' && unicode.IsControl(r) {
			return fmt.Errorf("plain text must not contain control characters: U+%04X at byte %d (only line breaks, \\n and \\r, may be in it)", r, i)
		}
	}
	return nil
}

// @Summary Send SMS message
// @Description Queues an SMS on cellular_0 through the delivery ledger, like every other send on the kit: a number with a chat key of its own (PUT /api/keys/sms/{number}), or the wildcard's (sms:*), gets the text sealed with that key as MeshSat Android seals it, whether or not the link encrypts; otherwise the interface's egress transforms (compression, encryption) apply; a plaintext peer such as the Hub gets clear text. The send is retried, counted against the SMS bundle and shown in the delivery queue; the SMS history keeps the words that were sent, marked encrypted when they went sealed. A message that would not fit the kit's max_sms_segments on air is refused rather than cut, because a cut ciphertext cannot be read.
// @Description plain true sends the text exactly as given (an SOS or an alarm test to an emergency contact): no chat key, no transform chain, no attribution, not trimmed; the SMS history keeps it with encrypted false. A kit's USB modem (AT text mode) cannot carry every character, so there it still gets the GSM clean-up ("[" becomes "(", "€" "EUR", "±" "+/-"); ModemManager on a phone sends it exactly. A plain text with a control character other than a line break (\n, \r) is refused with 400. Default false, as above.
// @Tags cellular
// @Accept json
// @Produce json
// @Param body body smsSendRequest true "SMS" example({"to":"+31612345678","text":"Hello","plain":false})
// @Success 202 {object} map[string]interface{} "status queued, delivery_id, plain"
// @Failure 400 {object} map[string]string "to or text missing, a plain text with a control character, or too long for max_sms_segments"
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/cellular/sms/send [post]
func (s *Server) handleSendSMS(w http.ResponseWriter, r *http.Request) {
	if s.cellTransport == nil {
		writeError(w, http.StatusServiceUnavailable, "cellular transport not available")
		return
	}
	if s.dispatcher == nil {
		writeError(w, http.StatusServiceUnavailable, "delivery pipeline not available")
		return
	}

	var req smsSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	req.To = strings.TrimSpace(req.To)
	if !req.Plain {
		req.Text = strings.TrimSpace(req.Text)
	}
	if req.To == "" || strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, "to and text are required")
		return
	}
	if req.Plain {
		if err := plainTextError(req.Text); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// This handler used to encrypt and send straight through the modem. It
	// ignored plaintext_peers, so the Hub got ciphertext it could not read,
	// and it skipped the ledger: no retry, no queue entry, and the request
	// waited on the modem. It now takes the same path as every other send.
	if onAir, limit, err := s.smsOnAirLength(req.To, req.Text, req.Plain); err == nil && limit > 0 && onAir > limit {
		writeError(w, http.StatusBadRequest, fmt.Sprintf(
			"message too long: %d characters on air, this kit sends at most %d (max_sms_segments); shorten it", onAir, limit))
		return
	}
	opts := engine.DirectSendOptions{Destination: req.To}
	if req.Plain {
		opts.Class = database.DeliveryClassPlain
	}
	id, _, err := s.dispatcher.QueueDirectSendTo("cellular_0", req.Text, opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]interface{}{"status": "queued", "delivery_id": id, "plain": req.Plain})
}

// smsOnAirLength gathers what the delivery worker will use for this send
// (the cellular gateway's config and cellular_0's egress chain) and predicts
// the on-air length with onAirSMSLength. A plain send is the text itself.
func (s *Server) smsOnAirLength(number, text string, plain bool) (onAir, limit int, err error) {
	var cfg gateway.CellularConfig
	if s.gwManager != nil {
		if cg, ok := s.gwManager.GatewayByInterfaceID("cellular_0").(*gateway.CellularGateway); ok && cg != nil {
			cfg = cg.Config()
		}
	}
	if plain {
		if cfg.MaxSMSSegments <= 0 {
			return 0, 0, nil
		}
		return len(text), 160 * cfg.MaxSMSSegments, nil
	}
	var egress func([]byte) ([]byte, error)
	if s.transforms != nil && s.db != nil {
		chain := ""
		if iface, gerr := s.db.GetInterface("cellular_0"); gerr == nil {
			chain = iface.EgressTransforms
		}
		if chain != "" && chain != "[]" {
			egress = func(b []byte) ([]byte, error) { return s.transforms.ApplyEgress(b, chain) }
		}
		if onAir, limit, sealed, err := sealedSMSLength(cfg, s.transforms, chain, number, text); sealed || err != nil {
			return onAir, limit, err
		}
	}
	return onAirSMSLength(cfg, egress, number, text)
}

// sealedSMSLength predicts the on-air length of text to a number with a chat
// key of its own, or the wildcard's: sealed with it whatever the chain says,
// as the delivery worker seals it. sealed is false when no chat key applies
// (none, a plaintext peer, or no segment limit to check against).
func sealedSMSLength(cfg gateway.CellularConfig, tp *engine.TransformPipeline, chain, number, text string) (onAir, limit int, sealed bool, err error) {
	if tp == nil || cfg.MaxSMSSegments <= 0 || cfg.IsPlaintextPeer(number) {
		return 0, 0, false, nil
	}
	limit = 160 * cfg.MaxSMSSegments
	hexKey, _, found, err := tp.SMSChatKey(number)
	if err != nil || !found {
		return 0, limit, false, err
	}
	wire, err := tp.SealChatSMS([]byte(text), hexKey, chain)
	if err != nil {
		return 0, limit, false, err
	}
	return len(wire), limit, true, nil
}

// onAirSMSLength predicts how long text is on air to number, with the same
// choice the delivery worker makes: clear text for a plaintext peer, else the
// egress chain plus the protocol version byte, else the sanitised text.
// limit is 160 characters per allowed segment; 0 means unknown, no check.
func onAirSMSLength(cfg gateway.CellularConfig, egress func([]byte) ([]byte, error), number, text string) (onAir, limit int, err error) {
	if cfg.MaxSMSSegments <= 0 {
		return 0, 0, nil
	}
	limit = 160 * cfg.MaxSMSSegments
	if cfg.IsPlaintextPeer(number) || egress == nil {
		return len(gateway.SanitizeSMSText(text)), limit, nil
	}
	out, err := egress([]byte(text))
	if err != nil {
		return 0, limit, err
	}
	return len(out) + 1, limit, nil // + protocol version byte
}

// smsBundleResponse is the bundle counter plus the reminder number, which
// stays off /cellular/status (the booth panel polls that one). [MESHSAT-1161]
func (s *Server) smsBundleResponse() map[string]interface{} {
	out := map[string]interface{}{"configured": false, "alert_number": s.smsBudget.AlertNumber()}
	if st := s.smsBudget.Status(); st != nil {
		out["configured"] = true
		out["bundle"] = st
	}
	return out
}

// @Summary Get the prepaid SMS bundle counter
// @Description Segments the network accepted since the last top-up against the bundle size, the warning threshold and the top-up reminder number [MESHSAT-1161]
// @Tags cellular
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 503 {object} map[string]string "no cellular modem"
// @Router /api/cellular/bundle [get]
func (s *Server) handleGetSMSBundle(w http.ResponseWriter, r *http.Request) {
	if s.smsBudget == nil {
		writeError(w, http.StatusServiceUnavailable, "no cellular modem")
		return
	}
	writeJSON(w, http.StatusOK, s.smsBundleResponse())
}

// @Summary Record a top-up or tune the prepaid SMS bundle counter
// @Description size (with optional sent, default 0) records a top-up and re-arms the warning and the reminder; warn_at and alert_number (E.164) change the threshold and the reminder number; each field is optional [MESHSAT-1161]
// @Tags cellular
// @Accept json
// @Produce json
// @Param body body object true "{size?, sent?, warn_at?, alert_number?}"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]string "invalid value"
// @Failure 503 {object} map[string]string "no cellular modem"
// @Router /api/cellular/bundle [put]
func (s *Server) handleSetSMSBundle(w http.ResponseWriter, r *http.Request) {
	if s.smsBudget == nil {
		writeError(w, http.StatusServiceUnavailable, "no cellular modem")
		return
	}
	var req struct {
		Size        *int    `json:"size"`
		Sent        *int    `json:"sent"`
		WarnAt      *int    `json:"warn_at"`
		AlertNumber *string `json:"alert_number"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Size == nil && req.WarnAt == nil && req.AlertNumber == nil {
		writeError(w, http.StatusBadRequest, "nothing to change: give size, warn_at or alert_number")
		return
	}
	// One call under one lock: the thresholds are evaluated after every
	// field is in place, never between the number and the size.
	if err := s.smsBudget.Apply(req.Size, req.Sent, req.WarnAt, req.AlertNumber); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Size != nil {
		log.Info().Int("size", *req.Size).Msg("sms budget: top-up recorded via API")
	}
	writeJSON(w, http.StatusOK, s.smsBundleResponse())
}

// --- Webhook Log ---

// handleCellularAT sends a raw AT command to the cellular modem. Debug only. [MESHSAT-448]
// @Summary Send raw AT command
// @Description Sends a raw AT command to the cellular modem for debugging
// @Tags cellular
// @Accept json
// @Produce json
// @Param body body object true "AT command" example({"command":"AT+CSQ","timeout":5})
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/cellular/at [post]
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

// @Summary Get webhook log
// @Description Returns recent webhook invocation log entries
// @Tags webhooks
// @Produce json
// @Param limit query integer false "Max results (default: 100, max: 1000)"
// @Success 200 {array} database.WebhookLogEntry
// @Failure 500 {object} map[string]string
// @Router /api/webhooks/log [get]
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

// @Summary List SIM cards
// @Description Returns all registered SIM cards
// @Tags cellular
// @Produce json
// @Success 200 {array} database.SIMCard
// @Failure 500 {object} map[string]string
// @Router /api/cellular/sim-cards [get]
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

// @Summary Create SIM card
// @Description Registers a new SIM card with ICCID, label, phone, and PIN
// @Tags cellular
// @Accept json
// @Produce json
// @Param body body object true "SIM card" example({"iccid":"89310...","label":"KPN SIM","phone":"+31...","pin":"1234","notes":""})
// @Success 201 {object} map[string]int64
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/cellular/sim-cards [post]
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

// @Summary Update SIM card
// @Description Updates a registered SIM card
// @Tags cellular
// @Accept json
// @Produce json
// @Param id path integer true "SIM card ID"
// @Param body body object true "SIM card fields"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/cellular/sim-cards/{id} [put]
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

// @Summary Delete SIM card
// @Description Removes a registered SIM card
// @Tags cellular
// @Produce json
// @Param id path integer true "SIM card ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/cellular/sim-cards/{id} [delete]
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

// @Summary Get current SIM ICCID
// @Description Returns the currently inserted SIM card's ICCID, SIM state, and IMEI
// @Tags cellular
// @Produce json
// @Success 200 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/cellular/sim-cards/current [get]
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
