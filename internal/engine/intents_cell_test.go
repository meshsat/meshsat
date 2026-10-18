package engine

import (
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/gateway"
)

// The 2G cell route (owner ruling 17 Oct 2026: its own contact kind,
// CELL) through the engine: the phone's short number plans a free route on
// cell_0 that completes delivered to the handset, and the phone's
// acknowledgement within the call closes the intent so. [MESHSAT-1584]

func TestIntentCell_TheHandsetsReceiptClosesIt(t *testing.T) {
	h := newIntentHarness(t)
	if err := h.db.InsertInterface(&database.Interface{ID: "cell_0", ChannelType: "cell", Label: "cell_0", Enabled: true,
		Config: "{}", IngressTransforms: "[]", EgressTransforms: "[]"}); err != nil {
		t.Fatal(err)
	}
	phone := &fakeAPRS{answer: func(s gateway.IntentSend) gateway.IntentOutcome {
		return gateway.IntentOutcome{Class: gateway.IntentAccepted, AcceptedBy: "handset",
			Receipt: &gateway.IntentReceipt{CorrKind: gateway.CellIntentCorrKind, CorrValue: s.CorrValue, Peer: s.Destination,
				Source: "handset", Event: "submit_sm_resp:0", Completes: database.OutcomeDeliveredHandset, Trust: "smpp_session"}}
	}}
	prov := ifaceProv{"cell_0": phone}
	h.d.gwProv = prov
	h.workers["cell_0"] = &DeliveryWorker{db: h.db, channelID: "cell_0", gwProv: prov, intents: h.esc,
		desc: channel.ChannelDescriptor{ID: "cell_0", CanSend: true}}
	sub, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: h.contact(directory.Address{Kind: directory.KindCell, Value: "1001"}),
		Body: []byte("on our cell"), Strategy: directory.StrategyPrimaryOnly})
	if err != nil {
		t.Fatal(err)
	}
	p := sub.Intents[0].RoutePlan[0]
	if !p.Admitted || p.Paid || p.AdapterMode != modeCell || p.InterfaceID != "cell_0" || p.CompletionRule != database.OutcomeDeliveredHandset {
		t.Fatalf("plan %+v", p)
	}
	h.work("cell_0")
	h.drain()
	s := h.snap(sub.Intents[0].ID)
	if phone.count() != 1 || s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeDeliveredHandset {
		t.Fatalf("%d sends, %s/%s; want closed delivered_handset", phone.count(), s.Intent.State, s.Intent.Outcome)
	}
}

// A delivery failure (0xFE) is unknown at the adapter (the phone may have
// the text): the engine records it may have been sent, never sends it on
// the cell again by itself, and an ordered fallback takes the next route
// (review 2026-10-18 13:47, CELL-R1).
func TestIntentCell_ADeliveryFailureIsUnknownNotAbsent(t *testing.T) {
	useCell := func(h *intentHarness, phone *fakeAPRS) {
		if err := h.db.InsertInterface(&database.Interface{ID: "cell_0", ChannelType: "cell", Label: "cell_0", Enabled: true,
			Config: "{}", IngressTransforms: "[]", EgressTransforms: "[]"}); err != nil {
			t.Fatal(err)
		}
		prov := ifaceProv{"cell_0": phone}
		h.d.gwProv = prov
		h.workers["cell_0"] = &DeliveryWorker{db: h.db, channelID: "cell_0", gwProv: prov, intents: h.esc,
			desc: channel.ChannelDescriptor{ID: "cell_0", CanSend: true}}
	}
	fe := func(gateway.IntentSend) gateway.IntentOutcome {
		return gateway.IntentOutcome{Class: gateway.IntentUnknown,
			Detail: "may have been sent: cell: the cell could not complete the delivery to the phone: " + gateway.ErrCellDeliveryFailure.Error()}
	}

	h := newIntentHarness(t)
	phone := &fakeAPRS{answer: fe}
	useCell(h, phone)
	sub, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: h.contact(directory.Address{Kind: directory.KindCell, Value: "1001"}),
		Body: []byte("only the cell"), Strategy: directory.StrategyPrimaryOnly})
	if err != nil {
		t.Fatal(err)
	}
	h.work("cell_0")
	h.drain()
	h.clock.Advance(3 * time.Minute)
	h.esc.Heartbeat()
	h.drain()
	h.work("cell_0")
	h.drain()
	if s := h.snap(sub.Intents[0].ID); s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeMayHaveBeenSent || phone.count() != 1 {
		t.Fatalf("%s/%s after %d submits; want closed may_have_been_sent after one", s.Intent.State, s.Intent.Outcome, phone.count())
	}

	h2 := newIntentHarness(t)
	phone2 := &fakeAPRS{answer: fe}
	useCell(h2, phone2)
	id := h2.submit(h2.contact(directory.Address{Kind: directory.KindCell, Value: "1001", PrimaryRank: 1}, meshAddr(2)),
		"cell then mesh", directory.StrategyOrderedFallback)
	h2.work("cell_0")
	h2.drain()
	h2.work("mesh_0")
	h2.drain()
	h2.work("cell_0")
	h2.drain()
	if phone2.count() != 1 || h2.mesh.sends() != 1 {
		t.Fatalf("%d submits, %d mesh sends; want one submit, never resent, then the mesh", phone2.count(), h2.mesh.sends())
	}
	if s := h2.snap(id); s.Intent.ActiveRound != 2 || s.Intent.LastTransition != "T8" {
		t.Fatalf("round %d at %s; want the second route at T8", s.Intent.ActiveRound, s.Intent.LastTransition)
	}
}
