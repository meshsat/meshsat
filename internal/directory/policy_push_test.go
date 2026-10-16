package directory_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"meshsat/internal/directory"
)

func queueOnly(s directory.Strategy) bool {
	return s == directory.StrategyPrimaryOnly || s == directory.StrategyAllBearers
}

func withIntents(s directory.Strategy) bool {
	return queueOnly(s) || s == directory.StrategyOrderedFallback
}

// S2, push half (HeMB-NG r3 D12): a push that mixes rows the bridge
// implements with rows it does not applies the first and names each of
// the others with its reason. An applied row keeps the bridge's own
// fields the Hub does not carry. [MESHSAT-1584]
func TestApplyPushedPolicies_PerRow(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	capCents := 40
	if err := s.UpsertPolicy(ctx, &directory.DispatchPolicy{ScopeType: directory.ScopeContact, ScopeID: "bo",
		Strategy: directory.StrategyAllBearers, MaxCostCents: &capCents}); err != nil {
		t.Fatal(err)
	}
	seed := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	edited := seed.Add(time.Hour)
	rows := []directory.PushedPolicy{
		{ID: "ok-contact", ScopeType: "contact", ScopeID: "bo", Strategy: directory.StrategyPrimaryOnly, CreatedAt: seed, UpdatedAt: edited},
		{ID: "ok-new", ScopeType: "contact", ScopeID: "cy", Strategy: directory.StrategyAllBearers, CreatedAt: edited, UpdatedAt: edited},
		{ID: "bonded", ScopeType: "precedence", ScopeID: "Flash", Strategy: directory.StrategyHeMBBonded, CreatedAt: seed, UpdatedAt: seed},
		{ID: "any", ScopeType: "contact", ScopeID: "bo", Strategy: directory.StrategyAnyReachable, CreatedAt: seed, UpdatedAt: edited},
		{ID: "group", ScopeType: "group", ScopeID: "g1", Strategy: directory.StrategyPrimaryOnly},
		{ID: "enc", ScopeType: "precedence", ScopeID: "Routine", Strategy: directory.StrategyPrimaryOnly, RequireEncryption: true},
		{ID: "pref", ScopeType: "default", Strategy: directory.StrategyAllBearers, Preferred: []string{"SMS"}},
		{ID: "retry", ScopeType: "default", Strategy: directory.StrategyAllBearers, MaxRetries: 3},
		{ID: "badprec", ScopeType: "precedence", ScopeID: "Urgent", Strategy: directory.StrategyPrimaryOnly},
		{ID: "nostrat", ScopeType: "contact", ScopeID: "bo"},
		{ID: "fallback", ScopeType: "precedence", ScopeID: "Priority", Strategy: directory.StrategyOrderedFallback, CreatedAt: seed, UpdatedAt: edited},
	}

	applied, rejected, err := s.ApplyPushedPolicies(ctx, rows, queueOnly)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(applied, []string{"ok-contact", "ok-new"}) {
		t.Fatalf("applied %v; want ok-contact, ok-new", applied)
	}
	wantReason := map[string]string{
		"bonded": directory.PushRejectStrategy, "any": directory.PushRejectStrategy, "group": directory.PushRejectScope,
		"enc": directory.PushRejectField, "pref": directory.PushRejectField, "retry": directory.PushRejectField,
		"badprec": directory.PushRejectInvalid, "nostrat": directory.PushRejectInvalid,
		"fallback": directory.PushRejectStrategy, // not on the queue path
	}
	if len(rejected) != len(wantReason) {
		t.Fatalf("rejected %d rows %+v; want %d", len(rejected), rejected, len(wantReason))
	}
	for _, r := range rejected {
		if wantReason[r.ID] != r.Reason || r.Detail == "" {
			t.Errorf("%s: %s (%s); want %s with a detail", r.ID, r.Reason, r.Detail, wantReason[r.ID])
		}
	}

	bo, _ := s.GetPolicy(ctx, directory.ScopeContact, "bo")
	if bo.Strategy != directory.StrategyPrimaryOnly || bo.MaxCostCents == nil || *bo.MaxCostCents != 40 || bo.Seeded {
		t.Fatalf("bo after the push: %+v; want PRIMARY_ONLY with its cap of 40 kept", bo)
	}
	if cy, err := s.GetPolicy(ctx, directory.ScopeContact, "cy"); err != nil || cy.Strategy != directory.StrategyAllBearers {
		t.Fatalf("cy: %+v %v", cy, err)
	}
	for _, prec := range []string{"Flash", "Routine", "Priority"} {
		if p, _ := s.GetPolicy(ctx, directory.ScopePrecedence, prec); !p.Seeded {
			t.Errorf("%s changed by a refused row: %+v", prec, p)
		}
	}
	if _, err := s.GetPolicy(ctx, directory.ScopeGroup, "g1"); err == nil {
		t.Error("a refused group row was written")
	}
}

// With delivery intents ORDERED_FALLBACK is implemented: an edited row
// applies; the Hub's unedited Priority seed does not, because re-seeding
// Priority to it is the owner's call (r3 D12).
func TestApplyPushedPolicies_OrderedFallbackWithIntents(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	seed := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	hubSeed := directory.PushedPolicy{ID: "hub-seed", ScopeType: "precedence", ScopeID: "Priority",
		Strategy: directory.StrategyOrderedFallback, CreatedAt: seed, UpdatedAt: seed}
	applied, rejected, err := s.ApplyPushedPolicies(ctx, []directory.PushedPolicy{hubSeed}, withIntents)
	if err != nil || len(applied) != 0 || len(rejected) != 1 || rejected[0].Reason != directory.PushRejectSeededDefault {
		t.Fatalf("hub seed: applied %v rejected %+v %v; want seeded_default", applied, rejected, err)
	}
	if p, _ := s.GetPolicy(ctx, directory.ScopePrecedence, "Priority"); p.Strategy != directory.StrategyAllBearers || !p.Seeded {
		t.Fatalf("Priority after a refused seed: %+v", p)
	}
	chosen := hubSeed
	chosen.ID, chosen.UpdatedAt = "chosen", seed.Add(time.Minute)
	applied, rejected, err = s.ApplyPushedPolicies(ctx, []directory.PushedPolicy{chosen}, withIntents)
	if err != nil || !slices.Equal(applied, []string{"chosen"}) || len(rejected) != 0 {
		t.Fatalf("edited: applied %v rejected %+v %v", applied, rejected, err)
	}
	if p, _ := s.GetPolicy(ctx, directory.ScopePrecedence, "Priority"); p.Strategy != directory.StrategyOrderedFallback || p.Seeded {
		t.Fatalf("Priority after an edited row: %+v", p)
	}
}
