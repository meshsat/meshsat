package oob

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"testing"
)

func TestPeerIDFromKey(t *testing.T) {
	key := testKey()
	a := PeerIDFromKey(key)
	b := PeerIDFromKey(append([]byte{}, key...))
	if a != b {
		t.Fatal("peer id not deterministic")
	}
	if a == 0 {
		t.Fatal("peer id zero")
	}
	other := testKey()
	other[31] ^= 0x01
	if PeerIDFromKey(other) == a {
		t.Fatal("different keys gave the same id (unlikely, check derivation)")
	}
	// Random keys never yield 0.
	for range 1000 {
		k, err := RandomKey()
		if err != nil {
			t.Fatal(err)
		}
		if PeerIDFromKey(k) == 0 {
			t.Fatal("random key mapped to peer id 0")
		}
	}
}

func TestRandomKey(t *testing.T) {
	a, err := RandomKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := RandomKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != KeyLen || bytes.Equal(a, b) {
		t.Fatal("random keys not distinct 32-byte values")
	}
}

func TestDeriveECDHKey(t *testing.T) {
	curve := ecdh.X25519()
	privA, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privB, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hashA := bytes.Repeat([]byte{0x11}, 16)
	hashB := bytes.Repeat([]byte{0x22}, 16)

	keyAB, err := DeriveECDHKey(privA, privB.PublicKey(), hashA, hashB)
	if err != nil {
		t.Fatal(err)
	}
	keyBA, err := DeriveECDHKey(privB, privA.PublicKey(), hashB, hashA)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(keyAB, keyBA) {
		t.Fatal("ecdh derivation not symmetric")
	}
	if len(keyAB) != KeyLen {
		t.Fatalf("key len %d", len(keyAB))
	}
	// Roles: the lower hash is the issuer, the other the importer.
	if RoleForECDH(hashA, hashB) != RoleIssuer || RoleForECDH(hashB, hashA) != RoleImporter {
		t.Fatal("role assignment by hash order wrong")
	}
	// A different pair of hashes gives a different key even with the same agreement.
	keyOther, err := DeriveECDHKey(privA, privB.PublicKey(), hashA, bytes.Repeat([]byte{0x33}, 16))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(keyOther, keyAB) {
		t.Fatal("hashes not bound into the key")
	}
	// Both sides agree on the peer id and can talk.
	peer := PeerIDFromKey(keyAB)
	wire, err := Seal(Frame{Enc: true, PeerID: peer, Counter: 1, Cmd: CmdPing}, keyAB, RoleForECDH(hashA, hashB))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(wire, keyBA, RoleForECDH(hashA, hashB)); err != nil {
		t.Fatalf("B cannot open A's frame: %v", err)
	}
	if _, err := DeriveECDHKey(nil, privB.PublicKey(), hashA, hashB); err == nil {
		t.Fatal("nil private key accepted")
	}
}
