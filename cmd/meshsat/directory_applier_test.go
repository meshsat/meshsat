package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/engine"
	"meshsat/internal/hubreporter"
)

// S2, push half, through the applier main wires (HeMB-NG r3 D12): a Hub
// push with a supported and an unsupported policy row applies the first
// and names the second; the contacts of the same push apply as before.
// The predicate is the dispatcher's, here with delivery intents off.
// [MESHSAT-1584]
func TestDirectoryApplier_PushAppliesSupportedRowsAndNamesTheRest(t *testing.T) {
	db, err := database.New(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := engine.NewDispatcher(db, channel.NewRegistry(), nil, nil)
	store := directory.NewSQLStore(db.DB)
	a := &bridgeDirectoryApplier{dir: store, supported: d.StrategySupported}
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	snap := &hubreporter.DirectorySnapshot{
		TenantID: "acme", Version: 3,
		Contacts: []hubreporter.DirectoryContact{{ID: "11111111-1111-4111-8111-111111111111", DisplayName: "Mo",
			Addresses: []hubreporter.DirectoryAddress{{Kind: "SMS", Value: "+31677777777"}}, CreatedAt: at, UpdatedAt: at}},
		Policies: []hubreporter.DirectoryPolicy{
			{ID: "hub-mo", ScopeType: "contact", ScopeID: "11111111-1111-4111-8111-111111111111", Strategy: "ALL_BEARERS", CreatedAt: at, UpdatedAt: at},
			{ID: "hub-flash", ScopeType: "precedence", ScopeID: "Flash", Strategy: "HEMB_BONDED", CreatedAt: at, UpdatedAt: at},
			{ID: "hub-priority", ScopeType: "precedence", ScopeID: "Priority", Strategy: "ORDERED_FALLBACK", CreatedAt: at, UpdatedAt: at},
		},
	}
	report, err := a.ApplySnapshot(context.Background(), snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.PoliciesApplied) != 1 || report.PoliciesApplied[0] != "hub-mo" {
		t.Fatalf("applied %v; want hub-mo", report.PoliciesApplied)
	}
	rej := map[string]string{}
	for _, r := range report.PoliciesRejected {
		rej[r.ID] = r.Reason
	}
	if len(rej) != 2 || rej["hub-flash"] != directory.PushRejectStrategy || rej["hub-priority"] != directory.PushRejectStrategy {
		t.Fatalf("rejected %+v; want hub-flash and hub-priority as unsupported_strategy", report.PoliciesRejected)
	}
	if p, err := store.GetPolicy(context.Background(), directory.ScopeContact, "11111111-1111-4111-8111-111111111111"); err != nil || p.Strategy != directory.StrategyAllBearers {
		t.Fatalf("contact policy: %+v %v", p, err)
	}
	for _, prec := range []string{"Flash", "Priority"} {
		if p, _ := store.GetPolicy(context.Background(), directory.ScopePrecedence, prec); p.Strategy != directory.StrategyAllBearers || !p.Seeded {
			t.Fatalf("%s changed by a refused row: %+v", prec, p)
		}
	}
	if c, err := store.Resolve(context.Background(), "11111111-1111-4111-8111-111111111111"); err != nil || len(c.Addresses) != 1 {
		t.Fatalf("the push's contact: %+v %v", c, err)
	}
}
