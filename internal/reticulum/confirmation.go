package reticulum

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
)

// Confirmation wire format sizes.
const (
	// ConfirmationMinLen: dest_hash(16) + payload_hash(32) + signature(64) = 112.
	ConfirmationMinLen = DestHashLen + 32 + SignatureLen
)

// DeliveryConfirmation is an unforgeable proof that a destination received and
// decrypted a message. The destination computes SHA-256 of the decrypted
// plaintext and signs the hash with its Ed25519 key.
type DeliveryConfirmation struct {
	DestHash    [DestHashLen]byte // destination that received the message
	PayloadHash [32]byte          // SHA-256 of decrypted plaintext
	Signature   []byte            // Ed25519 signature over (dest_hash + payload_hash)
}

// Marshal serializes the confirmation to wire format.
func (dc *DeliveryConfirmation) Marshal() []byte {
	buf := make([]byte, 0, ConfirmationMinLen)
	buf = append(buf, dc.DestHash[:]...)
	buf = append(buf, dc.PayloadHash[:]...)
	buf = append(buf, dc.Signature...)
	return buf
}

// UnmarshalConfirmation parses a wire-format delivery confirmation.
func UnmarshalConfirmation(data []byte) (*DeliveryConfirmation, error) {
	if len(data) < ConfirmationMinLen {
		return nil, ErrTooShort
	}

	dc := &DeliveryConfirmation{}
	pos := 0

	copy(dc.DestHash[:], data[pos:pos+DestHashLen])
	pos += DestHashLen

	copy(dc.PayloadHash[:], data[pos:pos+32])
	pos += 32

	dc.Signature = make([]byte, SignatureLen)
	copy(dc.Signature, data[pos:pos+SignatureLen])

	return dc, nil
}

// Verify checks the confirmation signature against the destination's known
// signing public key.
func (dc *DeliveryConfirmation) Verify(signingPub ed25519.PublicKey) bool {
	signable := dc.signableBody()
	return VerifySignature(signingPub, signable, dc.Signature)
}

// VerifyWithPlaintext verifies the confirmation AND checks that the payload
// hash matches the expected plaintext.
func (dc *DeliveryConfirmation) VerifyWithPlaintext(signingPub ed25519.PublicKey, expectedPlaintext []byte) bool {
	if !dc.Verify(signingPub) {
		return false
	}
	expected := sha256.Sum256(expectedPlaintext)
	return dc.PayloadHash == expected
}

func (dc *DeliveryConfirmation) signableBody() []byte {
	buf := make([]byte, DestHashLen+32)
	copy(buf, dc.DestHash[:])
	copy(buf[DestHashLen:], dc.PayloadHash[:])
	return buf
}

// ConfirmationPacket wraps a delivery confirmation with a message reference
// for sender-side correlation.
type ConfirmationPacket struct {
	MsgRef       uint64 // message reference ID
	Confirmation *DeliveryConfirmation
}

// Marshal serializes a confirmation packet with msg reference.
func (cp *ConfirmationPacket) Marshal() []byte {
	confData := cp.Confirmation.Marshal()
	buf := make([]byte, 8+len(confData))
	binary.BigEndian.PutUint64(buf[:8], cp.MsgRef)
	copy(buf[8:], confData)
	return buf
}

// UnmarshalConfirmationPacket parses a confirmation packet with msg reference.
func UnmarshalConfirmationPacket(data []byte) (*ConfirmationPacket, error) {
	if len(data) < 8+ConfirmationMinLen {
		return nil, ErrTooShort
	}
	cp := &ConfirmationPacket{
		MsgRef: binary.BigEndian.Uint64(data[:8]),
	}
	conf, err := UnmarshalConfirmation(data[8:])
	if err != nil {
		return nil, err
	}
	cp.Confirmation = conf
	return cp, nil
}
