package api

import (
	"encoding/json"
	"net/http"

	"meshsat/internal/directory"
	"meshsat/internal/engine"
	"meshsat/internal/types"
)

// POST /api/messages/send-to-contact — contact-aware send. Thin REST
// wrapper over [engine.Dispatcher.SendToRecipient] (MESHSAT-544). Per
// the S2-02 acceptance, this coexists with the legacy POST
// /api/messages/send during the grace window; both paths remain
// available until the UI reshape (MESHSAT-551) retires the explicit
// bearer picker. [MESHSAT-545]

type sendToContactRequest struct {
	ContactID  string `json:"contact_id,omitempty"`
	GroupID    string `json:"group_id,omitempty"`
	Text       string `json:"text"`
	Precedence string `json:"precedence,omitempty"` // Override|Flash|Immediate|Priority|Routine|Deferred, or ACP-127 prosign
	Strategy   string `json:"strategy,omitempty"`   // PRIMARY_ONLY|ANY_REACHABLE|ORDERED_FALLBACK|HEMB_BONDED|ALL_BEARERS

	// Raw-send escape hatch: queue directly on a named interface
	// without a directory lookup. Useful when the operator has a
	// one-off address (phone number on a scrap of paper) and hasn't
	// added it as a contact yet.
	RawInterfaceID string `json:"raw_interface_id,omitempty"`
	RawAddress     string `json:"raw_address,omitempty"`
}

type perBearerStatus struct {
	Kind        string  `json:"kind"`
	DeliveryIDs []int64 `json:"delivery_ids,omitempty"`
	Error       string  `json:"error,omitempty"`
}

type sendToContactResponse struct {
	Queued     bool              `json:"queued"`
	Strategy   string            `json:"strategy"`
	Precedence string            `json:"precedence"`
	PerBearer  []perBearerStatus `json:"per_bearer"`
}

// @Summary Send a message to a contact
// @Description Contact-aware send. Resolves the contact's addresses
// @Description against the active dispatch policy and queues one
// @Description delivery per selected bearer. Returns per-bearer
// @Description delivery IDs so the UI can render per-bearer
// @Description delivery ticks (WhatsApp grammar). A 503 is returned
// @Description when every bearer fails to queue; the body still
// @Description carries the `per_bearer` breakdown so operators see
// @Description which bearer rejected the send and why.
// @Tags messages
// @Accept json
// @Produce json
// @Param body body sendToContactRequest true "Target contact and message text"
// @Success 200 {object} sendToContactResponse
// @Success 207 {object} sendToContactResponse
// @Failure 400 {object} map[string]string
// @Failure 503 {object} sendToContactResponse
// @Router /api/messages/send-to-contact [post]
func (s *Server) handleSendToContact(w http.ResponseWriter, r *http.Request) {
	if s.dispatcher == nil {
		writeError(w, http.StatusServiceUnavailable, "dispatcher not available")
		return
	}
	var req sendToContactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	if req.ContactID == "" && req.GroupID == "" && req.RawInterfaceID == "" {
		writeError(w, http.StatusBadRequest, "contact_id, group_id, or raw_interface_id is required")
		return
	}

	precedence, err := types.ParsePrecedence(req.Precedence)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var strategy directory.Strategy
	if req.Strategy != "" {
		strategy = directory.Strategy(req.Strategy)
	}

	ref := engine.RecipientRef{
		ContactID: req.ContactID,
		GroupID:   req.GroupID,
	}
	if req.RawInterfaceID != "" {
		ref.Raw = &engine.RawRecipient{
			InterfaceID: req.RawInterfaceID,
			Address:     req.RawAddress,
		}
	}

	opts := engine.SendOptions{
		Precedence: precedence,
		Strategy:   strategy,
	}

	res, err := s.dispatcher.SendToRecipient(r.Context(), ref, []byte(req.Text), opts)
	resp := sendToContactResponseFrom(res)

	if err != nil {
		// No bearer queued at all → 503 with the per-bearer
		// breakdown so the caller can see which bearer failed.
		if len(res.DeliveryIDs) == 0 {
			resp.Queued = false
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		// Partial queue + error: 207 Multi-Status with the failure
		// details under per_bearer.
		writeJSON(w, http.StatusMultiStatus, resp)
		return
	}

	// 200 when every selected bearer queued cleanly; 207 Multi-Status
	// when some bearers queued and some errored.
	status := http.StatusOK
	if len(resp.PerBearer) > 0 {
		hasErr := false
		hasQ := false
		for _, b := range resp.PerBearer {
			if b.Error != "" {
				hasErr = true
			}
			if len(b.DeliveryIDs) > 0 {
				hasQ = true
			}
		}
		if hasErr && hasQ {
			status = http.StatusMultiStatus
		}
	}
	writeJSON(w, status, resp)
}

// sendToContactResponseFrom normalises a SendResult into the wire
// response shape. Each bearer kind with at least one delivery ID or
// an error becomes a single entry; kinds with neither are omitted.
func sendToContactResponseFrom(res engine.SendResult) sendToContactResponse {
	resp := sendToContactResponse{
		Strategy:   string(res.Strategy),
		Precedence: string(res.Precedence),
	}
	kinds := map[string]struct{}{}
	for k := range res.DeliveryIDs {
		kinds[string(k)] = struct{}{}
	}
	for k := range res.Errors {
		kinds[string(k)] = struct{}{}
	}
	for k := range kinds {
		entry := perBearerStatus{Kind: k}
		if ids := res.DeliveryIDs[directory.Kind(k)]; len(ids) > 0 {
			entry.DeliveryIDs = ids
			resp.Queued = true
		}
		if err := res.Errors[directory.Kind(k)]; err != nil {
			entry.Error = err.Error()
		}
		resp.PerBearer = append(resp.PerBearer, entry)
	}
	return resp
}
