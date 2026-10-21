package transport

import (
	"encoding/base64"
	"math/rand"
	"testing"

	"meshsat/internal/reticulum"
)

// A relayed kit-to-kit satellite message is the protocol version byte plus
// base64 text. It must be classified as an app message: on 20 Sep 2026 a real
// 57 byte MT reached parallax's 9704 and was queued for the Reticulum layer,
// because 0x01 reads as Reticulum flags and the ASCII escape did not fire
// with a non-printable first byte. [MESHSAT-1282]
func TestIsReticulumPacket_VersionedBase64IsAnAppMessage(t *testing.T) {
	body := base64.StdEncoding.EncodeToString(make([]byte, 40)) // 56 chars, like the real MT
	mt := append([]byte{0x01}, body...)
	if isReticulumPacket(mt) {
		t.Fatalf("a %d byte MeshSat payload (0x01 + base64) was classified as a Reticulum packet", len(mt))
	}
}

// Flags 0x01 is an announce and nothing else, and an announce is signed, so
// a real one still goes to the Reticulum layer and a tampered one does not.
// (Until HeMB-NG S3-2 any binary packet with flags 0x01 counted, which a
// MeshSat payload without base64 would match half the time.)
func TestIsReticulumPacket_SignedAnnounceWithFlags01(t *testing.T) {
	id, err := reticulum.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	a, err := reticulum.NewAnnounce(id, "meshsat.bridge", nil)
	if err != nil {
		t.Fatal(err)
	}
	pkt := a.MarshalPacket()
	if pkt[0] != 0x01 {
		t.Fatalf("announce flags 0x%02x, want 0x01", pkt[0])
	}
	if !isReticulumPacket(pkt) {
		t.Fatal("a signed announce is not classified as Reticulum")
	}
	bad := append([]byte(nil), pkt...)
	bad[len(bad)-1] ^= 0x01 // last signature byte
	if isReticulumPacket(bad) {
		t.Fatal("an announce whose signature fails is classified as Reticulum")
	}
}

// A MeshSat payload without base64 is the version byte and binary: here
// shaped to pass every header check the classifier makes (hops below 128,
// a defined context byte, announce length), and real chain output.
func TestIsReticulumPacket_VersionedBinaryIsAnAppMessage(t *testing.T) {
	shaped := make([]byte, 200)
	shaped[0], shaped[1], shaped[18] = 0x01, 5, 0x00
	for i := 2; i < len(shaped); i++ {
		if i != 18 {
			shaped[i] = byte(0x80 + i)
		}
	}
	if isReticulumPacket(shaped) {
		t.Fatal("a header-shaped MeshSat payload is classified as Reticulum")
	}
	rng := rand.New(rand.NewSource(1584))
	for n := 19; n <= 400; n++ {
		mt := make([]byte, n)
		rng.Read(mt)
		mt[0] = 0x01
		if isReticulumPacket(mt) {
			t.Fatalf("a %d-byte versioned binary payload is classified as Reticulum", n)
		}
	}
}

// Reticulum packets that do not start with 0x01 are classified as before.
func TestIsReticulumPacket_OtherFlagsUnchanged(t *testing.T) {
	pkt := make([]byte, 40)
	pkt[0] = 0x00 // HEADER_1 DATA
	pkt[1] = 2
	for i := 2; i < len(pkt); i++ {
		pkt[i] = byte(0x80 + i)
	}
	if !isReticulumPacket(pkt) {
		t.Fatal("a binary DATA packet is no longer classified as Reticulum")
	}
}

func TestIsVersionedBase64(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []byte
		want bool
	}{
		{"version + base64", append([]byte{0x01}, "QUJDRA=="...), true},
		{"no version byte", []byte("QUJDRA=="), false},
		{"version + binary", []byte{0x01, 0x00, 0xff}, false},
		{"version only", []byte{0x01}, false},
		{"empty", nil, false},
	} {
		if got := isVersionedBase64(tc.in); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
