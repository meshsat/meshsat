package engine

import (
	"context"
	"errors"
	"slices"
	"testing"

	"meshsat/internal/directory"
	"meshsat/internal/types"
)

// B5 (HeMB-NG r3 D12, stage 0b): every precedence resolves to a strategy
// the bridge implements, through the policy rows as v65 left them, on
// the queue and on the delivery-intent path. [MESHSAT-1584]
func TestStrategy_EveryPrecedenceResolvesToSupported(t *testing.T) {
	d, _ := testDispatcher(t, directory.KindSMS)
	for _, intents := range []bool{false, true} {
		for _, prec := range types.AllPrecedences() {
			r, err := d.resolveStrategy(context.Background(), "no-such-contact", prec, "", nil, intents)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(SupportedStrategies(intents), r.Effective) || r.Requested != r.Effective ||
				r.Reason != "policy:precedence" || r.Version == "" {
				t.Errorf("intents %v, %s: %+v; want an implemented strategy from the precedence policy", intents, prec, r)
			}
		}
	}
}

func countRows(t *testing.T, d *Dispatcher, table string) int {
	t.Helper()
	var n int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// S2, request half, on the queue path: an explicit strategy the bridge
// does not implement is refused before anything is queued.
func TestSendToRecipient_ExplicitUnsupportedRefusedBeforeQueue(t *testing.T) {
	d, store := testDispatcher(t, directory.KindSMS, directory.KindMeshtastic)
	ctx := context.Background()
	c := &directory.Contact{DisplayName: "Ida"}
	_ = store.CreateContact(ctx, c)
	_ = store.AddAddress(ctx, &directory.Address{ContactID: c.ID, Kind: directory.KindSMS, Value: "+31655555555"})
	_ = store.AddAddress(ctx, &directory.Address{ContactID: c.ID, Kind: directory.KindMeshtastic, Value: "!0000abcd", PrimaryRank: 1})
	for _, s := range []directory.Strategy{directory.StrategyAnyReachable, directory.StrategyHeMBBonded, directory.StrategyOrderedFallback, "BOGUS"} {
		_, err := d.SendToRecipient(ctx, RecipientRef{ContactID: c.ID}, []byte("x"), SendOptions{Strategy: s})
		if !errors.Is(err, ErrUnsupportedStrategy) {
			t.Errorf("%s: %v; want ErrUnsupportedStrategy", s, err)
		}
	}
	if n := countRows(t, d, "message_deliveries"); n != 0 {
		t.Fatalf("%d deliveries queued by refused requests", n)
	}
}

// An edited policy row that names a strategy the bridge does not
// implement is kept and used as legacy compatibility, labelled; never
// presented as implemented. An unknown value stays PRIMARY_ONLY.
func TestSendToRecipient_EditedUnsupportedPolicyIsLabelled(t *testing.T) {
	d, store := testDispatcher(t, directory.KindSMS, directory.KindMeshtastic)
	ctx := context.Background()
	cases := []struct {
		stored    directory.Strategy
		effective directory.Strategy
		reason    string
		bearers   int
	}{
		{directory.StrategyHeMBBonded, directory.StrategyAllBearers, StrategyReasonLegacyCompat, 2},
		{directory.StrategyAnyReachable, directory.StrategyAllBearers, StrategyReasonLegacyCompat, 2},
		{directory.StrategyOrderedFallback, directory.StrategyAllBearers, StrategyReasonLegacyCompat, 2}, // queue path
		{"MYSTERY", directory.StrategyPrimaryOnly, StrategyReasonUnknown, 1},
	}
	for i, tc := range cases {
		c := &directory.Contact{DisplayName: "Jo"}
		_ = store.CreateContact(ctx, c)
		_ = store.AddAddress(ctx, &directory.Address{ContactID: c.ID, Kind: directory.KindSMS, Value: "+3165555550" + string(rune('0'+i))})
		_ = store.AddAddress(ctx, &directory.Address{ContactID: c.ID, Kind: directory.KindMeshtastic, Value: "!0000abc" + string(rune('0'+i)), PrimaryRank: 1})
		if err := store.UpsertPolicy(ctx, &directory.DispatchPolicy{ScopeType: directory.ScopeContact, ScopeID: c.ID, Strategy: tc.stored}); err != nil {
			t.Fatal(err)
		}
		res, err := d.SendToRecipient(ctx, RecipientRef{ContactID: c.ID}, []byte("x"), SendOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if res.RequestedStrategy != tc.stored || res.Strategy != tc.effective || res.StrategyReason != tc.reason || len(res.DeliveryIDs) != tc.bearers {
			t.Errorf("%s: requested %s, used %s (%s) on %d bearers; want %s (%s) on %d",
				tc.stored, res.RequestedStrategy, res.Strategy, res.StrategyReason, len(res.DeliveryIDs), tc.effective, tc.reason, tc.bearers)
		}
	}
}

// On the delivery-intent path the seeded Priority row is ALL_BEARERS since
// v65 (it no longer needs A-9's read-time mapping), a Priority row somebody
// set to ORDERED_FALLBACK is the real fallback, and an explicit
// unsupported strategy writes nothing.
func TestIntentStrategy_AfterTheMigration(t *testing.T) {
	h := newIntentHarness(t)
	id := h.contact(meshAddr(0), lxmfAddr(1))
	sub, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: id, Body: []byte("a"), Precedence: types.PrecedencePriority})
	if err != nil {
		t.Fatal(err)
	}
	if in := sub.Intents[0]; in.EffectiveStrategy != string(directory.StrategyAllBearers) || in.StrategyReason != "policy:precedence" {
		t.Fatalf("seeded Priority: %s (%s); want ALL_BEARERS from the policy", in.EffectiveStrategy, in.StrategyReason)
	}
	if err := h.store.UpsertPolicy(h.ctx, &directory.DispatchPolicy{ScopeType: directory.ScopePrecedence, ScopeID: "Priority",
		Strategy: directory.StrategyOrderedFallback}); err != nil {
		t.Fatal(err)
	}
	sub, err = h.esc.Submit(h.ctx, IntentRequest{ContactID: id, Body: []byte("b"), Precedence: types.PrecedencePriority})
	if err != nil {
		t.Fatal(err)
	}
	if in := sub.Intents[0]; in.EffectiveStrategy != string(directory.StrategyOrderedFallback) || in.StrategyReason != "policy:precedence" {
		t.Fatalf("Priority set to ORDERED_FALLBACK: %s (%s)", in.EffectiveStrategy, in.StrategyReason)
	}
	var before int
	_ = h.db.QueryRow(`SELECT COUNT(*) FROM delivery_submissions`).Scan(&before)
	for _, s := range []directory.Strategy{directory.StrategyAnyReachable, directory.StrategyHeMBBonded} {
		if _, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: id, Body: []byte("c"), Strategy: s}); !errors.Is(err, ErrUnsupportedStrategy) {
			t.Fatalf("%s: %v; want ErrUnsupportedStrategy", s, err)
		}
	}
	var after int
	_ = h.db.QueryRow(`SELECT COUNT(*) FROM delivery_submissions`).Scan(&after)
	if after != before {
		t.Fatal("a refused request wrote a submission")
	}
}
