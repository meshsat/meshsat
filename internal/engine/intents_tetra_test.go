package engine

import (
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/gateway"
)

// The TETRA route (owner ruling 17 Oct 2026) through the engine: a TETRA
// contact's talk group plans a free route on tetra_0 in adapter mode
// tetra, taken by the radio closes accepted, not confirmed (a group SDS
// carries no report), and a refused SDS hands over to the next route.
// [MESHSAT-1584]

func useTetra(t *testing.T, h *intentHarness, f *fakeAPRS, prov ifaceProv) {
	t.Helper()
	if err := h.db.InsertInterface(&database.Interface{ID: "tetra_0", ChannelType: "tetra", Label: "tetra_0", Enabled: true,
		Config: "{}", IngressTransforms: "[]", EgressTransforms: "[]"}); err != nil {
		t.Fatal(err)
	}
	prov["tetra_0"] = f
	h.d.gwProv = prov
	h.workers["tetra_0"] = &DeliveryWorker{db: h.db, channelID: "tetra_0", gwProv: prov, intents: h.esc,
		desc: channel.ChannelDescriptor{ID: "tetra_0", CanSend: true}}
}

func TestIntentTETRA_TakenByTheRadio(t *testing.T) {
	h := newIntentHarness(t)
	radio := &fakeAPRS{answer: func(gateway.IntentSend) gateway.IntentOutcome {
		return gateway.IntentOutcome{Class: gateway.IntentAccepted, AcceptedBy: "radio"}
	}}
	useTetra(t, h, radio, ifaceProv{})
	sub, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: h.contact(directory.Address{Kind: directory.KindTETRA, Value: "2000"}),
		Body: []byte("by tetra"), Strategy: directory.StrategyPrimaryOnly})
	if err != nil {
		t.Fatal(err)
	}
	p := sub.Intents[0].RoutePlan[0]
	if !p.Admitted || p.Paid || p.AdapterMode != modeTETRA || p.InterfaceID != "tetra_0" || p.CompletionRule != "" {
		t.Fatalf("plan %+v", p)
	}
	h.work("tetra_0")
	h.drain()
	if radio.count() != 1 || radio.sends[0].Destination != "2000" || radio.sends[0].Text != "by tetra" {
		t.Fatalf("sends %+v", radio.sends)
	}
	h.clock.Advance(3 * time.Minute)
	h.esc.Heartbeat()
	h.drain()
	if s := h.snap(sub.Intents[0].ID); s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeAcceptedUnconfirmed {
		t.Fatalf("%s/%s", s.Intent.State, s.Intent.Outcome)
	}
}

func TestIntentTETRA_RefusedThenTheNextRoute(t *testing.T) {
	h := newIntentHarness(t)
	radio := &fakeAPRS{answer: func(gateway.IntentSend) gateway.IntentOutcome {
		return gateway.IntentOutcome{Class: gateway.IntentNotSent, Reason: "skipped_oversize", Detail: "+CME ERROR: 24"}
	}}
	useTetra(t, h, radio, ifaceProv{})
	id := h.submit(h.contact(directory.Address{Kind: directory.KindTETRA, Value: "2000", PrimaryRank: 1}, meshAddr(2)),
		"tetra then mesh", directory.StrategyOrderedFallback)
	h.work("tetra_0")
	h.drain()
	h.work("mesh_0")
	h.drain()
	if radio.count() != 1 || h.mesh.sends() != 1 {
		t.Fatalf("%d SDS, %d mesh sends; want the mesh to take over", radio.count(), h.mesh.sends())
	}
	if s := h.snap(id); s.Intent.ActiveRound != 2 {
		t.Fatalf("round %d; want the second route", s.Intent.ActiveRound)
	}
}
