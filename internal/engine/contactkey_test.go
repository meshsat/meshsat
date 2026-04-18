package engine

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/directory"
)

// newDirStore returns a directory.SQLStore sharing the given test DB.
// Migrations v44-v48 have already run — testDB calls database.New
// which runs all migrations.
func newDirStore(t *testing.T, db *database.DB) *directory.SQLStore {
	t.Helper()
	return directory.NewSQLStore(db.DB)
}

// seedContact creates a directory contact and returns its ID.
func seedContact(t *testing.T, dir *directory.SQLStore, name string) string {
	t.Helper()
	c := &directory.Contact{DisplayName: name}
	if err := dir.CreateContact(context.Background(), c); err != nil {
		t.Fatalf("seed contact: %v", err)
	}
	return c.ID
}

// --- MESHSAT-537: ContactKeyService ---------------------------------------

func TestContactKeyService_Resolve_Happy(t *testing.T) {
	db := testDB(t)
	ks := newTestKeystore(t, db, "ckr-happy")
	dir := newDirStore(t, db)
	cks := NewContactKeyService(ks, dir)
	if cks == nil {
		t.Fatal("NewContactKeyService returned nil with non-nil deps")
	}

	cid := seedContact(t, dir, "Alice")
	gen, err := cks.GenerateAndStoreAES(context.Background(), cid, directory.TrustAnchorLocal)
	if err != nil {
		t.Fatalf("GenerateAndStoreAES: %v", err)
	}
	if len(gen) != 64 { // 32 bytes hex = 64 chars
		t.Fatalf("generated key hex length: got %d, want 64", len(gen))
	}

	got, err := cks.ResolveContactKey(cid)
	if err != nil {
		t.Fatalf("ResolveContactKey: %v", err)
	}
	if got != gen {
		t.Errorf("resolved key differs from generated\n got:  %s\n want: %s", got, gen)
	}
	// Raw bytes decode cleanly.
	if _, err := hex.DecodeString(got); err != nil {
		t.Errorf("resolved hex not valid: %v", err)
	}
}

func TestContactKeyService_Resolve_NoKey_Error(t *testing.T) {
	db := testDB(t)
	ks := newTestKeystore(t, db, "ckr-empty")
	dir := newDirStore(t, db)
	cks := NewContactKeyService(ks, dir)

	cid := seedContact(t, dir, "Bob") // no key attached
	_, err := cks.ResolveContactKey(cid)
	if err == nil {
		t.Fatal("expected error for contact with no AES key, got nil")
	}
	if !strings.Contains(err.Error(), "no active AES256_GCM_SHARED key") {
		t.Errorf("error text: %v, want 'no active AES256_GCM_SHARED key'", err)
	}
}

func TestContactKeyService_Resolve_InputValidation(t *testing.T) {
	db := testDB(t)
	ks := newTestKeystore(t, db, "ckr-input")
	dir := newDirStore(t, db)
	cks := NewContactKeyService(ks, dir)

	if _, err := cks.ResolveContactKey(""); err == nil {
		t.Error("empty contact_id: expected error")
	}

	var nilCKS *ContactKeyService
	if _, err := nilCKS.ResolveContactKey("any"); err == nil {
		t.Error("nil service: expected error")
	}
}

func TestNewContactKeyService_NilDepsReturnsNil(t *testing.T) {
	if s := NewContactKeyService(nil, nil); s != nil {
		t.Error("nil deps: expected nil service")
	}
	db := testDB(t)
	ks := newTestKeystore(t, db, "")
	if s := NewContactKeyService(ks, nil); s != nil {
		t.Error("nil dir: expected nil service")
	}
	dir := newDirStore(t, db)
	if s := NewContactKeyService(nil, dir); s != nil {
		t.Error("nil ks: expected nil service")
	}
}

func TestContactKeyService_Generate_VersionIncrement(t *testing.T) {
	db := testDB(t)
	ks := newTestKeystore(t, db, "ckr-ver")
	dir := newDirStore(t, db)
	cks := NewContactKeyService(ks, dir)

	cid := seedContact(t, dir, "Carol")
	if _, err := cks.GenerateAndStoreAES(context.Background(), cid, directory.TrustAnchorLocal); err != nil {
		t.Fatal(err)
	}
	// Retire and generate again → version should tick to 2.
	keys, _ := dir.ListKeys(context.Background(), cid, false)
	if len(keys) != 1 || keys[0].Version != 1 {
		t.Fatalf("v1 state wrong: %+v", keys)
	}
	if err := dir.RetireKey(context.Background(), keys[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := cks.GenerateAndStoreAES(context.Background(), cid, directory.TrustAnchorQR); err != nil {
		t.Fatal(err)
	}
	keys, _ = dir.ListKeys(context.Background(), cid, false)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	versionsSeen := map[int]bool{}
	for _, k := range keys {
		versionsSeen[k.Version] = true
	}
	if !versionsSeen[1] || !versionsSeen[2] {
		t.Errorf("expected versions {1,2}, got %+v", versionsSeen)
	}
}

func TestContactKeyService_RotateAES(t *testing.T) {
	db := testDB(t)
	ks := newTestKeystore(t, db, "ckr-rotate")
	dir := newDirStore(t, db)
	cks := NewContactKeyService(ks, dir)

	cid := seedContact(t, dir, "Dave")
	oldHex, err := cks.GenerateAndStoreAES(context.Background(), cid, directory.TrustAnchorLocal)
	if err != nil {
		t.Fatal(err)
	}
	newHex, err := cks.RotateAES(context.Background(), cid, directory.TrustAnchorQR)
	if err != nil {
		t.Fatalf("RotateAES: %v", err)
	}
	if newHex == oldHex {
		t.Error("rotate returned same key material (collision or no-op)")
	}

	// Resolve must now return the new key, not the old.
	got, err := cks.ResolveContactKey(cid)
	if err != nil {
		t.Fatal(err)
	}
	if got != newHex {
		t.Errorf("resolve after rotate: got %s, want new %s", got, newHex)
	}

	// Old record must be retired.
	allKeys, _ := dir.ListKeys(context.Background(), cid, false)
	retired, active := 0, 0
	for _, k := range allKeys {
		switch k.Status {
		case directory.KeyRetired:
			retired++
		case directory.KeyActive:
			active++
		}
	}
	if retired != 1 || active != 1 {
		t.Errorf("post-rotate counts: retired=%d active=%d, want 1/1", retired, active)
	}
}

func TestContactKeyService_Resolve_RetiredKeySkipped(t *testing.T) {
	db := testDB(t)
	ks := newTestKeystore(t, db, "ckr-retired")
	dir := newDirStore(t, db)
	cks := NewContactKeyService(ks, dir)

	cid := seedContact(t, dir, "Eve")
	if _, err := cks.GenerateAndStoreAES(context.Background(), cid, directory.TrustAnchorLocal); err != nil {
		t.Fatal(err)
	}
	// Retire the only key.
	keys, _ := dir.ListKeys(context.Background(), cid, true)
	if err := dir.RetireKey(context.Background(), keys[0].ID); err != nil {
		t.Fatal(err)
	}

	_, err := cks.ResolveContactKey(cid)
	if err == nil {
		t.Fatal("expected error when only key is retired, got nil")
	}
}

// --- Transform pipeline integration (key_ref="contact:<uuid>") --------

func TestTransformPipeline_EncryptDecryptContactKeyRef(t *testing.T) {
	db := testDB(t)
	ks := newTestKeystore(t, db, "tp-contact")
	dir := newDirStore(t, db)
	cks := NewContactKeyService(ks, dir)

	cid := seedContact(t, dir, "Frank")
	if _, err := cks.GenerateAndStoreAES(context.Background(), cid, directory.TrustAnchorLocal); err != nil {
		t.Fatal(err)
	}

	tp := NewTransformPipeline()
	tp.SetContactKeyResolver(cks)

	// Egress: compress + encrypt + base64. Ingress reverses.
	specs := `[
		{"type":"encrypt","params":{"key_ref":"contact:` + cid + `"}},
		{"type":"base64"}
	]`
	plain := []byte("one box. one contact. any radio.")
	enc, err := tp.ApplyEgress(plain, specs)
	if err != nil {
		t.Fatalf("egress: %v", err)
	}
	if len(enc) == 0 {
		t.Fatal("egress returned empty")
	}
	dec, err := tp.ApplyIngress(enc, specs)
	if err != nil {
		t.Fatalf("ingress: %v", err)
	}
	if string(dec) != string(plain) {
		t.Errorf("round-trip:\n got:  %q\n want: %q", dec, plain)
	}
}

func TestTransformPipeline_ContactKeyRef_WithoutResolver_Errors(t *testing.T) {
	tp := NewTransformPipeline()
	// No SetContactKeyResolver. "contact:..." refs must fail loudly.
	specs := `[{"type":"encrypt","params":{"key_ref":"contact:abcdef"}}]`
	_, err := tp.ApplyEgress([]byte("x"), specs)
	if err == nil || !strings.Contains(err.Error(), "no ContactKeyResolver") {
		t.Errorf("want error containing 'no ContactKeyResolver', got: %v", err)
	}
}

func TestTransformPipeline_LegacyKeyRefUnaffected(t *testing.T) {
	// With only SetKeyResolver wired, legacy "sms:+31..." refs must
	// continue to work. The MESHSAT-537 extension must NOT break the
	// dual-read grace period (S2-05 / MESHSAT-548).
	tp := NewTransformPipeline()

	// Tiny stub KeyResolver returning a deterministic hex key.
	tp.SetKeyResolver(stubKeyResolver{hexKey: strings.Repeat("ab", 32)})

	specs := `[{"type":"encrypt","params":{"key_ref":"sms:+31612345678"}}]`
	enc, err := tp.ApplyEgress([]byte("hello legacy"), specs)
	if err != nil {
		t.Fatalf("legacy egress failed: %v", err)
	}
	dec, err := tp.ApplyIngress(enc, specs)
	if err != nil {
		t.Fatalf("legacy ingress failed: %v", err)
	}
	if string(dec) != "hello legacy" {
		t.Errorf("legacy round-trip mismatch: %q", dec)
	}
}

type stubKeyResolver struct{ hexKey string }

func (s stubKeyResolver) ResolveKeyHex(keyRef string) (string, error) { return s.hexKey, nil }

// --- MESHSAT-548 S2-05 dual-read grace period verification --------------

// TestTransformPipeline_DualReadCoexistence proves the pipeline
// routes per-channel and per-contact key_refs independently on the
// same instance — the grace-period invariant behind S2-05. Emit one
// message under a legacy sms: ref, another under a contact: ref,
// assert BOTH decrypt cleanly.
func TestTransformPipeline_DualReadCoexistence(t *testing.T) {
	db := testDB(t)
	ks := newTestKeystore(t, db, "s2-05-dual")
	dir := newDirStore(t, db)
	cks := NewContactKeyService(ks, dir)

	// Provision a per-contact key.
	cid := seedContact(t, dir, "Grace")
	if _, err := cks.GenerateAndStoreAES(context.Background(), cid, directory.TrustAnchorLocal); err != nil {
		t.Fatal(err)
	}

	tp := NewTransformPipeline()
	tp.SetContactKeyResolver(cks)
	tp.SetKeyResolver(stubKeyResolver{hexKey: strings.Repeat("cd", 32)})

	// Legacy ref round-trip.
	legacySpecs := `[{"type":"encrypt","params":{"key_ref":"sms:+31655555555"}}]`
	encLegacy, err := tp.ApplyEgress([]byte("legacy"), legacySpecs)
	if err != nil {
		t.Fatalf("legacy egress: %v", err)
	}
	decLegacy, err := tp.ApplyIngress(encLegacy, legacySpecs)
	if err != nil || string(decLegacy) != "legacy" {
		t.Errorf("legacy round-trip failed: %q err=%v", decLegacy, err)
	}

	// Per-contact ref round-trip on the same pipeline instance.
	contactSpecs := `[{"type":"encrypt","params":{"key_ref":"contact:` + cid + `"}}]`
	encContact, err := tp.ApplyEgress([]byte("new-world"), contactSpecs)
	if err != nil {
		t.Fatalf("contact egress: %v", err)
	}
	decContact, err := tp.ApplyIngress(encContact, contactSpecs)
	if err != nil || string(decContact) != "new-world" {
		t.Errorf("contact round-trip failed: %q err=%v", decContact, err)
	}

	// Cross-protect: a contact-keyed ciphertext must not decrypt under
	// the legacy resolver (different key material).
	if _, err := tp.ApplyIngress(encContact, legacySpecs); err == nil {
		t.Error("contact ciphertext unexpectedly decrypted under legacy key")
	}
}
