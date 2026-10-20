package directory_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"meshsat/internal/directory"
)

// r20 F1: an address the Hub sends for one of its contacts that a local
// contact holds is recorded with its holder; the operator removes the local
// address (never a Hub one), and the Hub's next snapshot, not a replay of
// the one already applied, gives the address to the Hub contact. [MESHSAT-1584]
func TestHubAddressConflictNamesTheHolderAndClears(t *testing.T) {
	s, db, path := fileHubStore(t)
	ctx := context.Background()
	if err := s.CreateContact(ctx, &directory.Contact{ID: "c-l", DisplayName: "Bob (mine)", Notes: "my notes"}); err != nil {
		t.Fatal(err)
	}
	local := &directory.Address{ContactID: "c-l", Kind: directory.KindSMS, Value: "+31600000002"}
	if err := s.AddAddress(ctx, local); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAddress(ctx, &directory.Address{ContactID: "c-l", Kind: directory.KindAPRS, Value: "PD0ABC-7"}); err != nil {
		t.Fatal(err)
	}
	rep := apply(t, s, base()) // c-b's SMS is the local one
	if len(rep.Skipped) != 1 || rep.Skipped[0].Reason != directory.SkipAddressTaken || !strings.Contains(rep.Skipped[0].Detail, "held by contact c-l") {
		t.Fatalf("skipped %+v", rep.Skipped)
	}
	conflicts, err := s.AddressConflicts(ctx, "")
	if err != nil || len(conflicts) != 1 || conflicts[0].ContactID != "c-b" || conflicts[0].HolderContactID != "c-l" ||
		conflicts[0].Kind != "SMS" || conflicts[0].Value != "+31600000002" {
		t.Fatalf("conflicts %+v %v", conflicts, err)
	}

	// A Hub address is not the operator's to remove; nor is another
	// contact's, nor one that is not there.
	if _, err := s.DeleteLocalAddress(ctx, "c-a", "a-1"); !errors.Is(err, directory.ErrHubManaged) {
		t.Errorf("a Hub address: %v", err)
	}
	if _, err := s.DeleteLocalAddress(ctx, "c-a", local.ID); !errors.Is(err, directory.ErrNotFound) {
		t.Errorf("another contact's address: %v", err)
	}
	if _, err := s.DeleteLocalAddress(ctx, "c-l", "nope"); !errors.Is(err, directory.ErrNotFound) {
		t.Errorf("no such address: %v", err)
	}
	waiting, err := s.DeleteLocalAddress(ctx, "c-l", local.ID)
	if err != nil || len(waiting) != 1 || waiting[0].ContactID != "c-b" {
		t.Fatalf("remove the local address: %+v %v", waiting, err)
	}
	// The local contact keeps the rest.
	if v := one(t, db, `SELECT display_name || '|' || notes || '|' || origin FROM directory_contacts WHERE id = 'c-l'`); v != "Bob (mine)|my notes|local" {
		t.Errorf("the local contact: %s", v)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM directory_addresses WHERE contact_id = 'c-l'`); n != 1 {
		t.Errorf("c-l has %d addresses, want its other one", n)
	}

	// The snapshot already applied is not applied again; a newer one,
	// after a restart, gives c-b its address and clears the conflict.
	if rep := apply(t, s, base()); rep.Applied {
		t.Fatal("the same snapshot applied twice")
	}
	if c, _ := s.FindByAddress(ctx, directory.KindSMS, "+31600000002"); c != nil {
		t.Fatalf("the address resolves to %s before a newer snapshot", c.ID)
	}
	s, db = reopen(t, db, path)
	snap := base()
	snap.Version = 6
	if rep := apply(t, s, snap); !rep.Applied || len(rep.Skipped) != 0 {
		t.Fatalf("the newer snapshot: %+v", rep)
	}
	if c, err := s.FindByAddress(ctx, directory.KindSMS, "+31600000002"); err != nil || c == nil || c.ID != "c-b" {
		t.Fatalf("the address resolves to %+v (%v), want c-b", c, err)
	}
	if conflicts, _ := s.AddressConflicts(ctx, ""); len(conflicts) != 0 {
		t.Errorf("conflicts left %+v", conflicts)
	}
}

// A conflict belongs to the snapshot that found it: the next one rewrites
// this Hub's rows, and a contact the Hub drops takes its conflicts along.
func TestHubAddressConflictsFollowTheSnapshot(t *testing.T) {
	s, _ := newHubStore(t)
	ctx := context.Background()
	if err := s.CreateContact(ctx, &directory.Contact{ID: "c-l", DisplayName: "L"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAddress(ctx, &directory.Address{ContactID: "c-l", Kind: directory.KindSMS, Value: "+31600000002"}); err != nil {
		t.Fatal(err)
	}
	apply(t, s, base())
	snap := base()
	snap.Version = 6
	snap.Contacts = snap.Contacts[:1] // c-b dropped
	snap.Groups[0].MemberIDs = []string{"c-a"}
	apply(t, s, snap)
	if conflicts, _ := s.AddressConflicts(ctx, ""); len(conflicts) != 0 {
		t.Errorf("a dropped contact's conflict stayed: %+v", conflicts)
	}
}
