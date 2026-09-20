package transport

import (
	"encoding/base64"
	"testing"
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

// The check must not swallow real Reticulum traffic whose flags byte happens
// to be 0x01: the binary destination hash is not base64.
func TestIsReticulumPacket_RealHeaderWithFlags01StillReticulum(t *testing.T) {
	pkt := make([]byte, 40)
	pkt[0] = 0x01 // header type 1, packet type 1
	pkt[1] = 2    // hops
	for i := 2; i < len(pkt); i++ {
		pkt[i] = byte(0x80 + i) // binary hash bytes, outside the base64 alphabet
	}
	if !isReticulumPacket(pkt) {
		t.Fatal("a binary packet with flags 0x01 is no longer classified as Reticulum")
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
