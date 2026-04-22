package keystore

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

type testSigner struct {
	priv ed25519.PrivateKey
}

func (s *testSigner) Sign(data []byte) []byte {
	return ed25519.Sign(s.priv, data)
}

func newTestSigner(t *testing.T) (*testSigner, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &testSigner{priv: priv}, pub
}

func testEntries() []BundleEntry {
	var k1, k2 [aesKeyLen]byte
	rand.Read(k1[:])
	rand.Read(k2[:])
	return []BundleEntry{
		{ChannelType: ChannelSMS, Address: "+31612345678", Key: k1},
		{ChannelType: ChannelMesh, Address: "!abc12345", Key: k2},
	}
}

func testBridgeHash() [destHashLen]byte {
	var h [destHashLen]byte
	rand.Read(h[:])
	return h
}

func TestMarshalBundleV1_RoundTrip(t *testing.T) {
	signer, pub := newTestSigner(t)
	entries := testEntries()
	bh := testBridgeHash()

	data, err := MarshalBundle(bh, entries, signer)
	if err != nil {
		t.Fatal(err)
	}

	b, err := UnmarshalBundle(data)
	if err != nil {
		t.Fatal(err)
	}
	if b.Version != bundleVersionV1 {
		t.Fatalf("version: got %d, want %d", b.Version, bundleVersionV1)
	}
	if b.BridgeHash != bh {
		t.Fatal("bridge hash mismatch")
	}
	if len(b.Entries) != 2 {
		t.Fatalf("entries: got %d, want 2", len(b.Entries))
	}
	if b.Entries[0].Address != "+31612345678" {
		t.Fatalf("address: got %q", b.Entries[0].Address)
	}
	if b.SigningPub != nil {
		t.Fatal("v1 should have nil SigningPub")
	}
	if !VerifyBundle(data, pub) {
		t.Fatal("valid v1 bundle should verify")
	}
}

func TestMarshalBundleV2_RoundTrip(t *testing.T) {
	signer, pub := newTestSigner(t)
	entries := testEntries()
	bh := testBridgeHash()

	data, err := MarshalBundleV2(bh, entries, signer, pub)
	if err != nil {
		t.Fatal(err)
	}

	b, err := UnmarshalBundle(data)
	if err != nil {
		t.Fatal(err)
	}
	if b.Version != bundleVersionV2 {
		t.Fatalf("version: got %d, want %d", b.Version, bundleVersionV2)
	}
	if b.BridgeHash != bh {
		t.Fatal("bridge hash mismatch")
	}
	if len(b.Entries) != 2 {
		t.Fatalf("entries: got %d, want 2", len(b.Entries))
	}
	if len(b.SigningPub) != pubKeyLen {
		t.Fatalf("signing pub: got %d bytes, want %d", len(b.SigningPub), pubKeyLen)
	}
	if !VerifyBundle(data, nil) {
		t.Fatal("valid v2 bundle should verify with embedded key")
	}
}

func TestVerifyBundle_V1_WrongKey(t *testing.T) {
	signer, _ := newTestSigner(t)
	_, wrongPub := newTestSigner(t)
	data, _ := MarshalBundle(testBridgeHash(), testEntries(), signer)

	if VerifyBundle(data, wrongPub) {
		t.Fatal("v1 bundle should not verify with wrong key")
	}
}

func TestVerifyBundle_V2_Tampered(t *testing.T) {
	signer, pub := newTestSigner(t)
	data, _ := MarshalBundleV2(testBridgeHash(), testEntries(), signer, pub)

	// Tamper with entry data (last byte)
	tampered := make([]byte, len(data))
	copy(tampered, data)
	tampered[len(tampered)-1] ^= 0xff

	if VerifyBundle(tampered, nil) {
		t.Fatal("tampered v2 bundle should not verify")
	}
}

func TestVerifyBundle_V2_SwappedPubKey(t *testing.T) {
	signer, pub := newTestSigner(t)
	_, otherPub := newTestSigner(t)
	data, _ := MarshalBundleV2(testBridgeHash(), testEntries(), signer, pub)

	// Replace embedded pubkey with a different one
	swapped := make([]byte, len(data))
	copy(swapped, data)
	copy(swapped[22:22+pubKeyLen], otherPub)

	if VerifyBundle(swapped, nil) {
		t.Fatal("v2 bundle with swapped pubkey should not verify")
	}
}

func TestMarshalBundleV2_EmptyEntries(t *testing.T) {
	signer, pub := newTestSigner(t)
	_, err := MarshalBundleV2(testBridgeHash(), nil, signer, pub)
	if err == nil {
		t.Fatal("should reject empty entries")
	}
}

func TestMarshalBundleV2_WrongPubKeyLen(t *testing.T) {
	signer, _ := newTestSigner(t)
	_, err := MarshalBundleV2(testBridgeHash(), testEntries(), signer, []byte{1, 2, 3})
	if err == nil {
		t.Fatal("should reject wrong pubkey length")
	}
}

func TestBundleURLRoundTrip(t *testing.T) {
	signer, pub := newTestSigner(t)
	data, _ := MarshalBundleV2(testBridgeHash(), testEntries(), signer, pub)
	url := BundleToURL(data)

	decoded, err := URLToBundle(url)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != len(data) {
		t.Fatal("decoded length mismatch")
	}
	if !VerifyBundle(decoded, nil) {
		t.Fatal("decoded v2 bundle should verify")
	}
}

func TestSigningKeyFingerprint(t *testing.T) {
	_, pub := newTestSigner(t)
	fp := SigningKeyFingerprint(pub)
	if len(fp) != 16 {
		t.Fatalf("fingerprint length: got %d, want 16", len(fp))
	}
	// Must be valid hex
	if _, err := hex.DecodeString(fp); err != nil {
		t.Fatalf("fingerprint not valid hex: %v", err)
	}
}

// TestGenerateCrossPlatformTestdata writes a v2 bundle with known keys to testdata/
// for cross-platform verification by the Android KeyBundleImporter.
func TestGenerateCrossPlatformTestdata(t *testing.T) {
	// Fixed seed for deterministic keypair (test only — not for production)
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1) // deterministic seed: 01 02 03 ... 20
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	signer := &testSigner{priv: priv}

	// Fixed bridge hash
	var bridgeHash [destHashLen]byte
	for i := range bridgeHash {
		bridgeHash[i] = byte(0xA0 + i)
	}

	// Fixed AES keys
	var key1, key2 [aesKeyLen]byte
	for i := range key1 {
		key1[i] = byte(0x10 + i)
	}
	for i := range key2 {
		key2[i] = byte(0x50 + i)
	}

	entries := []BundleEntry{
		{ChannelType: ChannelSMS, Address: "+31612345678", Key: key1},
		{ChannelType: ChannelMesh, Address: "!abc12345", Key: key2},
	}

	data, err := MarshalBundleV2(bridgeHash, entries, signer, pub)
	if err != nil {
		t.Fatal(err)
	}

	// Verify our own output
	if !VerifyBundle(data, nil) {
		t.Fatal("generated testdata bundle should verify")
	}

	// Write testdata
	dir := filepath.Join("testdata")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "v2_bundle.bin"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "v2_bundle_pubkey.bin"), pub, 0o644); err != nil {
		t.Fatal(err)
	}

	// Write a manifest for the Android test to use
	manifest := "# Cross-platform v2 bundle testdata\n" +
		"# Generated by TestGenerateCrossPlatformTestdata in bundle_test.go\n" +
		"signing_pub=" + hex.EncodeToString(pub) + "\n" +
		"fingerprint=" + SigningKeyFingerprint(pub) + "\n" +
		"bridge_hash=" + hex.EncodeToString(bridgeHash[:]) + "\n" +
		"entry_count=2\n" +
		"entry_0_type=sms\n" +
		"entry_0_address=+31612345678\n" +
		"entry_0_key=" + hex.EncodeToString(key1[:]) + "\n" +
		"entry_1_type=mesh\n" +
		"entry_1_address=!abc12345\n" +
		"entry_1_key=" + hex.EncodeToString(key2[:]) + "\n"

	if err := os.WriteFile(filepath.Join(dir, "v2_bundle_manifest.txt"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Logf("wrote testdata to %s (bundle=%d bytes, pubkey=%d bytes)", dir, len(data), len(pub))
}

// TestChannelBond_BundleRoundTrip verifies that the ChannelBond enum
// value (0x07, MESHSAT-664) survives a v2 bundle Marshal/Unmarshal
// and the string <-> byte mapping is symmetric. This is the on-wire
// compatibility gate for syncing a `bond:<group_id>` shared key
// across kits via POST /api/keys/import.
func TestChannelBond_BundleRoundTrip(t *testing.T) {
	if ChannelBond != 0x07 {
		t.Fatalf("ChannelBond wire byte must be 0x07, got 0x%02x", ChannelBond)
	}
	if ChannelTypeToByte("bond") != ChannelBond {
		t.Errorf("ChannelTypeToByte(bond)=0x%02x, want 0x%02x", ChannelTypeToByte("bond"), ChannelBond)
	}
	if got := ByteToChannelType(ChannelBond); got != "bond" {
		t.Errorf("ByteToChannelType(0x07)=%q, want bond", got)
	}

	var key [aesKeyLen]byte
	rand.Read(key[:])
	entries := []BundleEntry{
		{ChannelType: ChannelBond, Address: "bond1", Key: key},
	}
	signer, pub := newTestSigner(t)
	data, err := MarshalBundleV2(testBridgeHash(), entries, signer, pub)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyBundle(data, nil) {
		t.Fatal("bond bundle signature failed")
	}
	b, err := UnmarshalBundle(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(b.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(b.Entries))
	}
	if b.Entries[0].ChannelType != ChannelBond {
		t.Errorf("channel_type=0x%02x, want 0x%02x", b.Entries[0].ChannelType, ChannelBond)
	}
	if b.Entries[0].Address != "bond1" {
		t.Errorf("address=%q, want bond1", b.Entries[0].Address)
	}
	if b.Entries[0].Key != key {
		t.Error("key material mismatch across round-trip")
	}
}
