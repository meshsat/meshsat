// Package pair implements the bridge side of the pair-mode protocol.
// [MESHSAT-594 / MESHSAT-598]
//
// A pair-mode row is armed from the touch-display UI with a 6-digit
// PIN + a random 32-byte pairing key; a remote device (browser,
// Android, CLI) discovers the bridge, posts /api/v2/pair/claim with
// the PIN + its own generated Ed25519 public key; the bridge
// derives a shared secret (HKDF(pairing_key || pin)), verifies the
// HMAC the client attached, signs a leaf cert (if the internal CA
// is configured, MESHSAT-595) and responds with the cert + a
// client_id. From that point the client mints JWTs signed by its
// Ed25519 private key; the bridge's middleware verifies them
// against paired_clients.public_key.
//
// This package deliberately does NOT pull in CBOR (the issue spec
// mentions CBOR but we've been consistent with JSON across every
// signed artefact in the repo; see directory/qr.go). The wire shape
// is small enough that JSON overhead doesn't matter.

package pair

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/hkdf"
)

// PinLength is fixed at 6 digits — memorable but large enough (1M
// combinations) that the 90s TTL defeats brute force.
const PinLength = 6

// DefaultArmTTL is how long a pair-mode row stays armed before the
// bridge rejects claims. Operator taps "Arm pair mode", has 90
// seconds to enter the PIN on the remote device, done.
const DefaultArmTTL = 90 * time.Second

// JWTTTL is how long an issued JWT stays valid. Clients refresh via
// /api/v2/pair/refresh before expiry.
const JWTTTL = 1 * time.Hour

// GeneratePIN returns a zero-padded 6-digit string from crypto/rand.
func GeneratePIN() (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	n := uint32(buf[0])<<24 | uint32(buf[1])<<16 | uint32(buf[2])<<8 | uint32(buf[3])
	return fmt.Sprintf("%06d", n%1_000_000), nil
}

// GeneratePairingKey returns a 32-byte random key as hex.
func GeneratePairingKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// DeriveSharedSecret HKDF-SHA256(pairing_key, pin) → 32 bytes. Both
// sides derive the same value; the client uses it to HMAC its
// public key before sending, the bridge verifies.
func DeriveSharedSecret(pairingKeyHex, pin string) ([]byte, error) {
	pk, err := hex.DecodeString(pairingKeyHex)
	if err != nil {
		return nil, fmt.Errorf("pairing key: %w", err)
	}
	h := hkdf.New(sha256.New, pk, []byte(pin), []byte("meshsat-pair-v1"))
	secret := make([]byte, 32)
	if _, err := io.ReadFull(h, secret); err != nil {
		return nil, err
	}
	return secret, nil
}

// ClaimRequest is what the client POSTs to /api/v2/pair/claim.
type ClaimRequest struct {
	PIN           string `json:"pin"`
	PublicKeyHex  string `json:"public_key"`          // Ed25519 pub, hex
	Name          string `json:"name,omitempty"`      // operator-facing label
	Kind          string `json:"kind,omitempty"`      // browser / android / cli
	HMACHex       string `json:"hmac"`                // HMAC-SHA256(shared, public_key_bytes)
}

// ClaimResponse is what the bridge returns on success.
type ClaimResponse struct {
	ClientID   string `json:"client_id"`
	JWT        string `json:"jwt"`
	CertPEM    string `json:"cert_pem,omitempty"` // empty when internal CA isn't configured
	ExpiresAt  string `json:"expires_at"`
}

// VerifyClaimHMAC confirms the client knew both the pairing key and
// the PIN (i.e. read both from the touch display).
func VerifyClaimHMAC(sharedSecret []byte, publicKeyHex, hmacHex string) error {
	pk, err := hex.DecodeString(publicKeyHex)
	if err != nil || len(pk) != ed25519.PublicKeySize {
		return errors.New("invalid client public key")
	}
	got, err := hex.DecodeString(hmacHex)
	if err != nil {
		return errors.New("invalid HMAC encoding")
	}
	mac := hmac.New(sha256.New, sharedSecret)
	mac.Write(pk)
	if !hmac.Equal(mac.Sum(nil), got) {
		return errors.New("claim HMAC mismatch — wrong PIN or tampered request")
	}
	return nil
}

// MintJWT issues an HS256-less, Ed25519-signed JWT. Minimal (no
// `kid`, no `aud`), one-shot per issue — refresh mints a new one.
// Shape: "<header>.<claims>.<sig>" all base64url.
func MintJWT(clientID string, signerPriv ed25519.PrivateKey, issuedAt time.Time) (string, error) {
	header := map[string]string{"alg": "EdDSA", "typ": "JWT"}
	claims := map[string]interface{}{
		"sub": clientID,
		"iat": issuedAt.Unix(),
		"exp": issuedAt.Add(JWTTTL).Unix(),
		"iss": "meshsat-bridge",
	}
	hJSON, _ := json.Marshal(header)
	cJSON, _ := json.Marshal(claims)
	h := base64.RawURLEncoding.EncodeToString(hJSON)
	c := base64.RawURLEncoding.EncodeToString(cJSON)
	sig := ed25519.Sign(signerPriv, []byte(h+"."+c))
	return h + "." + c + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// VerifyJWT checks the three-part EdDSA JWT and returns the claims.
// Rejects expired tokens, mismatched signatures, and unknown
// algorithms. The caller looks up the claimed `sub` in
// paired_clients to find the verifying pubkey.
func VerifyJWT(token string, pubkey ed25519.PublicKey) (map[string]interface{}, error) {
	var h, c, s string
	// Split manually to avoid bringing in a lib.
	for i, part := range splitThree(token) {
		switch i {
		case 0:
			h = part
		case 1:
			c = part
		case 2:
			s = part
		}
	}
	if h == "" || c == "" || s == "" {
		return nil, errors.New("malformed JWT")
	}
	sig, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, errors.New("invalid signature encoding")
	}
	if !ed25519.Verify(pubkey, []byte(h+"."+c), sig) {
		return nil, errors.New("JWT signature failed")
	}
	// Verify algorithm.
	hdrBytes, _ := base64.RawURLEncoding.DecodeString(h)
	var hdr map[string]string
	_ = json.Unmarshal(hdrBytes, &hdr)
	if hdr["alg"] != "EdDSA" {
		return nil, fmt.Errorf("unsupported alg %q", hdr["alg"])
	}
	cBytes, _ := base64.RawURLEncoding.DecodeString(c)
	var claims map[string]interface{}
	if err := json.Unmarshal(cBytes, &claims); err != nil {
		return nil, err
	}
	// Expiry check.
	if exp, ok := claims["exp"].(float64); ok {
		if int64(exp) < time.Now().Unix() {
			return nil, errors.New("JWT expired")
		}
	}
	return claims, nil
}

func splitThree(s string) [3]string {
	var out [3]string
	cur := ""
	idx := 0
	for _, ch := range s {
		if ch == '.' {
			if idx >= 3 {
				return out
			}
			out[idx] = cur
			idx++
			cur = ""
			continue
		}
		cur += string(ch)
	}
	if idx < 3 {
		out[idx] = cur
	}
	return out
}
