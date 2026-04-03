package api

import (
	"encoding/json"
	"net/http"

	"meshsat/internal/gateway"
)

type takChatRequest struct {
	Text string `json:"text"`
}

// handleTAKSendChat publishes a GeoChat CoT event to the TAK event stream.
// @Summary Send TAK GeoChat
// @Tags tak
// @Accept json
// @Produce json
// @Success 200 {object} map[string]string
// @Router /api/tak/chat [post]
func (s *Server) handleTAKSendChat(w http.ResponseWriter, r *http.Request) {
	var req takChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text required"})
		return
	}

	ev := gateway.BuildChatEvent("meshsat-bridge", "MESHSAT", req.Text, 300)
	gateway.GlobalTakEventBus.Publish(gateway.CotEventToRecord(&ev, "outbound"))

	writeJSON(w, http.StatusOK, map[string]string{"status": "sent", "uid": ev.UID})
}

type takNineLineRequest struct {
	Text    string `json:"text"`
	Urgency string `json:"urgency"`
}

// handleTAKSendNineLine publishes a 9-Line MEDEVAC SOS CoT event.
// @Summary Send 9-Line MEDEVAC
// @Tags tak
// @Accept json
// @Produce json
// @Success 200 {object} map[string]string
// @Router /api/tak/nineline [post]
func (s *Server) handleTAKSendNineLine(w http.ResponseWriter, r *http.Request) {
	var req takNineLineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text required"})
		return
	}

	reason := "9-LINE MEDEVAC [" + req.Urgency + "]\n" + req.Text
	ev := gateway.BuildSOSEvent("meshsat-bridge", "MESHSAT", 0, 0, 0, 600, reason)
	gateway.GlobalTakEventBus.Publish(gateway.CotEventToRecord(&ev, "outbound"))

	writeJSON(w, http.StatusOK, map[string]string{"status": "sent", "type": "9-line-medevac", "urgency": req.Urgency})
}
