package engine

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"

	"meshsat/internal/database"
)

func testDB(t *testing.T) *database.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := database.New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSignAndVerify(t *testing.T) {
	db := testDB(t)
	ss, err := NewSigningService(db)
	if err != nil {
		t.Fatalf("NewSigningService: %v", err)
	}

	data := []byte("hello, world")
	sig := ss.Sign(data)

	if len(sig) != ed25519.SignatureSize {
		t.Fatalf("expected signature length %d, got %d", ed25519.SignatureSize, len(sig))
	}

	// Verify with the correct public key
	if !Verify(ss.SignerID(), data, sig) {
		t.Fatal("signature verification failed with correct key")
	}

	// Verify with wrong data fails
	if Verify(ss.SignerID(), []byte("tampered"), sig) {
		t.Fatal("signature verification should fail with wrong data")
	}
}

func TestVerifyWrongKey(t *testing.T) {
	db := testDB(t)
	ss, err := NewSigningService(db)
	if err != nil {
		t.Fatalf("NewSigningService: %v", err)
	}

	data := []byte("test message")
	sig := ss.Sign(data)

	// Generate a different key
	wrongPub, _, _ := ed25519.GenerateKey(nil)
	wrongKeyHex := hex.EncodeToString(wrongPub)

	if Verify(wrongKeyHex, data, sig) {
		t.Fatal("signature verification should fail with wrong key")
	}

	// Invalid hex should fail
	if Verify("not-hex", data, sig) {
		t.Fatal("signature verification should fail with invalid hex key")
	}

	// Wrong length key should fail
	if Verify("abcdef", data, sig) {
		t.Fatal("signature verification should fail with wrong length key")
	}
}

func TestSigningServicePersistence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "persist.db")

	// Create first instance — generates keypair
	db1, err := database.New(dbPath)
	if err != nil {
		t.Fatalf("create db1: %v", err)
	}
	ss1, err := NewSigningService(db1)
	if err != nil {
		t.Fatalf("NewSigningService 1: %v", err)
	}
	signerID1 := ss1.SignerID()
	db1.Close()

	// Create second instance — should load same keypair
	db2, err := database.New(dbPath)
	if err != nil {
		t.Fatalf("create db2: %v", err)
	}
	defer db2.Close()
	ss2, err := NewSigningService(db2)
	if err != nil {
		t.Fatalf("NewSigningService 2: %v", err)
	}

	if ss2.SignerID() != signerID1 {
		t.Fatalf("signer ID changed after reload: %s != %s", ss2.SignerID(), signerID1)
	}

	// Cross-verify: data signed by first instance should verify with second
	data := []byte("cross-instance test")
	sig := ss1.Sign(data)
	if !Verify(ss2.SignerID(), data, sig) {
		t.Fatal("cross-instance signature verification failed")
	}
}

func TestAuditEventHashChain(t *testing.T) {
	db := testDB(t)
	ss, err := NewSigningService(db)
	if err != nil {
		t.Fatalf("NewSigningService: %v", err)
	}

	// Insert several audit events
	ss.AuditEvent("dispatch", nil, nil, nil, nil, "message dispatched to iridium")
	ss.AuditEvent("deliver", nil, nil, nil, nil, "message delivered successfully")
	ss.AuditEvent("drop", nil, nil, nil, nil, "message dropped after retries")

	// Verify chain is intact
	valid, brokenAt := ss.VerifyChain(10)
	if brokenAt != -1 {
		t.Fatalf("chain broken at index %d, expected intact", brokenAt)
	}
	if valid != 3 {
		t.Fatalf("expected 3 valid entries, got %d", valid)
	}
}

func TestAuditEventWithAllFields(t *testing.T) {
	db := testDB(t)
	ss, err := NewSigningService(db)
	if err != nil {
		t.Fatalf("NewSigningService: %v", err)
	}

	ifaceID := "iridium_0"
	dir := "egress"
	delID := int64(42)
	ruleID := int64(7)
	ss.AuditEvent("deliver", &ifaceID, &dir, &delID, &ruleID, "delivered via Iridium SBD")

	entries, err := db.GetAuditLog(1)
	if err != nil {
		t.Fatalf("GetAuditLog: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.EventType != "deliver" {
		t.Errorf("event_type: got %q, want %q", e.EventType, "deliver")
	}
	if e.InterfaceID == nil || *e.InterfaceID != "iridium_0" {
		t.Errorf("interface_id: got %v, want %q", e.InterfaceID, "iridium_0")
	}
	if e.Direction == nil || *e.Direction != "egress" {
		t.Errorf("direction: got %v, want %q", e.Direction, "egress")
	}
	if e.DeliveryID == nil || *e.DeliveryID != 42 {
		t.Errorf("delivery_id: got %v, want 42", e.DeliveryID)
	}
	if e.RuleID == nil || *e.RuleID != 7 {
		t.Errorf("rule_id: got %v, want 7", e.RuleID)
	}
	if e.Hash == "" {
		t.Error("hash should not be empty")
	}
}

func TestVerifyChainDetectsTampering(t *testing.T) {
	db := testDB(t)
	ss, err := NewSigningService(db)
	if err != nil {
		t.Fatalf("NewSigningService: %v", err)
	}

	// Insert events
	ss.AuditEvent("dispatch", nil, nil, nil, nil, "first event")
	ss.AuditEvent("deliver", nil, nil, nil, nil, "second event")
	ss.AuditEvent("drop", nil, nil, nil, nil, "third event")

	// Verify chain is intact
	valid, brokenAt := ss.VerifyChain(10)
	if brokenAt != -1 {
		t.Fatalf("chain should be intact before tampering, broken at %d", brokenAt)
	}
	if valid != 3 {
		t.Fatalf("expected 3 valid, got %d", valid)
	}

	// Tamper with the middle entry (id=2) by changing its detail
	_, err = db.Exec("UPDATE audit_log SET detail = 'tampered' WHERE id = 2")
	if err != nil {
		t.Fatalf("tamper failed: %v", err)
	}

	// Verify should detect the break at index 1 (second entry, 0-indexed)
	valid, brokenAt = ss.VerifyChain(10)
	if brokenAt == -1 {
		t.Fatal("chain should be broken after tampering")
	}
	if brokenAt != 1 {
		t.Fatalf("expected break at index 1, got %d", brokenAt)
	}
	if valid != 1 {
		t.Fatalf("expected 1 valid entry before break, got %d", valid)
	}
}

func TestVerifyChainEmpty(t *testing.T) {
	db := testDB(t)
	ss, err := NewSigningService(db)
	if err != nil {
		t.Fatalf("NewSigningService: %v", err)
	}

	valid, brokenAt := ss.VerifyChain(10)
	if valid != 0 {
		t.Fatalf("expected 0 valid for empty chain, got %d", valid)
	}
	if brokenAt != -1 {
		t.Fatalf("expected -1 for empty chain, got %d", brokenAt)
	}
}

func TestAuditChainContinuity(t *testing.T) {
	// Verify that the hash chain links properly: entry N's prev_hash == entry N-1's hash
	db := testDB(t)
	ss, err := NewSigningService(db)
	if err != nil {
		t.Fatalf("NewSigningService: %v", err)
	}

	ss.AuditEvent("dispatch", nil, nil, nil, nil, "event 1")
	ss.AuditEvent("deliver", nil, nil, nil, nil, "event 2")

	entries, err := db.GetAuditLog(10)
	if err != nil {
		t.Fatalf("GetAuditLog: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// entries[0] is newest (id=2), entries[1] is oldest (id=1)
	newest := entries[0]
	oldest := entries[1]

	// First entry's prev_hash should be empty (genesis)
	if oldest.PrevHash != "" {
		t.Errorf("first entry prev_hash should be empty, got %q", oldest.PrevHash)
	}

	// Second entry's prev_hash should match first entry's hash
	if newest.PrevHash != oldest.Hash {
		t.Errorf("chain link broken: newest.PrevHash=%q != oldest.Hash=%q", newest.PrevHash, oldest.Hash)
	}

	// Manually verify the hash computation
	h := sha256.New()
	h.Write([]byte(oldest.PrevHash))
	h.Write([]byte(oldest.Timestamp))
	h.Write([]byte(oldest.EventType))
	h.Write([]byte(oldest.Detail))
	expectedHash := hex.EncodeToString(h.Sum(nil))

	if oldest.Hash != expectedHash {
		t.Errorf("hash mismatch: stored=%q computed=%q", oldest.Hash, expectedHash)
	}
}
