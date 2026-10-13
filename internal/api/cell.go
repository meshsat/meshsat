package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
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

// cellStatusResponse is GET /api/cell/status: the gateway side (nil when no
// cell gateway runs) and the kit's host side (the Pluto, the core, the radio,
// the phones), when the host agent and the cell package are there.
type cellStatusResponse struct {
	Configured bool                `json:"configured"` // a cell gateway runs
	Gateway    *gateway.CellStatus `json:"gateway,omitempty"`
	Host       *gateway.HostStatus `json:"host,omitempty"`
}

// @Summary MeshSat cell status
// @Description gateway: the cell gateway's settings and its SMPP link (state, since when, the last error, reconnects, bind refusals, enquire_link round trip) and counters (texts in and out, texts refused as not for the short code, receipts, submits refused because the phone was off the cell, alerts, errors, concatenated parts waiting); null when no cell gateway runs. host: the kit's host side through the OOB agent: whether the agent answers and the cell package is installed, the Pluto (on USB, state, interface, reachable), the core units, the radio (active, since), the BTS as osmo-bsc reports it, the phones the MSC's VLR knows, the ARFCN, the licence and site attestation, and the TX log. The radio keys up only through POST /api/cell/radio.
// @Tags cell
// @Produce json
// @Success 200 {object} cellStatusResponse
// @Router /api/cell/status [get]
func (s *Server) handleCellStatus(w http.ResponseWriter, r *http.Request) {
	resp := cellStatusResponse{}
	if cg := s.cellGateway(); cg != nil {
		st := cg.CellStatus()
		resp.Configured = true
		resp.Gateway = &st
	}
	if s.cellHost != nil {
		hs := s.cellHost.Status(r.Context())
		resp.Host = &hs
	}
	writeJSON(w, http.StatusOK, resp)
}

// cellRadioRequest is the body of POST /api/cell/radio.
type cellRadioRequest struct {
	Action  string `json:"action"`  // start or stop
	Purpose string `json:"purpose"` // start: what this emission is for; written to the kit's TX log (licence RDI-2610-00385)
}

// @Summary Key the cell's radio up or down
// @Description start keys osmo-bts-trx up on the kit's host through the OOB agent, after the host's preflight: DCS1800 and ARFCN 861-885 in the installed configs, band 1800 on the BTS, the Pluto on USB and running the meshsat-cell image, the licence number and the site attestation (CELL_SITE_OK=yes) in the host's cell.env. A purpose is required and goes into the kit's TX log before the radio keys up; a refusal names its reason. stop keys the radio down, writes the closing log row and parks the Pluto's transmitter. The core (no emission) is handled by POST /api/cell/core and by the adoption of the Pluto.
// @Tags cell
// @Accept json
// @Produce json
// @Param body body cellRadioRequest true "action and purpose" example({"action":"start","purpose":"MESHSAT-1578 e2e, Pixel texts the kit"})
// @Success 200 {object} map[string]interface{} "what the host did: radio, arfcn, licence, tx_log"
// @Failure 400 {object} map[string]string "unknown action, no purpose"
// @Failure 409 {object} map[string]string "the host refused: the preflight's reason"
// @Failure 503 {object} map[string]string "no host agent, or no cell package on the kit"
// @Router /api/cell/radio [post]
func (s *Server) handleCellRadio(w http.ResponseWriter, r *http.Request) {
	if s.cellHost == nil {
		writeError(w, http.StatusServiceUnavailable, "no cell host on this bridge")
		return
	}
	var req cellRadioRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	res, err := s.cellHost.Radio(r.Context(), strings.ToLower(strings.TrimSpace(req.Action)), req.Purpose)
	if err != nil {
		writeError(w, cellHostErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// cellCoreRequest is the body of POST /api/cell/core.
type cellCoreRequest struct {
	Action string `json:"action"` // start or stop
}

// @Summary Start or stop the cell's core
// @Description The core (stp, hlr, mgw, msc, cbc, bsc) on the kit's host, through the OOB agent; nothing transmits. start also binds cell_0. stop keys the radio down first.
// @Tags cell
// @Accept json
// @Produce json
// @Param body body cellCoreRequest true "action" example({"action":"start"})
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]string
// @Failure 409 {object} map[string]string "the host refused"
// @Failure 503 {object} map[string]string "no host agent, or no cell package on the kit"
// @Router /api/cell/core [post]
func (s *Server) handleCellCore(w http.ResponseWriter, r *http.Request) {
	if s.cellHost == nil {
		writeError(w, http.StatusServiceUnavailable, "no cell host on this bridge")
		return
	}
	var req cellCoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	res, err := s.cellHost.Core(r.Context(), strings.ToLower(strings.TrimSpace(req.Action)))
	if err != nil {
		writeError(w, cellHostErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func cellHostErrorStatus(err error) int {
	switch {
	case errors.Is(err, gateway.ErrNoCellHost), errors.Is(err, gateway.ErrNoCellKit):
		return http.StatusServiceUnavailable
	case strings.Contains(err.Error(), "unknown"), strings.Contains(err.Error(), "purpose"):
		return http.StatusBadRequest
	}
	return http.StatusConflict
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

// @Summary The cell's transmit log
// @Description Every key-up, key-down and refused key-up of the 2G cell's radio, oldest first: time (UTC), event, ARFCN and frequency, the TX setting, the estimated output, the purpose, the operator and the licence cited. The row is written before the radio keys up. format=md returns the table the monthly report is drawn from.
// @Tags cell
// @Produce json
// @Param format query string false "md for the markdown table"
// @Param limit query integer false "at most this many rows (default all)"
// @Success 200 {array} database.CellTXRow
// @Failure 500 {object} map[string]string
// @Router /api/cell/txlog [get]
func (s *Server) handleCellTXLog(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	rows, err := s.db.ListCellTX(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if r.URL.Query().Get("format") != "md" {
		writeJSON(w, http.StatusOK, rows)
		return
	}
	var b strings.Builder
	b.WriteString("| UTC | freq (MHz) / ARFCN | TX setting | est. output | event | purpose | operator | licence |\n")
	b.WriteString("|-----|--------------------|-----------|-------------|-------|---------|----------|---------|\n")
	for _, row := range rows {
		esc := func(v string) string { return strings.ReplaceAll(v, "|", "/") }
		at := row.At
		if !strings.HasSuffix(at, "Z") {
			at += "Z"
		}
		b.WriteString("| " + at + " | " + strconv.FormatFloat(row.FreqMHz, 'f', 1, 64) + " / ARFCN " + strconv.Itoa(row.ARFCN) + " | " +
			esc(row.Setting) + " | " + esc(row.Output) + " | " + row.Event + " | " + esc(row.Purpose) + " | " + esc(row.Operator) + " | " + esc(row.Licence) + " |\n")
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(b.String()))
}
