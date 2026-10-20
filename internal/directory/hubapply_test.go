package directory_test

import (
	"context"
	"path/filepath"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/directory"
)

func newHubStore(t *testing.T) (*directory.SQLStore, *database.DB) {
	t.Helper()
	db, err := database.New(filepath.Join(t.TempDir(), "dir.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	return directory.NewSQLStore(db.DB), db
}

func allStrategies(directory.Strategy) bool { return true }

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

func apply(t *testing.T, s *directory.SQLStore, snap directory.HubSnapshot) directory.HubApplyReport {
	t.Helper()
	rep, err := s.ApplyHubSnapshot(context.Background(), snap, allStrategies)
	if err != nil {
		t.Fatalf("apply v%d: %v", snap.Version, err)
	}
	return rep
}

func one(t *testing.T, db *database.DB, q string, args ...any) string {
	t.Helper()
	var v string
	if err := db.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

func count(t *testing.T, db *database.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func base() directory.HubSnapshot {
	return directory.HubSnapshot{TenantID: "t-1", Version: 5,
		Contacts: []directory.HubContact{
			{ID: "c-a", DisplayName: "Alice", Notes: "hub notes", TrustLevel: 1, HubVersion: 3,
				Addresses: []directory.HubAddress{{ID: "a-1", Kind: "TETRA", Value: "tetra:1001"}, {ID: "a-2", Kind: "MQTT", Value: "bridge:kit-b"}}},
			{ID: "c-b", DisplayName: "Bob", Addresses: []directory.HubAddress{{ID: "b-1", Kind: "SMS", Value: "+31600000002"}}},
		},
		Groups: []directory.HubGroup{{ID: "g-1", Name: "North team", Kind: "team", MemberIDs: []string{"c-a", "c-b"}}},
		Policies: []directory.PushedPolicy{
			{ID: "p-c", ScopeType: "contact", ScopeID: "c-a", Strategy: directory.StrategyAllBearers, Seeded: no()},
			{ID: "p-r", ScopeType: "precedence", ScopeID: "Routine", Strategy: directory.StrategyAllBearers, Seeded: no()},
			{ID: "p-s", ScopeType: "precedence", ScopeID: "Deferred", Strategy: directory.StrategyAllBearers, Seeded: yes()},
		},
	}
}

func TestHubSnapshotAppliesWithItsVersionAndOrigin(t *testing.T) {
	s, db := newHubStore(t)
	rep := apply(t, s, base())
	if !rep.Applied || rep.AppliedVersion != 5 || rep.TenantID != "t-1" || rep.Contacts != 2 || rep.Groups != 1 {
		t.Fatalf("report %+v", rep)
	}
	st, _ := s.GetHubState(context.Background())
	if st.TenantID != "t-1" || st.Version != 5 || st.RefusedReason != "" {
		t.Errorf("state %+v", st)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM directory_contacts WHERE origin = 'hub:t-1' AND tenant_id = 't-1'`); n != 2 {
		t.Errorf("%d hub contacts", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM directory_addresses WHERE origin = 'hub:t-1'`); n != 3 {
		t.Errorf("%d hub addresses", n)
	}
	if k := one(t, db, `SELECT kind || '/' || origin FROM directory_groups WHERE id = 'g-1'`); k != "TEAM/hub:t-1" {
		t.Errorf("group %s", k)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM directory_group_members WHERE group_id = 'g-1' AND origin = 'hub:t-1'`); n != 2 {
		t.Errorf("%d members", n)
	}
	if v := one(t, db, `SELECT strategy || '/' || origin || '/' || seeded FROM directory_dispatch_policy WHERE scope_type = 'precedence' AND scope_id = 'Routine'`); v != "ALL_BEARERS/hub:t-1/0" {
		t.Errorf("an operator's Hub policy: %s", v)
	}
	// The Hub's seeded default never replaces the bridge's own.
	if v := one(t, db, `SELECT strategy || '/' || origin || '/' || seeded FROM directory_dispatch_policy WHERE scope_type = 'precedence' AND scope_id = 'Deferred'`); v != "PRIMARY_ONLY/local/1" {
		t.Errorf("Deferred after a seeded Hub row: %s", v)
	}
	if len(rep.PoliciesRejected) != 1 || rep.PoliciesRejected[0].ID != "p-s" || rep.PoliciesRejected[0].Reason != directory.PushRejectSeededDefault {
		t.Errorf("rejections %+v", rep.PoliciesRejected)
	}
}

func TestHubSnapshotRefusalsChangeNothing(t *testing.T) {
	s, db := newHubStore(t)
	apply(t, s, base())
	before := count(t, db, `SELECT COUNT(*) FROM directory_contacts`)
	for _, c := range []struct {
		tenant  string
		version int64
		reason  string
	}{{"t-2", 9, directory.RefuseOtherTenant}, {"t-1", 5, directory.RefuseNotNewer}, {"t-1", 4, directory.RefuseNotNewer}, {"", 9, directory.RefuseOtherTenant}} {
		snap := directory.HubSnapshot{TenantID: c.tenant, Version: c.version}
		rep := apply(t, s, snap)
		if rep.Applied || rep.Reason != c.reason || rep.AppliedVersion != 5 {
			t.Errorf("%s v%d: %+v", c.tenant, c.version, rep)
		}
		st, _ := s.GetHubState(context.Background())
		if st.Version != 5 || st.TenantID != "t-1" || st.RefusedReason != c.reason || st.RefusedAt == "" {
			t.Errorf("%s v%d: state %+v", c.tenant, c.version, st)
		}
	}
	if after := count(t, db, `SELECT COUNT(*) FROM directory_contacts`); after != before {
		t.Errorf("a refused (empty) snapshot changed the contacts %d -> %d", before, after)
	}
	// The next good snapshot applies and clears the refusal.
	snap := base()
	snap.Version = 6
	if rep := apply(t, s, snap); !rep.Applied {
		t.Fatalf("%+v", rep)
	}
	if st, _ := s.GetHubState(context.Background()); st.RefusedReason != "" || st.Version != 6 {
		t.Errorf("state %+v", st)
	}
}

// TestHubSnapshotSkipsWhatItCannotStore: an unknown kind, an address a
// local contact holds and an id a local contact has are skipped with their
// reasons; the rest applies.
func TestHubSnapshotSkipsWhatItCannotStore(t *testing.T) {
	s, db := newHubStore(t)
	ctx := context.Background()
	local := &directory.Contact{ID: "c-local", DisplayName: "Mine"}
	if err := s.CreateContact(ctx, local); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAddress(ctx, &directory.Address{ContactID: "c-local", Kind: directory.KindSMS, Value: "+31600000009"}); err != nil {
		t.Fatal(err)
	}
	snap := base()
	snap.Contacts[0].Addresses = append(snap.Contacts[0].Addresses,
		directory.HubAddress{ID: "a-x", Kind: "PIGEON", Value: "loft 3"},
		directory.HubAddress{ID: "a-y", Kind: "SMS", Value: "+31600000009"})
	snap.Contacts = append(snap.Contacts, directory.HubContact{ID: "c-local", DisplayName: "Hub's claim"})
	rep := apply(t, s, snap)
	got := map[string]string{}
	for _, sk := range rep.Skipped {
		got[sk.ID] = sk.Reason
	}
	if got["a-x"] != directory.SkipUnknownKind || got["a-y"] != directory.SkipAddressTaken || got["c-local"] != directory.SkipIDTakenLocally {
		t.Errorf("skipped %+v", rep.Skipped)
	}
	if !rep.Applied || count(t, db, `SELECT COUNT(*) FROM directory_addresses WHERE contact_id = 'c-a'`) != 2 {
		t.Errorf("the rest of c-a did not apply: %+v", rep)
	}
	if n := one(t, db, `SELECT display_name || '/' || origin FROM directory_contacts WHERE id = 'c-local'`); n != "Mine/local" {
		t.Errorf("the local contact became %s", n)
	}
}

// TestLocalEditsSurviveSnapshots: an override and a local address on a Hub
// contact stay through the next snapshot; the Hub's own values move.
func TestLocalEditsSurviveSnapshots(t *testing.T) {
	s, db := newHubStore(t)
	ctx := context.Background()
	apply(t, s, base())
	name := "Alice (team lead)"
	if err := s.SetLocalOverride(ctx, "c-a", &name, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAddress(ctx, &directory.Address{ContactID: "c-a", Kind: directory.KindAPRS, Value: "PD0ABC-7"}); err != nil {
		t.Fatal(err)
	}
	snap := base()
	snap.Version = 6
	snap.Contacts[0].DisplayName, snap.Contacts[0].Notes = "Alice K.", "new hub notes"
	snap.Contacts[0].Addresses = []directory.HubAddress{{ID: "a-9", Kind: "TETRA", Value: "tetra:1009"}}
	apply(t, s, snap)
	if v := one(t, db, `SELECT display_name || '|' || notes FROM directory_contacts WHERE id = 'c-a'`); v != "Alice (team lead)|new hub notes" {
		t.Errorf("after the snapshot: %s", v)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM directory_addresses WHERE contact_id = 'c-a' AND origin = 'local' AND value = 'PD0ABC-7'`); n != 1 {
		t.Error("the local address went")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM directory_addresses WHERE contact_id = 'c-a' AND origin = 'hub:t-1'`); n != 1 {
		t.Errorf("hub addresses of c-a: %d, want the one sent", n)
	}
	if c, err := s.FindByAddress(ctx, directory.KindTETRA, "tetra:1001"); err == nil && c != nil {
		t.Error("the address the Hub dropped still resolves")
	}
	// On a local contact the edit is just the value, no override.
	if err := s.CreateContact(ctx, &directory.Contact{ID: "c-l", DisplayName: "L"}); err != nil {
		t.Fatal(err)
	}
	notes := "x"
	if err := s.SetLocalOverride(ctx, "c-l", nil, &notes); err != nil {
		t.Fatal(err)
	}
	if v := one(t, db, `SELECT notes || '|' || local_override FROM directory_contacts WHERE id = 'c-l'`); v != "x|{}" {
		t.Errorf("local contact edit: %s", v)
	}
}

// TestHubDeletionLeavesOnlyWhatTheOperatorMade: a contact the Hub no longer
// sends keeps its local address as a local contact without Hub authority; one
// with nothing local goes; a Hub group with a local member stays local.
func TestHubDeletionLeavesOnlyWhatTheOperatorMade(t *testing.T) {
	s, db := newHubStore(t)
	ctx := context.Background()
	apply(t, s, base())
	if err := s.AddAddress(ctx, &directory.Address{ContactID: "c-a", Kind: directory.KindAPRS, Value: "PD0ABC-7"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateContact(ctx, &directory.Contact{ID: "c-l", DisplayName: "L"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(ctx, "g-1", "c-l", ""); err != nil {
		t.Fatal(err)
	}
	snap := directory.HubSnapshot{TenantID: "t-1", Version: 6}
	rep := apply(t, s, snap)
	if rep.ContactsKept != 1 || rep.ContactsRemoved != 1 {
		t.Errorf("report %+v", rep)
	}
	if v := one(t, db, `SELECT origin || '/' || trust_level || '/' || hub_version || '/' || tenant_id FROM directory_contacts WHERE id = 'c-a'`); v != "local/0/0/" {
		t.Errorf("kept contact: %s", v)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM directory_addresses WHERE contact_id = 'c-a'`); n != 1 {
		t.Errorf("c-a has %d addresses, want its local one", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM directory_contacts WHERE id = 'c-b'`); n != 0 {
		t.Error("c-b, with nothing local, is still there")
	}
	if v := one(t, db, `SELECT origin FROM directory_groups WHERE id = 'g-1'`); v != "local" {
		t.Errorf("group with a local member: %s", v)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM directory_group_members WHERE group_id = 'g-1'`); n != 1 {
		t.Errorf("g-1 members %d, want the local one", n)
	}
	// The policies the Hub set go back to the bridge's own.
	if v := one(t, db, `SELECT strategy || '/' || origin || '/' || seeded FROM directory_dispatch_policy WHERE scope_type = 'precedence' AND scope_id = 'Routine'`); v != "PRIMARY_ONLY/local/1" {
		t.Errorf("Routine after the Hub dropped it: %s", v)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM directory_dispatch_policy WHERE scope_type = 'contact'`); n != 0 {
		t.Error("the Hub's contact policy is still there")
	}
	if rep.PoliciesReverted != 2 {
		t.Errorf("reverted %d", rep.PoliciesReverted)
	}
}

// TestHubSnapshotIsAllOrNothing: a write that fails part-way leaves the
// directory and the version as they were.
func TestHubSnapshotIsAllOrNothing(t *testing.T) {
	s, db := newHubStore(t)
	apply(t, s, base())
	if _, err := db.Exec(`CREATE TRIGGER boom BEFORE INSERT ON directory_contacts WHEN NEW.id = 'c-boom'
		BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	snap := base()
	snap.Version = 6
	snap.Contacts[0].DisplayName = "Changed"
	snap.Contacts = append(snap.Contacts, directory.HubContact{ID: "c-boom", DisplayName: "Boom"})
	if _, err := s.ApplyHubSnapshot(context.Background(), snap, allStrategies); err == nil {
		t.Fatal("no error")
	}
	if v := one(t, db, `SELECT display_name FROM directory_contacts WHERE id = 'c-a'`); v != "Alice" {
		t.Errorf("a failed snapshot changed c-a to %s", v)
	}
	if st, _ := s.GetHubState(context.Background()); st.Version != 5 {
		t.Errorf("a failed snapshot moved the version to %d", st.Version)
	}
}

// TestLegacyHubRowsAreTheNextHubs: rows a v1 apply wrote ('hub') are
// replaced by the next snapshot, and dropped when it does not have them.
func TestLegacyHubRowsAreTheNextHubs(t *testing.T) {
	s, db := newHubStore(t)
	ctx := context.Background()
	if err := s.CreateContact(ctx, &directory.Contact{ID: "c-old", DisplayName: "Old", Origin: directory.OriginHub}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO directory_addresses (id, contact_id, kind, value, origin) VALUES ('o-1', 'c-old', 'SMS', '+31600000007', 'hub')`); err != nil {
		t.Fatal(err)
	}
	snap := directory.HubSnapshot{TenantID: "t-1", Version: 1, Contacts: []directory.HubContact{{ID: "c-old", DisplayName: "Old",
		Addresses: []directory.HubAddress{{ID: "o-2", Kind: "SMS", Value: "+31600000007"}}}}}
	rep := apply(t, s, snap)
	if len(rep.Skipped) != 0 || one(t, db, `SELECT origin FROM directory_addresses WHERE contact_id = 'c-old'`) != "hub:t-1" {
		t.Errorf("the legacy address was not replaced: %+v", rep.Skipped)
	}
	apply(t, s, directory.HubSnapshot{TenantID: "t-1", Version: 2})
	if n := count(t, db, `SELECT COUNT(*) FROM directory_contacts WHERE id = 'c-old'`); n != 0 {
		t.Error("the legacy hub contact survived a snapshot without it")
	}
}

func TestHubRotationMovesTheKeyAndTheVersionTogether(t *testing.T) {
	s, db := newHubStore(t)
	ctx := context.Background()
	apply(t, s, base()) // t-1 at 5
	rep, err := s.ApplyHubRotation(ctx, "t-1", 6, []byte{0x30, 0x59})
	if err != nil || !rep.Applied || rep.AppliedVersion != 6 {
		t.Fatalf("%+v %v", rep, err)
	}
	if v := one(t, db, `SELECT value FROM system_config WHERE key = ?`, directory.RotationKeyConfig); v != "3059" {
		t.Errorf("pinned %s", v)
	}
	for _, c := range []struct {
		tenant  string
		version int64
		reason  string
	}{{"t-1", 6, directory.RefuseNotNewer}, {"t-2", 9, directory.RefuseOtherTenant}} {
		rep, err := s.ApplyHubRotation(ctx, c.tenant, c.version, []byte{0x01})
		if err != nil || rep.Applied || rep.Reason != c.reason {
			t.Errorf("%+v: %+v %v", c, rep, err)
		}
	}
	if v := one(t, db, `SELECT value FROM system_config WHERE key = ?`, directory.RotationKeyConfig); v != "3059" {
		t.Errorf("a refused rotation moved the key to %s", v)
	}
}
