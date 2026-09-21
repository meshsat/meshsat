package engine

import (
	"testing"

	"meshsat/internal/codec"
	"meshsat/internal/gateway"
)

// The booth screen's packet table showed a satellite MT as the far kit's
// wire bytes (version byte + ciphertext). The gateway sink decodes it through
// the interface's ingress chain, like the SMS receive record. [MESHSAT-1282]
func TestGatewayPacketSink_SatelliteMTShowsTheText(t *testing.T) {
	p, _, tp, _ := newInboundAuthHarness(t, "iridium_imt_0", "iridium_imt", authTestChain)
	body, err := tp.ApplyEgress([]byte("55588 tst"), authTestChain)
	if err != nil {
		t.Fatal(err)
	}
	sink := p.GatewayPacketSink()
	last := func() PacketRecord {
		recs := p.Packets().Newest(1, "", "")
		if len(recs) != 1 {
			t.Fatalf("want 1 record, got %d", len(recs))
		}
		return recs[0]
	}

	sink(PacketRecord{Bearer: gateway.BearerSat, Dir: gateway.DirRX, Iface: "iridium_imt_0", Bytes: 53,
		Text: string(codec.PrependVersionByte(body))})
	if got := last(); got.Text != "55588 tst" || got.Bytes != 53 {
		t.Fatalf("MT record = %q (%d B), want the text and the wire size", got.Text, got.Bytes)
	}

	// A frame that does not decrypt keeps no text rather than ciphertext.
	sink(PacketRecord{Bearer: gateway.BearerSat, Dir: gateway.DirRX, Iface: "iridium_imt_0",
		Text: string(codec.PrependVersionByte([]byte("bm90IG91ciBrZXk=")))})
	if got := last(); got.Text != "" {
		t.Fatalf("undecryptable MT shows %q, want nothing", got.Text)
	}

	// Other bearers pass untouched.
	sink(PacketRecord{Bearer: gateway.BearerLoRa, Dir: gateway.DirRX, Iface: "mesh_0", Text: "yo"})
	if got := last(); got.Text != "yo" {
		t.Fatalf("mesh record changed to %q", got.Text)
	}
}
