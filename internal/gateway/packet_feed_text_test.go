package gateway

import (
	"testing"

	"meshsat/internal/codec"
	"meshsat/internal/transport"
)

// The feed shows words, never ciphertext or the version byte. [MESHSAT-1282]
func TestSatFeedText(t *testing.T) {
	wire := string(codec.PrependVersionByte([]byte("yVFA6+qDWgB28igwYMByOnRBwKc+n")))
	for _, tc := range []struct {
		name string
		msg  transport.MeshMessage
		want string
	}{
		{"encrypted send shows the typed text", transport.MeshMessage{Encrypted: true, DecodedText: wire, PlainText: "55588 tst"}, "55588 tst"},
		{"encrypted send without the typed text shows nothing", transport.MeshMessage{Encrypted: true, DecodedText: wire}, ""},
		{"clear send drops the version byte", transport.MeshMessage{DecodedText: string(codec.PrependVersionByte([]byte("hello")))}, "hello"},
		{"clear send as typed", transport.MeshMessage{DecodedText: "hello"}, "hello"},
		{"binary frame", transport.MeshMessage{DecodedText: "MS\x01\x03\x00\xff"}, ""},
		{"hub uplink frame goes as raw bytes", transport.MeshMessage{RawPayload: []byte{0x4d, 0x53, 0x01, 0x03}}, ""},
	} {
		if got := satFeedText(&tc.msg); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestFeedText(t *testing.T) {
	for in, want := range map[string]string{
		"55588 tst":       "55588 tst",
		"line\nbreak\tok": "line\nbreak\tok",
		"\x01yVFA6":       "",
		"del\x7f":         "",
		"\xff\xfe":        "",
		"":                "",
	} {
		if got := FeedText([]byte(in)); got != want {
			t.Errorf("FeedText(%q) = %q, want %q", in, got, want)
		}
	}
}
