package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/gateway"
	"meshsat/internal/tetra"
)

// TETRA SDS through a radio's PEI (gateway type tetra, link tetra_0).
// [MESHSAT-1588]

func (s *Server) tetraGateway() *gateway.TetraGateway {
	if s.gwManager == nil {
		return nil
	}
	return s.gwManager.GetTetraGateway()
}

// tetraSendRequest is the body of POST /api/tetra/send.
type tetraSendRequest struct {
	To   string `json:"to"`   // an SSI or tetra:<ssi>; empty = the gateway's destination (the radio's selected group)
	Text string `json:"text"` // the words; plain text over one SDS goes in numbered parts
}

// @Summary Send a text over TETRA
// @Description Queues one text on tetra_0 through the delivery ledger. The radio sends it as an SDS-TL text (the kind every TETRA handheld shows) to the given SSI or TSI, or, with no to, to the gateway's destination, which defaults to the radio's selected group. About 140 Latin-1 characters fit one SDS (70 with a character outside Latin-1); a longer text goes as numbered parts. The response means queued, not sent: the delivery row says when the radio took it.
// @Tags tetra
// @Accept json
// @Produce json
// @Param body body tetraSendRequest true "text" example({"to":"2000","text":"Hello from the kit"})
// @Success 202 {object} map[string]interface{} "status queued, delivery_id, to"
// @Failure 400 {object} map[string]string "text missing, or to is not an SSI or TSI"
// @Failure 503 {object} map[string]string "no TETRA radio on this Bridge, or the delivery pipeline is not available"
// @Router /api/tetra/send [post]
func (s *Server) handleTetraSend(w http.ResponseWriter, r *http.Request) {
	tg := s.tetraGateway()
	if tg == nil {
		writeError(w, http.StatusServiceUnavailable, "no TETRA gateway: plug a Motorola TETRA radio into the Bridge's USB")
		return
	}
	if s.dispatcher == nil {
		writeError(w, http.StatusServiceUnavailable, "delivery pipeline not available")
		return
	}
	var req tetraSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	dest := ""
	if strings.TrimSpace(req.To) != "" {
		ssi, ok := tetra.ParseAddress(req.To)
		if !ok {
			writeError(w, http.StatusBadRequest, "to must be an SSI (1-16777214), a TSI, or tetra:<ssi>")
			return
		}
		dest = tetra.Address(ssi)
	}
	id, _, err := s.dispatcher.QueueDirectSendTo(tg.InstanceID(), req.Text, engine.DirectSendOptions{
		Destination: dest,
		Class:       database.DeliveryClassMessage,
		TTLSeconds:  3600,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	to := dest
	if to == "" {
		to = tetra.Address(tg.TetraStatus().Destination)
	}
	writeJSON(w, http.StatusAccepted, map[string]interface{}{"status": "queued", "delivery_id": id, "to": to})
}

// tetraStatusResponse is GET /api/tetra/status.
type tetraStatusResponse struct {
	Configured bool                 `json:"configured"` // a TETRA gateway runs
	Gateway    *gateway.TetraStatus `json:"gateway,omitempty"`
}

// @Summary TETRA radio and gateway status
// @Description The TETRA gateway's link (state, port, since when, last error), the radio as ATI reports it (model, TEI, ITSI, GTSI, firmware), its mode (dmo or tmo), its selected group and own SSI, where an unaddressed message goes, the battery, how many bytes one SDS holds, the settings, and the counters (SDS in and out, errors, SDS-TL reports, texts refused as too long, non-text SDS ignored). configured is false and gateway absent when no radio is plugged in.
// @Tags tetra
// @Produce json
// @Success 200 {object} tetraStatusResponse
// @Router /api/tetra/status [get]
func (s *Server) handleTetraStatus(w http.ResponseWriter, r *http.Request) {
	resp := tetraStatusResponse{}
	if tg := s.tetraGateway(); tg != nil {
		st := tg.TetraStatus()
		resp.Configured = true
		resp.Gateway = &st
	}
	writeJSON(w, http.StatusOK, resp)
}
