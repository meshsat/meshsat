package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"meshsat/internal/database"
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
	ContactID string `json:"contact_id,omitempty"`
	// GroupID is not supported: the dispatcher sends to one contact (or a
	// raw address) and has no team fan-out, so a request with only a
	// group_id is 400. [MESHSAT-1584]
	GroupID    string `json:"group_id,omitempty"`
	Text       string `json:"text"`
	Precedence string `json:"precedence,omitempty"` // Override|Flash|Immediate|Priority|Routine|Deferred, or ACP-127 prosign
	Strategy   string `json:"strategy,omitempty"`   // PRIMARY_ONLY|ALL_BEARERS, and ORDERED_FALLBACK with delivery intents on; others 400; raw: PRIMARY_ONLY only

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

	// The strategy asked for (or held by the policy row) and why the one
	// used was chosen, e.g. legacy_compat_all_bearers (r3 D12).
	// [MESHSAT-1584]
	RequestedStrategy string `json:"requested_strategy,omitempty"`
	StrategyReason    string `json:"strategy_reason,omitempty"`

	// With MESHSAT_DELIVERY_INTENTS on: the submission, its intent (one
	// per recipient; GET /api/deliveries/intents/{id} has its status),
	// and whether the Idempotency-Key named an earlier submission.
	// [MESHSAT-1581]
	SubmissionID string   `json:"submission_id,omitempty"`
	IntentIDs    []string `json:"intent_ids,omitempty"`
	Existing     bool     `json:"existing,omitempty"`
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
// @Description Strategies: PRIMARY_ONLY and ALL_BEARERS; any other
// @Description explicit strategy is 400 before anything is queued. A policy
// @Description row holding an unimplemented one is used as ALL_BEARERS and
// @Description says so (strategy_reason legacy_compat_all_bearers). A raw
// @Description send (raw_interface_id) is one destination and never an
// @Description intent: no strategy or PRIMARY_ONLY, anything else is 400.
// @Description With MESHSAT_DELIVERY_INTENTS on, a contact send becomes a
// @Description delivery intent (submission_id, intent_ids): its routes are
// @Description tried by the strategy (ORDERED_FALLBACK as well), and
// @Description GET /api/deliveries/intents/{id} reports what is known. An
// @Description Idempotency-Key header makes a retried request return the
// @Description first submission, with delivery intents on or off (off: its
// @Description submission_id and the delivery rows it queued, existing
// @Description true), also when the flag changed between the requests; the
// @Description same key with a different request is 409. Without a key a
// @Description retried request sends again. 413: no
// @Description route can carry a message that long; 422: no route of the
// @Description contact can be used (per_bearer says why). A team send
// @Description (group_id without contact_id) is not supported: 400.
// @Tags messages
// @Accept json
// @Produce json
// @Param body body sendToContactRequest true "Target contact and message text"
// @Param Idempotency-Key header string false "Retry protection for this request, with delivery intents on or off: a request again under the same key returns the first send's result and queues nothing"
// @Success 200 {object} sendToContactResponse
// @Success 207 {object} sendToContactResponse
// @Failure 400 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Failure 413 {object} sendToContactResponse
// @Failure 422 {object} sendToContactResponse
// @Failure 503 {object} sendToContactResponse
// @Router /api/messages/send-to-contact [post]
func (s *Server) handleSendToContact(w http.ResponseWriter, r *http.Request) {
	s.touchOperatorActivity()

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
	if req.ContactID == "" && req.RawInterfaceID == "" {
		if req.GroupID != "" {
			// The dispatcher has no team fan-out: this used to reach it and
			// come back 503, which reads as a passing outage. [MESHSAT-1584]
			writeError(w, http.StatusBadRequest, "sending to a team (group_id) is not supported; send to each contact")
			return
		}
		writeError(w, http.StatusBadRequest, "contact_id or raw_interface_id is required")
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
		Precedence:     precedence,
		Strategy:       strategy,
		Source:         "api",
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	}
	if r.Header.Get("X-MeshSat-Client") == "spa" {
		opts.Source = "spa"
	}

	res, err := s.dispatcher.SendToRecipient(r.Context(), ref, []byte(req.Text), opts)
	resp := sendToContactResponseFrom(res)

	// The delivery-intent path's refusals: nothing was written. [MESHSAT-1581]
	var noRoute *engine.NoRouteError
	switch {
	case errors.Is(err, engine.ErrUnsupportedStrategy):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, database.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, err.Error())
		return
	case errors.As(err, &noRoute):
		resp.Queued = false
		for _, p := range noRoute.Plan {
			resp.PerBearer = append(resp.PerBearer, perBearerStatus{Kind: p.Kind, Error: "not used: " + p.Reason})
		}
		code := http.StatusUnprocessableEntity
		if noRoute.Oversize {
			code = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, code, resp)
		return
	}

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
		Strategy:          string(res.Strategy),
		Precedence:        string(res.Precedence),
		RequestedStrategy: string(res.RequestedStrategy),
		StrategyReason:    res.StrategyReason,
	}
	if sub := res.Intent; sub != nil {
		resp.SubmissionID = sub.Submission.ID
		resp.IntentIDs = sub.Submission.IntentIDs
		resp.Existing = sub.Existing
	} else if res.Legacy != nil {
		// A keyed send outside delivery intents (B10-R4). [MESHSAT-1584]
		resp.SubmissionID, resp.Existing = res.Legacy.ID, res.Existing
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
