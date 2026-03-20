package reticulum

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"
)

func TestGenerateIdentity(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if id.EncryptionPublicKey() == nil {
		t.Fatal("encryption key is nil")
	}
	if len(id.SigningPublicKey()) != ed25519.PublicKeySize {
		t.Fatalf("signing key size: got %d, want %d", len(id.SigningPublicKey()), ed25519.PublicKeySize)
	}
}

func TestIdentity_PublicBytes(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	pub := id.PublicBytes()
	if len(pub) != IdentityKeySize {
		t.Fatalf("public bytes size: got %d, want %d", len(pub), IdentityKeySize)
	}

	// First 32 bytes should be X25519, last 32 should be Ed25519
	encPub := pub[:EncryptionPubLen]
	sigPub := pub[EncryptionPubLen:]

	if hex.EncodeToString(encPub) != hex.EncodeToString(id.EncryptionPublicKey().Bytes()) {
		t.Error("X25519 public key mismatch in PublicBytes")
	}
	if hex.EncodeToString(sigPub) != hex.EncodeToString(id.SigningPublicKey()) {
		t.Error("Ed25519 public key mismatch in PublicBytes")
	}
}

func TestLoadIdentity_Roundtrip(t *testing.T) {
	id1, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}

	id2, err := LoadIdentity(id1.EncryptionPrivateBytes(), id1.SigningPrivateBytes())
	if err != nil {
		t.Fatal(err)
	}

	if hex.EncodeToString(id1.PublicBytes()) != hex.EncodeToString(id2.PublicBytes()) {
		t.Error("loaded identity has different public keys")
	}

	appName := "meshsat.hub"
	if id1.DestHash(appName) != id2.DestHash(appName) {
		t.Error("loaded identity has different DestHash")
	}
}

func TestLoadIdentity_InvalidKeys(t *testing.T) {
	// Too short encryption key
	_, err := LoadIdentity([]byte{1, 2, 3}, make([]byte, ed25519.PrivateKeySize))
	if err == nil {
		t.Error("expected error for short encryption key")
	}

	// Wrong size signing key
	validEnc, _ := ecdh.X25519().GenerateKey(rand.Reader)
	_, err = LoadIdentity(validEnc.Bytes(), []byte{1, 2, 3})
	if err == nil {
		t.Error("expected error for short signing key")
	}
}

func TestComputeNameHash(t *testing.T) {
	h1 := ComputeNameHash("meshsat.hub")
	h2 := ComputeNameHash("meshsat.hub")
	if h1 != h2 {
		t.Error("same name should produce same hash")
	}

	h3 := ComputeNameHash("meshsat.bridge")
	if h1 == h3 {
		t.Error("different names should produce different hashes")
	}

	if h1 == [NameHashLen]byte{} {
		t.Error("name hash should not be zero")
	}
}

func TestComputeIdentityHash(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}

	h1 := id.IdentityHash()
	h2 := ComputeIdentityHash(id.EncryptionPublicKey(), id.SigningPublicKey())
	if h1 != h2 {
		t.Error("IdentityHash and ComputeIdentityHash should match")
	}
	if h1 == [TruncatedHashLen]byte{} {
		t.Error("identity hash should not be zero")
	}
}

func TestComputeDestHash_RNSFormat(t *testing.T) {
	// Verify the computation chain:
	// dest_hash = SHA256(name_hash || identity_hash)[:16]
	// where identity_hash = SHA256(enc_pub || sig_pub)[:16]
	// and name_hash = SHA256("app.name")[:10]
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}

	appName := "meshsat.hub"
	dest1 := id.DestHash(appName)
	dest2 := id.DestHash(appName)
	if dest1 != dest2 {
		t.Error("same identity + app name should produce same dest hash")
	}

	// Different app name → different dest hash
	dest3 := id.DestHash("meshsat.bridge")
	if dest1 == dest3 {
		t.Error("different app names should produce different dest hashes")
	}

	// Different identity → different dest hash
	id2, _ := GenerateIdentity()
	dest4 := id2.DestHash(appName)
	if dest1 == dest4 {
		t.Error("different identities should produce different dest hashes")
	}
}

func TestDestHash_DependsOnBothKeys(t *testing.T) {
	// Verify that changing either key changes the identity hash
	enc1, _ := ecdh.X25519().GenerateKey(rand.Reader)
	enc2, _ := ecdh.X25519().GenerateKey(rand.Reader)
	sig1, _, _ := ed25519.GenerateKey(rand.Reader)
	sig2, _, _ := ed25519.GenerateKey(rand.Reader)

	h1 := ComputeIdentityHash(enc1.PublicKey(), sig1)
	h2 := ComputeIdentityHash(enc2.PublicKey(), sig1)
	h3 := ComputeIdentityHash(enc1.PublicKey(), sig2)

	if h1 == h2 {
		t.Error("different enc keys should produce different identity hashes")
	}
	if h1 == h3 {
		t.Error("different sig keys should produce different identity hashes")
	}
}

func TestIdentity_Sign_Verify(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}

	data := []byte("test message for signing")
	sig := id.Sign(data)

	if !VerifySignature(id.SigningPublicKey(), data, sig) {
		t.Error("valid signature should verify")
	}
	if VerifySignature(id.SigningPublicKey(), []byte("wrong data"), sig) {
		t.Error("wrong data should not verify")
	}
}

func TestVerifySignature_InvalidInputs(t *testing.T) {
	if VerifySignature(nil, []byte("data"), make([]byte, SignatureLen)) {
		t.Error("nil key should not verify")
	}
	if VerifySignature(make(ed25519.PublicKey, ed25519.PublicKeySize), []byte("data"), []byte{1}) {
		t.Error("short signature should not verify")
	}
}

func TestParsePublicKeys(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}

	pub := id.PublicBytes()
	encPub, sigPub, err := ParsePublicKeys(pub)
	if err != nil {
		t.Fatal(err)
	}

	if hex.EncodeToString(encPub.Bytes()) != hex.EncodeToString(id.EncryptionPublicKey().Bytes()) {
		t.Error("parsed encryption key mismatch")
	}
	if hex.EncodeToString(sigPub) != hex.EncodeToString(id.SigningPublicKey()) {
		t.Error("parsed signing key mismatch")
	}
}

func TestParsePublicKeys_InvalidSize(t *testing.T) {
	_, _, err := ParsePublicKeys([]byte{1, 2, 3})
	if err == nil {
		t.Error("expected error for wrong size")
	}
}

func TestDestHashHex(t *testing.T) {
	var dest [TruncatedHashLen]byte
	for i := range dest {
		dest[i] = byte(i)
	}
	h := DestHashHex(dest)
	if len(h) != TruncatedHashLen*2 {
		t.Errorf("hex length: got %d, want %d", len(h), TruncatedHashLen*2)
	}
	if h != "000102030405060708090a0b0c0d0e0f" {
		t.Errorf("hex: got %s", h)
	}
}
