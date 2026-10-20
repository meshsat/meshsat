package directory_test

import (
	"context"
	"path/filepath"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/directory"
)

// The r19 cases of the Hub directory apply (HeMB-NG stage 2 C7): the key
// moves on its own counter (C7-R1), an address moves between Hub contacts
// in any order (C7-R2), and a contact or group the Hub stops sending keeps
// only what the operator made (C7-R6). [MESHSAT-1584]

// reopen closes the database at path and opens it again, as a restart.
func reopen(t *testing.T, db *database.DB, path string) (*directory.SQLStore, *database.DB) {
	t.Helper()
	_ = db.Close()
	db2, err := database.New(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	return directory.NewSQLStore(db2.DB), db2
}

func fileHubStore(t *testing.T) (*directory.SQLStore, *database.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dir.db")
	db, err := database.New(path)
	if err != nil {
		t.Fatal(err)
	}
	return directory.NewSQLStore(db.DB), db, path
}

func rotate(t *testing.T, s *directory.SQLStore, tenant string, version int64, key byte) directory.HubApplyReport {
	t.Helper()
	rep, err := s.ApplyHubRotation(context.Background(), tenant, version, []byte{0x30, key})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// TestHubRotationKeepsItsOwnCounter: a rotation never makes a snapshot of
// the same or a lower version look applied, a snapshot never makes a
// rotation look old, and both survive a restart.
func TestHubRotationKeepsItsOwnCounter(t *testing.T) {
	s, db, path := fileHubStore(t)
	ctx := context.Background()
	apply(t, s, base()) // snapshot 5

	// Offline across an edit, a deletion and two rotations; the newest
	// snapshot has the version of the last rotation.
	if rep := rotate(t, s, "t-1", 7, 0x01); !rep.Applied || rep.AppliedVersion != 5 || rep.KeyVersion != 7 {
		t.Fatalf("rotation 7: %+v", rep)
	}
	s, db = reopen(t, db, path)
	if rep := rotate(t, s, "t-1", 9, 0x02); !rep.Applied || rep.AppliedVersion != 5 || rep.KeyVersion != 9 {
		t.Fatalf("rotation 9 after a restart: %+v", rep)
	}
	if st, _ := s.GetHubState(ctx); st.Version != 5 || st.KeyVersion != 9 {
		t.Fatalf("state %+v: the rotations moved the snapshot version", st)
	}
	s, db = reopen(t, db, path)
	snap := base()
	snap.Version = 9
	snap.Contacts[0].DisplayName = "Alice, renamed offline"
	snap.Contacts = snap.Contacts[:1] // Bob deleted
	snap.Groups[0].MemberIDs = []string{"c-a"}
	rep := apply(t, s, snap)
	if !rep.Applied || rep.AppliedVersion != 9 || rep.KeyVersion != 9 {
		t.Fatalf("the snapshot of the last rotation's version: %+v", rep)
	}
	if v := one(t, db, `SELECT display_name FROM directory_contacts WHERE id = 'c-a'`); v != "Alice, renamed offline" {
		t.Errorf("c-a is %q", v)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM directory_contacts WHERE id = 'c-b'`); n != 0 {
		t.Error("the deleted contact survived")
	}
	if c, err := s.FindByAddress(ctx, directory.KindSMS, "+31600000002"); err == nil && c != nil {
		t.Error("the deleted contact's address still resolves")
	}
	if v := one(t, db, `SELECT value FROM system_config WHERE key = ?`, directory.RotationKeyConfig); v != "3002" {
		t.Errorf("key %s; want the second rotation's", v)
	}

	// The same rotation again (its answer lost) or an older one is not
	// newer on the key's counter.
	for _, v := range []int64{9, 7} {
		if rep := rotate(t, s, "t-1", v, 0x03); rep.Applied || rep.Reason != directory.RefuseNotNewer || rep.KeyVersion != 9 {
			t.Errorf("rotation %d again: %+v", v, rep)
		}
	}

	// A replica that had not reloaded the new key signed snapshot 11 with
	// the old one, and the bridge took it; the rotation at 10 still applies.
	snap.Version = 11
	apply(t, s, snap)
	if rep := rotate(t, s, "t-1", 10, 0x04); !rep.Applied || rep.AppliedVersion != 11 || rep.KeyVersion != 10 {
		t.Fatalf("a rotation older than the snapshot held: %+v", rep)
	}
	if st, _ := s.GetHubState(ctx); st.Version != 11 || st.KeyVersion != 10 {
		t.Errorf("state %+v", st)
	}
	// Another tenant's rotation is refused whatever its version.
	if rep := rotate(t, s, "t-2", 99, 0x05); rep.Applied || rep.Reason != directory.RefuseOtherTenant {
		t.Errorf("other tenant: %+v", rep)
	}
}

// TestHubAddressMovesWhateverTheOrder: an address that moves between Hub
// contacts lands on its new owner in either order, a swap swaps, an address
// whose former owner is gone lands, and only a genuinely local address
// stops a Hub one. Each holds after a restart.
func TestHubAddressMovesWhateverTheOrder(t *testing.T) {
	sms := directory.HubAddress{ID: "b-1", Kind: "SMS", Value: "+31600000002"}
	tetra := directory.HubAddress{ID: "a-1", Kind: "TETRA", Value: "tetra:1001"}
	owner := func(t *testing.T, s *directory.SQLStore, kind directory.Kind, value string) string {
		t.Helper()
		c, err := s.FindByAddress(context.Background(), kind, value)
		if err != nil || c == nil {
			t.Fatalf("%s %s resolves to nobody: %v", kind, value, err)
		}
		return c.ID
	}
	for _, c := range []struct {
		name  string
		build func() directory.HubSnapshot
		want  map[string]string // value -> contact
	}{
		{"to the contact before", func() directory.HubSnapshot {
			s := base()
			s.Contacts[0].Addresses = append(s.Contacts[0].Addresses, sms)
			s.Contacts[1].Addresses = nil
			return s
		}, map[string]string{sms.Value: "c-a", tetra.Value: "c-a"}},
		{"to the contact after", func() directory.HubSnapshot {
			s := base()
			s.Contacts[1].Addresses = append(s.Contacts[1].Addresses, tetra)
			s.Contacts[0].Addresses = s.Contacts[0].Addresses[1:]
			return s
		}, map[string]string{sms.Value: "c-b", tetra.Value: "c-b"}},
		{"swapped", func() directory.HubSnapshot {
			s := base()
			s.Contacts[0].Addresses = []directory.HubAddress{sms}
			s.Contacts[1].Addresses = []directory.HubAddress{tetra}
			return s
		}, map[string]string{sms.Value: "c-a", tetra.Value: "c-b"}},
		{"former owner deleted", func() directory.HubSnapshot {
			s := base()
			s.Contacts = []directory.HubContact{s.Contacts[1], s.Contacts[0]} // Bob first, then gone
			s.Contacts[1].Addresses = append(s.Contacts[1].Addresses, sms)
			s.Contacts = s.Contacts[1:]
			s.Groups[0].MemberIDs = []string{"c-a"}
			return s
		}, map[string]string{sms.Value: "c-a", tetra.Value: "c-a"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, db, path := fileHubStore(t)
			apply(t, s, base())
			snap := c.build()
			snap.Version = 6
			rep := apply(t, s, snap)
			if !rep.Applied || len(rep.Skipped) != 0 {
				t.Fatalf("report %+v", rep)
			}
			for pass := 0; pass < 2; pass++ {
				for value, want := range c.want {
					kind := directory.KindSMS
					if value == tetra.Value {
						kind = directory.KindTETRA
					}
					if got := owner(t, s, kind, value); got != want {
						t.Errorf("pass %d: %s belongs to %s, want %s", pass, value, got, want)
					}
					if n := count(t, db, `SELECT COUNT(*) FROM directory_addresses WHERE value = ? AND origin = 'hub:t-1'`, value); n != 1 {
						t.Errorf("pass %d: %d rows of %s", pass, n, value)
					}
				}
				s, db = reopen(t, db, path)
			}
		})
	}

	t.Run("genuine local conflict", func(t *testing.T) {
		s, db, path := fileHubStore(t)
		ctx := context.Background()
		apply(t, s, base())
		if err := s.CreateContact(ctx, &directory.Contact{ID: "c-l", DisplayName: "Local"}); err != nil {
			t.Fatal(err)
		}
		if err := s.AddAddress(ctx, &directory.Address{ContactID: "c-l", Kind: directory.KindSMS, Value: "+31600000099"}); err != nil {
			t.Fatal(err)
		}
		snap := base()
		snap.Version = 6
		snap.Contacts[0].Addresses = append(snap.Contacts[0].Addresses, sms) // moved from Bob
		snap.Contacts[1].Addresses = []directory.HubAddress{{ID: "b-9", Kind: "SMS", Value: "+31600000099"}}
		rep := apply(t, s, snap)
		if len(rep.Skipped) != 1 || rep.Skipped[0].ID != "b-9" || rep.Skipped[0].Reason != directory.SkipAddressTaken {
			t.Fatalf("skipped %+v; want only the local one", rep.Skipped)
		}
		s, _ = reopen(t, db, path)
		if got := owner(t, s, directory.KindSMS, "+31600000099"); got != "c-l" {
			t.Errorf("the local address went to %s", got)
		}
		if got := owner(t, s, directory.KindSMS, sms.Value); got != "c-a" {
			t.Errorf("the moved address is on %s", got)
		}
	})
}

// TestHubDeletionStubKeepsOnlyLocalData: a contact the Hub stops sending is
// kept for what the operator added, and nothing the Hub wrote stays on it.
func TestHubDeletionStubKeepsOnlyLocalData(t *testing.T) {
	name, notes := "My name for her", "my notes"
	for _, c := range []struct {
		case_       string
		address     bool
		name, notes *string
		wantName    string
		wantNotes   string
	}{
		{"a local address", true, nil, nil, "APRS PD0ABC-7", ""},
		{"local notes", false, nil, &notes, directory.LocalStubName, notes},
		{"a local name", false, &name, nil, name, ""},
		{"both overrides", false, &name, &notes, name, notes},
	} {
		t.Run(c.case_, func(t *testing.T) {
			s, db := newHubStore(t)
			ctx := context.Background()
			snap := base()
			a := &snap.Contacts[0]
			a.GivenName, a.FamilyName, a.Org, a.Role, a.Team, a.SIDC = "Alice", "Kowalski", "Hub org", "Hub role", "Hub team", "SFGPU"
			apply(t, s, snap)
			if c.address {
				if err := s.AddAddress(ctx, &directory.Address{ContactID: "c-a", Kind: directory.KindAPRS, Value: "PD0ABC-7"}); err != nil {
					t.Fatal(err)
				}
			}
			if c.name != nil || c.notes != nil {
				if err := s.SetLocalOverride(ctx, "c-a", c.name, c.notes); err != nil {
					t.Fatal(err)
				}
			}
			rep := apply(t, s, directory.HubSnapshot{TenantID: "t-1", Version: 6})
			if rep.ContactsKept != 1 {
				t.Fatalf("report %+v", rep)
			}
			got := one(t, db, `SELECT display_name || '|' || given_name || '|' || family_name || '|' || org || '|' || role || '|' ||
				team || '|' || sidc || '|' || notes || '|' || origin || '|' || tenant_id || '|' || trust_level || '|' ||
				hub_version || '|' || hub_etag || '|' || local_override FROM directory_contacts WHERE id = 'c-a'`)
			want := c.wantName + "||||||" + "|" + c.wantNotes + "|local||0|0||{}"
			if got != want {
				t.Errorf("stub %q\n want %q", got, want)
			}
			hubAddr := count(t, db, `SELECT COUNT(*) FROM directory_addresses WHERE contact_id = 'c-a' AND origin != 'local'`)
			members := count(t, db, `SELECT COUNT(*) FROM directory_group_members WHERE contact_id = 'c-a'`)
			if hubAddr != 0 || members != 0 {
				t.Errorf("%d Hub addresses, %d memberships left", hubAddr, members)
			}
		})
	}
}

// TestHubDeletedGroupKeptLocalLosesHubFields: a Hub group kept for a local
// member keeps its kind and nothing else the Hub wrote.
func TestHubDeletedGroupKeptLocalLosesHubFields(t *testing.T) {
	s, db := newHubStore(t)
	ctx := context.Background()
	snap := base()
	snap.Groups[0].SIDC, snap.Groups[0].MLSGroupID, snap.Groups[0].HubEtag = "SFGPU", "mls-1", "etag-1"
	apply(t, s, snap)
	if err := s.CreateContact(ctx, &directory.Contact{ID: "c-l", DisplayName: "L"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(ctx, "g-1", "c-l", ""); err != nil {
		t.Fatal(err)
	}
	apply(t, s, directory.HubSnapshot{TenantID: "t-1", Version: 6})
	got := one(t, db, `SELECT display_name || '|' || kind || '|' || sidc || '|' || mls_group_id || '|' || origin || '|' ||
		tenant_id || '|' || hub_version || '|' || hub_etag FROM directory_groups WHERE id = 'g-1'`)
	if want := directory.LocalGroupName + "|TEAM|||local||0|"; got != want {
		t.Errorf("group %q, want %q", got, want)
	}
}
