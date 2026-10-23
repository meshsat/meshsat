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
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

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

	requested, effective string // the strategies the plan was made for
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
	reasonAddressHeld   = "skipped_address_held_locally" // the Hub sent it, another contact holds it (r20 F1)
	reasonUnsupported   = "skipped_unsupported"
	reasonOffline       = "skipped_offline"
	reasonOversize      = "skipped_oversize"
	reasonEncoding      = "skipped_encoding"
	reasonNoPrice       = "skipped_no_price"
	reasonBudget        = "skipped_budget"
)

// IntentRequest is a send-to-contact handed to the intent path.
type IntentRequest struct {
	Source         string // spa, api, hub
	IdempotencyKey string
	ContactID      string
	GroupID        string // a team send (C8): the TEAM group, no contact
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
	canon := map[string]any{
		"contact_id": r.ContactID, "payload_sha256": hex.EncodeToString(sum[:]),
		"precedence": string(r.Precedence), "strategy": string(r.Strategy), "max_cost_cents": cost,
	}
	if r.GroupID != "" {
		canon["group_id"] = r.GroupID // only then, so a contact send's hash is what it was
	}
	b, _ := json.Marshal(canon)
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
	subUUID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	ni, nre, err := e.prepareIntent(ctx, req, subUUID.String())
	if err != nil {
		return nil, err
	}
	if nre != nil {
		return nil, nre
	}
	sub := database.DeliverySubmission{ID: subUUID.String(), Source: req.Source, IdempotencyKey: req.IdempotencyKey,
		RequestSHA256: fp, IntentIDs: []string{ni.Intent.ID}}
	return e.create(sub, []database.NewIntent{ni})
}

// create writes a submission and its intents in one transaction, then
// starts the active ones.
func (e *Escalator) create(sub database.DeliverySubmission, intents []database.NewIntent) (*IntentSubmission, error) {
	existing, rowIDs, err := e.db.CreateSubmission(sub, intents)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		// A concurrent duplicate won the key: answer with it.
		return e.loadSubmission(existing)
	}
	out := &IntentSubmission{Submission: sub, RowIDs: rowIDs}
	for _, ni := range intents {
		stored, err := e.db.GetIntent(ni.Intent.ID)
		if err != nil {
			return nil, err
		}
		out.Intents = append(out.Intents, *stored)
		if stored.State != database.IntentActive {
			continue
		}
		e.mu.Lock()
		e.bases[stored.ID] = clockBase{ms: 0, at: e.clock.Now()}
		e.mu.Unlock()
		e.Notify(stored.ID)
	}
	return out, nil
}

// firstRound is round 1 of a plan (r3 section 3a), a copy: PRIMARY_ONLY
// the primary route, which must admit; ORDERED_FALLBACK the first route
// that admits; ALL_BEARERS every route that admits.
//
// excluded names the routes whose link is jammed or deaf now (the route
// gate): ORDERED_FALLBACK starts on the first admitted route not excluded,
// ALL_BEARERS sends on the admitted routes not excluded, and either falls
// back to every admitted route when all of them are, since a verdict can
// be wrong and the message must still leave. PRIMARY_ONLY is the
// operator's choice of route and ignores it. [MESHSAT-1650]
func firstRound(plan []database.RoutePlanEntry, effective directory.Strategy, excluded map[int]bool) []database.RoutePlanEntry {
	var first []database.RoutePlanEntry
	switch effective {
	case directory.StrategyPrimaryOnly:
		if len(plan) > 0 && plan[0].Admitted {
			first = append(first, plan[0])
		}
	case directory.StrategyOrderedFallback:
		var fallback []database.RoutePlanEntry
		for _, p := range plan {
			if !p.Admitted {
				continue
			}
			if !excluded[p.RouteIndex] {
				return append(first, p)
			}
			if fallback == nil {
				fallback = append(fallback, p)
			}
		}
		return fallback
	default:
		var all []database.RoutePlanEntry
		for _, p := range plan {
			if !p.Admitted {
				continue
			}
			all = append(all, p)
			if !excluded[p.RouteIndex] {
				first = append(first, p)
			}
		}
		if len(first) == 0 {
			return all
		}
	}
	return first
}

// excludedRoutes is the route gate's answer for each admitted route.
func (e *Escalator) excludedRoutes(plan []database.RoutePlanEntry) map[int]bool {
	m := map[int]bool{}
	for _, p := range plan {
		if p.Admitted {
			if ex, why := e.d.routeExcluded(p.InterfaceID); ex {
				m[p.RouteIndex] = true
				log.Info().Str("interface", p.InterfaceID).Str("why", why).Msg("delivery intents: route skipped in round 1")
			}
		}
	}
	return m
}

// prepareIntent builds one recipient's intent and its round-1 attempts, or
// says why no route admits (nre). An error is anything else; nothing is
// written either way.
func (e *Escalator) prepareIntent(ctx context.Context, req IntentRequest, subID string) (database.NewIntent, *NoRouteError, error) {
	if e.d == nil || e.d.recipientResolver == nil {
		return database.NewIntent{}, nil, errors.New("delivery intents: no recipient resolver")
	}
	contact, err := e.d.recipientResolver.Resolve(ctx, req.ContactID)
	if err != nil {
		return database.NewIntent{}, nil, fmt.Errorf("resolve contact %s: %w", req.ContactID, err)
	}
	if contact == nil || len(contact.Addresses) == 0 {
		return database.NewIntent{}, nil, fmt.Errorf("contact %s has %w", req.ContactID, errNoAddresses)
	}
	requested, effective, reason, policyVersion, capCents, err := e.resolveIntentStrategy(ctx, req)
	if err != nil {
		return database.NewIntent{}, nil, err
	}

	intentUUID, err := uuid.NewV7()
	if err != nil {
		return database.NewIntent{}, nil, err
	}
	sum := sha256.Sum256(req.Body)
	in := database.DeliveryIntent{
		ID:                intentUUID.String(),
		SubmissionID:      subID,
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
	// By default an admitted Hub app route goes first (C4, your Q2 answer). An explicit
	// strategy keeps the contact's order, and so does PRIMARY_ONLY: the contact's primary
	// route is the one it names, never one the plan moved there. [MESHSAT-1584]
	in.RoutePlan = e.routePlan(contact.Addresses, req.Body, req.Strategy == "" &&
		(effective == directory.StrategyOrderedFallback || effective == directory.StrategyAllBearers))
	if in.CapMinor != nil {
		// A paid route whose one submission costs more than the cap is
		// never tried (r3 D8). [MESHSAT-1584]
		for i := range in.RoutePlan {
			if p := &in.RoutePlan[i]; p.Admitted && p.Paid && p.EstMinor > *in.CapMinor {
				p.Admitted, p.Reason = false, reasonBudget
			}
		}
	}

	excluded := e.excludedRoutes(in.RoutePlan)
	if len(firstRound(in.RoutePlan, effective, excluded)) == 0 {
		nre := &NoRouteError{Plan: in.RoutePlan, Oversize: true}
		for _, p := range in.RoutePlan {
			if p.Reason != reasonOversize {
				nre.Oversize = false
			}
		}
		if effective == directory.StrategyPrimaryOnly {
			nre.Detail = "PRIMARY_ONLY sends on the contact's primary route only"
		}
		nre.requested, nre.effective = in.RequestedStrategy, in.EffectiveStrategy
		return database.NewIntent{Intent: in}, nre, nil
	}
	for i := range in.RoutePlan {
		if in.RoutePlan[i].Admitted && in.RoutePlan[i].AdapterMode == modeLXMF {
			ts, err := e.db.AllocateWireTimestampMs(e.clock.Wall().UnixMilli())
			if err != nil {
				return database.NewIntent{}, nil, err
			}
			in.RoutePlan[i].WireTimestampMs = ts
		}
	}
	first := firstRound(in.RoutePlan, effective, excluded)
	var rows []database.MessageDelivery
	for i, p := range first {
		rows = append(rows, e.buildRow(in, p, 1, i+1))
	}
	return database.NewIntent{Intent: in, FirstRows: rows}, nil, nil
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

// resolveIntentStrategy is r3 D12 on the intent path: PRIMARY_ONLY,
// ALL_BEARERS and ORDERED_FALLBACK are implemented; an explicit request
// for any other is refused before anything is written; a policy row
// with another resolves as StoredStrategy says. The seeded Priority
// ORDERED_FALLBACK, which did ALL_BEARERS, is ALL_BEARERS since v65 (B5).
func (e *Escalator) resolveIntentStrategy(ctx context.Context, req IntentRequest) (requested, effective directory.Strategy, reason, version string, capCents *int, err error) {
	r, err := e.d.resolveStrategy(ctx, req.ContactID, req.Precedence, req.Strategy, req.MaxCostCents, true)
	if err != nil {
		return req.Strategy, "", "", "", nil, err
	}
	return r.Requested, r.Effective, r.Reason, r.Version, r.CapCents, nil
}

// routePlan orders a contact's addresses by rank (kind as the tiebreak, as
// selectAddresses does) and admits each for its route (r3 D9).
func (e *Escalator) routePlan(addrs []directory.Address, payload []byte, hubRouteFirst bool) []database.RoutePlanEntry {
	sorted := append([]directory.Address(nil), addrs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].PrimaryRank != sorted[j].PrimaryRank {
			return sorted[i].PrimaryRank < sorted[j].PrimaryRank
		}
		return sorted[i].Kind < sorted[j].Kind
	})
	admitted := make([]database.RoutePlanEntry, 0, len(sorted))
	for _, a := range sorted {
		admitted = append(admitted, e.admit(a, payload))
	}
	if hubRouteFirst {
		admitted = hubFirst(admitted)
	}
	seen := map[string]bool{}
	var plan []database.RoutePlanEntry
	for i, p := range admitted {
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
		if _, ok := e.d.mesh.(meshPreparedSender); !ok || e.d.mesh == nil {
			p.Reason = reasonUnsupported // no send under a packet id chosen first (R-3)
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
	case directory.KindAPRS:
		// Stage 1b: an APRS message with our own message id; an ack from
		// the addressed station is its receipt: acked by the station,
		// unauthenticated, never "delivered" (r3 D11, APRS-R3). [MESHSAT-1584]
		p.AdapterMode = modeAPRS
		p.CompletionRule = database.OutcomeAckedStation
		e.admitGateway(&p, a.Kind, payload)
		return p
	case directory.KindMQTT:
		// Stage 2: "bridge:<id>" through the Hub app route (C4); any other MQTT address
		// has no route on the intent path. [MESHSAT-1584]
		if !isHubAddress(a) {
			p.Reason = reasonNotEnabled
			return p
		}
		e.admitHub(&p, payload)
		return p
	case directory.KindSMS:
		// Stage 1b, a paid route (B9): one SMS through the kit's modem;
		// accepted by the network is all it knows (no status reports yet,
		// B7(i)), so no completion rule. [MESHSAT-1584]
		p.AdapterMode = modeSMS
		e.admitGateway(&p, a.Kind, payload)
		return p
	case directory.KindIridiumIMT:
		// A paid route (B9): one MO through the kit's 9704, only to the
		// destination recorded for its messages; acknowledged by the
		// network is all it knows, so no completion rule. [MESHSAT-1584]
		p.AdapterMode = modeIMT
		e.admitGateway(&p, a.Kind, payload)
		return p
	case directory.KindTETRA:
		// A free route: one SDS through the kit's TETRA radio to a talk
		// group (or later an ITSI); taken by the radio is all a group SDS
		// says, so no completion rule. [MESHSAT-1584]
		p.AdapterMode = modeTETRA
		e.admitGateway(&p, a.Kind, payload)
		return p
	case directory.KindZigBee:
		// A free route: one frame through the kit's ZigBee coordinator to
		// one device; written to the coordinator is all it knows (no APS
		// acknowledgement), so no completion rule. [MESHSAT-1584]
		p.AdapterMode = modeZB
		e.admitGateway(&p, a.Kind, payload)
		return p
	case directory.KindCell:
		// A free route: one text to a phone on the MeshSat 2G cell; in
		// forward mode the cell answers once the phone acknowledged it, so
		// the route completes delivered to the handset. [MESHSAT-1584]
		p.AdapterMode = modeCell
		p.CompletionRule = database.OutcomeDeliveredHandset
		e.admitGateway(&p, a.Kind, payload)
		return p
	case directory.KindIridiumSBD:
		// A paid route (B9): one SBD session through the kit's 9603, only
		// to the destination recorded for its messages; taken by the
		// Iridium gateway is all it knows, so no completion rule.
		// [MESHSAT-1584]
		p.AdapterMode = modeSBD
		e.admitGateway(&p, a.Kind, payload)
		return p
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
	// A link whose band is jammed or whose receiver is deaf is not the next
	// route; the walk takes the one after it. [MESHSAT-1650]
	if excluded, _ := e.d.routeExcluded(p.InterfaceID); excluded {
		return false
	}
	iface, err := e.d.db.GetInterface(p.InterfaceID)
	if err != nil || !iface.Enabled {
		return false
	}
	switch p.AdapterMode {
	case modeMeshUnicast:
		_, ok := e.d.mesh.(meshPreparedSender)
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
	if gatewayModes[p.AdapterMode] {
		ad := e.d.intentAdapter(p.InterfaceID)
		if ad == nil {
			return false
		}
		if billed(ad) {
			t, err := e.d.db.CurrentTariff(p.InterfaceID)
			return err == nil && t != nil
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
// UUIDv7 id (the first 48 bits are milliseconds since the epoch). Only a
// fallback for a plan without an allocated wire timestamp: two intents in
// one millisecond share it (red-team F-4), so Submit allocates one per
// route (RoutePlanEntry.WireTimestampMs).
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
		// The route's wire timestamp, allocated at submission (one hash
		// for every retry, a distinct one per intent), and free_only:
		// every packet of the send stays off paid links. [MESHSAT-1584]
		ts := intentTimestamp(in)
		if p.WireTimestampMs > 0 {
			ts = float64(p.WireTimestampMs) / 1000
		}
		row.Class = database.DeliveryClassLXMF
		row.Payload, _ = json.Marshal(map[string]any{"content": text, "title": "", "ts": ts, "free_only": true})
	}
	if e.d != nil && e.d.signing != nil {
		row.Signature = e.d.signing.Sign(row.Payload)
		row.SignerID = e.d.signing.SignerID()
	}
	return row
}

// meshCorrKind and meshCorrWindowS: a mesh transmission is named by its
// packet id on its interface for as long as the transmission is kept
// (window 0: no time bound). An answer's age says nothing about which send
// it answers, so an id we pick is never one a kept transmission used, and
// an id the radio reused names every kept transmission that had it: the
// answer is ambiguous and completes nothing (red-team R-3). The one
// assumption: no answer arrives after its transmission was pruned
// (retention, at least a day). [MESHSAT-1584]
const (
	meshCorrKind    = "mesh_packet_id"
	meshCorrWindowS = 0
)

// classifyMeshReceipt says what a ROUTING_APP answer stored as e proves of
// the one transmission t it names: from the destination with no error,
// the destination radio's acknowledgement; with no error from another node
// (a relay, our own radio's implicit acknowledgement), nothing; any error,
// that no acknowledgement will come.
func classifyMeshReceipt(t database.DeliveryTransmission, e database.DeliveryEvidence) (event, source, trust, completes string) {
	reason, name := uint64(0), "NONE"
	if parts := strings.SplitN(e.Event, ":", 3); len(parts) == 3 && parts[0] == "routing" {
		reason, _ = strconv.ParseUint(parts[1], 10, 32)
		name = parts[2]
	}
	switch {
	case reason == 0 && e.CorrPeer == t.CorrPeer:
		return "ack", "destination_node", "unauthenticated", database.OutcomeDeliveredDevice
	case reason == 0:
		return "relay_ack", "radio", "unauthenticated", ""
	case e.CorrPeer == t.CorrPeer:
		return "nak:" + name, "destination_node", "unauthenticated", ""
	case e.Source == meshOwnRadio && meshRefusedNothingSent(reason, t, e):
		// Our own radio refused the packet and nothing of it was sent
		// (live, 17 Oct 2026: a DM to a node whose public key the radio did
		// not have, PKI_SEND_FAIL_PUBLIC_KEY): the transmission is not
		// sent, whatever the serial handoff said. [MESHSAT-1584]
		return "dropped:" + name, meshOwnRadio, "unauthenticated", ""
	}
	return "nak:" + name, "radio", "unauthenticated", ""
}

// meshOwnRadio is the source of a ROUTING_APP answer our own radio
// addressed to itself (from = to = our node): the radio's verdict on a
// packet we handed it, not a relay's or the destination's.
const meshOwnRadio = "own_radio"

// Which of our own radio's refusals prove that nothing of the packet went on
// air, read from the firmware the kits run (meshsat-firmware, upstream
// 2.8.0.47db0e3; review RADIO-R1):
//
//   - NO_INTERFACE 4 (Router::sendLocal) and RATE_LIMIT_EXCEEDED 38 (the
//     phone API's text throttle) only ever refuse the packet as the Bridge
//     hands it over: nothing was sent, ever.
//   - NO_CHANNEL 6, TOO_LARGE 7, PKI_FAILED 34 and PKI_SEND_FAIL_PUBLIC_KEY
//     39 come from the encode step in Router::send, which every copy goes
//     through, retransmissions included (ReliableRouter keeps the decoded
//     packet and re-sends it). Such a refusal stops the retransmissions, but
//     proves nothing earlier went only when it refuses the first copy: the
//     first retransmission waits at least PROCESSING_TIME_MSEC, 4.5 s, plus
//     two airtimes (RadioInterface::getRetransmissionMsec). So it counts
//     when it came within meshFirstCopyS of the handoff.
//   - DUTY_CYCLE_LIMIT 9 never proves it: Router::send checks it on every
//     copy, and ReliableRouter keeps the packet for a later retry after it
//     (it does not stop retransmission on that error), so a copy may have
//     gone before it or may go after it.
//
// The same codes from a relay mean the packet left us; MAX_RETRANSMIT and
// the rest mean it was sent and nobody answered.
var (
	meshRefusedAtHandoff = map[uint64]bool{4: true, 38: true}
	meshRefusedAtEncode  = map[uint64]bool{6: true, 7: true, 34: true, 39: true}
)

// meshFirstCopyS bounds, in the whole seconds the ledger stores, how long
// after the handoff a refusal can only be about the first copy: 3 observed
// seconds is under 4 real ones, below the 4.5 s before any retransmission.
const meshFirstCopyS = 3

func meshRefusedNothingSent(reason uint64, t database.DeliveryTransmission, e database.DeliveryEvidence) bool {
	if meshRefusedAtHandoff[reason] {
		return true
	}
	if !meshRefusedAtEncode[reason] || t.HandoffAt == nil {
		return false
	}
	handoff, ok := database.ParseTime(*t.HandoffAt)
	if !ok {
		return false
	}
	at := time.Now().UTC()
	if e.ReceivedAt != "" {
		if r, ok := database.ParseTime(e.ReceivedAt); ok {
			at = r
		}
	}
	d := at.Sub(handoff)
	return d >= 0 && d <= meshFirstCopyS*time.Second
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
	reason, name := uint32(0), "NONE"
	if msg.Routing != nil {
		reason = msg.Routing.ErrorReason
		if msg.Routing.ErrorName != "" {
			name = msg.Routing.ErrorName
		}
	}
	// What the answer said, kept with it: an answer that overtakes its
	// send's finalization is matched later from this (reconcile).
	source := "radio"
	if msg.From != 0 && msg.From == msg.To {
		source = meshOwnRadio
	}
	ev := database.DeliveryEvidence{
		Source: source, Event: fmt.Sprintf("routing:%d:%s", reason, name), Trust: "unauthenticated",
		CorrKind: meshCorrKind, CorrValue: fmt.Sprintf("%08x", msg.RequestID), CorrScope: ifaceID, CorrPeer: fmt.Sprintf("!%08x", msg.From),
	}
	stored, err := e.db.RecordEvidence(ev, meshCorrWindowS, func(t database.DeliveryTransmission) (string, string, string, string) {
		return classifyMeshReceipt(t, ev)
	})
	if err != nil {
		return
	}
	if stored.Matched {
		e.Notify(stored.IntentID)
	}
}
