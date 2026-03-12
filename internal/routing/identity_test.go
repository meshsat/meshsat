package routing

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func TestComputeDestHash(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	h1 := ComputeDestHash(pub, encKey.PublicKey())
	h2 := ComputeDestHash(pub, encKey.PublicKey())

	if h1 != h2 {
		t.Fatal("same keys should produce same hash")
	}
	if h1 == [DestHashLen]byte{} {
		t.Fatal("hash should not be zero")
	}
}

func TestComputeDestHash_DifferentKeys(t *testing.T) {
	pub1, _, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	enc, _ := ecdh.X25519().GenerateKey(rand.Reader)

	h1 := ComputeDestHash(pub1, enc.PublicKey())
	h2 := ComputeDestHash(pub2, enc.PublicKey())

	if h1 == h2 {
		t.Fatal("different signing keys should produce different hashes")
	}
}

func TestVerifyWith(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	data := []byte("test message")
	sig := ed25519.Sign(priv, data)

	if !VerifyWith(pub, data, sig) {
		t.Fatal("valid signature should verify")
	}
	if VerifyWith(pub, []byte("wrong data"), sig) {
		t.Fatal("wrong data should not verify")
	}
	if VerifyWith(nil, data, sig) {
		t.Fatal("nil key should not verify")
	}
}
