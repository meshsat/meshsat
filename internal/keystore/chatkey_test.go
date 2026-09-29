package keystore

import (
	"bytes"
	"encoding/hex"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/routing"
)

func newChatKeyStore(t *testing.T) (*KeyStore, *database.DB) {
	t.Helper()
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	id, err := routing.NewIdentity(db)
	if err != nil {
		t.Fatal(err)
	}
	ks, err := NewKeyStore(db, id, "chat-key-store-test")
	if err != nil {
		t.Fatal(err)
	}
	return ks, db
}

// LookupKey tells "no key" from "a key that cannot be read", and keeps the
// label a key was stored with; the chat key resolver is its hex.
func TestLookupKey_FoundLabelRevokedBroken(t *testing.T) {
	ks, db := newChatKeyStore(t)
	const addr = "+31612345678"
	keyA := bytes.Repeat([]byte{0xa1}, aesKeyLen)
	keyB := bytes.Repeat([]byte{0xb2}, aesKeyLen)

	if _, _, _, found, err := ks.LookupKey("sms", addr); found || err != nil {
		t.Fatalf("empty store: found=%v err=%v", found, err)
	}
	if k, found, err := ks.ChatKeyHex("sms", addr); k != "" || found || err != nil {
		t.Fatalf("ChatKeyHex on an empty store: %q %v %v", k, found, err)
	}

	if v, err := ks.StoreKeyLabelled("sms", addr, keyA, ""); err != nil || v != 1 {
		t.Fatalf("store: v%d %v", v, err)
	}
	raw, v, label, found, err := ks.LookupKey("sms", addr)
	if err != nil || !found || v != 1 || label != "" || !bytes.Equal(raw, keyA) {
		t.Fatalf("lookup: v%d %q found=%v err=%v", v, label, found, err)
	}

	if v, err := ks.StoreKeyLabelled("sms", addr, keyB, "hub-rotated-v4"); err != nil || v != 2 {
		t.Fatalf("second store: v%d %v", v, err)
	}
	raw, v, label, found, err = ks.LookupKey("sms", addr)
	if err != nil || !found || v != 2 || label != "hub-rotated-v4" || !bytes.Equal(raw, keyB) {
		t.Fatalf("lookup after the new version: v%d %q found=%v err=%v", v, label, found, err)
	}
	if k, found, err := ks.ChatKeyHex("sms", addr); k != hex.EncodeToString(keyB) || !found || err != nil {
		t.Fatalf("ChatKeyHex: %q %v %v", k, found, err)
	}
	metas, err := ks.ListKeys()
	if err != nil {
		t.Fatal(err)
	}
	labelled := false
	for _, m := range metas {
		if m.Address == addr && m.KeyVersion == 2 && m.Label == "hub-rotated-v4" && m.Status == "active" {
			labelled = true
		}
	}
	if !labelled {
		t.Fatalf("ListKeys: %+v", metas)
	}
	// StoreKey (the Hub's and the OOB service's path) keeps working, unlabelled.
	if v, err := ks.StoreKey("sms", "*", keyA); err != nil || v != 1 {
		t.Fatalf("StoreKey: v%d %v", v, err)
	}

	if err := ks.RevokeKey("sms", addr); err != nil {
		t.Fatal(err)
	}
	if _, _, _, found, err := ks.LookupKey("sms", addr); found || err != nil {
		t.Fatalf("after revoke: found=%v err=%v", found, err)
	}

	if _, err := db.Exec(`UPDATE key_bundles SET encrypted_key = x'00' WHERE address = '*'`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, found, err := ks.LookupKey("sms", "*"); found || err == nil {
		t.Fatalf("broken key: found=%v err=%v, want an error", found, err)
	}
	if _, found, err := ks.ChatKeyHex("sms", "*"); found || err == nil {
		t.Fatalf("ChatKeyHex on a broken key: found=%v err=%v, want an error", found, err)
	}
}
