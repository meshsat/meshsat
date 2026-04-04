package api

import (
	"encoding/json"
	"net/http"

	"meshsat/internal/gateway"
)

// @Summary Generic inbound webhook
// @Description Receives inbound messages via webhook and forwards to mesh or cellular gateway
// @Tags webhooks
// @Accept json
// @Produce json
// @Param body body object true "Inbound message" example({"text":"hello","to":"","channel":0})
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/webhooks/inbound [post]
func (s *Server) handleWebhookInbound(w http.ResponseWriter, r *http.Request) {
	if s.gwManager == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway manager not available")
		return
	}

	wgw := s.gwManager.GetWebhookGateway()
	if wgw == nil {
		// Fallback: try cellular gateway for backwards compatibility
		s.handleWebhookCellularInbound(w, r)
		return
	}

	// Validate webhook secret if configured
	cfg := wgw.Config()
	if cfg.InboundSecret != "" {
		secret := r.Header.Get("X-Webhook-Secret")
		if secret != cfg.InboundSecret {
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

	wgw.ForwardWebhookInbound(gateway.InboundMessage{
		Text:    req.Text,
		To:      req.To,
		Channel: req.Channel,
		Source:  "webhook",
	})

	_ = s.db.InsertWebhookLog("inbound", "/api/webhooks/inbound", "POST", 200, req.Text, "", "")

	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}
