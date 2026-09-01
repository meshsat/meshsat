package keystore

import (
	"crypto/rand"
	"testing"
)

// TestChannelMgmt_BundleRoundTrip is the on-wire compatibility gate for
// transporting an OOB management key in a signed QR bundle: the enum
// value 0x08, both string/byte mappings, and a v2 Marshal/Verify/Unmarshal
// round trip with the issuer alias as the entry address. [MESHSAT-756]
func TestChannelMgmt_BundleRoundTrip(t *testing.T) {
	if ChannelMgmt != 0x08 {
		t.Fatalf("ChannelMgmt wire byte must be 0x08, got 0x%02x", ChannelMgmt)
	}
	if got, ok := CanonicalChannelType("mgmt"); !ok || got != "mgmt" {
		t.Fatalf("CanonicalChannelType(mgmt)=(%q,%v)", got, ok)
	}
	if ChannelTypeToByte("mgmt") != ChannelMgmt {
		t.Errorf("ChannelTypeToByte(mgmt)=0x%02x, want 0x%02x", ChannelTypeToByte("mgmt"), ChannelMgmt)
	}
	if got := ByteToChannelType(ChannelMgmt); got != "mgmt" {
		t.Errorf("ByteToChannelType(0x08)=%q, want mgmt", got)
	}
	found := false
	for _, ct := range SupportedChannelTypes() {
		if ct == "mgmt" {
			found = true
		}
	}
	if !found {
		t.Error("mgmt missing from SupportedChannelTypes")
	}

	var key [aesKeyLen]byte
	rand.Read(key[:])
	entries := []BundleEntry{
		{ChannelType: ChannelMgmt, Address: "tesseract", Key: key},
	}
	signer, pub := newTestSigner(t)
	data, err := MarshalBundleV2(testBridgeHash(), entries, signer, pub)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyBundle(data, nil) {
		t.Fatal("mgmt bundle signature failed")
	}
	b, err := UnmarshalBundle(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(b.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(b.Entries))
	}
	e := b.Entries[0]
	if e.ChannelType != ChannelMgmt || e.Address != "tesseract" || e.Key != key {
		t.Fatalf("entry mismatch: type=0x%02x addr=%q", e.ChannelType, e.Address)
	}
	// URL form survives too (what the QR carries).
	back, err := URLToBundle(BundleToURL(data))
	if err != nil {
		t.Fatalf("url round trip: %v", err)
	}
	if string(back) != string(data) {
		t.Fatal("url round trip altered the bundle")
	}
}
