package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/cell"
)

// Cell broadcast on the MeshSat 2G cell: the operator's test sends and
// what is on the air (MESHSAT-1734; docs/cell-broadcast.md). Every send
// goes through the same client, allow-list and TX log the warning relay
// uses.

// SetCellCBC sets the cell broadcast client for the /api/cell/cbs routes.
func (s *Server) SetCellCBC(c *cell.CBC) {
	s.cellCBC = c
}

// cellCBSTestRequest is the body of POST /api/cell/cbs/test.
type cellCBSTestRequest struct {
	Channel    int    `json:"channel"`    // the message identifier; default 4380 (the EU-Alert / CMAS test identifier)
	Text       string `json:"text"`       // the words; GSM 7-bit pages of 93, or UCS-2 pages of 41 when needed; at most 15 pages
	Language   string `json:"language"`   // ISO 639-1, into the DCS of a GSM 7-bit page (nl, en, de, el, ...)
	Broadcasts int    `json:"broadcasts"` // how many times; default 30
	Period     int    `json:"period"`     // repetition period in units of 1.883 s; default 5 (about 9.4 s)
	Purpose    string `json:"purpose"`    // what the test is for; written to the TX log (default: the text's first words)
}

// cellCBSResponse is GET /api/cell/cbs.
type cellCBSResponse struct {
	Live    bool             `json:"live"`    // the radio is keyed up: a broadcast can go now
	OnAir   []cell.Broadcast `json:"on_air"`  // what this Bridge put on the air and has not taken off
	Allowed cellCBSAllowed   `json:"allowed"` // the allow-list in force
	CBC     []string         `json:"cbc"`     // what osmo-cbc itself says it holds (show messages cbs), null when it does not answer
}

type cellCBSAllowed struct {
	Ordinary string `json:"ordinary"` // "1-4351"
	Test     []int  `json:"test"`     // 4355, 4380, 4381
	Real     []int  `json:"real"`     // MESHSAT_CELL_CBS_REAL_IDS, empty unless the owner cleared them
}

// @Summary Broadcast a test message on the MeshSat cell
// @Description Puts one message on the cell's broadcast channel for the owner's tests at the licensed site (T1, T2, T6 in docs/cell-broadcast.md), through the same client as the warning relay: the allow-list (ordinary channels 1-4351, the test identifiers 4355, 4380 and 4381, and only the real identifiers MESHSAT_CELL_CBS_REAL_IDS names), the radio keyed up, a TX-log row written before osmo-cbc hears of it. A phone shows 4380 and 4381 only with "Test alerts" on, marked as a test; an ordinary channel only when subscribed to it. A message already on the channel is replaced.
// @Tags cell
// @Accept json
// @Produce json
// @Param body body cellCBSTestRequest true "the message" example({"channel":4380,"text":"TEST MeshSat relay on 4380, not an official alert","language":"en","broadcasts":30})
// @Success 201 {object} cell.Broadcast "what went on the air: channel, serial number, pages, coding, until when"
// @Failure 400 {object} map[string]string "bad JSON, no text, too long, bad period or count"
// @Failure 403 {object} map[string]string "the channel is a real public-warning identifier the owner has not cleared"
// @Failure 409 {object} map[string]string "the radio is not keyed up"
// @Failure 502 {object} map[string]string "osmo-cbc refused or did not answer"
// @Failure 503 {object} map[string]string "no cell in this image"
// @Router /api/cell/cbs/test [post]
func (s *Server) handleCellCBSTest(w http.ResponseWriter, r *http.Request) {
	if s.cellCBC == nil {
		writeError(w, http.StatusServiceUnavailable, "no cell in this image (the Osmocom daemons are not installed)")
		return
	}
	var req cellCBSTestRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	if req.Channel == 0 {
		req.Channel = cell.ChannelEUAlertTest
	}
	purpose := strings.TrimSpace(req.Purpose)
	if purpose == "" {
		purpose = "operator test send: " + firstWords(req.Text, 40)
	}
	b, err := s.cellCBC.Broadcast(r.Context(), cell.BroadcastRequest{Channel: req.Channel, Text: req.Text, Language: req.Language,
		Purpose: purpose, Test: true, Period: req.Period, Count: req.Broadcasts})
	if err != nil {
		writeError(w, cellCBSErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

// @Summary What the MeshSat cell is broadcasting
// @Description live: the radio is keyed up. on_air: every message this Bridge put on the air and has not taken off (channel, what a phone makes of it, serial number, pages, coding, the warning it is about, the purpose logged, until when). allowed: the channels the allow-list lets through. cbc: osmo-cbc's own list from its VTY, null when the core is down.
// @Tags cell
// @Produce json
// @Success 200 {object} cellCBSResponse
// @Failure 503 {object} map[string]string "no cell in this image"
// @Router /api/cell/cbs [get]
func (s *Server) handleCellCBS(w http.ResponseWriter, r *http.Request) {
	if s.cellCBC == nil {
		writeError(w, http.StatusServiceUnavailable, "no cell in this image (the Osmocom daemons are not installed)")
		return
	}
	resp := cellCBSResponse{Live: s.cellCBC.Live(), OnAir: s.cellCBC.OnAir(),
		Allowed: cellCBSAllowed{Ordinary: fmt.Sprintf("1-%d", cell.OrdinaryChannelMax), Test: append([]int(nil), cell.TestChannels...), Real: s.cellCBC.Allow().Real()}}
	if resp.OnAir == nil {
		resp.OnAir = []cell.Broadcast{}
	}
	if resp.Allowed.Real == nil {
		resp.Allowed.Real = []int{}
	}
	resp.CBC = s.cellCBC.VTYMessages()
	writeJSON(w, http.StatusOK, resp)
}

// @Summary Take a broadcast off the MeshSat cell
// @Description Deletes the message on the channel from osmo-cbc and writes the closing TX-log row (T5 by hand).
// @Tags cell
// @Produce json
// @Param channel path integer true "the message identifier"
// @Success 200 {object} map[string]interface{} "status stopped, channel"
// @Failure 400 {object} map[string]string "not a channel"
// @Failure 502 {object} map[string]string "osmo-cbc refused or did not answer"
// @Failure 503 {object} map[string]string "no cell in this image"
// @Router /api/cell/cbs/{channel} [delete]
func (s *Server) handleCellCBSStop(w http.ResponseWriter, r *http.Request) {
	if s.cellCBC == nil {
		writeError(w, http.StatusServiceUnavailable, "no cell in this image (the Osmocom daemons are not installed)")
		return
	}
	ch, err := strconv.Atoi(chi.URLParam(r, "channel"))
	if err != nil || ch < 1 || ch > 65535 {
		writeError(w, http.StatusBadRequest, "channel must be a message identifier 1-65535")
		return
	}
	if err := s.cellCBC.Delete(r.Context(), ch, "operator stopped the broadcast"); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "stopped", "channel": ch})
}

func cellCBSErrorStatus(err error) int {
	switch {
	case errors.Is(err, cell.ErrChannelRefused):
		return http.StatusForbidden
	case errors.Is(err, cell.ErrRadioDown):
		return http.StatusConflict
	case strings.Contains(err.Error(), "osmo-cbc"):
		return http.StatusBadGateway
	case strings.Contains(err.Error(), "TX log"):
		return http.StatusInternalServerError
	}
	return http.StatusBadRequest
}

// firstWords is the first n bytes of s on a word boundary.
func firstWords(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := strings.LastIndex(s[:n], " ")
	if cut <= 0 {
		cut = n
	}
	return s[:cut] + "..."
}
