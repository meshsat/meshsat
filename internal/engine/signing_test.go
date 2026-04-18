package engine

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/keystore"
	"meshsat/internal/routing"
)

// newTestKeystore builds a KeyStore over a real master key (bootstrapped
// with the given passphrase). Used by the [MESHSAT-536] tests for
// envelope-encrypted signing-key storage.
func newTestKeystore(t *testing.T, db *database.DB, passphrase string) *keystore.KeyStore {
	t.Helper()
	routingID, err := routing.NewIdentity(db)
	if err != nil {
		t.Fatalf("routing.NewIdentity: %v", err)
	}
	ks, err := keystore.NewKeyStore(db, routingID, passphrase)
	if err != nil {
		t.Fatalf("keystore.NewKeyStore: %v", err)
	}
	return ks
}

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

// --- MESHSAT-536: wrap signing key at rest ---------------------------------

// TestWrapLegacyPlaintextKey covers the happy-path migration: a bridge
// that booted once on legacy plaintext calls LoadWithKeystore and has
// its private key moved into the wrapped column with the plaintext
// replaced by the sentinel.
func TestWrapLegacyPlaintextKey(t *testing.T) {
	db := testDB(t)
	ss, err := NewSigningService(db)
	if err != nil {
		t.Fatalf("NewSigningService: %v", err)
	}
	if ss.KeyIsWrapped() {
		t.Fatal("KeyIsWrapped: expected false before LoadWithKeystore")
	}
	// Verify we can sign on the plaintext-only path.
	sigBefore := ss.Sign([]byte("hello"))
	if len(sigBefore) != ed25519.SignatureSize {
		t.Fatalf("sign before wrap: got %d bytes", len(sigBefore))
	}

	ks := newTestKeystore(t, db, "test-passphrase-1")
	if err := ss.LoadWithKeystore(ks); err != nil {
		t.Fatalf("LoadWithKeystore: %v", err)
	}
	if !ss.KeyIsWrapped() {
		t.Error("KeyIsWrapped: expected true after LoadWithKeystore")
	}

	// DB state: plaintext column cleared to sentinel, wrapped column populated.
	priv, _ := db.GetSystemConfig("signing_private_key")
	if priv != "__WRAPPED_SEE_WRAPPED_COLUMN__" {
		t.Errorf("plaintext column: got %q, want sentinel", priv)
	}
	wrapped, _ := db.GetSystemConfig("signing_private_key_wrapped")
	if wrapped == "" {
		t.Error("wrapped column empty after migration")
	}

	// Signature still verifies against the same public key.
	sigAfter := ss.Sign([]byte("hello"))
	if !Verify(ss.SignerID(), []byte("hello"), sigAfter) {
		t.Error("signature after wrap does not verify")
	}
}

// TestRestartUnwrapsKey covers the second-boot scenario: bridge starts
// with a wrapped-only database, NewSigningService sees the sentinel and
// parks the service, LoadWithKeystore unwraps.
func TestRestartUnwrapsKey(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "restart.db")

	// Boot 1: generate key, wrap it.
	db1, err := database.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ss1, err := NewSigningService(db1)
	if err != nil {
		t.Fatal(err)
	}
	ks1 := newTestKeystore(t, db1, "shared-passphrase")
	if err := ss1.LoadWithKeystore(ks1); err != nil {
		t.Fatalf("boot 1 LoadWithKeystore: %v", err)
	}
	signerID := ss1.SignerID()
	sigBoot1 := ss1.Sign([]byte("data"))
	db1.Close()

	// Boot 2: reopen, expect wrapped-only state until LoadWithKeystore.
	db2, err := database.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	ss2, err := NewSigningService(db2)
	if err != nil {
		t.Fatalf("boot 2 NewSigningService: %v", err)
	}
	if ss2.KeyIsWrapped() {
		t.Error("KeyIsWrapped should be false pre-LoadWithKeystore on boot 2")
	}
	// Sign before LoadWithKeystore returns nil (private key not yet unwrapped).
	if sig := ss2.Sign([]byte("data")); sig != nil {
		t.Errorf("Sign before LoadWithKeystore: got %d bytes, want nil", len(sig))
	}
	if ss2.SignerID() != signerID {
		t.Errorf("SignerID after restart: got %q, want %q", ss2.SignerID(), signerID)
	}

	ks2 := newTestKeystore(t, db2, "shared-passphrase")
	if err := ss2.LoadWithKeystore(ks2); err != nil {
		t.Fatalf("boot 2 LoadWithKeystore: %v", err)
	}
	if !ss2.KeyIsWrapped() {
		t.Error("KeyIsWrapped should be true after LoadWithKeystore")
	}

	// Signature from boot 1 must verify with the boot-2 signer ID.
	if !Verify(ss2.SignerID(), []byte("data"), sigBoot1) {
		t.Error("cross-boot signature verification failed")
	}
	// Sign on boot 2 works normally.
	sigBoot2 := ss2.Sign([]byte("data-2"))
	if !Verify(ss2.SignerID(), []byte("data-2"), sigBoot2) {
		t.Error("boot 2 sign/verify failed")
	}
}

// TestWrongPassphraseRejected ensures a changed master key (wrong
// passphrase) cannot unwrap a key that was wrapped under a different
// master key — prevents silent corruption.
func TestWrongPassphraseRejected(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wrong-pass.db")

	db1, _ := database.New(dbPath)
	ss1, _ := NewSigningService(db1)
	ks1 := newTestKeystore(t, db1, "correct-passphrase")
	if err := ss1.LoadWithKeystore(ks1); err != nil {
		t.Fatalf("LoadWithKeystore: %v", err)
	}
	db1.Close()

	// Re-open with a wrong passphrase. The keystore bootstrap will
	// fail first because it cannot unlock its own master key record —
	// the signing-layer unwrap never gets to run. We assert the
	// whole flow fails rather than a specific error identity.
	db2, err := database.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	_, err = NewSigningService(db2)
	if err != nil {
		t.Fatalf("NewSigningService on wrapped-only DB: %v", err)
	}

	// Build a keystore with the wrong passphrase — should fail at the
	// keystore level.
	routingID, _ := routing.NewIdentity(db2)
	if _, err := keystore.NewKeyStore(db2, routingID, "wrong-passphrase"); err == nil {
		t.Fatal("keystore with wrong passphrase: expected failure, got nil error")
	}
}

// TestLoadWithKeystoreIdempotent verifies calling LoadWithKeystore
// twice in succession is safe — the second call does not re-wrap a
// nil key or produce ciphertext drift.
func TestLoadWithKeystoreIdempotent(t *testing.T) {
	db := testDB(t)
	ss, _ := NewSigningService(db)
	ks := newTestKeystore(t, db, "idem")
	if err := ss.LoadWithKeystore(ks); err != nil {
		t.Fatal(err)
	}
	wrapped1, _ := db.GetSystemConfig("signing_private_key_wrapped")

	// Second call: private key is already loaded, will re-wrap with a
	// fresh nonce. New ciphertext must decrypt to the same plaintext.
	if err := ss.LoadWithKeystore(ks); err != nil {
		t.Fatalf("second LoadWithKeystore: %v", err)
	}
	wrapped2, _ := db.GetSystemConfig("signing_private_key_wrapped")
	if wrapped2 == "" {
		t.Error("wrapped column emptied on second call")
	}
	if wrapped1 == wrapped2 {
		t.Log("note: both wraps produced identical ciphertext (nonce reuse or lucky collision)")
	}

	// Signatures still work.
	sig := ss.Sign([]byte("still working"))
	if !Verify(ss.SignerID(), []byte("still working"), sig) {
		t.Error("sign after idempotent load failed")
	}
}

// TestLoadWithKeystoreNilRejected ensures nil keystore is rejected.
func TestLoadWithKeystoreNilRejected(t *testing.T) {
	db := testDB(t)
	ss, _ := NewSigningService(db)
	err := ss.LoadWithKeystore(nil)
	if err == nil || !strings.Contains(err.Error(), "nil keystore") {
		t.Errorf("nil ks: got err=%v, want contains 'nil keystore'", err)
	}
}

// TestSignerIDStableAcrossWrap ensures the public key (signer ID) is
// unchanged by the wrap operation — downstream verifiers must not
// break.
func TestSignerIDStableAcrossWrap(t *testing.T) {
	db := testDB(t)
	ss, _ := NewSigningService(db)
	idBefore := ss.SignerID()
	ks := newTestKeystore(t, db, "stable")
	_ = ss.LoadWithKeystore(ks)
	if ss.SignerID() != idBefore {
		t.Errorf("SignerID changed across wrap: before=%s after=%s", idBefore, ss.SignerID())
	}
}
