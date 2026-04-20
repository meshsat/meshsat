package federation

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func signedManifestFor(t *testing.T) *CapabilityManifest {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManifest(hex.EncodeToString(pub), "routingABC", "kitParallax", []Bearer{
		{Type: "mesh", Address: "meshsat", MTU: 230},
		{Type: "ble", Address: "AA:BB:CC:DD:EE:FF"},
	})
	if err := m.Sign(priv); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestWrapUnwrapRoundTrip confirms the magic-prefix envelope is
// reversible.
func TestWrapUnwrapRoundTrip(t *testing.T) {
	m := signedManifestFor(t)
	wire, err := WrapManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	if !IsManifestFrame(wire) {
		t.Fatal("IsManifestFrame false on our own output")
	}
	back, err := UnwrapManifest(wire)
	if err != nil {
		t.Fatal(err)
	}
	if back.SignerID != m.SignerID {
		t.Errorf("signer_id: got %q want %q", back.SignerID, m.SignerID)
	}
	if vr := back.Verify(time.Now(), DefaultMaxAge); !vr.OK {
		t.Fatalf("verify after wire round-trip failed: %s", vr.Reason)
	}
}

// TestDispatchRoutesByPrefix — manifest frames go to onManifest, other
// bytes fall through to onReticulum.
func TestDispatchRoutesByPrefix(t *testing.T) {
	var gotManifest *CapabilityManifest
	var gotRNS []byte

	onM := func(m *CapabilityManifest) { gotManifest = m }
	onR := func(b []byte) { gotRNS = b }

	m := signedManifestFor(t)
	wire, _ := WrapManifest(m)
	DispatchPacket(wire, onM, onR)
	if gotManifest == nil {
		t.Fatal("manifest frame did not reach onManifest")
	}
	if gotRNS != nil {
		t.Fatal("manifest frame leaked into onReticulum")
	}

	gotManifest, gotRNS = nil, nil
	rnsFake := []byte{0x80, 0x01, 0xff, 0xee, 0xdd}
	DispatchPacket(rnsFake, onM, onR)
	if gotManifest != nil {
		t.Fatal("non-manifest bytes reached onManifest")
	}
	if string(gotRNS) != string(rnsFake) {
		t.Errorf("reticulum fall-through wrong: got %x", gotRNS)
	}
}

// TestSummariseAliasFallback — if a peer ships no alias, the persist
// summary synthesises one from the short signer_id.
func TestSummariseAliasFallback(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	m := NewManifest(hex.EncodeToString(pub), "", "", nil)
	if err := m.Sign(priv); err != nil {
		t.Fatal(err)
	}
	s, err := SummariseForPersist(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s.Alias, "peer-") {
		t.Errorf("alias fallback wrong: %q", s.Alias)
	}
}

// TestBuildManifestBytesSmoke — the end-to-end build-and-sign path via
// the opaque-signer callback.
func TestBuildManifestBytesSmoke(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signFn := func(body []byte) []byte { return ed25519.Sign(priv, body) }
	wire, err := BuildManifestBytes(hex.EncodeToString(pub), "rt", "kit", []Bearer{{Type: "mesh"}}, signFn)
	if err != nil {
		t.Fatal(err)
	}
	if !IsManifestFrame(wire) {
		t.Fatal("built bytes are not a manifest frame")
	}
	m, err := UnwrapManifest(wire)
	if err != nil {
		t.Fatal(err)
	}
	if vr := m.Verify(time.Now(), DefaultMaxAge); !vr.OK {
		t.Fatalf("built manifest fails verify: %s", vr.Reason)
	}
}

// TestSignWithRejectsBadSig — a sign fn that returns the wrong length
// must error, not silently corrupt.
func TestSignWithRejectsBadSig(t *testing.T) {
	m := NewManifest("00", "", "", nil)
	if err := m.SignWith(func(_ []byte) []byte { return []byte{1, 2, 3} }); err == nil {
		t.Fatal("expected error from too-short signature")
	}
}
