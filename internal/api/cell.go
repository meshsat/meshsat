package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/gateway"
)

// The MeshSat 2G cell: texts between phones on the kit's own cell and the
// Bridge over SMPP (gateway type cell, link cell_0). [MESHSAT-1557]

// cellGateway returns the running cell gateway, or nil.
func (s *Server) cellGateway() *gateway.CellGateway {
	if s.gwManager == nil {
		return nil
	}
	return s.gwManager.GetCellGateway()
}

// cellSendRequest is the body of POST /api/cell/sms/send.
type cellSendRequest struct {
	To   string `json:"to"`   // the phone on the cell: "1001" or "cell:1001"
	Text string `json:"text"` // the words; GSM 03.38 or, when needed, UCS-2, split into parts over 160/70 characters
}

// @Summary Send a text to a phone on the MeshSat cell
// @Description Queues one text on cell_0 through the delivery ledger, addressed cell:<msisdn>, from the kit's short code. The cell delivers it when the phone acknowledges (the response proves delivery); a phone that is not on the cell right now is not a failure: the row waits a day for it, every minute for the first ten and every five minutes after, and the cell's alert when the phone attaches makes it due at once. Over 160 GSM characters (70 with a character outside GSM 03.38, Greek or an emoji) the text goes as concatenated parts.
// @Tags cell
// @Accept json
// @Produce json
// @Param body body cellSendRequest true "text" example({"to":"1001","text":"Hello from the kit"})
// @Success 202 {object} map[string]interface{} "status queued, delivery_id, to"
// @Failure 400 {object} map[string]string "to or text missing, or to is not a phone on the cell"
// @Failure 503 {object} map[string]string "no cell gateway configured, or the delivery pipeline is not available"
// @Router /api/cell/sms/send [post]
func (s *Server) handleCellSend(w http.ResponseWriter, r *http.Request) {
	cg := s.cellGateway()
	if cg == nil {
		writeError(w, http.StatusServiceUnavailable, "no cell gateway: set CELL_SMPP_HOST or configure gateway type cell")
		return
	}
	if s.dispatcher == nil {
		writeError(w, http.StatusServiceUnavailable, "delivery pipeline not available")
		return
	}
	var req cellSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	msisdn, ok := gateway.ParseCellAddress(req.To)
	if !ok {
		writeError(w, http.StatusBadRequest, "to must be a phone on the cell: digits, or cell:<digits>")
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	id, _, err := s.dispatcher.QueueDirectSendTo(cg.InstanceID(), req.Text, engine.DirectSendOptions{
		Destination: gateway.CellAddress(msisdn),
		Class:       database.DeliveryClassMessage,
		TTLSeconds:  86400,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]interface{}{"status": "queued", "delivery_id": id, "to": gateway.CellAddress(msisdn)})
}

// @Summary MeshSat cell gateway status
// @Description The cell gateway's settings (host, port, system id, short code, reply window, receipts) and its SMPP link: state (disconnected, connecting, bound), since when, the last error, reconnects and bind refusals, the enquire_link round trip, when the cell last sent anything, and the counters: texts in and out, texts refused because they were not for the short code, delivery receipts, submits refused because the phone was off the cell, alerts, errors, and concatenated parts still waiting for the rest.
// @Tags cell
// @Produce json
// @Success 200 {object} gateway.CellStatus
// @Failure 503 {object} map[string]string "no cell gateway configured"
// @Router /api/cell/status [get]
func (s *Server) handleCellStatus(w http.ResponseWriter, r *http.Request) {
	cg := s.cellGateway()
	if cg == nil {
		writeError(w, http.StatusServiceUnavailable, "no cell gateway: set CELL_SMPP_HOST or configure gateway type cell")
		return
	}
	writeJSON(w, http.StatusOK, cg.CellStatus())
}

// @Summary Phones seen on the MeshSat cell
// @Description Every phone that texted the kit's short code or was texted by it: when first seen, last text in and out, counts, the number it last dialled, and whether the cell last reported it reachable (alert_notification), newest activity first. A phone that texted within the gateway's reply window is where a rule to cell_0 with no destination of its own sends.
// @Tags cell
// @Produce json
// @Success 200 {array} database.CellPeer
// @Failure 500 {object} map[string]string
// @Router /api/cell/peers [get]
func (s *Server) handleCellPeers(w http.ResponseWriter, r *http.Request) {
	peers, err := s.db.ListCellPeers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, peers)
}
