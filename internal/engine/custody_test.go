package engine

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func TestCustodyOfferMarshalRoundtrip(t *testing.T) {
	offer := &CustodyOffer{
		DeliveryID: 42,
		Payload:    []byte("hello custody transfer"),
	}
	if _, err := rand.Read(offer.CustodyID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(offer.SourceHash[:]); err != nil {
		t.Fatal(err)
	}

	data := MarshalCustodyOffer(offer)

	// Verify type byte
	if data[0] != CustodyOfferType {
		t.Fatalf("expected type 0x%02x, got 0x%02x", CustodyOfferType, data[0])
	}

	// Unmarshal and compare
	got, err := UnmarshalCustodyOffer(data)
	if err != nil {
		t.Fatalf("UnmarshalCustodyOffer: %v", err)
	}
	if got.CustodyID != offer.CustodyID {
		t.Fatal("CustodyID mismatch")
	}
	if got.SourceHash != offer.SourceHash {
		t.Fatal("SourceHash mismatch")
	}
	if got.DeliveryID != offer.DeliveryID {
		t.Fatalf("DeliveryID mismatch: got %d, want %d", got.DeliveryID, offer.DeliveryID)
	}
	if !bytes.Equal(got.Payload, offer.Payload) {
		t.Fatalf("Payload mismatch: got %q, want %q", got.Payload, offer.Payload)
	}
}

func TestCustodyOfferEmptyPayload(t *testing.T) {
	offer := &CustodyOffer{
		DeliveryID: 1,
	}
	if _, err := rand.Read(offer.CustodyID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(offer.SourceHash[:]); err != nil {
		t.Fatal(err)
	}

	data := MarshalCustodyOffer(offer)
	if len(data) != custodyOfferHeaderLen {
		t.Fatalf("expected %d bytes for empty payload, got %d", custodyOfferHeaderLen, len(data))
	}

	got, err := UnmarshalCustodyOffer(data)
	if err != nil {
		t.Fatalf("UnmarshalCustodyOffer: %v", err)
	}
	if len(got.Payload) != 0 {
		t.Fatalf("expected empty payload, got %d bytes", len(got.Payload))
	}
}

func TestCustodyOfferTooShort(t *testing.T) {
	_, err := UnmarshalCustodyOffer([]byte{0x16, 0x00})
	if err == nil {
		t.Fatal("expected error for too-short data")
	}
}

func TestCustodyOfferWrongType(t *testing.T) {
	data := make([]byte, custodyOfferHeaderLen)
	data[0] = 0xFF // wrong type
	_, err := UnmarshalCustodyOffer(data)
	if err == nil {
		t.Fatal("expected error for wrong type byte")
	}
}

func TestCustodyACKMarshalRoundtrip(t *testing.T) {
	ack := &CustodyACK{}
	if _, err := rand.Read(ack.CustodyID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(ack.AcceptorHash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(ack.Signature[:]); err != nil {
		t.Fatal(err)
	}

	data := MarshalCustodyACK(ack)

	// Verify type byte
	if data[0] != CustodyACKType {
		t.Fatalf("expected type 0x%02x, got 0x%02x", CustodyACKType, data[0])
	}
	if len(data) != custodyACKLen {
		t.Fatalf("expected %d bytes, got %d", custodyACKLen, len(data))
	}

	got, err := UnmarshalCustodyACK(data)
	if err != nil {
		t.Fatalf("UnmarshalCustodyACK: %v", err)
	}
	if got.CustodyID != ack.CustodyID {
		t.Fatal("CustodyID mismatch")
	}
	if got.AcceptorHash != ack.AcceptorHash {
		t.Fatal("AcceptorHash mismatch")
	}
	if got.Signature != ack.Signature {
		t.Fatal("Signature mismatch")
	}
}

func TestCustodyACKTooShort(t *testing.T) {
	_, err := UnmarshalCustodyACK([]byte{0x17, 0x00})
	if err == nil {
		t.Fatal("expected error for too-short data")
	}
}

func TestCustodyACKWrongType(t *testing.T) {
	data := make([]byte, custodyACKLen)
	data[0] = 0xFF
	_, err := UnmarshalCustodyACK(data)
	if err == nil {
		t.Fatal("expected error for wrong type byte")
	}
}

func TestCustodySignAndVerify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	var custodyID, acceptorHash [16]byte
	if _, err := rand.Read(custodyID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(acceptorHash[:]); err != nil {
		t.Fatal(err)
	}

	ack := SignCustodyACK(custodyID, acceptorHash, priv)

	// Verify the signature
	if !VerifyCustodyACK(ack, pub) {
		t.Fatal("signature verification failed with correct key")
	}

	// Verify fields
	if ack.CustodyID != custodyID {
		t.Fatal("CustodyID mismatch")
	}
	if ack.AcceptorHash != acceptorHash {
		t.Fatal("AcceptorHash mismatch")
	}
}

func TestCustodySignVerifyWrongKey(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// Generate a different key pair for verification
	wrongPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	var custodyID, acceptorHash [16]byte
	if _, err := rand.Read(custodyID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(acceptorHash[:]); err != nil {
		t.Fatal(err)
	}

	ack := SignCustodyACK(custodyID, acceptorHash, priv)

	if VerifyCustodyACK(ack, wrongPub) {
		t.Fatal("signature should not verify with wrong key")
	}
}

func TestCustodySignVerifyTampered(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	var custodyID, acceptorHash [16]byte
	if _, err := rand.Read(custodyID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(acceptorHash[:]); err != nil {
		t.Fatal(err)
	}

	ack := SignCustodyACK(custodyID, acceptorHash, priv)

	// Tamper with the acceptor hash
	ack.AcceptorHash[0] ^= 0xFF

	if VerifyCustodyACK(ack, pub) {
		t.Fatal("signature should not verify after tampering with AcceptorHash")
	}
}

func TestCustodyACKMarshalVerifyRoundtrip(t *testing.T) {
	// Sign, marshal, unmarshal, verify — full wire roundtrip
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	var custodyID, acceptorHash [16]byte
	if _, err := rand.Read(custodyID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(acceptorHash[:]); err != nil {
		t.Fatal(err)
	}

	ack := SignCustodyACK(custodyID, acceptorHash, priv)
	wire := MarshalCustodyACK(ack)
	parsed, err := UnmarshalCustodyACK(wire)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !VerifyCustodyACK(parsed, pub) {
		t.Fatal("signature verification failed after wire roundtrip")
	}
}

func TestIsCustodyOffer(t *testing.T) {
	offer := &CustodyOffer{DeliveryID: 1, Payload: []byte("test")}
	if _, err := rand.Read(offer.CustodyID[:]); err != nil {
		t.Fatal(err)
	}
	data := MarshalCustodyOffer(offer)
	if !IsCustodyOffer(data) {
		t.Fatal("IsCustodyOffer returned false for valid offer")
	}
	if IsCustodyOffer([]byte{0x17}) {
		t.Fatal("IsCustodyOffer returned true for ACK type")
	}
	if IsCustodyOffer(nil) {
		t.Fatal("IsCustodyOffer returned true for nil")
	}
}

func TestIsCustodyACK(t *testing.T) {
	ack := &CustodyACK{}
	if _, err := rand.Read(ack.CustodyID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(ack.Signature[:]); err != nil {
		t.Fatal(err)
	}
	data := MarshalCustodyACK(ack)
	if !IsCustodyACK(data) {
		t.Fatal("IsCustodyACK returned false for valid ACK")
	}
	if IsCustodyACK([]byte{0x16}) {
		t.Fatal("IsCustodyACK returned true for offer type")
	}
	if IsCustodyACK(nil) {
		t.Fatal("IsCustodyACK returned true for nil")
	}
}

func TestNewCustodyID(t *testing.T) {
	id1, err := NewCustodyID()
	if err != nil {
		t.Fatal(err)
	}
	id2, err := NewCustodyID()
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatal("two generated IDs should not be identical")
	}
	// Check UUID v4 bits
	if (id1[6] & 0xf0) != 0x40 {
		t.Fatalf("version nibble should be 4, got 0x%02x", id1[6]>>4)
	}
	if (id1[8] & 0xc0) != 0x80 {
		t.Fatalf("variant bits should be 10xx, got 0x%02x", id1[8]&0xc0)
	}
}

func TestCustodyManagerRegisterAndACK(t *testing.T) {
	cm := NewCustodyManager(5 * time.Second)

	offer := &CustodyOffer{DeliveryID: 100}
	if _, err := rand.Read(offer.CustodyID[:]); err != nil {
		t.Fatal(err)
	}

	ch := cm.RegisterOffer(offer)
	if cm.PendingCount() != 1 {
		t.Fatalf("expected 1 pending, got %d", cm.PendingCount())
	}

	ack := &CustodyACK{CustodyID: offer.CustodyID}
	if _, err := rand.Read(ack.AcceptorHash[:]); err != nil {
		t.Fatal(err)
	}

	if !cm.HandleACK(ack) {
		t.Fatal("HandleACK returned false for matching ACK")
	}

	// Channel should deliver the ACK
	select {
	case got := <-ch:
		if got.CustodyID != ack.CustodyID {
			t.Fatal("received ACK has wrong CustodyID")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ACK on channel")
	}
}

func TestCustodyManagerUnknownACK(t *testing.T) {
	cm := NewCustodyManager(5 * time.Second)

	ack := &CustodyACK{}
	if _, err := rand.Read(ack.CustodyID[:]); err != nil {
		t.Fatal(err)
	}

	if cm.HandleACK(ack) {
		t.Fatal("HandleACK should return false for unknown CustodyID")
	}
}

func TestCustodyManagerReap(t *testing.T) {
	cm := NewCustodyManager(10 * time.Millisecond)

	offer := &CustodyOffer{DeliveryID: 200}
	if _, err := rand.Read(offer.CustodyID[:]); err != nil {
		t.Fatal(err)
	}

	ch := cm.RegisterOffer(offer)
	if cm.PendingCount() != 1 {
		t.Fatalf("expected 1 pending, got %d", cm.PendingCount())
	}

	// Wait for timeout
	time.Sleep(50 * time.Millisecond)

	reaped := cm.Reap()
	if reaped != 1 {
		t.Fatalf("expected 1 reaped, got %d", reaped)
	}
	if cm.PendingCount() != 0 {
		t.Fatalf("expected 0 pending after reap, got %d", cm.PendingCount())
	}

	// Channel should be closed (no ACK)
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("channel should be closed without delivering ACK")
		}
	case <-time.After(time.Second):
		t.Fatal("channel should be closed after reap")
	}
}

func TestCustodyManagerDuplicateACK(t *testing.T) {
	cm := NewCustodyManager(5 * time.Second)

	offer := &CustodyOffer{DeliveryID: 300}
	if _, err := rand.Read(offer.CustodyID[:]); err != nil {
		t.Fatal(err)
	}

	cm.RegisterOffer(offer)

	ack := &CustodyACK{CustodyID: offer.CustodyID}
	if !cm.HandleACK(ack) {
		t.Fatal("first ACK should match")
	}
	if cm.HandleACK(ack) {
		t.Fatal("second ACK should not match (already accepted)")
	}
}

func TestCustodyStateString(t *testing.T) {
	tests := []struct {
		state CustodyState
		want  string
	}{
		{CustodyOffered, "offered"},
		{CustodyAccepted, "accepted"},
		{CustodyExpired, "expired"},
		{CustodyState(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("CustodyState(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}
