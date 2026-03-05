package api

import (
	"encoding/json"
	"net/http"

	"meshsat/internal/gateway"
)

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
