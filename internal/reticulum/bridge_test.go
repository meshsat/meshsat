package reticulum

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"testing"
)

// Bridge-format tests: link handshake, confirmations, keepalive.
// These test the Bridge-compatible framing used by MeshSat Bridge.

func TestLinkRequestRoundTrip(t *testing.T) {
	var dest [DestHashLen]byte
	rand.Read(dest[:])

	ephKey, _ := ecdh.X25519().GenerateKey(rand.Reader)
	lr := &LinkRequest{
		DestHash:     dest,
		EphemeralPub: ephKey.PublicKey(),
	}
	rand.Read(lr.Random[:])

	wire := lr.Marshal()
	if len(wire) != LinkRequestLen {
		t.Fatalf("wire len: got %d, want %d", len(wire), LinkRequestLen)
	}

	lr2, err := UnmarshalLinkRequest(wire)
	if err != nil {
		t.Fatal(err)
	}
	if lr2.DestHash != lr.DestHash {
		t.Fatal("dest hash mismatch")
	}
	if !bytes.Equal(lr2.EphemeralPub.Bytes(), lr.EphemeralPub.Bytes()) {
		t.Fatal("ephemeral pub mismatch")
	}
	if lr2.Random != lr.Random {
		t.Fatal("random mismatch")
	}
}

func TestLinkRequestWrongType(t *testing.T) {
	data := make([]byte, LinkRequestLen)
	data[0] = 0xFF
	_, err := UnmarshalLinkRequest(data)
	if err != ErrWrongType {
		t.Fatalf("expected ErrWrongType, got %v", err)
	}
}

func TestLinkProofRoundTrip(t *testing.T) {
	id, _ := GenerateIdentity()
	var linkID [LinkIDLen]byte
	rand.Read(linkID[:])

	ephKey, _ := ecdh.X25519().GenerateKey(rand.Reader)
	signable := LinkProofSignable(linkID, ephKey.PublicKey())
	sig := id.Sign(signable)

	lp := &LinkProof{
		LinkID:       linkID,
		EphemeralPub: ephKey.PublicKey(),
		Signature:    sig,
	}

	wire := lp.Marshal()
	if len(wire) != LinkProofLen {
		t.Fatalf("wire len: got %d, want %d", len(wire), LinkProofLen)
	}

	lp2, err := UnmarshalLinkProof(wire)
	if err != nil {
		t.Fatal(err)
	}
	if lp2.LinkID != linkID {
		t.Fatal("link ID mismatch")
	}
	if !lp2.Verify(id.SigningPublicKey()) {
		t.Fatal("link proof signature verification failed")
	}
}

func TestComputeLinkID(t *testing.T) {
	ephKey, _ := ecdh.X25519().GenerateKey(rand.Reader)
	lr := &LinkRequest{EphemeralPub: ephKey.PublicKey()}
	id1 := lr.ComputeLinkID()
	id2 := lr.ComputeLinkID()
	if id1 != id2 {
		t.Fatal("same request should produce same link ID")
	}
}

func TestDeriveSymKeys_HKDF(t *testing.T) {
	secret := make([]byte, 32)
	rand.Read(secret)
	var linkID [LinkIDLen]byte
	rand.Read(linkID[:])

	enc1, hmac1, enc2, hmac2 := DeriveSymKeys(secret, linkID)
	if len(enc1) != SymKeyLen || len(hmac1) != HMACLen || len(enc2) != SymKeyLen || len(hmac2) != HMACLen {
		t.Fatalf("key lengths: %d, %d, %d, %d", len(enc1), len(hmac1), len(enc2), len(hmac2))
	}

	// All four keys should be distinct
	keys := [][]byte{enc1, hmac1, enc2, hmac2}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if bytes.Equal(keys[i], keys[j]) {
				t.Fatalf("keys[%d] and keys[%d] should differ", i, j)
			}
		}
	}

	// Deterministic
	e1b, h1b, e2b, h2b := DeriveSymKeys(secret, linkID)
	if !bytes.Equal(enc1, e1b) || !bytes.Equal(hmac1, h1b) || !bytes.Equal(enc2, e2b) || !bytes.Equal(hmac2, h2b) {
		t.Fatal("key derivation not deterministic")
	}
}

func TestCBCHMACEncryptDecrypt(t *testing.T) {
	encKey := make([]byte, SymKeyLen)
	hmacKey := make([]byte, HMACLen)
	rand.Read(encKey)
	rand.Read(hmacKey)

	plaintext := []byte("hello reticulum link")
	ct, err := CBCHMACEncrypt(encKey, hmacKey, plaintext)
	if err != nil {
		t.Fatal(err)
	}

	pt, err := CBCHMACDecrypt(encKey, hmacKey, ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pt, plaintext) {
		t.Fatalf("decrypted: got %q, want %q", pt, plaintext)
	}
}

func TestCBCHMACDecrypt_TamperedHMAC(t *testing.T) {
	encKey := make([]byte, SymKeyLen)
	hmacKey := make([]byte, HMACLen)
	rand.Read(encKey)
	rand.Read(hmacKey)

	ct, _ := CBCHMACEncrypt(encKey, hmacKey, []byte("test"))
	ct[len(ct)-1] ^= 0xff // tamper with HMAC
	_, err := CBCHMACDecrypt(encKey, hmacKey, ct)
	if err == nil {
		t.Fatal("should reject tampered HMAC")
	}
}

func TestCBCHMACDecrypt_TooShort(t *testing.T) {
	encKey := make([]byte, SymKeyLen)
	hmacKey := make([]byte, HMACLen)
	_, err := CBCHMACDecrypt(encKey, hmacKey, make([]byte, 10))
	if err == nil {
		t.Fatal("should reject short ciphertext")
	}
}

func TestConfirmationRoundTrip(t *testing.T) {
	var dest [DestHashLen]byte
	rand.Read(dest[:])
	plaintext := []byte("secret message")
	payloadHash := sha256.Sum256(plaintext)

	_, sigKey, _ := ed25519.GenerateKey(rand.Reader)
	signable := make([]byte, DestHashLen+32)
	copy(signable, dest[:])
	copy(signable[DestHashLen:], payloadHash[:])
	sig := ed25519.Sign(sigKey, signable)

	dc := &DeliveryConfirmation{
		DestHash:    dest,
		PayloadHash: payloadHash,
		Signature:   sig,
	}

	wire := dc.Marshal()
	if len(wire) != ConfirmationMinLen {
		t.Fatalf("wire len: got %d, want %d", len(wire), ConfirmationMinLen)
	}

	dc2, err := UnmarshalConfirmation(wire)
	if err != nil {
		t.Fatal(err)
	}
	if dc2.DestHash != dest {
		t.Fatal("dest hash mismatch")
	}

	sigPub := sigKey.Public().(ed25519.PublicKey)
	if !dc2.Verify(sigPub) {
		t.Fatal("confirmation verification failed")
	}
	if !dc2.VerifyWithPlaintext(sigPub, plaintext) {
		t.Fatal("confirmation plaintext verification failed")
	}
	if dc2.VerifyWithPlaintext(sigPub, []byte("wrong")) {
		t.Fatal("should fail with wrong plaintext")
	}
}

func TestConfirmationPacketRoundTrip(t *testing.T) {
	var dest [DestHashLen]byte
	rand.Read(dest[:])
	var ph [32]byte
	rand.Read(ph[:])

	_, sigKey, _ := ed25519.GenerateKey(rand.Reader)
	signable := make([]byte, DestHashLen+32)
	copy(signable, dest[:])
	copy(signable[DestHashLen:], ph[:])

	cp := &ConfirmationPacket{
		MsgRef: 42,
		Confirmation: &DeliveryConfirmation{
			DestHash:    dest,
			PayloadHash: ph,
			Signature:   ed25519.Sign(sigKey, signable),
		},
	}

	wire := cp.Marshal()
	cp2, err := UnmarshalConfirmationPacket(wire)
	if err != nil {
		t.Fatal(err)
	}
	if cp2.MsgRef != 42 {
		t.Fatalf("msg ref: got %d, want 42", cp2.MsgRef)
	}
}

func TestKeepaliveRoundTrip(t *testing.T) {
	var linkID [LinkIDLen]byte
	rand.Read(linkID[:])

	kp := &KeepalivePacket{LinkID: linkID, Random: 0xAB}
	wire := kp.Marshal()
	if len(wire) != 1+KeepalivePacketLen {
		t.Fatalf("wire len: got %d, want %d", len(wire), 1+KeepalivePacketLen)
	}

	kp2, err := UnmarshalKeepalive(wire)
	if err != nil {
		t.Fatal(err)
	}
	if kp2.LinkID != linkID {
		t.Fatal("link ID mismatch")
	}
	if kp2.Random != 0xAB {
		t.Fatalf("random: got %d, want 0xAB", kp2.Random)
	}
}

func TestKeepaliveWrongType(t *testing.T) {
	data := make([]byte, 1+KeepalivePacketLen)
	data[0] = 0xFF
	_, err := UnmarshalKeepalive(data)
	if err != ErrWrongType {
		t.Fatalf("expected ErrWrongType, got %v", err)
	}
}

func TestMaxLinksForBandwidth(t *testing.T) {
	n := MaxLinksForBandwidth(1200, 2.0)
	if n != 53 {
		t.Fatalf("max links: got %d, want 53", n)
	}
	if MaxLinksForBandwidth(0, 2.0) != 0 {
		t.Fatal("zero bandwidth should return 0")
	}
	if MaxLinksForBandwidth(1200, 0) != 0 {
		t.Fatal("zero capacity should return 0")
	}
}

func TestUnmarshalShortPackets(t *testing.T) {
	if _, err := UnmarshalLinkRequest(nil); err == nil {
		t.Fatal("nil link request should error")
	}
	if _, err := UnmarshalLinkProof(nil); err == nil {
		t.Fatal("nil link proof should error")
	}
	if _, err := UnmarshalKeepalive(nil); err == nil {
		t.Fatal("nil keepalive should error")
	}
	if _, err := UnmarshalConfirmation(nil); err == nil {
		t.Fatal("nil confirmation should error")
	}
	if _, err := UnmarshalConfirmationPacket(nil); err == nil {
		t.Fatal("nil confirmation packet should error")
	}
}
