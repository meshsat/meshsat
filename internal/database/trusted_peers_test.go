package database

import (
	"database/sql"
	"testing"
)

func TestTrustedPeerUpsertInsertsThenUpdatesPreservingFlag(t *testing.T) {
	db := testDB(t)

	// Fresh insert — honor the default flag.
	if err := db.UpsertTrustedPeer("sig1", "routing1", "kitA", `{"protocol":1}`, true); err != nil {
		t.Fatalf("insert: %v", err)
	}
	p, err := db.GetTrustedPeer("sig1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !p.AutoFederate {
		t.Fatalf("auto_federate not set on fresh insert")
	}
	if p.Alias != "kitA" {
		t.Errorf("alias: got %q want kitA", p.Alias)
	}

	// Operator flips it OFF.
	if err := db.SetTrustedPeerAutoFederate("sig1", false); err != nil {
		t.Fatalf("toggle off: %v", err)
	}

	// A subsequent upsert must NOT re-enable auto_federate, even if
	// the new default is true — once the operator has made a choice,
	// respect it.
	if err := db.UpsertTrustedPeer("sig1", "routing1b", "kitA", `{"protocol":1,"v":2}`, true); err != nil {
		t.Fatalf("update: %v", err)
	}
	p2, err := db.GetTrustedPeer("sig1")
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if p2.AutoFederate {
		t.Fatalf("auto_federate flipped back on — operator toggle lost")
	}
	if p2.RoutingIdentity != "routing1b" {
		t.Errorf("routing_identity: got %q want routing1b", p2.RoutingIdentity)
	}
	if p2.ManifestJSON != `{"protocol":1,"v":2}` {
		t.Errorf("manifest_json not refreshed: got %q", p2.ManifestJSON)
	}
}

func TestTrustedPeerListOrderedByUpdatedAtDesc(t *testing.T) {
	db := testDB(t)
	if err := db.UpsertTrustedPeer("sigA", "", "A", "{}", true); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertTrustedPeer("sigB", "", "B", "{}", true); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertTrustedPeer("sigC", "", "C", "{}", true); err != nil {
		t.Fatal(err)
	}
	// Touch A so it moves to the top.
	if err := db.UpsertTrustedPeer("sigA", "newrouting", "A", "{}", true); err != nil {
		t.Fatal(err)
	}
	peers, err := db.ListTrustedPeers()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(peers) != 3 {
		t.Fatalf("count: got %d want 3", len(peers))
	}
	if peers[0].SignerID != "sigA" {
		t.Errorf("expected sigA first (freshest updated_at), got %q", peers[0].SignerID)
	}
}

func TestDeleteTrustedPeer(t *testing.T) {
	db := testDB(t)
	if err := db.UpsertTrustedPeer("sigX", "", "", "{}", true); err != nil {
		t.Fatal(err)
	}
	ok, err := db.DeleteTrustedPeer("sigX")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("deleted returned false for existing row")
	}
	if _, err := db.GetTrustedPeer("sigX"); err != sql.ErrNoRows {
		t.Fatalf("expected ErrNoRows after delete, got %v", err)
	}
	ok, err = db.DeleteTrustedPeer("sigX")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("deleted returned true for missing row")
	}
}

func TestSetAutoFederateOnMissingReturnsErrNoRows(t *testing.T) {
	db := testDB(t)
	if err := db.SetTrustedPeerAutoFederate("ghost", true); err != sql.ErrNoRows {
		t.Fatalf("expected ErrNoRows, got %v", err)
	}
}
