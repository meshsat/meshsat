package routing

import (
	"crypto/sha256"

	"meshsat/internal/reticulum"
)

// Confirmation wire format constants — re-exported from reticulum.
const (
	ConfirmationMinLen = reticulum.ConfirmationMinLen // 112 bytes
)

// Type aliases for wire format types — delegate to reticulum package.
type (
	DeliveryConfirmation = reticulum.DeliveryConfirmation
	ConfirmationPacket   = reticulum.ConfirmationPacket
)

// Wire format functions — delegate to reticulum package.
var (
	UnmarshalConfirmation       = reticulum.UnmarshalConfirmation
	UnmarshalConfirmationPacket = reticulum.UnmarshalConfirmationPacket
)

// NewDeliveryConfirmation creates an unforgeable delivery confirmation.
// The caller provides the decrypted plaintext; the function hashes it and signs.
func NewDeliveryConfirmation(id *Identity, plaintext []byte) *DeliveryConfirmation {
	payloadHash := sha256.Sum256(plaintext)
	dc := &DeliveryConfirmation{
		DestHash:    id.DestHash(),
		PayloadHash: payloadHash,
	}

	// Sign: dest_hash || payload_hash
	signable := make([]byte, DestHashLen+32)
	copy(signable, dc.DestHash[:])
	copy(signable[DestHashLen:], dc.PayloadHash[:])
	dc.Signature = id.Sign(signable)
	return dc
}
