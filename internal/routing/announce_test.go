package routing

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

// testIdentity creates an in-memory identity (no DB) for tests.
func testIdentity(t *testing.T) *Identity {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := &Identity{
		signingKey:    priv,
		signingPub:    pub,
		encryptionKey: encKey,
		encryptionPub: encKey.PublicKey(),
	}
	id.computeDestHash()
	return id
}

func TestAnnounceRoundtrip(t *testing.T) {
	id := testIdentity(t)
	appData := []byte("meshsat-node-1")

	announce, err := NewAnnounce(id, appData)
	if err != nil {
		t.Fatal(err)
	}

	data := announce.Marshal()
	parsed, err := UnmarshalAnnounce(data)
	if err != nil {
		t.Fatal("unmarshal:", err)
	}

	if parsed.DestHash != announce.DestHash {
		t.Error("dest hash mismatch")
	}
	if string(parsed.AppData) != string(appData) {
		t.Errorf("app data: got %q, want %q", parsed.AppData, appData)
	}
	if parsed.HopCount != 0 {
		t.Errorf("hop count: got %d, want 0", parsed.HopCount)
	}
	if parsed.Random != announce.Random {
		t.Error("random blob mismatch")
	}
}

func TestAnnounceVerify(t *testing.T) {
	id := testIdentity(t)

	announce, err := NewAnnounce(id, []byte("test"))
	if err != nil {
		t.Fatal(err)
	}

	if !announce.Verify() {
		t.Fatal("valid announce should verify")
	}
}

func TestAnnounceVerify_TamperedDestHash(t *testing.T) {
	id := testIdentity(t)
	announce, _ := NewAnnounce(id, nil)

	announce.DestHash[0] ^= 0xff // tamper
	if announce.Verify() {
		t.Fatal("tampered dest hash should not verify")
	}
}

func TestAnnounceVerify_TamperedSignature(t *testing.T) {
	id := testIdentity(t)
	announce, _ := NewAnnounce(id, nil)

	announce.Signature[0] ^= 0xff // tamper
	if announce.Verify() {
		t.Fatal("tampered signature should not verify")
	}
}

func TestAnnounceNoAppData(t *testing.T) {
	id := testIdentity(t)
	announce, err := NewAnnounce(id, nil)
	if err != nil {
		t.Fatal(err)
	}

	if announce.Flags&FlagHasAppData != 0 {
		t.Error("no app data flag should be clear")
	}

	data := announce.Marshal()
	parsed, err := UnmarshalAnnounce(data)
	if err != nil {
		t.Fatal(err)
	}

	if len(parsed.AppData) != 0 {
		t.Error("parsed app data should be empty")
	}
	if !parsed.Verify() {
		t.Fatal("announce without app data should verify")
	}
}

func TestAnnounceIncrementHop(t *testing.T) {
	id := testIdentity(t)
	announce, _ := NewAnnounce(id, nil)

	for i := 0; i < MaxAnnounceHops; i++ {
		if !announce.IncrementHop() {
			t.Fatalf("increment should succeed at hop %d", i)
		}
	}

	if announce.IncrementHop() {
		t.Fatal("increment should fail at max hops")
	}
	if int(announce.HopCount) != MaxAnnounceHops {
		t.Errorf("hop count: got %d, want %d", announce.HopCount, MaxAnnounceHops)
	}
}

func TestUnmarshalAnnounce_TooShort(t *testing.T) {
	_, err := UnmarshalAnnounce([]byte{0x01, 0x00})
	if err == nil {
		t.Fatal("should fail on short data")
	}
}

func TestUnmarshalAnnounce_NotAnnounce(t *testing.T) {
	data := make([]byte, AnnounceMinLen)
	data[0] = 0x00 // no announce flag
	_, err := UnmarshalAnnounce(data)
	if err == nil {
		t.Fatal("should fail when announce flag not set")
	}
}

func TestAnnounceUniqueness(t *testing.T) {
	id := testIdentity(t)
	a1, _ := NewAnnounce(id, nil)
	a2, _ := NewAnnounce(id, nil)

	if a1.Random == a2.Random {
		t.Fatal("two announces should have different random blobs")
	}
}
