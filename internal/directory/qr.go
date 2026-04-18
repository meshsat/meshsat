package directory

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// QR contact-card protocol: meshsat://contact/<base64url(payload)>
// where payload is a compact JSON object carrying the operator's
// display name, addresses, optional SIDC, signer pubkey, and an
// Ed25519 signature over the canonical JSON of the `contact`
// inner object. Long-lived (no TTL) so the QR stays scannable for
// months; trust decisions are layered on top of this by MESHSAT-560
// (trust level + verified_at). [MESHSAT-561]
//
// We keep this hand-rolled instead of pulling in a CBOR library:
// the wire is <500 bytes for a typical contact with 2-3 addresses
// which fits comfortably in a version-10 QR code at ECC L, and the
// JSON path lines up with how every other signed artefact in the
// bridge is encoded (audit log, directory snapshot).

type QRAddress struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

type QRContact struct {
	ID          string      `json:"id"`
	DisplayName string      `json:"display_name"`
	SIDC        string      `json:"sidc,omitempty"`
	Team        string      `json:"team,omitempty"`
	Role        string      `json:"role,omitempty"`
	Org         string      `json:"org,omitempty"`
	Addresses   []QRAddress `json:"addresses,omitempty"`
}

type QRCard struct {
	V       int       `json:"v"`
	Contact QRContact `json:"contact"`
	Signer  string    `json:"signer"`   // hex-encoded Ed25519 pubkey
	Sig     string    `json:"sig"`      // base64 std
	IssuedAt string   `json:"issued_at,omitempty"`
}

// BuildQRCard serialises + signs the contact card. signer is a
// closure the caller provides so we don't leak the SigningService
// type into this package (avoids an import cycle with engine).
//
// Returns: the signed JSON blob, the meshsat:// URL, and any error.
func BuildQRCard(c QRContact, signerHex string, sign func([]byte) []byte) ([]byte, string, error) {
	if c.ID == "" {
		return nil, "", errors.New("contact id is empty")
	}
	// Canonical JSON of the inner contact — this is what the
	// signature covers. encoding/json produces deterministic output
	// for map-less structs, so we can treat marshal output as canon.
	inner, err := json.Marshal(c)
	if err != nil {
		return nil, "", fmt.Errorf("marshal contact: %w", err)
	}
	sig := sign(inner)
	if len(sig) == 0 {
		return nil, "", errors.New("signer produced empty signature")
	}
	card := QRCard{
		V:       1,
		Contact: c,
		Signer:  signerHex,
		Sig:     base64.StdEncoding.EncodeToString(sig),
	}
	buf, err := json.Marshal(card)
	if err != nil {
		return nil, "", fmt.Errorf("marshal card: %w", err)
	}
	url := "meshsat://contact/" + base64.RawURLEncoding.EncodeToString(buf)
	return buf, url, nil
}

// ParseQRCard decodes and verifies a meshsat://contact/... URL or a
// raw card JSON blob. Returns the parsed contact on verified
// success; returns an error that distinguishes decode failures from
// signature failures so callers can surface a tamper warning.
func ParseQRCard(input string) (*QRCard, error) {
	raw := []byte(input)
	// Strip the URL prefix if present.
	const prefix = "meshsat://contact/"
	if len(input) > len(prefix) && input[:len(prefix)] == prefix {
		decoded, err := base64.RawURLEncoding.DecodeString(input[len(prefix):])
		if err != nil {
			return nil, fmt.Errorf("decode URL payload: %w", err)
		}
		raw = decoded
	}
	var card QRCard
	if err := json.Unmarshal(raw, &card); err != nil {
		return nil, fmt.Errorf("parse card JSON: %w", err)
	}
	if card.V != 1 {
		return nil, fmt.Errorf("unsupported card version %d", card.V)
	}
	inner, err := json.Marshal(card.Contact)
	if err != nil {
		return nil, fmt.Errorf("re-marshal contact: %w", err)
	}
	pubKeyBytes, err := hex.DecodeString(card.Signer)
	if err != nil || len(pubKeyBytes) != ed25519.PublicKeySize {
		return nil, errors.New("invalid signer public key")
	}
	sigBytes, err := base64.StdEncoding.DecodeString(card.Sig)
	if err != nil || len(sigBytes) != ed25519.SignatureSize {
		return nil, errors.New("invalid signature encoding")
	}
	if !ed25519.Verify(pubKeyBytes, inner, sigBytes) {
		return nil, errors.New("signature verification failed — card is forged or corrupt")
	}
	return &card, nil
}
