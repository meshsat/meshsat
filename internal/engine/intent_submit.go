package engine

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/lxmf"
	"meshsat/internal/transport"
	"meshsat/internal/types"
)

// Errors of an intent submission (architecture r3 T2, D12).
var (
	// ErrUnsupportedStrategy: an explicit request names a strategy the
	// intent path does not implement. Nothing is written.
	ErrUnsupportedStrategy = errors.New("strategy not supported")
	// ErrNoRouteAdmits: no route of the contact can carry the message.
	// Nothing is written. Oversize says every refusal was the size.
	ErrNoRouteAdmits = errors.New("no route can carry this message")
)

// NoRouteError is ErrNoRouteAdmits with the reason per route.
type NoRouteError struct {
	Plan     []database.RoutePlanEntry
	Oversize bool
	Detail   string
}

func (e *NoRouteError) Error() string {
	var parts []string
	for _, p := range e.Plan {
		parts = append(parts, fmt.Sprintf("%s: %s", p.Kind, p.Reason))
	}
	msg := ErrNoRouteAdmits.Error()
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	if len(parts) > 0 {
		msg += " (" + strings.Join(parts, "; ") + ")"
	}
	return msg
}

func (e *NoRouteError) Unwrap() error { return ErrNoRouteAdmits }

// Route modes of stage 1a, and the reasons a route is not admitted
// (r3 D11's list, plus the stage's own).
const (
	modeMeshUnicast = "mesh_unicast"
	modeLXMF        = "lxmf"

	reasonNotEnabled    = "skipped_route_not_enabled" // stage 1a carries Meshtastic unicast and LXMF only
	reasonSameInterface = "skipped_same_interface"    // a plan never names one interface twice (r3 D3)
	reasonBadAddress    = "skipped_bad_address"
	reasonUnsupported   = "skipped_unsupported"
	reasonOffline       = "skipped_offline"
	reasonOversize      = "skipped_oversize"
	reasonEncoding      = "skipped_encoding"
	reasonNoPrice       = "skipped_no_price"
)

// IntentRequest is a send-to-contact handed to the intent path.
type IntentRequest struct {
	Source         string // spa, api, hub
	IdempotencyKey string
	ContactID      string
	Body           []byte
	Precedence     types.Precedence
	Strategy       directory.Strategy // explicit; empty = the policy's
	MaxCostCents   *int
}

// IntentSubmission is what a submission wrote, or found under its key.
type IntentSubmission struct {
	Submission database.DeliverySubmission
	Existing   bool
	Intents    []database.DeliveryIntent
	RowIDs     [][]int64
}

// fingerprint is the canonical request's hash (r3 D2): recipients,
// payload and options.
func (r IntentRequest) fingerprint() string {
	sum := sha256.Sum256(r.Body)
	var cost any
	if r.MaxCostCents != nil {
		cost = *r.MaxCostCents
	}
	b, _ := json.Marshal(map[string]any{
		"contact_id": r.ContactID, "payload_sha256": hex.EncodeToString(sum[:]),
		"precedence": string(r.Precedence), "strategy": string(r.Strategy), "max_cost_cents": cost,
	})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Submit turns a send-to-contact into a submission with one intent and its
// round-1 attempts, written in one transaction (T1), or nothing (T2). The
// same source and idempotency key with the same request returns the
// original submission without resolving anything again; with a different
// request, database.ErrIdempotencyConflict.
func (e *Escalator) Submit(ctx context.Context, req IntentRequest) (*IntentSubmission, error) {
	if req.Source == "" {
		req.Source = "api"
	}
	if req.Precedence == "" {
		req.Precedence = types.DefaultPrecedence
	}
	fp := req.fingerprint()
	if req.IdempotencyKey != "" {
		prev, err := e.db.GetSubmissionByKey(req.Source, req.IdempotencyKey)
		if err != nil {
			return nil, err
		}
		if prev != nil {
			if prev.RequestSHA256 != fp {
				return nil, database.ErrIdempotencyConflict
			}
			return e.loadSubmission(prev)
		}
	}
	if e.d == nil || e.d.recipientResolver == nil {
		return nil, errors.New("delivery intents: no recipient resolver")
	}
	contact, err := e.d.recipientResolver.Resolve(ctx, req.ContactID)
	if err != nil {
		return nil, fmt.Errorf("resolve contact %s: %w", req.ContactID, err)
	}
	if contact == nil || len(contact.Addresses) == 0 {
		return nil, fmt.Errorf("contact %s has no addresses", req.ContactID)
	}
	requested, effective, reason, policyVersion, capCents, err := e.resolveIntentStrategy(ctx, req)
	if err != nil {
		return nil, err
	}

	intentUUID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	subUUID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(req.Body)
	in := database.DeliveryIntent{
		ID:                intentUUID.String(),
		SubmissionID:      subUUID.String(),
		ContactID:         req.ContactID,
		Precedence:        string(req.Precedence),
		Class:             database.DeliveryClassMessage,
		Payload:           append([]byte(nil), req.Body...),
		PayloadSHA256:     hex.EncodeToString(sum[:]),
		RequestedStrategy: string(requested),
		EffectiveStrategy: string(effective),
		StrategyReason:    reason,
		PolicyVersion:     policyVersion,
		Currency:          "EUR",
		ExpiryAllowS:      e.cfg.ExpiryAllowS,
		State:             database.IntentActive,
		Outcome:           database.OutcomePending,
		Version:           1,
		ActiveRound:       1,
	}
	if capCents != nil {
		c := int64(*capCents)
		in.CapMinor = &c
	}
	wall := e.clock.Wall().UTC().Format("2006-01-02 15:04:05")
	in.HeartbeatWallAt = &wall
	in.HeartbeatWallTrusted = e.clock.WallTrusted()
	in.RoutePlan = e.routePlan(contact.Addresses, req.Body)

	// Round 1 (r3 section 3a): PRIMARY_ONLY the primary route, which
	// must admit; ORDERED_FALLBACK the first route that admits;
	// ALL_BEARERS every route that admits.
	var first []database.RoutePlanEntry
	switch effective {
	case directory.StrategyPrimaryOnly:
		if len(in.RoutePlan) > 0 && in.RoutePlan[0].Admitted {
			first = in.RoutePlan[:1]
		}
	case directory.StrategyOrderedFallback:
		for _, p := range in.RoutePlan {
			if p.Admitted {
				first = []database.RoutePlanEntry{p}
				break
			}
		}
	default:
		for _, p := range in.RoutePlan {
			if p.Admitted {
				first = append(first, p)
			}
		}
	}
	if len(first) == 0 {
		nre := &NoRouteError{Plan: in.RoutePlan, Oversize: true}
		for _, p := range in.RoutePlan {
			if p.Reason != reasonOversize {
				nre.Oversize = false
			}
		}
		if effective == directory.StrategyPrimaryOnly {
			nre.Detail = "PRIMARY_ONLY sends on the contact's primary route only"
		}
		return nil, nre
	}
	var rows []database.MessageDelivery
	for i, p := range first {
		rows = append(rows, e.buildRow(in, p, 1, i+1))
	}
	sub := database.DeliverySubmission{ID: subUUID.String(), Source: req.Source, IdempotencyKey: req.IdempotencyKey,
		RequestSHA256: fp, IntentIDs: []string{in.ID}}
	existing, rowIDs, err := e.db.CreateSubmission(sub, []database.NewIntent{{Intent: in, FirstRows: rows}})
	if err != nil {
		return nil, err
	}
	if existing != nil {
		// A concurrent duplicate won the key: answer with it.
		return e.loadSubmission(existing)
	}
	e.mu.Lock()
	e.bases[in.ID] = clockBase{ms: 0, at: e.clock.Now()}
	e.mu.Unlock()
	stored, err := e.db.GetIntent(in.ID)
	if err != nil {
		return nil, err
	}
	e.Notify(in.ID)
	return &IntentSubmission{Submission: sub, Intents: []database.DeliveryIntent{*stored}, RowIDs: rowIDs}, nil
}

func (e *Escalator) loadSubmission(s *database.DeliverySubmission) (*IntentSubmission, error) {
	out := &IntentSubmission{Submission: *s, Existing: true}
	for _, id := range s.IntentIDs {
		in, err := e.db.GetIntent(id)
		if err != nil {
			return nil, err
		}
		out.Intents = append(out.Intents, *in)
		snap, err := e.db.LoadIntentSnapshot(id)
		if err != nil {
			return nil, err
		}
		var ids []int64
		for _, r := range snap.Rows {
			if r.Round == 1 {
				ids = append(ids, r.ID)
			}
		}
		out.RowIDs = append(out.RowIDs, ids)
	}
	return out, nil
}

// resolveIntentStrategy is r3 D12 on the intent path. Supported:
// PRIMARY_ONLY, ALL_BEARERS, ORDERED_FALLBACK. An explicit request for any
// other is refused before anything is written. A policy row with an
// unsupported strategy, and a precedence row that still holds its seeded
// ORDERED_FALLBACK (whose behaviour until now was ALL_BEARERS; re-seeding
// it is the owner's call, r3 D12), resolve to ALL_BEARERS with
// strategy_reason legacy_compat_all_bearers.
func (e *Escalator) resolveIntentStrategy(ctx context.Context, req IntentRequest) (requested, effective directory.Strategy, reason, version string, capCents *int, err error) {
	capCents = req.MaxCostCents
	supported := func(s directory.Strategy) bool {
		return s == directory.StrategyPrimaryOnly || s == directory.StrategyAllBearers || s == directory.StrategyOrderedFallback
	}
	if req.Strategy != "" {
		if !supported(req.Strategy) {
			return req.Strategy, "", "", "", nil, fmt.Errorf("%w: %s (supported: PRIMARY_ONLY, ALL_BEARERS, ORDERED_FALLBACK)", ErrUnsupportedStrategy, req.Strategy)
		}
		return req.Strategy, req.Strategy, "requested", "", capCents, nil
	}
	r := e.d.recipientResolver
	for _, sc := range []struct {
		scope directory.PolicyScope
		id    string
	}{{directory.ScopeContact, req.ContactID}, {directory.ScopePrecedence, string(req.Precedence)}, {directory.ScopeDefault, ""}} {
		p, perr := r.GetPolicy(ctx, sc.scope, sc.id)
		if perr != nil || p == nil || p.Strategy == "" {
			continue
		}
		version = p.ID + "@" + p.UpdatedAt
		if capCents == nil {
			capCents = p.MaxCostCents
		}
		seeded := sc.scope == directory.ScopePrecedence && p.UpdatedAt == p.CreatedAt
		switch {
		case supported(p.Strategy) && !(seeded && p.Strategy == directory.StrategyOrderedFallback):
			return p.Strategy, p.Strategy, "policy:" + string(sc.scope), version, capCents, nil
		case p.Strategy == directory.StrategyOrderedFallback, p.Strategy == directory.StrategyAnyReachable, p.Strategy == directory.StrategyHeMBBonded:
			return p.Strategy, directory.StrategyAllBearers, "legacy_compat_all_bearers", version, capCents, nil
		default:
			return p.Strategy, directory.StrategyPrimaryOnly, "unknown_strategy_primary_only", version, capCents, nil
		}
	}
	return "", directory.StrategyPrimaryOnly, "default", "", capCents, nil
}

// routePlan orders a contact's addresses by rank (kind as the tiebreak, as
// selectAddresses does) and admits each for its route (r3 D9).
func (e *Escalator) routePlan(addrs []directory.Address, payload []byte) []database.RoutePlanEntry {
	sorted := append([]directory.Address(nil), addrs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].PrimaryRank != sorted[j].PrimaryRank {
			return sorted[i].PrimaryRank < sorted[j].PrimaryRank
		}
		return sorted[i].Kind < sorted[j].Kind
	})
	seen := map[string]bool{}
	var plan []database.RoutePlanEntry
	for i, a := range sorted {
		p := e.admit(a, payload)
		p.RouteIndex = i + 1
		if p.Admitted && seen[p.InterfaceID] {
			p.Admitted, p.Reason = false, reasonSameInterface
		}
		if p.Admitted {
			seen[p.InterfaceID] = true
		}
		plan = append(plan, p)
	}
	return plan
}

// meshIDSender is a mesh transport that sends under its own packet id.
type meshIDSender interface {
	SendMessageID(ctx context.Context, req transport.SendRequest) (uint32, error)
}

// paidPathChecker is an LXMF sender that knows whether its path to a
// destination leaves on a paid link.
type paidPathChecker interface {
	CrossesPaidLink(destHex string) bool
}

// admit decides whether one address's route can carry the payload as it
// is, without cutting or changing it (r3 D9). Stage 1a admits the two free
// routes only: Meshtastic unicast and LXMF on a path without a paid link.
func (e *Escalator) admit(a directory.Address, payload []byte) database.RoutePlanEntry {
	p := database.RoutePlanEntry{Kind: string(a.Kind), AddressID: a.ID,
		QueueAllowS: e.cfg.QueueAllowS, ReceiptAllowS: e.cfg.ReceiptAllowS}
	dest, err := contactDestination(a)
	if err != nil {
		p.Reason = reasonBadAddress
		return p
	}
	p.Destination = dest
	switch a.Kind {
	case directory.KindMeshtastic:
		p.AdapterMode = modeMeshUnicast
		p.CompletionRule = database.OutcomeDeliveredDevice
		if dest == "!ffffffff" {
			p.Reason = reasonBadAddress // a broadcast has no receipt
			return p
		}
		ifaceID, err := e.d.interfaceForKind(a.Kind)
		if err != nil {
			p.Reason = reasonOffline
			return p
		}
		p.InterfaceID = ifaceID
		iface, err := e.d.db.GetInterface(ifaceID)
		if err != nil || !iface.Enabled {
			p.Reason = reasonOffline
			return p
		}
		if _, ok := e.d.mesh.(meshIDSender); !ok || e.d.mesh == nil {
			p.Reason = reasonUnsupported
			return p
		}
		size, chainSHA, err := e.meshWireSize(iface.EgressTransforms, payload)
		if err != nil {
			p.Reason = reasonEncoding
			return p
		}
		p.EstWireBytes, p.TransformSHA256 = size, chainSHA
		if size > e.cfg.MeshMTU {
			p.Reason = reasonOversize
			return p
		}
	case directory.KindReticulum:
		p.AdapterMode = modeLXMF
		p.CompletionRule = database.OutcomeAcceptedDestinationNode
		p.InterfaceID = "lxmf_0"
		iface, err := e.d.db.GetInterface("lxmf_0")
		if err != nil || !iface.Enabled {
			p.Reason = reasonOffline
			return p
		}
		if e.d.lxmf == nil {
			p.Reason = reasonUnsupported
			return p
		}
		if pc, ok := e.d.lxmf.(paidPathChecker); ok && pc.CrossesPaidLink(dest) {
			p.Reason, p.Paid = reasonNoPrice, true // a paid link has no tariff in stage 1a (B9)
			return p
		}
		p.EstWireBytes = len(payload)
		if len(payload) > lxmf.MaxTransferBytes {
			p.Reason = reasonOversize
			return p
		}
	default:
		p.Reason = reasonNotEnabled
		return p
	}
	p.Admitted = true
	return p
}

// meshWireSize is the payload's size on the mesh after the interface's
// egress chain (the real chain; its output length does not depend on the
// key) plus the version byte the worker adds after a chain.
func (e *Escalator) meshWireSize(chain string, payload []byte) (int, string, error) {
	if chain == "" || chain == "[]" {
		return len(payload), "", nil
	}
	sum := sha256.Sum256([]byte(chain))
	if e.d.transforms == nil {
		return 0, "", errors.New("no transform pipeline")
	}
	out, err := e.d.transforms.ApplyEgress(payload, chain)
	if err != nil {
		return 0, "", err
	}
	return len(out) + 1, hex.EncodeToString(sum[:]), nil
}

// usable re-checks, at escalation time, that an admitted route can still be
// tried: its interface is on, the mesh transport still sends under its own
// packet ids, and an LXMF path has not moved onto a paid link.
func (e *Escalator) usable(p database.RoutePlanEntry) bool {
	if !p.Admitted {
		return false
	}
	iface, err := e.d.db.GetInterface(p.InterfaceID)
	if err != nil || !iface.Enabled {
		return false
	}
	switch p.AdapterMode {
	case modeMeshUnicast:
		_, ok := e.d.mesh.(meshIDSender)
		return ok
	case modeLXMF:
		if e.d.lxmf == nil {
			return false
		}
		if pc, ok := e.d.lxmf.(paidPathChecker); ok && pc.CrossesPaidLink(p.Destination) {
			return false
		}
		return true
	}
	return false
}

// eligibility is usable for every route of a plan, read before the
// Escalator's transaction (inside it only the transaction may be used).
func (e *Escalator) eligibility(in *database.DeliveryIntent) map[int]bool {
	m := map[int]bool{}
	for _, p := range in.RoutePlan {
		m[p.RouteIndex] = e.usable(p)
	}
	return m
}

// intentTimestamp is the intent's creation time in seconds, from its
// UUIDv7 id (the first 48 bits are milliseconds since the epoch): the LXMF
// timestamp of every attempt, so every copy is one message (B7(d)).
func intentTimestamp(in database.DeliveryIntent) float64 {
	if u, err := uuid.Parse(in.ID); err == nil && u.Version() == 7 {
		var b [8]byte
		copy(b[2:], u[0:6])
		return float64(binary.BigEndian.Uint64(b[:])) / 1000
	}
	if t, err := time.Parse("2006-01-02 15:04:05", in.CreatedAt); err == nil {
		return float64(t.Unix())
	}
	return float64(time.Now().Unix())
}

// buildRow is an attempt row for one route of an intent, built from the
// intent's payload, never from another attempt's bytes (r3 D2), with the
// shape QueueDirectSendTo gives a direct send: priority 1, never Critical,
// never class hub_uplink, so no attempt can take the SOS's budget
// exception (r3 D8, D13).
func (e *Escalator) buildRow(in database.DeliveryIntent, p database.RoutePlanEntry, round, attemptNo int) database.MessageDelivery {
	text := string(in.Payload)
	preview := text
	if len(preview) > 200 {
		preview = preview[:200]
	}
	row := database.MessageDelivery{
		MsgRef:      fmt.Sprintf("intent-%s-%d", in.ID, attemptNo),
		Channel:     p.InterfaceID,
		Status:      "queued",
		Priority:    1,
		Payload:     append([]byte(nil), in.Payload...),
		TextPreview: preview,
		MaxRetries:  3,
		QoSLevel:    1,
		Precedence:  in.Precedence,
		Destination: p.Destination,
		Class:       database.DeliveryClassMessage,
		IntentID:    in.ID,
		Round:       round,
		AttemptNo:   attemptNo,
		RouteIndex:  p.RouteIndex,
	}
	if p.AdapterMode == modeLXMF {
		row.Class = database.DeliveryClassLXMF
		row.Payload, _ = json.Marshal(map[string]any{"content": text, "title": "", "ts": intentTimestamp(in)})
	}
	if e.d != nil && e.d.signing != nil {
		row.Signature = e.d.signing.Sign(row.Payload)
		row.SignerID = e.d.signing.SignerID()
	}
	return row
}

// MeshRoutingReceipt takes a ROUTING_APP packet from the mesh (r3 B7(a)):
// stored as evidence and matched by its request_id to the transmission
// that sent under that packet id on this interface. From the destination
// with no error it is the destination radio's acknowledgement:
// delivered_device (the radio decoded it, not the app; anyone with the
// channel key could forge it, so its trust is unauthenticated). With no
// error from another node (our own radio's implicit acknowledgement, a
// relay) it completes nothing. Any error says no acknowledgement will come.
func (e *Escalator) MeshRoutingReceipt(ifaceID string, msg transport.MeshMessage) {
	if e == nil || msg.RequestID == 0 {
		return
	}
	reason, name := uint32(0), "ACK"
	if msg.Routing != nil {
		reason, name = msg.Routing.ErrorReason, msg.Routing.ErrorName
	}
	from := fmt.Sprintf("!%08x", msg.From)
	ev := database.DeliveryEvidence{
		Source: "radio", Event: "routing:" + name, Trust: "unauthenticated",
		CorrKind: "mesh_packet_id", CorrValue: fmt.Sprintf("%08x", msg.RequestID), CorrScope: ifaceID, CorrPeer: from,
	}
	stored, err := e.db.RecordEvidence(ev, func(t database.DeliveryTransmission) (string, string, string, string) {
		switch {
		case reason == 0 && from == t.CorrPeer:
			return "ack", "destination_node", "unauthenticated", database.OutcomeDeliveredDevice
		case reason == 0:
			return "relay_ack", "radio", "unauthenticated", ""
		case from == t.CorrPeer:
			return "nak:" + name, "destination_node", "unauthenticated", ""
		default:
			return "nak:" + name, "radio", "unauthenticated", ""
		}
	})
	if err != nil {
		return
	}
	if stored.Matched {
		e.Notify(stored.IntentID)
	}
}
