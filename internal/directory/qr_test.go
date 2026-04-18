package directory

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
)

func TestQRCard_RoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer := hex.EncodeToString(pub)
	sign := func(data []byte) []byte { return ed25519.Sign(priv, data) }

	c := QRContact{
		ID:          "abc123",
		DisplayName: "Alice",
		SIDC:        "SFGPUCI---*****",
		Addresses: []QRAddress{
			{Kind: "SMS", Value: "+31612345678"},
			{Kind: "MESHTASTIC", Value: "!abcd1234"},
		},
	}

	_, url, err := BuildQRCard(c, signer, sign)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.HasPrefix(url, "meshsat://contact/") {
		t.Fatalf("unexpected URL prefix: %q", url)
	}

	parsed, err := ParseQRCard(url)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Contact.DisplayName != "Alice" || parsed.Signer != signer {
		t.Errorf("round-trip mismatch: %+v", parsed)
	}
	if len(parsed.Contact.Addresses) != 2 {
		t.Errorf("addresses: got %d", len(parsed.Contact.Addresses))
	}
}

func TestQRCard_TamperRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := hex.EncodeToString(pub)
	sign := func(data []byte) []byte { return ed25519.Sign(priv, data) }

	c := QRContact{ID: "abc", DisplayName: "Alice"}
	raw, _, err := BuildQRCard(c, signer, sign)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte in the display-name region of the card.
	tampered := make([]byte, len(raw))
	copy(tampered, raw)
	idx := strings.Index(string(tampered), "Alice")
	if idx < 0 {
		t.Fatal("Alice not found in raw card")
	}
	tampered[idx] = 'B' // "Blice"

	_, err = ParseQRCard(string(tampered))
	if err == nil {
		t.Fatal("expected signature failure on tampered card")
	}
	if !strings.Contains(err.Error(), "signature") {
		t.Errorf("expected sig error, got %v", err)
	}
}

func TestQRCard_InvalidURL(t *testing.T) {
	cases := []string{
		"",
		"not a url",
		"meshsat://contact/!!!notbase64",
	}
	for _, s := range cases {
		if _, err := ParseQRCard(s); err == nil {
			t.Errorf("expected error for %q", s)
		}
	}
}
