package engine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/directory"
)

// testDispatcher wires a Dispatcher backed by a fresh bridge DB,
// directory.SQLStore resolver, and a seeded set of interfaces for
// each bearer kind the test uses. Returns both the dispatcher and
// the store so callers can create contacts + addresses.
func testDispatcher(t *testing.T, kinds ...directory.Kind) (*Dispatcher, *directory.SQLStore) {
	t.Helper()
	dir := t.TempDir()
	db, err := database.New(filepath.Join(dir, "bridge.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	reg := channel.NewRegistry()
	d := NewDispatcher(db, reg, nil, nil)
	store := directory.NewSQLStore(db.DB)
	d.SetRecipientResolver(store)

	// Clear any interfaces seeded by migrations and re-insert the
	// bearer kinds the test explicitly needs. Avoids the
	// UNIQUE(interfaces.id) collision on the default mesh_0 row.
	if _, err := db.Exec(`DELETE FROM interfaces`); err != nil {
		t.Fatalf("reset interfaces: %v", err)
	}
	for _, k := range kinds {
		chanType, ok := kindToChannelType[k]
		if !ok {
			t.Fatalf("no channel type for kind %s", k)
		}
		iface := database.Interface{
			ID:                chanType + "_0",
			ChannelType:       chanType,
			Label:             string(k) + " test",
			Enabled:           true,
			Config:            "{}",
			IngressTransforms: "[]",
			EgressTransforms:  "[]",
		}
		if err := db.InsertInterface(&iface); err != nil {
			t.Fatalf("seed interface %s: %v", chanType, err)
		}
	}
	return d, store
}

// --- MESHSAT-544 S2-01 tests --------------------------------------------

func TestSendToRecipient_PrimaryOnly_QueuesOneDelivery(t *testing.T) {
	d, store := testDispatcher(t, directory.KindSMS, directory.KindMeshtastic)
	ctx := context.Background()

	c := &directory.Contact{DisplayName: "Alice"}
	if err := store.CreateContact(ctx, c); err != nil {
		t.Fatal(err)
	}
	// Primary (rank 0): SMS. Secondary (rank 1): mesh.
	if err := store.AddAddress(ctx, &directory.Address{
		ContactID: c.ID, Kind: directory.KindSMS, Value: "+31612345678", PrimaryRank: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddAddress(ctx, &directory.Address{
		ContactID: c.ID, Kind: directory.KindMeshtastic, Value: "!abcd1234", PrimaryRank: 1,
	}); err != nil {
		t.Fatal(err)
	}

	res, err := d.SendToRecipient(ctx, RecipientRef{ContactID: c.ID},
		[]byte("hello Alice"), SendOptions{
			Strategy:   directory.StrategyPrimaryOnly,
			Precedence: "Flash",
		})
	if err != nil {
		t.Fatalf("SendToRecipient: %v", err)
	}
	if got := len(res.DeliveryIDs); got != 1 {
		t.Fatalf("bearer-count: got %d, want 1", got)
	}
	if len(res.DeliveryIDs[directory.KindSMS]) != 1 {
		t.Errorf("expected single SMS delivery, got %+v", res.DeliveryIDs)
	}
	if res.Strategy != directory.StrategyPrimaryOnly {
		t.Errorf("strategy: got %q", res.Strategy)
	}

	// Verify the queued delivery carries the Flash precedence.
	row, _ := d.db.GetDelivery(res.DeliveryIDs[directory.KindSMS][0])
	if row.Precedence != "Flash" {
		t.Errorf("delivery precedence: got %q, want Flash", row.Precedence)
	}
}

func TestSendToRecipient_AllBearers_QueuesOnEveryAddress(t *testing.T) {
	d, store := testDispatcher(t, directory.KindSMS, directory.KindMeshtastic, directory.KindAPRS)
	ctx := context.Background()

	c := &directory.Contact{DisplayName: "Broadcast"}
	_ = store.CreateContact(ctx, c)
	for _, a := range []directory.Address{
		{ContactID: c.ID, Kind: directory.KindSMS, Value: "+316200"},
		{ContactID: c.ID, Kind: directory.KindMeshtastic, Value: "!bb"},
		{ContactID: c.ID, Kind: directory.KindAPRS, Value: "XX1X-1"},
	} {
		a := a
		_ = store.AddAddress(ctx, &a)
	}

	res, err := d.SendToRecipient(ctx, RecipientRef{ContactID: c.ID},
		[]byte("broadcast"), SendOptions{Strategy: directory.StrategyAllBearers})
	if err != nil {
		t.Fatalf("SendToRecipient: %v", err)
	}
	if len(res.DeliveryIDs) != 3 {
		t.Errorf("expected 3 bearers, got %d: %+v", len(res.DeliveryIDs), res.DeliveryIDs)
	}
	for _, kind := range []directory.Kind{directory.KindSMS, directory.KindMeshtastic, directory.KindAPRS} {
		if len(res.DeliveryIDs[kind]) != 1 {
			t.Errorf("kind %s: got %d deliveries", kind, len(res.DeliveryIDs[kind]))
		}
	}
}

func TestSendToRecipient_RawEscapeHatch(t *testing.T) {
	d, _ := testDispatcher(t, directory.KindSMS)
	ctx := context.Background()
	res, err := d.SendToRecipient(ctx, RecipientRef{
		Raw: &RawRecipient{InterfaceID: "sms_0", Address: "+31600000000"},
	}, []byte("raw msg"), SendOptions{Precedence: "Immediate"})
	if err != nil {
		t.Fatalf("raw: %v", err)
	}
	if len(res.DeliveryIDs[directory.Kind("")]) != 1 {
		t.Errorf("raw: expected 1 delivery under empty-kind, got %+v", res.DeliveryIDs)
	}
}

func TestSendToRecipient_NoContactID_NoRaw_Errors(t *testing.T) {
	d, _ := testDispatcher(t)
	_, err := d.SendToRecipient(context.Background(), RecipientRef{}, []byte("x"), SendOptions{})
	if err == nil {
		t.Fatal("expected error for empty RecipientRef")
	}
}

func TestSendToRecipient_StrategyResolution_CallerOverrideWins(t *testing.T) {
	// Seed a contact-scoped policy of ALL_BEARERS for a contact that
	// only has one SMS address. When the caller passes PRIMARY_ONLY,
	// the resolver must honour it rather than the stored policy.
	d, store := testDispatcher(t, directory.KindSMS, directory.KindMeshtastic)
	ctx := context.Background()

	c := &directory.Contact{DisplayName: "Carol"}
	_ = store.CreateContact(ctx, c)
	_ = store.AddAddress(ctx, &directory.Address{
		ContactID: c.ID, Kind: directory.KindSMS, Value: "+31612999999", PrimaryRank: 0,
	})
	_ = store.AddAddress(ctx, &directory.Address{
		ContactID: c.ID, Kind: directory.KindMeshtastic, Value: "!cc", PrimaryRank: 1,
	})
	_ = store.UpsertPolicy(ctx, &directory.DispatchPolicy{
		ScopeType: directory.ScopeContact,
		ScopeID:   c.ID,
		Strategy:  directory.StrategyAllBearers,
	})

	// Caller override → PRIMARY_ONLY must win over the stored ALL_BEARERS.
	res, err := d.SendToRecipient(ctx, RecipientRef{ContactID: c.ID},
		[]byte("x"), SendOptions{Strategy: directory.StrategyPrimaryOnly})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DeliveryIDs) != 1 {
		t.Errorf("caller override ignored: %+v", res.DeliveryIDs)
	}
	if res.Strategy != directory.StrategyPrimaryOnly {
		t.Errorf("resolved strategy: got %q", res.Strategy)
	}
}

func TestSendToRecipient_StrategyResolution_FallsToContactPolicy(t *testing.T) {
	d, store := testDispatcher(t, directory.KindSMS, directory.KindMeshtastic)
	ctx := context.Background()

	c := &directory.Contact{DisplayName: "Dave"}
	_ = store.CreateContact(ctx, c)
	_ = store.AddAddress(ctx, &directory.Address{
		ContactID: c.ID, Kind: directory.KindSMS, Value: "+31633333333", PrimaryRank: 0,
	})
	_ = store.AddAddress(ctx, &directory.Address{
		ContactID: c.ID, Kind: directory.KindMeshtastic, Value: "!dd", PrimaryRank: 1,
	})
	_ = store.UpsertPolicy(ctx, &directory.DispatchPolicy{
		ScopeType: directory.ScopeContact,
		ScopeID:   c.ID,
		Strategy:  directory.StrategyAllBearers,
	})

	// No caller override → resolver picks up the contact-scoped policy.
	res, err := d.SendToRecipient(ctx, RecipientRef{ContactID: c.ID},
		[]byte("x"), SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Strategy != directory.StrategyAllBearers {
		t.Errorf("resolved strategy: got %q, want ALL_BEARERS", res.Strategy)
	}
	if len(res.DeliveryIDs) != 2 {
		t.Errorf("expected 2 bearers under ALL_BEARERS, got %d", len(res.DeliveryIDs))
	}
}

func TestSendToRecipient_StrategyResolution_PrecedenceDefault(t *testing.T) {
	// No contact policy → falls to precedence-scoped default (seeded
	// by v48 migration: Flash → HEMB_BONDED which selectAddresses
	// currently treats as ALL_BEARERS).
	d, store := testDispatcher(t, directory.KindSMS, directory.KindMeshtastic)
	ctx := context.Background()

	c := &directory.Contact{DisplayName: "Eve"}
	_ = store.CreateContact(ctx, c)
	_ = store.AddAddress(ctx, &directory.Address{
		ContactID: c.ID, Kind: directory.KindSMS, Value: "+31644444444", PrimaryRank: 0,
	})
	_ = store.AddAddress(ctx, &directory.Address{
		ContactID: c.ID, Kind: directory.KindMeshtastic, Value: "!ee", PrimaryRank: 1,
	})

	res, err := d.SendToRecipient(ctx, RecipientRef{ContactID: c.ID},
		[]byte("urgent"), SendOptions{Precedence: "Flash"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Strategy != directory.StrategyHeMBBonded {
		t.Errorf("Flash default: got %q, want HEMB_BONDED", res.Strategy)
	}
	// HEMB_BONDED falls back to ALL_BEARERS in selectAddresses (MVP).
	if len(res.DeliveryIDs) != 2 {
		t.Errorf("expected 2 bearers, got %d", len(res.DeliveryIDs))
	}
}

func TestSendToRecipient_UnknownContact_Errors(t *testing.T) {
	d, _ := testDispatcher(t, directory.KindSMS)
	_, err := d.SendToRecipient(context.Background(),
		RecipientRef{ContactID: "nonexistent"}, []byte("x"), SendOptions{})
	if err == nil || !errors.Is(err, errMissing(err)) && err.Error() == "" {
		// Just check that we got a non-nil error; specific shape not important
		if err == nil {
			t.Fatal("expected error")
		}
	}
}

// errMissing is a no-op helper; the TestSendToRecipient_UnknownContact
// assertion above only needs a non-nil error.
func errMissing(_ error) error { return nil }
