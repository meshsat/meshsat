package reticulum

import (
	"bytes"
	"testing"
)

func TestNewAnnounce_Basic(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}

	a, err := NewAnnounce(id, "meshsat.hub", nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(a.PublicKey) != IdentityKeySize {
		t.Errorf("PublicKey size: got %d, want %d", len(a.PublicKey), IdentityKeySize)
	}
	if len(a.Signature) != SignatureLen {
		t.Errorf("Signature size: got %d, want %d", len(a.Signature), SignatureLen)
	}
	if a.Hops != 0 {
		t.Errorf("Hops: got %d, want 0", a.Hops)
	}
	if a.Random == [RandomHashLen]byte{} {
		t.Error("Random should not be zero")
	}
}

func TestNewAnnounce_WithAppData(t *testing.T) {
	id, _ := GenerateIdentity()
	appData := []byte(`{"name":"hub-01","version":"1.0"}`)

	a, err := NewAnnounce(id, "meshsat.hub", appData)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(a.AppData, appData) {
		t.Error("AppData mismatch")
	}
}

func TestAnnounce_Verify(t *testing.T) {
	id, _ := GenerateIdentity()
	a, _ := NewAnnounce(id, "meshsat.hub", []byte("metadata"))

	if err := a.Verify(); err != nil {
		t.Fatalf("valid announce should verify: %v", err)
	}
}

func TestAnnounce_VerifyFailsOnTamperedSignature(t *testing.T) {
	id, _ := GenerateIdentity()
	a, _ := NewAnnounce(id, "meshsat.hub", nil)

	a.Signature[0] ^= 0xFF
	if err := a.Verify(); err == nil {
		t.Error("tampered signature should fail verification")
	}
}

func TestAnnounce_VerifyFailsOnWrongDestHash(t *testing.T) {
	id, _ := GenerateIdentity()
	a, _ := NewAnnounce(id, "meshsat.hub", nil)

	a.DestHash[0] ^= 0xFF
	if err := a.Verify(); err == nil {
		t.Error("wrong dest hash should fail verification")
	}
}

func TestAnnounce_VerifyFailsOnTamperedAppData(t *testing.T) {
	id, _ := GenerateIdentity()
	a, _ := NewAnnounce(id, "meshsat.hub", []byte("original"))

	a.AppData = []byte("tampered")
	if err := a.Verify(); err == nil {
		t.Error("tampered app data should fail verification")
	}
}

func TestAnnounce_PayloadMarshalRoundtrip(t *testing.T) {
	id, _ := GenerateIdentity()
	a1, _ := NewAnnounce(id, "meshsat.hub", []byte("test-data"))

	payload := a1.MarshalPayload()
	if len(payload) < AnnounceMinPayload {
		t.Fatalf("payload too short: %d < %d", len(payload), AnnounceMinPayload)
	}

	a2, err := UnmarshalAnnouncePayload(payload, a1.DestHash, a1.Hops, a1.ContextFlag)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(a2.PublicKey, a1.PublicKey) {
		t.Error("PublicKey mismatch")
	}
	if a2.NameHash != a1.NameHash {
		t.Error("NameHash mismatch")
	}
	if a2.Random != a1.Random {
		t.Error("Random mismatch")
	}
	if !bytes.Equal(a2.Signature, a1.Signature) {
		t.Error("Signature mismatch")
	}
	if !bytes.Equal(a2.AppData, a1.AppData) {
		t.Error("AppData mismatch")
	}

	// Verify the round-tripped announce
	if err := a2.Verify(); err != nil {
		t.Fatalf("round-tripped announce should verify: %v", err)
	}
}

func TestAnnounce_PacketMarshalRoundtrip(t *testing.T) {
	id, _ := GenerateIdentity()
	a1, _ := NewAnnounce(id, "meshsat.hub", []byte("app-data"))

	raw := a1.MarshalPacket()

	// Verify packet header
	h, err := UnmarshalHeader(raw)
	if err != nil {
		t.Fatal(err)
	}
	if h.PacketType != PacketAnnounce {
		t.Errorf("PacketType: got %d, want %d", h.PacketType, PacketAnnounce)
	}
	if h.HeaderType != HeaderType1 {
		t.Errorf("HeaderType: got %d, want %d", h.HeaderType, HeaderType1)
	}
	if h.DestHash != a1.DestHash {
		t.Error("DestHash mismatch in header")
	}

	// Full packet unmarshal
	a2, err := UnmarshalAnnouncePacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := a2.Verify(); err != nil {
		t.Fatalf("packet round-tripped announce should verify: %v", err)
	}
	if !bytes.Equal(a2.AppData, a1.AppData) {
		t.Error("AppData mismatch after full packet roundtrip")
	}
}

func TestAnnounce_NoAppData(t *testing.T) {
	id, _ := GenerateIdentity()
	a, _ := NewAnnounce(id, "meshsat.hub", nil)

	payload := a.MarshalPayload()
	if len(payload) != AnnounceMinPayload {
		t.Errorf("payload size without app data: got %d, want %d", len(payload), AnnounceMinPayload)
	}

	a2, err := UnmarshalAnnouncePayload(payload, a.DestHash, a.Hops, a.ContextFlag)
	if err != nil {
		t.Fatal(err)
	}
	if len(a2.AppData) != 0 {
		t.Error("AppData should be nil/empty")
	}
	if err := a2.Verify(); err != nil {
		t.Fatalf("no-appdata announce should verify: %v", err)
	}
}

func TestUnmarshalAnnouncePayload_TooShort(t *testing.T) {
	_, err := UnmarshalAnnouncePayload(make([]byte, 10), [TruncatedHashLen]byte{}, 0, 0)
	if err == nil {
		t.Error("expected error for short payload")
	}
}

func TestUnmarshalAnnouncePacket_WrongType(t *testing.T) {
	// Build a DATA packet and try to parse as announce
	h := &Header{
		HeaderType: HeaderType1,
		PacketType: PacketData,
		Context:    ContextNone,
		Data:       make([]byte, 200),
	}
	raw := h.Marshal()
	_, err := UnmarshalAnnouncePacket(raw)
	if err == nil {
		t.Error("expected error for non-announce packet")
	}
}

func TestAnnounce_IncrementHop(t *testing.T) {
	id, _ := GenerateIdentity()
	a, _ := NewAnnounce(id, "meshsat.hub", nil)

	if a.Hops != 0 {
		t.Fatal("initial hops should be 0")
	}

	if !a.IncrementHop() {
		t.Error("first hop increment should succeed")
	}
	if a.Hops != 1 {
		t.Errorf("hops after increment: got %d, want 1", a.Hops)
	}

	// Still verifiable after hop increment (hops not in signature)
	if err := a.Verify(); err != nil {
		t.Fatalf("announce should verify after hop increment: %v", err)
	}

	// Max out hops
	a.Hops = PathfinderM
	if a.IncrementHop() {
		t.Error("should not increment past max hops")
	}
}

func TestAnnounce_UniqueRandom(t *testing.T) {
	id, _ := GenerateIdentity()
	a1, _ := NewAnnounce(id, "meshsat.hub", nil)
	a2, _ := NewAnnounce(id, "meshsat.hub", nil)

	if a1.Random == a2.Random {
		t.Error("two announces from same identity should have different randoms")
	}
}

func TestAnnounce_DestHashMatchesIdentity(t *testing.T) {
	id, _ := GenerateIdentity()
	appName := "meshsat.hub"

	a, _ := NewAnnounce(id, appName, nil)
	expected := id.DestHash(appName)

	if a.DestHash != expected {
		t.Error("announce DestHash should match identity.DestHash for same app name")
	}
}

func TestAnnounce_PublicKeyOrder(t *testing.T) {
	// Verify the public key is [X25519][Ed25519] per RNS spec
	id, _ := GenerateIdentity()
	a, _ := NewAnnounce(id, "meshsat.hub", nil)

	encBytes := id.EncryptionPublicKey().Bytes()
	sigBytes := id.SigningPublicKey()

	if !bytes.Equal(a.PublicKey[:EncryptionPubLen], encBytes) {
		t.Error("first 32 bytes of PublicKey should be X25519")
	}
	if !bytes.Equal(a.PublicKey[EncryptionPubLen:], sigBytes) {
		t.Error("last 32 bytes of PublicKey should be Ed25519")
	}
}
