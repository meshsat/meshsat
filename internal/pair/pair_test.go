package pair

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func TestDeriveSharedSecret_Agrees(t *testing.T) {
	pk, err := GeneratePairingKey()
	if err != nil { t.Fatal(err) }
	pin := "123456"

	a, err := DeriveSharedSecret(pk, pin)
	if err != nil { t.Fatal(err) }
	b, err := DeriveSharedSecret(pk, pin)
	if err != nil { t.Fatal(err) }
	if !hmac.Equal(a, b) {
		t.Fatalf("HKDF disagrees between runs: %x vs %x", a, b)
	}
	// Different PIN ⇒ different secret.
	c, _ := DeriveSharedSecret(pk, "654321")
	if hmac.Equal(a, c) {
		t.Fatal("HKDF produced the same secret for different PINs")
	}
}

func TestClaimHMAC_RoundTrip(t *testing.T) {
	pk, _ := GeneratePairingKey()
	secret, _ := DeriveSharedSecret(pk, "123456")
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	mac := hmac.New(sha256.New, secret)
	mac.Write(pub)
	h := hex.EncodeToString(mac.Sum(nil))
	if err := VerifyClaimHMAC(secret, hex.EncodeToString(pub), h); err != nil {
		t.Fatalf("valid HMAC rejected: %v", err)
	}
	// Tamper.
	if err := VerifyClaimHMAC(secret, hex.EncodeToString(pub), "00"+h[2:]); err == nil {
		t.Fatal("tampered HMAC accepted")
	}
}

func TestJWT_RoundTrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()
	tok, err := MintJWT("client-123", priv, now)
	if err != nil { t.Fatal(err) }
	claims, err := VerifyJWT(tok, pub)
	if err != nil { t.Fatalf("verify: %v", err) }
	if claims["sub"] != "client-123" {
		t.Errorf("sub: %v", claims["sub"])
	}
}

func TestJWT_ExpiryRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	tok, _ := MintJWT("c", priv, time.Now().Add(-2*time.Hour))
	if _, err := VerifyJWT(tok, pub); err == nil {
		t.Fatal("expired JWT accepted")
	}
}

func TestJWT_TamperRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	tok, _ := MintJWT("c", priv, time.Now())
	// Replace the signature region with an all-zeros b64 so we know
	// verify actually checks bytes, not just reads them.
	lastDot := -1
	for i := len(tok) - 1; i >= 0; i-- {
		if tok[i] == '.' { lastDot = i; break }
	}
	if lastDot < 0 { t.Fatal("no '.' in token") }
	tampered := tok[:lastDot+1] + "AAAAAAAA"
	if _, err := VerifyJWT(tampered, pub); err == nil {
		t.Fatal("tampered JWT accepted")
	}
}

func TestGeneratePIN_Length(t *testing.T) {
	pin, err := GeneratePIN()
	if err != nil { t.Fatal(err) }
	if len(pin) != PinLength {
		t.Fatalf("len %d want %d", len(pin), PinLength)
	}
}
