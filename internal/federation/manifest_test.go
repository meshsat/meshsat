package federation

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func genKeypair(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen keypair: %v", err)
	}
	return hex.EncodeToString(pub), priv
}

// TestSignRoundTrip confirms sign → marshal → unmarshal → verify
// succeeds with the expected signer + bearer count.
func TestSignRoundTrip(t *testing.T) {
	signerID, priv := genKeypair(t)
	m := NewManifest(signerID, "routingHash123", "parallax01", []Bearer{
		{Type: "mesh", InterfaceID: "mesh_0", Address: "meshsat", Cost: 0, MTU: 230},
		{Type: "iridium_sbd", InterfaceID: "iridium_0", Address: "301434060000100", Cost: 0.05, MTU: 340},
	})
	if err := m.Sign(priv); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if m.Signature == "" {
		t.Fatal("signature empty")
	}
	raw, err := m.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := UnmarshalManifest(raw)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	vr := out.Verify(time.Now(), DefaultMaxAge)
	if !vr.OK {
		t.Fatalf("verify failed: %s", vr.Reason)
	}
	if vr.SignerID != signerID {
		t.Errorf("signer_id mismatch: got %q want %q", vr.SignerID, signerID)
	}
	if vr.NumBearers != 2 {
		t.Errorf("bearer count: got %d want 2", vr.NumBearers)
	}
}

// TestTamperDetectable — any mutation of a signed manifest must invalidate.
func TestTamperDetectable(t *testing.T) {
	signerID, priv := genKeypair(t)
	m := NewManifest(signerID, "routingHash", "kitA", []Bearer{
		{Type: "sms", Address: "+31611111111", Cost: 0.01},
	})
	if err := m.Sign(priv); err != nil {
		t.Fatal(err)
	}
	raw, _ := m.Marshal()
	// Swap the phone number without re-signing.
	mut := strings.ReplaceAll(string(raw), "+31611111111", "+31699999999")
	out, err := UnmarshalManifest([]byte(mut))
	if err != nil {
		t.Fatal(err)
	}
	vr := out.Verify(time.Now(), DefaultMaxAge)
	if vr.OK {
		t.Fatal("tampered manifest verified — signature check is broken")
	}
	if !strings.Contains(vr.Reason, "verify failed") {
		t.Errorf("wrong reason: %s", vr.Reason)
	}
}

// TestReplayRejected — a manifest too old is rejected.
func TestReplayRejected(t *testing.T) {
	signerID, priv := genKeypair(t)
	m := NewManifest(signerID, "", "", []Bearer{{Type: "mesh"}})
	m.Timestamp = time.Now().Add(-30 * time.Minute).Unix()
	if err := m.Sign(priv); err != nil {
		t.Fatal(err)
	}
	vr := m.Verify(time.Now(), DefaultMaxAge)
	if vr.OK {
		t.Fatal("old manifest accepted")
	}
	if !strings.Contains(vr.Reason, "too old") {
		t.Errorf("wrong reason: %s", vr.Reason)
	}
}

// TestWrongProtocolRejected — bumping the protocol version must fail
// verify of an old-protocol manifest even with a good signature.
func TestWrongProtocolRejected(t *testing.T) {
	signerID, priv := genKeypair(t)
	m := NewManifest(signerID, "", "", []Bearer{{Type: "mesh"}})
	m.Protocol = 99
	if err := m.Sign(priv); err != nil {
		t.Fatal(err)
	}
	vr := m.Verify(time.Now(), DefaultMaxAge)
	if vr.OK {
		t.Fatal("wrong-protocol manifest accepted")
	}
	if !strings.Contains(vr.Reason, "unsupported protocol") {
		t.Errorf("wrong reason: %s", vr.Reason)
	}
}

// TestNonceUnique — two freshly-constructed manifests must have
// different nonces (probabilistic; 16 bytes of rand → collision prob
// effectively zero).
func TestNonceUnique(t *testing.T) {
	_, priv := genKeypair(t)
	signer, _ := genKeypair(t)
	m1 := NewManifest(signer, "", "", nil)
	_ = priv
	m2 := NewManifest(signer, "", "", nil)
	if m1.Nonce == m2.Nonce {
		t.Fatalf("nonce collision: both %q", m1.Nonce)
	}
}

// TestEmptySignerRejected — a manifest without a signer_id must not
// pass verify even if the signature bytes happen to parse.
func TestEmptySignerRejected(t *testing.T) {
	m := &CapabilityManifest{}
	m.Protocol = ProtocolVersion
	m.Nonce = "deadbeef"
	m.Signature = strings.Repeat("00", ed25519.SignatureSize)
	vr := m.Verify(time.Now(), DefaultMaxAge)
	if vr.OK {
		t.Fatal("empty-signer manifest accepted")
	}
}
