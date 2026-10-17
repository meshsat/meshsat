package engine

import (
	"testing"

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
