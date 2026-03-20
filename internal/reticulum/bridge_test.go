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

func TestLinkResponseRoundTrip(t *testing.T) {
	id, _ := GenerateIdentity()
	var linkID [LinkIDLen]byte
	rand.Read(linkID[:])

	ephKey, _ := ecdh.X25519().GenerateKey(rand.Reader)
	signable := LinkResponseSignable(linkID, ephKey.PublicKey())
	sig := id.Sign(signable)

	resp := &LinkResponse{
		LinkID:       linkID,
		EphemeralPub: ephKey.PublicKey(),
		Signature:    sig,
	}

	wire := resp.Marshal()
	if len(wire) != LinkResponseLen {
		t.Fatalf("wire len: got %d, want %d", len(wire), LinkResponseLen)
	}

	resp2, err := UnmarshalLinkResponse(wire)
	if err != nil {
		t.Fatal(err)
	}
	if resp2.LinkID != linkID {
		t.Fatal("link ID mismatch")
	}
	if !resp2.Verify(id.SigningPublicKey()) {
		t.Fatal("link response signature verification failed")
	}
}

func TestLinkConfirmRoundTrip(t *testing.T) {
	var linkID [LinkIDLen]byte
	rand.Read(linkID[:])
	secret := make([]byte, 32)
	rand.Read(secret)

	proof := ComputeConfirmProof(secret, linkID)
	lc := &LinkConfirm{LinkID: linkID, Proof: proof}

	wire := lc.Marshal()
	if len(wire) != LinkConfirmLen {
		t.Fatalf("wire len: got %d, want %d", len(wire), LinkConfirmLen)
	}

	lc2, err := UnmarshalLinkConfirm(wire)
	if err != nil {
		t.Fatal(err)
	}
	if lc2.LinkID != linkID {
		t.Fatal("link ID mismatch")
	}
	if lc2.Proof != proof {
		t.Fatal("proof mismatch")
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

func TestDeriveSymKeys(t *testing.T) {
	secret := make([]byte, 32)
	rand.Read(secret)
	var linkID [LinkIDLen]byte
	rand.Read(linkID[:])

	k1, k2 := DeriveSymKeys(secret, linkID)
	if len(k1) != SymKeyLen || len(k2) != SymKeyLen {
		t.Fatalf("key lengths: %d, %d", len(k1), len(k2))
	}
	if bytes.Equal(k1, k2) {
		t.Fatal("key1 and key2 should differ")
	}

	k1b, k2b := DeriveSymKeys(secret, linkID)
	if !bytes.Equal(k1, k1b) || !bytes.Equal(k2, k2b) {
		t.Fatal("key derivation not deterministic")
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
	if _, err := UnmarshalLinkResponse(nil); err == nil {
		t.Fatal("nil link response should error")
	}
	if _, err := UnmarshalLinkConfirm(nil); err == nil {
		t.Fatal("nil link confirm should error")
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
