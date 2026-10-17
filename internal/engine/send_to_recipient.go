package engine

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/types"
)

// RecipientRef identifies the target of a SendToRecipient call. Exactly
// one of ContactID, GroupID, or Raw must be populated. Raw is the escape
// hatch for operators who have a bearer + address string but no contact
// record yet — the dispatcher queues directly to the named interface
// without consulting the directory. [MESHSAT-544 / S2-01]
type RecipientRef struct {
	ContactID string
	GroupID   string
	Raw       *RawRecipient
}

// RawRecipient is the escape hatch payload — a specific interface and
// an opaque address string. The dispatcher queues the message against
// this interface via [Dispatcher.QueueDirectSend] without any further
// resolution.
type RawRecipient struct {
	InterfaceID string
	Address     string
}

// SendOptions controls how a SendToRecipient call is turned into
// delivery rows. Zero values mean "use the resolved policy default"
// (for Strategy and Precedence) or "no constraint" (for MaxCostCents
// and TTL). [MESHSAT-544]
type SendOptions struct {
	Precedence   types.Precedence
	Strategy     directory.Strategy // zero value triggers policy resolution
	MaxCostCents *int
	// Class and MaxRetries apply to the Raw escape hatch only (OOB
	// management replies use them). [MESHSAT-756]
	Class      string
	MaxRetries int
	// Source and IdempotencyKey scope a retried request to its first
	// submission (spa, api, hub), on the delivery-intent path and, since
	// B10-R4, outside it too. [MESHSAT-1581, MESHSAT-1584]
	Source         string
	IdempotencyKey string
}

// SendResult reports what the dispatcher actually did on behalf of a
// SendToRecipient call. DeliveryIDs is keyed by bearer kind so callers
// can render per-bearer status (delivery ticks, errors) in the UI; a
// single contact with two SMS addresses yields two IDs under "SMS".
// Errors records per-kind failures encountered during dispatch —
// non-fatal so a partial fan-out still produces as many IDs as the
// bearers accepted. Strategy is the strategy that was actually used
// (after policy resolution). [MESHSAT-544] RequestedStrategy is the one
// asked for or held by the policy row, StrategyReason why Strategy was
// used (r3 D12). [MESHSAT-1584]
type SendResult struct {
	DeliveryIDs       map[directory.Kind][]int64
	Errors            map[directory.Kind]error
	Strategy          directory.Strategy
	RequestedStrategy directory.Strategy
	StrategyReason    string
	Precedence        types.Precedence
	// Intent is the submission when the send went the delivery-intent
	// way (MESHSAT_DELIVERY_INTENTS); DeliveryIDs then hold its round-1
	// attempts and Errors the routes it did not admit. [MESHSAT-1581]
	Intent *IntentSubmission
	// Legacy is the submission of a send made outside delivery intents
	// under an idempotency key; Existing says it was made by an earlier
	// request with that key and nothing was queued now. [MESHSAT-1584]
	Legacy   *database.DeliverySubmission
	Existing bool
}

// kindToChannelType maps a directory.Kind to the `channel_type` value
// stored on the interfaces table (which in turn drives which bearer
// handler picks up a queued delivery). The mapping is the inverse of
// the v44 bridge migration's kind-normalisation step: MESHTASTIC →
// "mesh", IRIDIUM_SBD → "iridium", etc.
//
// SMS goes to the cellular gateway: "sms" is the channel type of sms_0,
// Reticulum's packet interface over SMS, which has no delivery gateway, so
// a contact's SMS queued there never went out. A Reticulum contact is an
// LXMF message (queueContactAddress); it used to map to "tcp", which has no
// gateway either. [MESHSAT-1581]
var kindToChannelType = map[directory.Kind]string{
	directory.KindSMS:        "cellular",
	directory.KindMeshtastic: "mesh",
	directory.KindAPRS:       "aprs",
	directory.KindIridiumSBD: "iridium",
	directory.KindIridiumIMT: "iridium_imt",
	directory.KindCellular:   "cellular",
	directory.KindTAK:        "tak",
	directory.KindZigBee:     "zigbee",
	directory.KindBLE:        "ble",
	directory.KindWebhook:    "webhook",
	directory.KindMQTT:       "mqtt",
	directory.KindTETRA:      "tetra",
	directory.KindCell:       "cell",
}

// RecipientResolver is the narrow view of [directory.Store] the
// dispatcher needs. Declared locally so the engine package does not
// pull the full Store mutation surface into its dependency graph.
type RecipientResolver interface {
	Resolve(ctx context.Context, id string) (*directory.Contact, error)
	GetPolicy(ctx context.Context, scope directory.PolicyScope, scopeID string) (*directory.DispatchPolicy, error)
}

// SetRecipientResolver attaches a resolver so SendToRecipient can
// expand contact IDs into addresses and consult policy rows. A nil
// resolver limits SendToRecipient to the Raw escape hatch.
func (d *Dispatcher) SetRecipientResolver(r RecipientResolver) {
	d.recipientResolver = r
}

// SendToRecipient queues a message against every bearer the resolved
// dispatch strategy selects. Returns per-bearer delivery IDs so the
// UI can render WhatsApp-style per-bearer ticks.
//
// Strategy resolution order:
//  1. opts.Strategy (explicit caller override).
//  2. Per-contact policy (scope_type=contact, scope_id=contactID).
//  3. Per-precedence policy (scope_type=precedence, scope_id=<level>).
//  4. Default policy (scope_type=default, scope_id="").
//  5. PRIMARY_ONLY fallback.
//
// Implemented strategies: PRIMARY_ONLY, ALL_BEARERS; ORDERED_FALLBACK
// too on the delivery-intent path. An explicit request for another is
// ErrUnsupportedStrategy before anything is queued; a policy row holding
// another resolves as StoredStrategy says, labelled in StrategyReason
// (r3 D12, stage 0b). [MESHSAT-544, MESHSAT-1584]
func (d *Dispatcher) SendToRecipient(ctx context.Context, rcpt RecipientRef, body []byte, opts SendOptions) (SendResult, error) {
	res := SendResult{
		DeliveryIDs: map[directory.Kind][]int64{},
		Errors:      map[directory.Kind]error{},
		Precedence:  opts.Precedence,
	}
	if res.Precedence == "" {
		res.Precedence = types.DefaultPrecedence
	}

	// A key names one send whatever the mode: a request that comes again
	// under it (an answer lost on the way back) gets the first send's
	// result, made with delivery intents or without, and nothing is queued
	// again, also when MESHSAT_DELIVERY_INTENTS changed in between
	// (B10-R4). The key is looked up before anything that depends on the
	// mode, the strategy check included: a request the bridge took is
	// answered for what it was, and only new work meets today's rules
	// (review of 5bef352). [MESHSAT-1584]
	fp := d.sendFingerprint(rcpt, body, opts, res.Precedence)
	if opts.IdempotencyKey != "" {
		prev, err := d.db.GetSubmissionByKey(sendSource(opts), opts.IdempotencyKey)
		if err != nil {
			return res, err
		}
		if prev != nil {
			if prev.RequestSHA256 != fp {
				return res, database.ErrIdempotencyConflict
			}
			if prev.Mode == database.SubmissionLegacy {
				return replayLegacySend(prev, res)
			}
			if d.escalator == nil {
				return res, errors.New("this key named a delivery intent and delivery intents are not running")
			}
			return d.sendViaIntent(ctx, rcpt, body, opts, res)
		}
	}

	// An explicit strategy is checked before any branch can queue (r3
	// D12, B5-R1). A raw send is one destination on a named interface and
	// never becomes a delivery intent, so only PRIMARY_ONLY describes it,
	// whatever MESHSAT_DELIVERY_INTENTS says. [MESHSAT-1584]
	if opts.Strategy != "" {
		supported := SupportedStrategies(d.DeliveryIntents())
		if rcpt.Raw != nil {
			supported = []directory.Strategy{directory.StrategyPrimaryOnly}
		}
		if err := explicitStrategyError(opts.Strategy, supported, rcpt.Raw != nil); err != nil {
			return res, err
		}
	}

	// Raw escape hatch: no directory lookup, queue directly on the
	// named interface with the caller's precedence.
	if rcpt.Raw != nil {
		if rcpt.Raw.InterfaceID == "" {
			return res, fmt.Errorf("RawRecipient requires InterfaceID")
		}
		if opts.IdempotencyKey != "" {
			del := d.prepareDirectSend(rcpt.Raw.InterfaceID, string(body), DirectSendOptions{
				Precedence:  string(res.Precedence),
				Destination: rcpt.Raw.Address,
				Class:       opts.Class,
				MaxRetries:  opts.MaxRetries,
			})
			res.Strategy, res.RequestedStrategy, res.StrategyReason = directory.StrategyPrimaryOnly, opts.Strategy, StrategyReasonRaw
			return d.queueLegacyKeyed(opts, fp, []directory.Kind{""}, []database.MessageDelivery{del}, res)
		}
		// The raw address reaches the delivery row as its destination
		// (reply-to-sender) instead of being dropped. [MESHSAT-756]
		id, _, err := d.QueueDirectSendTo(rcpt.Raw.InterfaceID, string(body), DirectSendOptions{
			Precedence:  string(res.Precedence),
			Destination: rcpt.Raw.Address,
			Class:       opts.Class,
			MaxRetries:  opts.MaxRetries,
		})
		if err != nil {
			return res, err
		}
		// Raw sends don't have a bearer kind on their face; record
		// under the empty Kind so callers can see "one generic ID".
		res.DeliveryIDs[directory.Kind("")] = []int64{id}
		res.Strategy, res.RequestedStrategy, res.StrategyReason = directory.StrategyPrimaryOnly, opts.Strategy, StrategyReasonRaw
		return res, nil
	}

	if rcpt.ContactID == "" {
		return res, fmt.Errorf("SendToRecipient requires ContactID or Raw")
	}
	if d.recipientResolver == nil {
		return res, fmt.Errorf("SendToRecipient: no RecipientResolver configured")
	}
	if d.escalator.Accepting() {
		return d.sendViaIntent(ctx, rcpt, body, opts, res)
	}

	contact, err := d.recipientResolver.Resolve(ctx, rcpt.ContactID)
	if err != nil {
		return res, fmt.Errorf("resolve contact %s: %w", rcpt.ContactID, err)
	}
	if contact == nil || len(contact.Addresses) == 0 {
		return res, fmt.Errorf("contact %s has no addresses", rcpt.ContactID)
	}

	sr, err := d.resolveStrategy(ctx, rcpt.ContactID, res.Precedence, opts.Strategy, nil, false)
	if err != nil {
		return res, err
	}
	res.Strategy, res.RequestedStrategy, res.StrategyReason = sr.Effective, sr.Requested, sr.Reason

	addrs := selectAddresses(contact.Addresses, res.Strategy)
	if len(addrs) == 0 {
		return res, fmt.Errorf("contact %s has no addresses usable by strategy %s", rcpt.ContactID, res.Strategy)
	}

	if opts.IdempotencyKey != "" {
		var kinds []directory.Kind
		var dels []database.MessageDelivery
		for _, a := range addrs {
			del, err := d.prepareContactAddress(a, body, res.Precedence)
			if err != nil {
				res.Errors[a.Kind] = err
				continue
			}
			kinds, dels = append(kinds, a.Kind), append(dels, del)
		}
		if len(dels) > 0 {
			return d.queueLegacyKeyed(opts, fp, kinds, dels, res)
		}
	} else {
		for _, a := range addrs {
			delID, err := d.queueContactAddress(a, body, res.Precedence)
			if err != nil {
				res.Errors[a.Kind] = err
				continue
			}
			res.DeliveryIDs[a.Kind] = append(res.DeliveryIDs[a.Kind], delID)
		}
	}

	if len(res.DeliveryIDs) == 0 {
		// Every bearer failed — surface the first recorded error so
		// callers get actionable context instead of a bare
		// "nothing queued".
		for _, e := range res.Errors {
			return res, fmt.Errorf("all bearers failed: %w", e)
		}
		return res, fmt.Errorf("no bearer queued (no addresses matched any interface)")
	}
	return res, nil
}

// sendViaIntent is SendToRecipient on the delivery-intent path: one
// submission, one intent, its round-1 attempts. [MESHSAT-1581]
func (d *Dispatcher) sendViaIntent(ctx context.Context, rcpt RecipientRef, body []byte, opts SendOptions, res SendResult) (SendResult, error) {
	sub, err := d.escalator.Submit(ctx, IntentRequest{
		Source: opts.Source, IdempotencyKey: opts.IdempotencyKey, ContactID: rcpt.ContactID, Body: body,
		Precedence: res.Precedence, Strategy: opts.Strategy, MaxCostCents: opts.MaxCostCents,
	})
	if err != nil {
		return res, err
	}
	res.Intent = sub
	if len(sub.Intents) == 0 {
		return res, nil
	}
	in := sub.Intents[0]
	res.Strategy = directory.Strategy(in.EffectiveStrategy)
	res.RequestedStrategy, res.StrategyReason = directory.Strategy(in.RequestedStrategy), in.StrategyReason
	// A route not used keeps its reason beside the rows of the same bearer
	// (a second mesh address that is no node id, say): one route queued
	// must not hide another that could not be (B10-R6). A second route on
	// an interface the plan already uses is left out by design (r3 D3) and
	// is no failure; the intent's status still lists it as not used.
	// [MESHSAT-1584]
	kindOf := map[int]directory.Kind{}
	for _, p := range in.RoutePlan {
		kindOf[p.RouteIndex] = directory.Kind(p.Kind)
		if !p.Admitted && p.Reason != reasonSameInterface {
			res.Errors[directory.Kind(p.Kind)] = fmt.Errorf("not used: %s", p.Reason)
		}
	}
	if len(sub.RowIDs) > 0 {
		for _, id := range sub.RowIDs[0] {
			if row, err := d.db.GetDelivery(id); err == nil {
				k := kindOf[row.RouteIndex]
				res.DeliveryIDs[k] = append(res.DeliveryIDs[k], id)
			}
		}
	}
	return res, nil
}

// queueContactAddress queues body for one of a contact's addresses, with
// that address on the row as its destination, so the gateway sends to the
// contact the strategy chose. The destination used to be dropped: an SMS
// went to the gateway's own default numbers and a mesh text was broadcast
// to everyone. [MESHSAT-1581]
func (d *Dispatcher) queueContactAddress(a directory.Address, body []byte, precedence types.Precedence) (int64, error) {
	del, err := d.prepareContactAddress(a, body, precedence)
	if err != nil {
		return 0, err
	}
	id, err := d.db.InsertDelivery(del)
	if err != nil {
		return 0, fmt.Errorf("queue delivery: %w", err)
	}
	d.directSendQueued(del, id)
	return id, nil
}

// prepareContactAddress builds the row queueContactAddress writes, without
// writing it.
func (d *Dispatcher) prepareContactAddress(a directory.Address, body []byte, precedence types.Precedence) (database.MessageDelivery, error) {
	dest, err := contactDestination(a)
	if err != nil {
		return database.MessageDelivery{}, err
	}
	if a.Kind == directory.KindReticulum {
		// As POST /api/lxmf/send queues it: the router packs, signs and
		// waits for the proof, the ledger keeps the retries.
		payload, _ := json.Marshal(map[string]string{"content": string(body), "title": ""})
		return d.prepareDirectSend("lxmf_0", string(body), DirectSendOptions{
			Precedence:  string(precedence),
			Destination: dest,
			Class:       database.DeliveryClassLXMF,
			MaxRetries:  5,
			Payload:     payload,
		}), nil
	}
	ifaceID, err := d.interfaceForKind(a.Kind)
	if err != nil {
		return database.MessageDelivery{}, err
	}
	return d.prepareDirectSend(ifaceID, string(body), DirectSendOptions{
		Precedence:  string(precedence),
		Destination: dest,
	}), nil
}

// contactDestination is an address in the form its gateway reads from a
// delivery's destination: a mesh node id "!xxxxxxxx", an upper-case APRS
// callsign, a 32-hex LXMF destination hash, anything else as stored (an
// SMS number). An address that cannot be one is refused, never sent to a
// default.
func contactDestination(a directory.Address) (string, error) {
	v := strings.TrimSpace(a.Value)
	if v == "" {
		return "", fmt.Errorf("%s address is empty", a.Kind)
	}
	switch a.Kind {
	case directory.KindMeshtastic:
		id := strings.ToLower(v)
		bang := strings.HasPrefix(id, "!")
		id = strings.TrimPrefix(id, "!")
		if _, err := strconv.ParseUint(id, 16, 32); err != nil || len(id) > 8 || (!bang && len(id) != 8) {
			return "", fmt.Errorf("mesh address %q is not a node id (!xxxxxxxx)", a.Value)
		}
		return "!" + id, nil
	case directory.KindAPRS:
		return strings.ToUpper(v), nil
	case directory.KindReticulum:
		if b, err := hex.DecodeString(v); err != nil || len(b) != 16 {
			return "", fmt.Errorf("reticulum address %q is not a 32-hex LXMF destination hash", a.Value)
		}
		return strings.ToLower(v), nil
	}
	return v, nil
}

// selectAddresses picks the subset of a contact's addresses that the
// strategy queues against: PRIMARY_ONLY the lowest-primary_rank address
// across all kinds, ALL_BEARERS every address. Resolution hands it only
// these two (r3 D12); anything else is one address, as before.
func selectAddresses(all []directory.Address, strategy directory.Strategy) []directory.Address {
	if len(all) == 0 {
		return nil
	}
	sorted := make([]directory.Address, len(all))
	copy(sorted, all)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].PrimaryRank != sorted[j].PrimaryRank {
			return sorted[i].PrimaryRank < sorted[j].PrimaryRank
		}
		// Stable tiebreak on kind so the same inputs always produce
		// the same output — useful for deterministic tests.
		return strings.Compare(string(sorted[i].Kind), string(sorted[j].Kind)) < 0
	})
	switch strategy {
	case directory.StrategyPrimaryOnly:
		// One bearer only.
		return sorted[:1]
	case directory.StrategyAllBearers:
		return sorted
	default:
		log.Warn().Str("strategy", string(strategy)).Msg("SendToRecipient: unknown strategy, defaulting to PRIMARY_ONLY")
		return sorted[:1]
	}
}

// interfaceForKind looks up the first enabled interface whose
// channel_type matches the bearer kind. Returns an error when no
// interface exists for the kind; callers record the error against the
// kind and proceed with the remaining addresses.
func (d *Dispatcher) interfaceForKind(k directory.Kind) (string, error) {
	chanType, ok := kindToChannelType[k]
	if !ok {
		return "", fmt.Errorf("kind %s has no interface mapping", k)
	}
	ifaces, err := d.db.GetInterfacesByType(chanType)
	if err != nil {
		return "", fmt.Errorf("lookup interfaces for %s: %w", chanType, err)
	}
	for _, iface := range ifaces {
		if iface.Enabled {
			return iface.ID, nil
		}
	}
	if len(ifaces) > 0 {
		// Fall back to the first interface even if disabled — the
		// delivery worker will surface "no bearer online" as the
		// error; better than dropping silently.
		return ifaces[0].ID, nil
	}
	return "", fmt.Errorf("no interface configured for kind %s (channel_type=%s)", k, chanType)
}
