package routing

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// Confirmation wire format constants.
const (
	// ConfirmationMinLen: dest_hash(16) + payload_hash(32) + signature(64) = 112 bytes.
	ConfirmationMinLen = DestHashLen + 32 + ed25519.SignatureSize
)

// DeliveryConfirmation is an unforgeable proof that a destination received and
// decrypted a message. The destination computes SHA-256 of the decrypted
// plaintext and signs the hash with its Ed25519 key. No intermediary can forge
// this without both the decryption key and the signing key.
type DeliveryConfirmation struct {
	DestHash    [DestHashLen]byte // destination that received the message
	PayloadHash [32]byte          // SHA-256 of decrypted plaintext
	Signature   []byte            // Ed25519 signature over (dest_hash + payload_hash)
}

// NewDeliveryConfirmation creates an unforgeable delivery confirmation.
// The caller provides the decrypted plaintext; the function hashes it and signs.
func NewDeliveryConfirmation(id *Identity, plaintext []byte) *DeliveryConfirmation {
	payloadHash := sha256.Sum256(plaintext)
	dc := &DeliveryConfirmation{
		DestHash:    id.DestHash(),
		PayloadHash: payloadHash,
	}

	// Sign: dest_hash || payload_hash
	signable := dc.signableBody()
	dc.Signature = id.Sign(signable)
	return dc
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
		return nil, errors.New("confirmation too short")
	}

	dc := &DeliveryConfirmation{}
	pos := 0

	copy(dc.DestHash[:], data[pos:pos+DestHashLen])
	pos += DestHashLen

	copy(dc.PayloadHash[:], data[pos:pos+32])
	pos += 32

	dc.Signature = make([]byte, ed25519.SignatureSize)
	copy(dc.Signature, data[pos:pos+ed25519.SignatureSize])

	return dc, nil
}

// Verify checks the confirmation signature against the destination's known
// signing public key. Returns true if the confirmation is valid.
func (dc *DeliveryConfirmation) Verify(signingPub ed25519.PublicKey) bool {
	if len(signingPub) != ed25519.PublicKeySize {
		return false
	}

	// Verify that dest_hash matches this public key (requires encryption pub too).
	// If we only have signing pub, we skip the dest_hash cross-check and rely
	// on the caller to have looked up the correct key from the destination table.

	signable := dc.signableBody()
	return ed25519.Verify(signingPub, signable, dc.Signature)
}

// VerifyWithPlaintext verifies the confirmation AND checks that the payload
// hash matches the expected plaintext. This is the full verification path used
// by the sender to confirm delivery.
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
// so the sender can correlate it to the original message.
type ConfirmationPacket struct {
	MsgRef       uint64 // message reference ID for correlation
	Confirmation *DeliveryConfirmation
}

// MarshalConfirmationPacket serializes a confirmation packet with msg reference.
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
		return nil, errors.New("confirmation packet too short")
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
