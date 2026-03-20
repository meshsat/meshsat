package routing

import (
	"testing"

	"meshsat/internal/reticulum"
)

// testIdentity creates an in-memory identity (no DB) for tests.
func testIdentity(t *testing.T) *Identity {
	t.Helper()
	retID, err := reticulum.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	id := &Identity{
		retID:   retID,
		appName: DefaultAppName,
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
	if len(data) == 0 {
		t.Fatal("marshal returned empty data")
	}

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

	// Tamper the dest hash (shared between routing.Announce and reticulum.Announce)
	announce.DestHash[0] ^= 0xff
	if announce.ret != nil {
		announce.ret.DestHash = announce.DestHash
	}
	if announce.Verify() {
		t.Fatal("tampered dest hash should not verify")
	}
}

func TestAnnounceVerify_TamperedSignature(t *testing.T) {
	id := testIdentity(t)
	announce, _ := NewAnnounce(id, nil)

	// Tamper the signature (slices are shared, so only need to tamper once)
	announce.Signature[0] ^= 0xff
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

func TestAnnounceUniqueness(t *testing.T) {
	id := testIdentity(t)
	a1, _ := NewAnnounce(id, nil)
	a2, _ := NewAnnounce(id, nil)

	if a1.Random == a2.Random {
		t.Fatal("two announces should have different random blobs")
	}
}

func TestAnnounceVerifyAfterHopIncrement(t *testing.T) {
	id := testIdentity(t)
	announce, _ := NewAnnounce(id, []byte("test"))

	announce.IncrementHop()
	announce.IncrementHop()

	// Announce should still verify — hop count is excluded from signature
	if !announce.Verify() {
		t.Fatal("announce should verify after hop increment")
	}
}

func TestAnnounce_MarshalRoundtripAfterHopIncrement(t *testing.T) {
	id := testIdentity(t)
	announce, _ := NewAnnounce(id, []byte("hop-test"))

	announce.IncrementHop()
	announce.IncrementHop()
	announce.IncrementHop()

	data := announce.Marshal()
	parsed, err := UnmarshalAnnounce(data)
	if err != nil {
		t.Fatal(err)
	}

	if parsed.HopCount != 3 {
		t.Errorf("hop count after roundtrip: got %d, want 3", parsed.HopCount)
	}
	if !parsed.Verify() {
		t.Fatal("roundtripped announce should verify")
	}
}

func TestAnnounce_NameHashPresent(t *testing.T) {
	id := testIdentity(t)
	announce, _ := NewAnnounce(id, nil)

	expectedNameHash := reticulum.ComputeNameHash(DefaultAppName)
	if announce.NameHash != expectedNameHash {
		t.Error("announce NameHash should match default app name hash")
	}
}
