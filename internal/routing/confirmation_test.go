package routing

import (
	"crypto/sha256"
	"testing"
)

func TestDeliveryConfirmation_Roundtrip(t *testing.T) {
	id := testIdentity(t)
	plaintext := []byte("secret message content")

	dc := NewDeliveryConfirmation(id, plaintext)

	data := dc.Marshal()
	parsed, err := UnmarshalConfirmation(data)
	if err != nil {
		t.Fatal("unmarshal:", err)
	}

	if parsed.DestHash != dc.DestHash {
		t.Error("dest hash mismatch")
	}
	if parsed.PayloadHash != dc.PayloadHash {
		t.Error("payload hash mismatch")
	}
}

func TestDeliveryConfirmation_Verify(t *testing.T) {
	id := testIdentity(t)
	plaintext := []byte("hello world")

	dc := NewDeliveryConfirmation(id, plaintext)

	if !dc.Verify(id.SigningPublicKey()) {
		t.Fatal("valid confirmation should verify")
	}
}

func TestDeliveryConfirmation_VerifyWithPlaintext(t *testing.T) {
	id := testIdentity(t)
	plaintext := []byte("hello world")

	dc := NewDeliveryConfirmation(id, plaintext)

	if !dc.VerifyWithPlaintext(id.SigningPublicKey(), plaintext) {
		t.Fatal("valid confirmation with matching plaintext should verify")
	}

	if dc.VerifyWithPlaintext(id.SigningPublicKey(), []byte("wrong plaintext")) {
		t.Fatal("confirmation with wrong plaintext should not verify")
	}
}

func TestDeliveryConfirmation_VerifyWrongKey(t *testing.T) {
	id1 := testIdentity(t)
	id2 := testIdentity(t)
	plaintext := []byte("test")

	dc := NewDeliveryConfirmation(id1, plaintext)

	if dc.Verify(id2.SigningPublicKey()) {
		t.Fatal("confirmation should not verify with wrong key")
	}
}

func TestDeliveryConfirmation_TamperedHash(t *testing.T) {
	id := testIdentity(t)
	dc := NewDeliveryConfirmation(id, []byte("test"))

	dc.PayloadHash[0] ^= 0xff // tamper
	if dc.Verify(id.SigningPublicKey()) {
		t.Fatal("tampered payload hash should not verify")
	}
}

func TestDeliveryConfirmation_PayloadHash(t *testing.T) {
	id := testIdentity(t)
	plaintext := []byte("deterministic")

	dc := NewDeliveryConfirmation(id, plaintext)
	expected := sha256.Sum256(plaintext)

	if dc.PayloadHash != expected {
		t.Fatal("payload hash should be SHA-256 of plaintext")
	}
}

func TestUnmarshalConfirmation_TooShort(t *testing.T) {
	_, err := UnmarshalConfirmation([]byte{0x01, 0x02})
	if err == nil {
		t.Fatal("should fail on short data")
	}
}

func TestConfirmationPacket_Roundtrip(t *testing.T) {
	id := testIdentity(t)
	dc := NewDeliveryConfirmation(id, []byte("test"))

	cp := &ConfirmationPacket{
		MsgRef:       42,
		Confirmation: dc,
	}

	data := cp.Marshal()
	parsed, err := UnmarshalConfirmationPacket(data)
	if err != nil {
		t.Fatal("unmarshal:", err)
	}

	if parsed.MsgRef != 42 {
		t.Errorf("msg ref: got %d, want 42", parsed.MsgRef)
	}
	if parsed.Confirmation.DestHash != dc.DestHash {
		t.Error("confirmation dest hash mismatch")
	}
	if !parsed.Confirmation.Verify(id.SigningPublicKey()) {
		t.Fatal("parsed confirmation should verify")
	}
}
