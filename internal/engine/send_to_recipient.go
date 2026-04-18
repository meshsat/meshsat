package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"

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
}

// SendResult reports what the dispatcher actually did on behalf of a
// SendToRecipient call. DeliveryIDs is keyed by bearer kind so callers
// can render per-bearer status (delivery ticks, errors) in the UI; a
// single contact with two SMS addresses yields two IDs under "SMS".
// Errors records per-kind failures encountered during dispatch —
// non-fatal so a partial fan-out still produces as many IDs as the
// bearers accepted. Strategy is the strategy that was actually used
// (after policy resolution). [MESHSAT-544]
type SendResult struct {
	DeliveryIDs map[directory.Kind][]int64
	Errors      map[directory.Kind]error
	Strategy    directory.Strategy
	Precedence  types.Precedence
}

// kindToChannelType maps a directory.Kind to the `channel_type` value
// stored on the interfaces table (which in turn drives which bearer
// handler picks up a queued delivery). The mapping is the inverse of
// the v44 bridge migration's kind-normalisation step: MESHTASTIC →
// "mesh", IRIDIUM_SBD → "iridium", etc.
var kindToChannelType = map[directory.Kind]string{
	directory.KindSMS:        "sms",
	directory.KindMeshtastic: "mesh",
	directory.KindAPRS:       "aprs",
	directory.KindIridiumSBD: "iridium",
	directory.KindIridiumIMT: "iridium_imt",
	directory.KindCellular:   "cellular",
	directory.KindTAK:        "tak",
	directory.KindReticulum:  "tcp", // Reticulum uses the TCP interface as default bearer
	directory.KindZigBee:     "zigbee",
	directory.KindBLE:        "ble",
	directory.KindWebhook:    "webhook",
	directory.KindMQTT:       "mqtt",
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
// Currently implemented strategies: PRIMARY_ONLY, ALL_BEARERS. The
// other three (ORDERED_FALLBACK, ANY_REACHABLE, HEMB_BONDED) fall back
// to ALL_BEARERS with a warning log; full async fallback + HeMB
// coordination land in follow-ups. [MESHSAT-544]
func (d *Dispatcher) SendToRecipient(ctx context.Context, rcpt RecipientRef, body []byte, opts SendOptions) (SendResult, error) {
	res := SendResult{
		DeliveryIDs: map[directory.Kind][]int64{},
		Errors:      map[directory.Kind]error{},
		Precedence:  opts.Precedence,
	}
	if res.Precedence == "" {
		res.Precedence = types.DefaultPrecedence
	}

	// Raw escape hatch: no directory lookup, queue directly on the
	// named interface with the caller's precedence.
	if rcpt.Raw != nil {
		if rcpt.Raw.InterfaceID == "" {
			return res, fmt.Errorf("RawRecipient requires InterfaceID")
		}
		id, _, err := d.QueueDirectSend(rcpt.Raw.InterfaceID, string(body), string(res.Precedence))
		if err != nil {
			return res, err
		}
		// Raw sends don't have a bearer kind on their face; record
		// under the empty Kind so callers can see "one generic ID".
		res.DeliveryIDs[directory.Kind("")] = []int64{id}
		res.Strategy = directory.StrategyPrimaryOnly
		return res, nil
	}

	if rcpt.ContactID == "" {
		return res, fmt.Errorf("SendToRecipient requires ContactID or Raw")
	}
	if d.recipientResolver == nil {
		return res, fmt.Errorf("SendToRecipient: no RecipientResolver configured")
	}

	contact, err := d.recipientResolver.Resolve(ctx, rcpt.ContactID)
	if err != nil {
		return res, fmt.Errorf("resolve contact %s: %w", rcpt.ContactID, err)
	}
	if contact == nil || len(contact.Addresses) == 0 {
		return res, fmt.Errorf("contact %s has no addresses", rcpt.ContactID)
	}

	res.Strategy = d.resolveStrategy(ctx, rcpt.ContactID, res.Precedence, opts.Strategy)

	addrs := selectAddresses(contact.Addresses, res.Strategy)
	if len(addrs) == 0 {
		return res, fmt.Errorf("contact %s has no addresses usable by strategy %s", rcpt.ContactID, res.Strategy)
	}

	for _, a := range addrs {
		ifaceID, err := d.interfaceForKind(a.Kind)
		if err != nil {
			res.Errors[a.Kind] = err
			continue
		}
		delID, _, err := d.QueueDirectSend(ifaceID, string(body), string(res.Precedence))
		if err != nil {
			res.Errors[a.Kind] = err
			continue
		}
		res.DeliveryIDs[a.Kind] = append(res.DeliveryIDs[a.Kind], delID)
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

// resolveStrategy walks the policy hierarchy: explicit opts →
// contact-scoped policy → precedence-scoped policy → default policy
// → PRIMARY_ONLY fall-back. Returns the strategy that will actually
// drive the dispatch. Missing resolver or lookup failures degrade
// gracefully to the next tier.
func (d *Dispatcher) resolveStrategy(ctx context.Context, contactID string, precedence types.Precedence, explicit directory.Strategy) directory.Strategy {
	if explicit != "" {
		return explicit
	}
	if d.recipientResolver == nil {
		return directory.StrategyPrimaryOnly
	}
	tryScope := func(scope directory.PolicyScope, scopeID string) (directory.Strategy, bool) {
		p, err := d.recipientResolver.GetPolicy(ctx, scope, scopeID)
		if err != nil || p == nil || p.Strategy == "" {
			return "", false
		}
		return p.Strategy, true
	}
	if s, ok := tryScope(directory.ScopeContact, contactID); ok {
		return s
	}
	if s, ok := tryScope(directory.ScopePrecedence, string(precedence)); ok {
		return s
	}
	if s, ok := tryScope(directory.ScopeDefault, ""); ok {
		return s
	}
	return directory.StrategyPrimaryOnly
}

// selectAddresses picks the subset of a contact's addresses that the
// strategy should queue against. PRIMARY_ONLY chooses the lowest-
// primary_rank address across all kinds; the other strategies fan out
// across every address (ORDERED_FALLBACK / ANY_REACHABLE / HEMB_BONDED
// treated identically today — future stories wire cross-delivery
// observation and bond-group coordination).
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
	case directory.StrategyAllBearers,
		directory.StrategyAnyReachable,
		directory.StrategyHeMBBonded,
		directory.StrategyOrderedFallback:
		// Today all three mean "queue on every address". Async
		// fallback + bond coordination land in follow-ups; the
		// delivery ledger still records distinct rows so the UI can
		// differentiate once they do.
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
