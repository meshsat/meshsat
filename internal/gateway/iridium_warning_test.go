package gateway

import (
	"testing"

	"meshsat/internal/transport"
)

// A public warning digest sent as an MT (MESHSAT-1734) starts with 0x57,
// which the compact decoder would read as flags; the SBD gateway hands such
// bytes on whole instead, for the processor's relay hook.
func TestAWarningDigestMTIsHandedOnWhole(t *testing.T) {
	g := &SBDGateway{IridiumGateway: IridiumGateway{inCh: make(chan InboundMessage, 1)}}
	digest := append([]byte{MsgTypeWarning, 0x01, 0x01, 0x02}, make([]byte, 110)...)
	g.deliverMT(digest)
	select {
	case m := <-g.inCh:
		if m.Text != string(digest) || m.Source != "iridium" {
			t.Fatalf("handed on changed: %d bytes, source %q", len(m.Text), m.Source)
		}
	default:
		t.Fatal("nothing handed on")
	}
	// A compact frame still decodes as before (positive control).
	compact, err := EncodeCompact(&transport.MeshMessage{From: 0x12345678, DecodedText: "hi"}, false)
	if err != nil {
		t.Fatal(err)
	}
	g.deliverMT(compact)
	select {
	case m := <-g.inCh:
		if m.Text != "hi" {
			t.Fatalf("compact: %q", m.Text)
		}
	default:
		t.Fatal("the compact frame was not handed on")
	}
}
