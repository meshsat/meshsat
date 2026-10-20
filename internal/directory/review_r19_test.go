package directory_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"testing"
)

func TestReviewR19RotationMustNotConsumeUnappliedSnapshot(t *testing.T) {
	s, db := newHubStore(t)
	snap := base()
	apply(t, s, snap)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := s.ApplyHubRotation(context.Background(), snap.TenantID, 7, der)
	if err != nil || !rep.Applied {
		t.Fatalf("rotation %+v %v", rep, err)
	}
	snap.Version = 7
	snap.Contacts[0].DisplayName = "Changed while bridge offline"
	rep = apply(t, s, snap)
	got := one(t, db, `SELECT display_name FROM directory_contacts WHERE id = 'c-a'`)
	t.Logf("snapshot applied=%v reason=%s reported_version=%d actual_name=%q", rep.Applied, rep.Reason, rep.AppliedVersion, got)
	if got != snap.Contacts[0].DisplayName {
		t.Fatal("rotation marked unseen directory data current")
	}
}

func TestReviewR19AddressMoveMustNotDependOnContactOrder(t *testing.T) {
	s, db := newHubStore(t)
	snap := base()
	apply(t, s, snap)
	moved := snap.Contacts[1].Addresses[0]
	snap.Contacts[0].Addresses = append(snap.Contacts[0].Addresses, moved)
	snap.Contacts[1].Addresses = nil
	snap.Version++
	rep := apply(t, s, snap)
	n := count(t, db, `SELECT COUNT(*) FROM directory_addresses WHERE contact_id = 'c-a' AND value = ?`, moved.Value)
	t.Logf("applied=%v version=%d moved_address_count=%d skipped=%+v", rep.Applied, rep.AppliedVersion, n, rep.Skipped)
	if n != 1 {
		t.Fatal("valid transferred Hub address lost during full snapshot")
	}
}

func TestReviewR19DeletedHubContactClearsHubFields(t *testing.T) {
	s, db := newHubStore(t)
	snap := base()
	snap.Contacts[0].Org = "Hub-only organisation"
	snap.Contacts[0].Role = "Hub-only role"
	apply(t, s, snap)
	notes := "Local note retained"
	if err := s.SetLocalOverride(context.Background(), "c-a", nil, &notes); err != nil {
		t.Fatal(err)
	}
	snap.Version++
	snap.Contacts = nil
	snap.Groups = nil
	snap.Policies = nil
	apply(t, s, snap)
	got := one(t, db, `SELECT org || '/' || role FROM directory_contacts WHERE id = 'c-a'`)
	t.Logf("local stub inherited Hub fields %q", got)
	if got != "/" {
		t.Fatal("deleted Hub fields survive on local stub")
	}
}
