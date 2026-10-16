package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"meshsat/internal/directory"
	"meshsat/internal/types"
)

// Why a send uses the strategy it uses (HeMB-NG r3 D12). A policy row
// that names an implemented strategy gives "policy:<scope>".
const (
	StrategyReasonRequested    = "requested"
	StrategyReasonDefault      = "default"
	StrategyReasonLegacyCompat = "legacy_compat_all_bearers"
	StrategyReasonUnknown      = "unknown_strategy_primary_only"
	StrategyReasonRaw          = "raw_destination"
)

// SupportedStrategies is what the bridge implements (r3 D12, stage 0b):
// PRIMARY_ONLY and ALL_BEARERS on the delivery queue, and ORDERED_FALLBACK
// as well on the delivery-intent path (MESHSAT_DELIVERY_INTENTS).
// [MESHSAT-1584]
func SupportedStrategies(intents bool) []directory.Strategy {
	s := []directory.Strategy{directory.StrategyPrimaryOnly, directory.StrategyAllBearers}
	if intents {
		s = append(s, directory.StrategyOrderedFallback)
	}
	return s
}

// StoredStrategy is what the bridge does with a strategy held in a policy
// row: the strategy itself when implemented; ALL_BEARERS, labelled legacy
// compatibility, for one the bridge used to fan out on every address
// (ANY_REACHABLE, HEMB_BONDED, and ORDERED_FALLBACK on the queue), which
// was what such a row did; PRIMARY_ONLY for a value the bridge never knew,
// as before. reason is "" when the strategy is implemented.
func StoredStrategy(s directory.Strategy, intents bool) (effective directory.Strategy, reason string) {
	switch {
	case slices.Contains(SupportedStrategies(intents), s):
		return s, ""
	case s == directory.StrategyAnyReachable, s == directory.StrategyHeMBBonded, s == directory.StrategyOrderedFallback:
		return directory.StrategyAllBearers, StrategyReasonLegacyCompat
	default:
		return directory.StrategyPrimaryOnly, StrategyReasonUnknown
	}
}

// StrategyResolution is the outcome of the policy walk for one send.
type StrategyResolution struct {
	Requested directory.Strategy // as asked, or as the policy row holds it
	Effective directory.Strategy // what the send does
	Reason    string
	Version   string // policy row id@updated_at; "" without a row
	CapCents  *int   // the request's cap, else the policy row's
}

// DeliveryIntents says whether contact sends become delivery intents.
func (d *Dispatcher) DeliveryIntents() bool { return d.escalator.Accepting() }

// StrategySupported says whether the bridge implements s now.
func (d *Dispatcher) StrategySupported(s directory.Strategy) bool {
	return slices.Contains(SupportedStrategies(d.DeliveryIntents()), s)
}

// explicitStrategyError is ErrUnsupportedStrategy for an explicit
// strategy outside supported, nil otherwise. raw: the request names one
// destination on one interface, which only PRIMARY_ONLY describes.
func explicitStrategyError(explicit directory.Strategy, supported []directory.Strategy, raw bool) error {
	if slices.Contains(supported, explicit) {
		return nil
	}
	names := make([]string, len(supported))
	for i, s := range supported {
		names[i] = string(s)
	}
	msg := fmt.Sprintf("%s (supported: %s)", explicit, strings.Join(names, ", "))
	switch {
	case raw:
		msg = fmt.Sprintf("%s (a raw send has one destination on one interface: no strategy, or PRIMARY_ONLY)", explicit)
	case explicit == directory.StrategyOrderedFallback:
		msg += "; ORDERED_FALLBACK needs MESHSAT_DELIVERY_INTENTS=true"
	}
	return fmt.Errorf("%w: %s", ErrUnsupportedStrategy, msg)
}

// resolveStrategy walks explicit -> contact policy -> precedence policy ->
// default policy -> PRIMARY_ONLY (r3 D12). An explicit strategy the bridge
// does not implement is ErrUnsupportedStrategy, before anything is
// written. A lookup failure falls through to the next tier.
func (d *Dispatcher) resolveStrategy(ctx context.Context, contactID string, precedence types.Precedence, explicit directory.Strategy, capCents *int, intents bool) (StrategyResolution, error) {
	res := StrategyResolution{CapCents: capCents}
	if explicit != "" {
		if err := explicitStrategyError(explicit, SupportedStrategies(intents), false); err != nil {
			return res, err
		}
		res.Requested, res.Effective, res.Reason = explicit, explicit, StrategyReasonRequested
		return res, nil
	}
	if d.recipientResolver != nil {
		for _, sc := range []struct {
			scope directory.PolicyScope
			id    string
		}{{directory.ScopeContact, contactID}, {directory.ScopePrecedence, string(precedence)}, {directory.ScopeDefault, ""}} {
			p, err := d.recipientResolver.GetPolicy(ctx, sc.scope, sc.id)
			if err != nil || p == nil || p.Strategy == "" {
				continue
			}
			res.Requested, res.Version = p.Strategy, p.ID+"@"+p.UpdatedAt
			if res.CapCents == nil {
				res.CapCents = p.MaxCostCents
			}
			res.Effective, res.Reason = StoredStrategy(p.Strategy, intents)
			if res.Reason == "" {
				res.Reason = "policy:" + string(sc.scope)
			}
			return res, nil
		}
	}
	res.Effective, res.Reason = directory.StrategyPrimaryOnly, StrategyReasonDefault
	return res, nil
}
