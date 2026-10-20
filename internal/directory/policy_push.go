package directory

import (
	"context"
	"fmt"
	"time"

	"meshsat/internal/types"
)

// PushedPolicy is one dispatch policy row of a Hub directory snapshot. It
// keeps the fields the bridge does not implement, so a row that asks for
// them is refused instead of applied in part. [MESHSAT-1584]
type PushedPolicy struct {
	ID                string
	ScopeType         string
	ScopeID           string
	Strategy          Strategy
	Preferred         []string
	Fallback          []string
	RequireEncryption bool
	MaxRetries        int
	RetryDelay        time.Duration
	CreatedAt         time.Time
	UpdatedAt         time.Time
	// Seeded is the Hub's own mark (v2 snapshots, C7.7): true for a
	// precedence default the Hub installed, nil when the snapshot cannot
	// say (v1), which falls back to the updated_at = created_at reading.
	Seeded *bool
}

// Reasons a pushed policy row is not applied (HeMB-NG r3 D12, S2).
const (
	PushRejectInvalid       = "invalid"
	PushRejectScope         = "unsupported_scope"
	PushRejectStrategy      = "unsupported_strategy"
	PushRejectField         = "unsupported_field"
	PushRejectSeededDefault = "seeded_default"
)

// PolicyRejection names a pushed row that was not applied, and why.
type PolicyRejection struct {
	ID        string
	ScopeType string
	ScopeID   string
	Strategy  string
	Reason    string
	Detail    string
}

// hubSeeds are the precedence defaults the Hub seeds per tenant
// (meshsat-hub internal/directory PrecedenceDefaults), the same table as
// the bridge's v48.
var hubSeeds = map[string]Strategy{
	"Override": StrategyHeMBBonded, "Flash": StrategyHeMBBonded, "Immediate": StrategyAnyReachable,
	"Priority": StrategyOrderedFallback, "Routine": StrategyPrimaryOnly, "Deferred": StrategyPrimaryOnly,
}

// CheckPushedPolicy says why the bridge cannot apply a pushed row as
// written, or "" when it can. supported is the strategy set the bridge
// implements now. Refused: a scope the dispatcher does not consult
// (group), a strategy it does not implement, a field it does not
// implement (preferred and fallback bearers, required encryption, retry
// settings: applying the strategy without them would claim behaviour the
// bridge does not have), and a Hub default: one the Hub marks seeded (v2,
// C7.7), or, from a v1 snapshot that cannot mark it, an unedited Hub seed
// of ORDERED_FALLBACK (re-seeding Priority to it is the owner's call, r3
// D12).
func CheckPushedPolicy(p PushedPolicy, supported func(Strategy) bool) (reason, detail string) {
	if p.Seeded != nil && *p.Seeded {
		// A Hub default carries nobody's intent: the bridge keeps its own (C7.7).
		return PushRejectSeededDefault, "a Hub default; the bridge keeps its own for the scope"
	}
	switch PolicyScope(p.ScopeType) {
	case ScopeContact:
		if p.ScopeID == "" {
			return PushRejectInvalid, "contact scope without a contact id"
		}
	case ScopePrecedence:
		if !types.Precedence(p.ScopeID).Valid() {
			return PushRejectInvalid, fmt.Sprintf("unknown precedence %q", p.ScopeID)
		}
	case ScopeDefault:
		if p.ScopeID != "" {
			return PushRejectInvalid, "default scope with a scope id"
		}
	case ScopeGroup:
		return PushRejectScope, "group policies are not used by this bridge"
	default:
		return PushRejectInvalid, fmt.Sprintf("unknown scope %q", p.ScopeType)
	}
	if p.Strategy == "" {
		return PushRejectInvalid, "no strategy"
	}
	if !supported(p.Strategy) {
		return PushRejectStrategy, fmt.Sprintf("%s is not implemented by this bridge", p.Strategy)
	}
	switch {
	case len(p.Preferred) > 0:
		return PushRejectField, "preferred bearers are not implemented"
	case len(p.Fallback) > 0:
		return PushRejectField, "fallback bearers are not implemented"
	case p.RequireEncryption:
		return PushRejectField, "require_encryption is not implemented"
	case p.MaxRetries != 0 || p.RetryDelay != 0:
		return PushRejectField, "retry settings are not implemented"
	}
	if p.Seeded == nil && PolicyScope(p.ScopeType) == ScopePrecedence && p.Strategy == StrategyOrderedFallback &&
		hubSeeds[p.ScopeID] == p.Strategy && p.UpdatedAt.Equal(p.CreatedAt) {
		return PushRejectSeededDefault, "unedited Hub default; re-seeding Priority to ORDERED_FALLBACK is the owner's call"
	}
	return "", ""
}

// ApplyPushedPolicies applies each row the bridge can honour as written
// and names the others with the reason (r3 D12: per row, the supported
// rows of the same push still apply). An applied row sets the strategy of
// the bridge's row for that scope, or creates it; the bridge's own fields
// the Hub does not carry (cost cap, latency, bearer lists) are kept.
// applied lists the pushed ids.
func (s *SQLStore) ApplyPushedPolicies(ctx context.Context, rows []PushedPolicy, supported func(Strategy) bool) (applied []string, rejected []PolicyRejection, err error) {
	for _, p := range rows {
		if reason, detail := CheckPushedPolicy(p, supported); reason != "" {
			rejected = append(rejected, PolicyRejection{ID: p.ID, ScopeType: p.ScopeType, ScopeID: p.ScopeID,
				Strategy: string(p.Strategy), Reason: reason, Detail: detail})
			continue
		}
		ts := now()
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO directory_dispatch_policy (id, scope_type, scope_id, strategy, seeded, created_at, updated_at)
			VALUES (?, ?, ?, ?, 0, ?, ?)
			ON CONFLICT(scope_type, scope_id) DO UPDATE SET
				strategy = excluded.strategy, seeded = 0, updated_at = excluded.updated_at`,
			NewID(), p.ScopeType, p.ScopeID, string(p.Strategy), ts, ts); err != nil {
			return applied, rejected, fmt.Errorf("apply policy %s: %w", p.ID, translateErr(err))
		}
		applied = append(applied, p.ID)
	}
	return applied, rejected, nil
}
