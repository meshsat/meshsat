package database

import (
	"strings"
	"testing"
)

// A-13 at the ledger: a stored proof hash is found by its full hash or its
// first 16 bytes; a proof that does not validate, or names no stored
// transmission, records nothing; the destination's proof is recorded once
// per transmission, matched and signed, and marks the intent dirty.
// [MESHSAT-1584]
func TestRecordProofEvidence(t *testing.T) {
	db := intentTestDB(t)
	rows := newTestIntent(t, db, "proof", 1, nil)
	gen := claim(t, db, rows[0])
	txID, err := db.AuthorizeTransmission(rows[0], gen, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.MarkTransmissionHandedOff(txID)
	full := strings.Repeat("ab", 32)
	if err := db.SetTransmissionProof(rows[0], gen, full, strings.Repeat("cd", 32)); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTransmissionProof(rows[0], gen+1, full, "x"); err == nil {
		t.Fatal("stored on a generation that has no handed-off transmission")
	}
	if hs, _ := db.ExpectedProofHashes(); len(hs) != 1 || hs[0] != full {
		t.Fatalf("expected hashes %v", hs)
	}
	_, _ = db.Exec(`UPDATE delivery_intents SET dirty = 0`)
	yes := func(ph, pk string) bool { return ph == full }
	no := func(string, string) bool { return false }

	if _, found, err := db.RecordProofEvidence(full, false, no); err != nil || found {
		t.Fatalf("invalid proof: found %v %v", found, err)
	}
	if _, found, _ := db.RecordProofEvidence(strings.Repeat("ef", 32), false, yes); found {
		t.Fatal("a proof for no stored transmission was found")
	}
	e, found, err := db.RecordProofEvidence(full[:32], true, yes) // implicit: first 16 bytes
	if err != nil || !found || e.ID == 0 || !e.Matched || e.Trust != "signed" || e.TransmissionID != txID || e.Completes != OutcomeAcceptedDestinationNode {
		t.Fatalf("by prefix: %+v %v %v", e, found, err)
	}
	var dirty int
	_ = db.Get(&dirty, `SELECT dirty FROM delivery_intents WHERE id = 'proof'`)
	if dirty != 1 {
		t.Fatal("intent not marked dirty")
	}
	if again, found, _ := db.RecordProofEvidence(full, false, yes); !found || again.ID != 0 {
		t.Fatalf("second copy: %+v; want found, nothing new", again)
	}
	if again, err := db.RecordLateDelivery(rows[0], gen); err != nil || again.ID != 0 {
		t.Fatalf("late resource proof after the packet proof: %+v %v; want nothing new", again, err)
	}
	var n int
	_ = db.Get(&n, `SELECT COUNT(*) FROM delivery_evidence WHERE transmission_id = ? AND event = 'proof'`, txID)
	if n != 1 {
		t.Fatalf("%d proof rows; want 1", n)
	}
	if hs, _ := db.ExpectedProofHashes(); len(hs) != 0 {
		t.Fatalf("a proven hash is still expected: %v", hs)
	}
}
