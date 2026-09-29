package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/transport"
	"meshsat/internal/types"
)

// handleGetMessages returns paginated message history.
// @Summary Get message history
// @Description Returns paginated mesh messages with optional filters
// @Tags messages
// @Param node query string false "Filter by node ID (!hex format)"
// @Param since query string false "Start time (RFC3339)"
// @Param until query string false "End time (RFC3339)"
// @Param portnum query int false "Filter by port number"
// @Param transport query string false "Filter by transport (radio, mqtt, satellite)"
// @Param direction query string false "Filter by direction (rx, tx)"
// @Param limit query int false "Results per page (default 50, max 1000)"
// @Param offset query int false "Offset for pagination"
// @Success 200 {object} map[string]interface{} "messages, total, limit, offset"
// @Router /api/messages [get]
func (s *Server) handleGetMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter := database.MessageFilter{
		Node:      q.Get("node"),
		Since:     q.Get("since"),
		Until:     q.Get("until"),
		Transport: q.Get("transport"),
		Direction: q.Get("direction"),
		Limit:     intParam(q.Get("limit"), 50),
		Offset:    intParam(q.Get("offset"), 0),
	}

	if v := q.Get("portnum"); v != "" {
		pn, err := strconv.Atoi(v)
		if err == nil {
			filter.PortNum = &pn
		}
	}

	msgs, total, err := s.db.GetMessages(filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to query messages: "+err.Error())
		return
	}
	if msgs == nil {
		msgs = []database.Message{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"messages": msgs,
		"total":    total,
		"limit":    filter.Limit,
		"offset":   filter.Offset,
	})
}

// handleGetMessageStats returns aggregate message statistics.
// @Summary Get message statistics
// @Description Returns message counts grouped by transport and port number
// @Tags messages
// @Success 200 {object} database.MessageStats
// @Router /api/messages/stats [get]
func (s *Server) handleGetMessageStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.db.GetMessageStats()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to get stats: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// simulateMeshRxRequest is the body of POST /api/messages/simulate-mesh-rx.
type simulateMeshRxRequest struct {
	Text string `json:"text"`
	From string `json:"from"` // mesh node id, "!27ca8f1c" or decimal; default = a fixed test node
}

// handleSimulateMeshRx injects a text as if a handheld on this kit's mesh had sent it.
// @Summary Simulate an inbound mesh text
// @Description Feeds a text into the same path a radio-received packet takes (dedup, persistence,
// @Description the access rules, the delivery ledger), so the kit-to-kit relay can be pre-flighted
// @Description without a handheld. Operator tool for the booth checklist. [MESHSAT-857]
// @Tags messages
// @Param body body simulateMeshRxRequest true "Text and optional source node"
// @Success 200 {object} map[string]string "accepted"
// @Failure 400 {object} map[string]string "error"
// @Failure 503 {object} map[string]string "processor unavailable"
// @Router /api/messages/simulate-mesh-rx [post]
func (s *Server) handleSimulateMeshRx(w http.ResponseWriter, r *http.Request) {
	var req simulateMeshRxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	if s.processor == nil {
		writeError(w, http.StatusServiceUnavailable, "processor unavailable")
		return
	}
	from := uint32(0x00c0ffee) // "!00c0ffee": a node that exists on no mesh
	if req.From != "" {
		if v, err := strconv.ParseUint(strings.TrimPrefix(req.From, "!"), 16, 32); err == nil && strings.HasPrefix(req.From, "!") {
			from = uint32(v)
		} else if v, err := strconv.ParseUint(req.From, 10, 32); err == nil {
			from = uint32(v)
		} else {
			writeError(w, http.StatusBadRequest, "from must be !hex or decimal")
			return
		}
	}
	msg := transport.MeshMessage{From: from, To: 0xffffffff, PortNum: 1, PortNumName: "TEXT_MESSAGE_APP", DecodedText: req.Text}
	if err := s.processor.SimulateInbound(msg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted", "from": fmt.Sprintf("!%08x", from), "text": req.Text})
}

// sendMessageRequest is the body of POST /api/messages/send: the mesh send
// request, and whether a gateway send goes exactly as given.
type sendMessageRequest struct {
	transport.SendRequest
	// Plain sends the text exactly as given: no chat key, no transform chain
	// (no encryption, compression or base64), no attribution; the SMS
	// history keeps it with encrypted false. For an SOS or an alarm test to
	// an emergency contact, which MeshSat Android sends as plain text
	// whatever its encryption settings. A kit's USB modem (AT text mode)
	// still gets the GSM clean-up, since it cannot carry every character;
	// ModemManager sends the text as it is. A plain text with a control
	// character other than a line break is refused (plainTextError). Default
	// false: the link's own processing, as before. A mesh send (no gateway)
	// goes as given anyway.
	Plain bool `json:"plain"`
}

// handleSendMessage sends a text message via the mesh transport or a satellite gateway.
// @Summary Send a message
// @Description Sends a text message through the Meshtastic radio or a satellite gateway.
// @Description Set gateway to "iridium" (9603 SBD), "iridium_imt" (9704 IMT), "mqtt", "cellular", or "webhook".
// @Description A text sent on the mesh also goes to TAK as GeoChat from this Bridge when the TAK gateway runs.
// @Description An SMS (gateway "cellular") to a number with a chat key of its own, or the wildcard's (PUT /api/keys/sms/{number}), goes sealed with that key as MeshSat Android seals it, whether or not the link encrypts.
// @Description plain true sends the text exactly as given (an SOS or an alarm test to an emergency contact): no chat key, no transform chain (no encryption, compression or base64), no attribution, and the SMS history keeps it with encrypted false. A kit's USB modem (AT text mode) cannot carry every character, so there it still gets the GSM clean-up ("[" becomes "(", "€" "EUR", "±" "+/-"); ModemManager on a phone sends it exactly. A plain text with a control character other than a line break (\n, \r) is refused with 400. Default false, the link's own processing.
// @Tags messages
// @Accept json
// @Produce json
// @Param body body sendMessageRequest true "Message to send"
// @Success 200 {object} map[string]interface{} "sent (mesh), or queued with delivery_id, msg_ref, precedence and plain (gateway)"
// @Failure 400 {object} map[string]string "text missing, a plain text with a control character, a bad precedence or an unknown gateway"
// @Failure 409 {object} map[string]string "the mesh is switched off"
// @Failure 503 {object} map[string]string "no dispatcher, gateway manager or mesh transport"
// @Router /api/messages/send [post]
func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	s.touchOperatorActivity()

	var body sendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	req := body.SendRequest
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	if body.Plain {
		if err := plainTextError(req.Text); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// Normalise precedence (STANAG 4406 Edition 2). Accepts full names and
	// ACP-127 prosigns (Z/O/P/R/M); empty → Routine. [MESHSAT-543]
	precedence, err := types.ParsePrecedence(req.Precedence)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// If a gateway is specified, queue via the delivery ledger.
	// The DeliveryWorker picks it up within 2 seconds and sends via the gateway.
	if req.Gateway != "" {
		if s.dispatcher == nil {
			writeError(w, http.StatusServiceUnavailable, "dispatcher unavailable")
			return
		}
		// Resolve gateway type to interface ID (e.g. "iridium_imt" → "iridium_0")
		if s.gwManager == nil {
			writeError(w, http.StatusServiceUnavailable, "gateway manager unavailable")
			return
		}
		ifaceID := s.gwManager.ResolveGatewayInterface(req.Gateway)
		if ifaceID == "" {
			writeError(w, http.StatusBadRequest, "unknown gateway: "+req.Gateway)
			return
		}
		// `to` names a gateway-side address (phone number for cellular,
		// CALL-SSID for APRS); empty keeps the interface default. The TTC
		// composer uses it to text the Hub instead of the peer kit. [MESHSAT-962]
		opts := engine.DirectSendOptions{Precedence: string(precedence), Destination: strings.TrimSpace(req.To)}
		if body.Plain {
			opts.Class = database.DeliveryClassPlain
		}
		delID, msgRef, err := s.dispatcher.QueueDirectSendTo(ifaceID, req.Text, opts)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "queue failed: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":      "queued",
			"gateway":     req.Gateway,
			"delivery_id": delID,
			"msg_ref":     msgRef,
			"precedence":  string(precedence),
			"plain":       body.Plain,
		})
		return
	}

	// Default: send via Meshtastic mesh radio.
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}
	// A person who switched the mesh off gets nothing sent on it until it
	// is switched on again (the SOS worker's own sends are not refused).
	// [MESHSAT-1401]
	if s.meshSwitchedOff() {
		writeError(w, http.StatusConflict, "Not sent: Mesh is switched off. Switch it on in Links.")
		return
	}
	if err := s.mesh.SendMessage(r.Context(), req); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to send: "+err.Error())
		return
	}
	s.recordMeshTX(req)
	s.takOwnChat(req.Text) // Android's sendChat for every text sent to the mesh [MESHSAT-1421]
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// handlePurgeMessages deletes messages older than a given timestamp.
// @Summary Purge old messages
// @Description Deletes messages older than the specified RFC3339 timestamp
// @Tags messages
// @Produce json
// @Param before query string true "RFC3339 timestamp cutoff"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/messages [delete]
func (s *Server) handlePurgeMessages(w http.ResponseWriter, r *http.Request) {
	before := r.URL.Query().Get("before")
	if before == "" {
		writeError(w, http.StatusBadRequest, "before parameter required (RFC3339 timestamp)")
		return
	}

	deleted, err := s.db.PurgeMessages(before)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to purge: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": deleted,
	})
}

// meshSwitchedOff reports whether this Bridge has mesh links and every one of
// them is switched off. A database without mesh interface rows sends as
// before. [MESHSAT-1401]
func (s *Server) meshSwitchedOff() bool {
	if s.db == nil {
		return false
	}
	ifaces, err := s.db.GetInterfacesByType("mesh")
	if err != nil || len(ifaces) == 0 {
		return false
	}
	for _, iface := range ifaces {
		if iface.Enabled {
			return false
		}
	}
	return true
}

func intParam(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return v
}
