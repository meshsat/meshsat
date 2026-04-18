package directory_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"meshsat/internal/database"
	"meshsat/internal/directory"
)

// newStore opens a fresh DB (with all migrations), enables foreign
// keys, and returns a SQLStore over it. Each test gets an isolated
// tempdir.
func newStore(t *testing.T) *directory.SQLStore {
	t.Helper()
	dir := t.TempDir()
	db, err := database.New(filepath.Join(dir, "dir.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable fks: %v", err)
	}
	return directory.NewSQLStore(db.DB)
}

// ensure *SQLStore satisfies Store at compile time.
var _ directory.Store = (*directory.SQLStore)(nil)

// ---------------------------------------------------------------------
// Kind enum
// ---------------------------------------------------------------------

func TestKindValid(t *testing.T) {
	for _, k := range directory.AllKinds {
		if !k.Valid() {
			t.Errorf("Kind %q reported invalid", k)
		}
	}
	if directory.Kind("BOGUS").Valid() {
		t.Error("BOGUS kind reported valid")
	}
}

// ---------------------------------------------------------------------
// Contact CRUD
// ---------------------------------------------------------------------

func TestCreateContact_Happy(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	c := &directory.Contact{DisplayName: "Alice Kowalski", Team: "Red", Role: "Medic"}
	if err := s.CreateContact(ctx, c); err != nil {
		t.Fatalf("create: %v", err)
	}
	if c.ID == "" {
		t.Error("ID not populated")
	}
	if c.Origin != directory.OriginLocal {
		t.Errorf("default origin: got %q want %q", c.Origin, directory.OriginLocal)
	}
	if c.CreatedAt == "" || c.UpdatedAt == "" {
		t.Error("timestamps not populated")
	}

	got, err := s.GetContact(ctx, c.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DisplayName != "Alice Kowalski" || got.Team != "Red" || got.Role != "Medic" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestCreateContact_NilAndEmptyReject(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if err := s.CreateContact(ctx, nil); !errors.Is(err, directory.ErrInvalid) {
		t.Errorf("nil contact: err=%v, want ErrInvalid", err)
	}
	if err := s.CreateContact(ctx, &directory.Contact{}); !errors.Is(err, directory.ErrInvalid) {
		t.Errorf("empty display_name: err=%v, want ErrInvalid", err)
	}
}

func TestGetContact_NotFound(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	_, err := s.GetContact(ctx, "nonexistent-id")
	if !errors.Is(err, directory.ErrNotFound) {
		t.Errorf("err=%v, want ErrNotFound", err)
	}
	_, err = s.GetContact(ctx, "")
	if !errors.Is(err, directory.ErrInvalid) {
		t.Errorf("empty id err=%v, want ErrInvalid", err)
	}
}

func TestUpdateContact(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	c := &directory.Contact{DisplayName: "Original"}
	if err := s.CreateContact(ctx, c); err != nil {
		t.Fatal(err)
	}
	c.DisplayName = "Updated"
	c.Team = "Blue"
	c.TrustLevel = directory.TrustQR
	if err := s.UpdateContact(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetContact(ctx, c.ID)
	if got.DisplayName != "Updated" || got.Team != "Blue" || got.TrustLevel != directory.TrustQR {
		t.Errorf("update mismatch: %+v", got)
	}

	// Update on a missing row → ErrNotFound.
	missing := &directory.Contact{ID: "00000000-0000-4000-8000-000000000000", DisplayName: "X"}
	if err := s.UpdateContact(ctx, missing); !errors.Is(err, directory.ErrNotFound) {
		t.Errorf("missing row: err=%v, want ErrNotFound", err)
	}
}

func TestListContactsFilter(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	_ = s.CreateContact(ctx, &directory.Contact{DisplayName: "Alice", TenantID: "acme", Team: "Red"})
	_ = s.CreateContact(ctx, &directory.Contact{DisplayName: "Bob", TenantID: "acme", Team: "Blue"})
	_ = s.CreateContact(ctx, &directory.Contact{DisplayName: "Charlie", TenantID: "other", Team: "Red"})

	all, _ := s.ListContacts(ctx, directory.ContactFilter{})
	if len(all) != 3 {
		t.Errorf("list all: got %d, want 3", len(all))
	}

	acmeRed, _ := s.ListContacts(ctx, directory.ContactFilter{TenantID: "acme", Team: "Red"})
	if len(acmeRed) != 1 || acmeRed[0].DisplayName != "Alice" {
		t.Errorf("tenant+team filter: got %+v", acmeRed)
	}

	ab, _ := s.ListContacts(ctx, directory.ContactFilter{NameLike: "A%"})
	if len(ab) != 1 || ab[0].DisplayName != "Alice" {
		t.Errorf("name_like: got %+v", ab)
	}

	paged, _ := s.ListContacts(ctx, directory.ContactFilter{Limit: 2})
	if len(paged) != 2 {
		t.Errorf("limit: got %d, want 2", len(paged))
	}
}

// ---------------------------------------------------------------------
// Addresses
// ---------------------------------------------------------------------

func TestAddAddress_Happy(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	c := &directory.Contact{DisplayName: "Bob"}
	_ = s.CreateContact(ctx, c)

	a := &directory.Address{
		ContactID: c.ID,
		Kind:      directory.KindSMS,
		Value:     "+31612345678",
		Label:     "Mobile",
	}
	if err := s.AddAddress(ctx, a); err != nil {
		t.Fatalf("add: %v", err)
	}
	if a.ID == "" || a.BearerHint != 50 {
		t.Errorf("defaults not populated: %+v", a)
	}

	got, err := s.GetAddress(ctx, a.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Value != "+31612345678" {
		t.Errorf("value round-trip: %s", got.Value)
	}
}

func TestAddAddress_InvalidKindRejected(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	c := &directory.Contact{DisplayName: "X"}
	_ = s.CreateContact(ctx, c)
	err := s.AddAddress(ctx, &directory.Address{
		ContactID: c.ID, Kind: "BOGUS", Value: "x",
	})
	if !errors.Is(err, directory.ErrInvalid) {
		t.Errorf("invalid kind: err=%v, want ErrInvalid", err)
	}
}

func TestAddAddress_UniqueKindValueConflict(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	c1 := &directory.Contact{DisplayName: "A"}
	_ = s.CreateContact(ctx, c1)
	c2 := &directory.Contact{DisplayName: "B"}
	_ = s.CreateContact(ctx, c2)

	a := &directory.Address{ContactID: c1.ID, Kind: directory.KindSMS, Value: "+31600000000"}
	if err := s.AddAddress(ctx, a); err != nil {
		t.Fatal(err)
	}
	dup := &directory.Address{ContactID: c2.ID, Kind: directory.KindSMS, Value: "+31600000000"}
	err := s.AddAddress(ctx, dup)
	if !errors.Is(err, directory.ErrConflict) {
		t.Errorf("dup kind+value: err=%v, want ErrConflict", err)
	}
}

func TestUpdateAndDeleteAddress(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	c := &directory.Contact{DisplayName: "U"}
	_ = s.CreateContact(ctx, c)
	a := &directory.Address{ContactID: c.ID, Kind: directory.KindSMS, Value: "+31600000001"}
	_ = s.AddAddress(ctx, a)

	a.Label = "Work"
	if err := s.UpdateAddress(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetAddress(ctx, a.ID)
	if got.Label != "Work" {
		t.Errorf("update label: %s", got.Label)
	}

	if err := s.DeleteAddress(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.GetAddress(ctx, a.ID)
	if !errors.Is(err, directory.ErrNotFound) {
		t.Errorf("post-delete get: err=%v, want ErrNotFound", err)
	}
	if err := s.DeleteAddress(ctx, "missing"); !errors.Is(err, directory.ErrNotFound) {
		t.Errorf("delete missing: err=%v", err)
	}
}

// ---------------------------------------------------------------------
// Resolve & FindByAddress — the acceptance-critical paths
// ---------------------------------------------------------------------

func TestResolve_LoadsAddressesAndKeys(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	c := &directory.Contact{DisplayName: "Diana", Team: "Red"}
	_ = s.CreateContact(ctx, c)

	// Three addresses across two kinds; primary_rank enforces order.
	for _, a := range []directory.Address{
		{ContactID: c.ID, Kind: directory.KindSMS, Value: "+31600001111", PrimaryRank: 0},
		{ContactID: c.ID, Kind: directory.KindSMS, Value: "+31600001112", PrimaryRank: 1},
		{ContactID: c.ID, Kind: directory.KindMeshtastic, Value: "!abcd1234", PrimaryRank: 0},
	} {
		a := a
		_ = s.AddAddress(ctx, &a)
	}
	// One active + one retired key.
	active := &directory.ContactKey{
		ContactID: c.ID, Kind: directory.KeyAES256GCMShared, Version: 1,
		TrustAnchor: directory.TrustAnchorQR,
		PublicData:  []byte{0xAA, 0xBB},
	}
	_ = s.AddKey(ctx, active)
	retired := &directory.ContactKey{
		ContactID: c.ID, Kind: directory.KeyAES256GCMShared, Version: 2,
		Status: directory.KeyRetired,
	}
	_ = s.AddKey(ctx, retired)

	got, err := s.Resolve(ctx, c.ID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ID != c.ID {
		t.Errorf("id mismatch")
	}
	if len(got.Addresses) != 3 {
		t.Errorf("addresses len: got %d, want 3", len(got.Addresses))
	}
	if len(got.Keys) != 2 {
		t.Errorf("keys len: got %d, want 2", len(got.Keys))
	}

	// Helper correctness.
	if prim := got.PrimaryAddress(directory.KindSMS); prim == nil || prim.Value != "+31600001111" {
		t.Errorf("PrimaryAddress(SMS): %+v", prim)
	}
	smsOrdered := got.AddressByKind(directory.KindSMS)
	if len(smsOrdered) != 2 || smsOrdered[0].PrimaryRank != 0 {
		t.Errorf("AddressByKind(SMS) ordering: %+v", smsOrdered)
	}
	if got.PrimaryAddress(directory.KindAPRS) != nil {
		t.Error("PrimaryAddress(APRS): want nil")
	}
}

func TestResolve_NotFoundAndEmpty(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	_, err := s.Resolve(ctx, "does-not-exist")
	if !errors.Is(err, directory.ErrNotFound) {
		t.Errorf("missing: err=%v, want ErrNotFound", err)
	}
	_, err = s.Resolve(ctx, "")
	if !errors.Is(err, directory.ErrInvalid) {
		t.Errorf("empty: err=%v, want ErrInvalid", err)
	}
}

func TestFindByAddress_HappyAndErrors(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	c := &directory.Contact{DisplayName: "Eve"}
	_ = s.CreateContact(ctx, c)
	_ = s.AddAddress(ctx, &directory.Address{
		ContactID: c.ID, Kind: directory.KindMeshtastic, Value: "!11223344",
	})
	_ = s.AddAddress(ctx, &directory.Address{
		ContactID: c.ID, Kind: directory.KindSMS, Value: "+31655556666",
	})

	got, err := s.FindByAddress(ctx, directory.KindMeshtastic, "!11223344")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.DisplayName != "Eve" {
		t.Errorf("display_name: %q", got.DisplayName)
	}
	if len(got.Addresses) != 2 {
		t.Errorf("eager-load addresses: got %d, want 2", len(got.Addresses))
	}

	// Unknown address → ErrNotFound.
	_, err = s.FindByAddress(ctx, directory.KindSMS, "+31000000000")
	if !errors.Is(err, directory.ErrNotFound) {
		t.Errorf("unknown address: err=%v, want ErrNotFound", err)
	}
	// Invalid kind → ErrInvalid.
	_, err = s.FindByAddress(ctx, "BOGUS", "x")
	if !errors.Is(err, directory.ErrInvalid) {
		t.Errorf("bogus kind: err=%v, want ErrInvalid", err)
	}
	// Empty value → ErrInvalid.
	_, err = s.FindByAddress(ctx, directory.KindSMS, "")
	if !errors.Is(err, directory.ErrInvalid) {
		t.Errorf("empty value: err=%v, want ErrInvalid", err)
	}
}

// ---------------------------------------------------------------------
// DeleteContact cascade
// ---------------------------------------------------------------------

func TestDeleteContact_CascadesEverything(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	c := &directory.Contact{DisplayName: "Frank"}
	_ = s.CreateContact(ctx, c)
	_ = s.AddAddress(ctx, &directory.Address{
		ContactID: c.ID, Kind: directory.KindSMS, Value: "+31677778888",
	})
	_ = s.AddKey(ctx, &directory.ContactKey{
		ContactID: c.ID, Kind: directory.KeyEd25519SignPub, PublicData: []byte{1},
	})
	g := &directory.Group{DisplayName: "T", Kind: directory.GroupTeam}
	_ = s.CreateGroup(ctx, g)
	_ = s.AddMember(ctx, g.ID, c.ID, "")

	if err := s.DeleteContact(ctx, c.ID); err != nil {
		t.Fatal(err)
	}

	if addrs, _ := s.ListAddresses(ctx, c.ID); len(addrs) != 0 {
		t.Errorf("address leak: %d", len(addrs))
	}
	if keys, _ := s.ListKeys(ctx, c.ID, false); len(keys) != 0 {
		t.Errorf("key leak: %d", len(keys))
	}
	members, _ := s.ListGroupMembers(ctx, g.ID)
	if len(members) != 0 {
		t.Errorf("membership leak: %d", len(members))
	}

	if err := s.DeleteContact(ctx, "missing"); !errors.Is(err, directory.ErrNotFound) {
		t.Errorf("delete missing: err=%v", err)
	}
}

// ---------------------------------------------------------------------
// Keys — lifecycle
// ---------------------------------------------------------------------

func TestKeyLifecycle(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	c := &directory.Contact{DisplayName: "K"}
	_ = s.CreateContact(ctx, c)

	k1 := &directory.ContactKey{
		ContactID: c.ID, Kind: directory.KeyEd25519SignPub,
		PublicData: []byte{0xFE}, TrustAnchor: directory.TrustAnchorHub,
	}
	if err := s.AddKey(ctx, k1); err != nil {
		t.Fatal(err)
	}
	if k1.Version != 1 || k1.Status != directory.KeyActive {
		t.Errorf("defaults: %+v", k1)
	}

	k2 := &directory.ContactKey{
		ContactID: c.ID, Kind: directory.KeyEd25519SignPub, Version: 2,
	}
	_ = s.AddKey(ctx, k2)

	all, _ := s.ListKeys(ctx, c.ID, false)
	if len(all) != 2 {
		t.Errorf("list all: got %d, want 2", len(all))
	}

	if err := s.RetireKey(ctx, k1.ID); err != nil {
		t.Fatal(err)
	}
	onlyActive, _ := s.ListKeys(ctx, c.ID, true)
	if len(onlyActive) != 1 || onlyActive[0].ID != k2.ID {
		t.Errorf("only-active filter wrong: %+v", onlyActive)
	}

	if err := s.RevokeKey(ctx, k2.ID); err != nil {
		t.Fatal(err)
	}
	onlyActive, _ = s.ListKeys(ctx, c.ID, true)
	if len(onlyActive) != 0 {
		t.Errorf("all-revoked: got %d keys", len(onlyActive))
	}

	if err := s.RetireKey(ctx, "missing"); !errors.Is(err, directory.ErrNotFound) {
		t.Errorf("retire missing: err=%v", err)
	}
}

// ---------------------------------------------------------------------
// Groups + members
// ---------------------------------------------------------------------

func TestGroupAndMembers(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	c1 := &directory.Contact{DisplayName: "G1"}
	c2 := &directory.Contact{DisplayName: "G2"}
	_ = s.CreateContact(ctx, c1)
	_ = s.CreateContact(ctx, c2)

	g := &directory.Group{DisplayName: "Red Team", Kind: directory.GroupTeam, TenantID: "acme"}
	if err := s.CreateGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	_ = s.AddMember(ctx, g.ID, c1.ID, "leader")
	_ = s.AddMember(ctx, g.ID, c2.ID, "")

	got, err := s.GetGroup(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Members) != 2 {
		t.Errorf("members count: %d", len(got.Members))
	}
	contacts, _ := s.ListGroupMembers(ctx, g.ID)
	if len(contacts) != 2 {
		t.Errorf("ListGroupMembers: %d", len(contacts))
	}

	if err := s.RemoveMember(ctx, g.ID, c1.ID); err != nil {
		t.Fatal(err)
	}
	contacts, _ = s.ListGroupMembers(ctx, g.ID)
	if len(contacts) != 1 || contacts[0].DisplayName != "G2" {
		t.Errorf("after remove: %+v", contacts)
	}

	if err := s.UpdateGroup(ctx, &directory.Group{ID: g.ID, DisplayName: "Blue Team", Kind: directory.GroupTeam}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetGroup(ctx, g.ID)
	if got.DisplayName != "Blue Team" {
		t.Errorf("update: %s", got.DisplayName)
	}

	groups, _ := s.ListGroups(ctx, "acme")
	if len(groups) != 0 {
		t.Errorf("list acme (tenant cleared by update): %d", len(groups))
		// Update cleared tenant; list with empty should still see it.
	}
	all, _ := s.ListGroups(ctx, "")
	if len(all) != 1 {
		t.Errorf("list all: %d", len(all))
	}

	if err := s.DeleteGroup(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.GetGroup(ctx, g.ID)
	if !errors.Is(err, directory.ErrNotFound) {
		t.Errorf("post-delete get: err=%v", err)
	}
}

// ---------------------------------------------------------------------
// Dispatch policy
// ---------------------------------------------------------------------

func TestPolicy_SeededPrecedenceDefaults(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// From migration v48 seed.
	cases := map[string]directory.Strategy{
		"Override":  directory.StrategyHeMBBonded,
		"Flash":     directory.StrategyHeMBBonded,
		"Immediate": directory.StrategyAnyReachable,
		"Priority":  directory.StrategyOrderedFallback,
		"Routine":   directory.StrategyPrimaryOnly,
		"Deferred":  directory.StrategyPrimaryOnly,
	}
	for prec, want := range cases {
		p, err := s.GetPolicy(ctx, directory.ScopePrecedence, prec)
		if err != nil {
			t.Errorf("get precedence %q: %v", prec, err)
			continue
		}
		if p.Strategy != want {
			t.Errorf("%s strategy: got %q, want %q", prec, p.Strategy, want)
		}
	}
	// Default row exists.
	def, err := s.GetPolicy(ctx, directory.ScopeDefault, "")
	if err != nil {
		t.Fatalf("default policy: %v", err)
	}
	if def.Strategy != directory.StrategyPrimaryOnly {
		t.Errorf("default strategy: got %q", def.Strategy)
	}
}

func TestPolicy_UpsertReplacesMatchingScope(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// Upsert same precedence row with a different strategy.
	p := &directory.DispatchPolicy{
		ScopeType: directory.ScopePrecedence,
		ScopeID:   "Routine",
		Strategy:  directory.StrategyHeMBBonded,
	}
	if err := s.UpsertPolicy(ctx, p); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, _ := s.GetPolicy(ctx, directory.ScopePrecedence, "Routine")
	if got.Strategy != directory.StrategyHeMBBonded {
		t.Errorf("after upsert: got %q, want HEMB_BONDED", got.Strategy)
	}

	// ListPolicies returns everything (seeded + upserted — which overwrote).
	all, err := s.ListPolicies(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 7 {
		t.Errorf("list count: got %d, want 7 (upsert overwrote Routine)", len(all))
	}

	// Insert a new contact-scoped policy.
	pc := &directory.DispatchPolicy{
		ScopeType: directory.ScopeContact,
		ScopeID:   "some-contact-id",
		Strategy:  directory.StrategyAllBearers,
	}
	_ = s.UpsertPolicy(ctx, pc)
	if err := s.DeletePolicy(ctx, pc.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.DeletePolicy(ctx, "nonexistent"); !errors.Is(err, directory.ErrNotFound) {
		t.Errorf("delete missing: err=%v", err)
	}
}

func TestPolicy_InvalidArgs(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if _, err := s.GetPolicy(ctx, "", ""); !errors.Is(err, directory.ErrInvalid) {
		t.Errorf("empty scope: err=%v", err)
	}
	if err := s.UpsertPolicy(ctx, nil); !errors.Is(err, directory.ErrInvalid) {
		t.Errorf("nil policy: err=%v", err)
	}
	if err := s.UpsertPolicy(ctx, &directory.DispatchPolicy{}); !errors.Is(err, directory.ErrInvalid) {
		t.Errorf("missing scope+strategy: err=%v", err)
	}
}

// ---------------------------------------------------------------------
// Get paths (gap fillers)
// ---------------------------------------------------------------------

func TestGetAddressAndGetKey_InputValidation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if _, err := s.GetAddress(ctx, ""); !errors.Is(err, directory.ErrInvalid) {
		t.Errorf("GetAddress empty: %v", err)
	}
	if _, err := s.GetAddress(ctx, "nope"); !errors.Is(err, directory.ErrNotFound) {
		t.Errorf("GetAddress missing: %v", err)
	}
	if _, err := s.GetKey(ctx, ""); !errors.Is(err, directory.ErrInvalid) {
		t.Errorf("GetKey empty: %v", err)
	}
	if _, err := s.GetKey(ctx, "nope"); !errors.Is(err, directory.ErrNotFound) {
		t.Errorf("GetKey missing: %v", err)
	}

	c := &directory.Contact{DisplayName: "G"}
	_ = s.CreateContact(ctx, c)
	k := &directory.ContactKey{
		ContactID: c.ID, Kind: directory.KeyX25519LongTermPub,
		PublicData: []byte{0xCA, 0xFE},
	}
	_ = s.AddKey(ctx, k)
	got, err := s.GetKey(ctx, k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.PublicData) != 2 || got.PublicData[0] != 0xCA {
		t.Errorf("public_data round-trip: %x", got.PublicData)
	}

	// Group/member edge cases: empty IDs.
	if err := s.AddMember(ctx, "", c.ID, ""); !errors.Is(err, directory.ErrInvalid) {
		t.Errorf("AddMember empty group: %v", err)
	}
	if err := s.RemoveMember(ctx, "", c.ID); !errors.Is(err, directory.ErrInvalid) {
		t.Errorf("RemoveMember empty: %v", err)
	}
	if _, err := s.ListGroupMembers(ctx, ""); !errors.Is(err, directory.ErrInvalid) {
		t.Errorf("ListGroupMembers empty: %v", err)
	}
}

func TestResolveEmptyCollections(t *testing.T) {
	// Resolve a contact with no addresses and no keys — attach must still
	// succeed and return empty slices (not nil-panics).
	s := newStore(t)
	ctx := context.Background()
	c := &directory.Contact{DisplayName: "Empty"}
	_ = s.CreateContact(ctx, c)
	got, err := s.Resolve(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Addresses) != 0 || len(got.Keys) != 0 {
		t.Errorf("expected empty slices, got %d addrs / %d keys",
			len(got.Addresses), len(got.Keys))
	}
}

// ---------------------------------------------------------------------
// Resolver interaction with the v44/v45 legacy backfill
// ---------------------------------------------------------------------

// TestResolver_SeesLegacyBackfill uses the low-level rawDB path: the
// database.New() already ran the v44 backfill from any v23
// contacts/contact_addresses we seed via raw Exec *before* calling
// New. Here we instead verify that a Store working on the freshly
// migrated schema can round-trip through Resolve/FindByAddress without
// surprises with the 32-char hex IDs that v44 produces for
// backfilled rows.
func TestResolver_32CharHexIDCoexistsWithUUID(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// Create two contacts with distinct ID shapes.
	hex32 := strings.Repeat("a", 32) // shaped like a backfilled legacy row
	uuidlike := directory.NewID()    // shaped like a newly-created row

	for _, c := range []*directory.Contact{
		{ID: hex32, DisplayName: "Legacy-Shaped"},
		{ID: uuidlike, DisplayName: "UUID-Shaped"},
	} {
		if err := s.CreateContact(ctx, c); err != nil {
			t.Fatalf("create %q: %v", c.DisplayName, err)
		}
	}

	for _, id := range []string{hex32, uuidlike} {
		got, err := s.Resolve(ctx, id)
		if err != nil {
			t.Errorf("resolve %q: %v", id, err)
			continue
		}
		if got.ID != id {
			t.Errorf("id round-trip: got %q want %q", got.ID, id)
		}
	}
}
