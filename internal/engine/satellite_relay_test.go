package engine

import (
	"encoding/json"
	"testing"

	"meshsat/internal/codec"
	"meshsat/internal/database"
	"meshsat/internal/gateway"
	"meshsat/internal/rules"
	"meshsat/internal/transport"
)

// Kit to kit over satellite [MESHSAT-1282]: the Hub relays the sender's MO
// byte for byte as an MT, so the far kit receives exactly what the near kit's
// DeliveryWorker produced: the protocol version byte, then the transformed
// body. These tests hold that contract from both ends.

// An MT that arrives with its version byte in front must pass the decrypt
// gate and be relayed to the mesh as the text. Before the fix the gate ran
// base64 on the 0x01, failed, and dropped the frame as unauthenticated.
func TestGatewayReceiver_SatelliteMTWithVersionByteIsRelayed(t *testing.T) {
	p, db, tp, gw := newInboundAuthHarness(t, "iridium_imt_0", "iridium_imt", authTestChain)
	body, err := tp.ApplyEgress([]byte("sat test"), authTestChain)
	if err != nil {
		t.Fatal(err)
	}
	wire := codec.PrependVersionByte(body) // what DeliveryWorker puts on air

	msgs, dropped := deliver(t, p, db, gw, gateway.InboundMessage{Text: string(wire), Source: "iridium_imt", FromAddr: "cloudloop"})
	if dropped != 0 {
		t.Fatalf("a peer kit's satellite frame was dropped as unauthenticated")
	}
	if len(msgs) == 0 || msgs[0].DecodedText != "sat test" {
		t.Fatalf("frame not stored as its text: %+v", msgs)
	}
	dl, err := db.GetPendingDeliveries("mesh_0", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(dl) != 1 {
		t.Fatalf("want one relay delivery to mesh_0, got %d", len(dl))
	}
	if string(dl[0].Payload) != "sat test" {
		t.Fatalf("mesh_0 would transmit %q, want the bare text", dl[0].Payload)
	}
}

// A bridge older than this change ships the whole mesh packet as a JSON
// envelope. The receiver still has to put the TEXT on its mesh: the envelope
// does not fit a Meshtastic text packet.
func TestGatewayReceiver_OldEnvelopeBodyIsRelayedAsItsText(t *testing.T) {
	p, db, tp, gw := newInboundAuthHarness(t, "iridium_imt_0", "iridium_imt", authTestChain)
	env, _ := json.Marshal(transport.MeshMessage{From: 0x4370c1d8, PortNum: 1, PortNumName: "TEXT_MESSAGE_APP", DecodedText: "sat test"})
	body, err := tp.ApplyEgress(env, authTestChain)
	if err != nil {
		t.Fatal(err)
	}
	msgs, dropped := deliver(t, p, db, gw, gateway.InboundMessage{Text: string(codec.PrependVersionByte(body)), Source: "iridium_imt", FromAddr: "cloudloop"})
	if dropped != 0 || len(msgs) == 0 || msgs[0].DecodedText != "sat test" {
		t.Fatalf("envelope not unwrapped to its text: dropped=%d msgs=%+v", dropped, msgs)
	}
	dl, _ := db.GetPendingDeliveries("mesh_0", 10)
	if len(dl) != 1 || string(dl[0].Payload) != "sat test" {
		t.Fatalf("mesh_0 delivery = %+v, want the bare text", dl)
	}
}

// Loop safety, because a loop burns Iridium credit: the text the far kit
// injects into its mesh must not be picked up again as mesh ingress and sent
// back up. The guard compares the mesh text against what was injected, so it
// only holds while both are the same string, which is why the body is the
// text and an envelope is unwrapped before it is marked.
func TestSatelliteInjectionIsNotSentBackUp(t *testing.T) {
	p, db, tp, gw := newInboundAuthHarness(t, "iridium_imt_0", "iridium_imt", authTestChain)
	body, _ := tp.ApplyEgress([]byte("loop probe"), authTestChain)
	if _, dropped := deliver(t, p, db, gw, gateway.InboundMessage{Text: string(codec.PrependVersionByte(body)), Source: "iridium_imt", FromAddr: "cloudloop"}); dropped != 0 {
		t.Fatal("frame dropped")
	}
	if !p.isRecentGatewayInjection("loop probe") {
		t.Fatal("the injected text is not marked: its mesh echo would be relayed back over satellite")
	}
	if p.isRecentGatewayInjection("a different text") {
		t.Fatal("the guard matches texts that were never injected")
	}
}

// The sender's half: a mesh text bound for iridium_imt is queued as the text,
// not as the JSON envelope. 8 characters used to cost 405 bytes on air.
func TestDispatchAccess_IMTCarriesTheTextNotTheEnvelope(t *testing.T) {
	d, db := setupTestDispatcher(t)
	defer db.Close()
	for id, ct := range map[string]string{"mesh_0": "mesh", "iridium_imt_0": "iridium_imt"} {
		if _, err := db.Exec(`INSERT OR IGNORE INTO interfaces (id, channel_type, label, enabled, config, ingress_transforms, egress_transforms)
			VALUES (?, ?, ?, 1, '{}', '[]', '[]')`, id, ct, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.InsertAccessRule(&database.AccessRule{InterfaceID: "mesh_0", Direction: "ingress", Priority: 1, Name: "ttc:imt",
		Enabled: true, Action: "forward", ForwardTo: "iridium_imt_0", Filters: "{}"}); err != nil {
		t.Fatal(err)
	}
	ae := rules.NewAccessEvaluator(db)
	if err := ae.ReloadFromDB(); err != nil {
		t.Fatal(err)
	}
	d.SetAccessEvaluator(ae)
	env, _ := json.Marshal(transport.MeshMessage{From: 0xde11f199, PortNum: 1, DecodedText: "sat test"})
	if n := d.DispatchAccess("mesh_0", rules.RouteMessage{Text: "sat test", From: "!de11f199", PortNum: 1}, env); n != 1 {
		t.Fatalf("want 1 delivery, got %d", n)
	}
	dl, _ := db.GetPendingDeliveries("iridium_imt_0", 10)
	if len(dl) != 1 || string(dl[0].Payload) != "sat test" {
		t.Fatalf("IMT payload = %q, want the bare text", dl[0].Payload)
	}
}

func TestMeshEnvelopeText(t *testing.T) {
	env, _ := json.Marshal(transport.MeshMessage{PortNum: 1, DecodedText: "hi"})
	for _, tc := range []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"envelope", string(env), "hi", true},
		{"bare text", "hi", "", false},
		{"text that looks like json", `{"note":"not a mesh packet"}`, "", false},
		{"empty", "", "", false},
	} {
		got, ok := meshEnvelopeText([]byte(tc.in))
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got (%q,%v), want (%q,%v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}
