package engine

import (
	"strings"
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
// carries no report), a MeshSat Bridge's acknowledgement completes it as
// acked by a station, its silence is may have been sent with the next
// route taking over, and a refused SDS hands over to the next route.
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

func TestIntentTETRA_APeersAckCompletesIt(t *testing.T) {
	h := newIntentHarness(t)
	radio := &fakeAPRS{answer: func(s gateway.IntentSend) gateway.IntentOutcome {
		return gateway.IntentOutcome{Class: gateway.IntentAccepted, AcceptedBy: "radio",
			Receipt: &gateway.IntentReceipt{CorrKind: gateway.TetraIntentCorrKind, CorrValue: s.CorrValue, Peer: s.Destination,
				Source: "station", Event: "ack", Completes: database.OutcomeAckedStation, Trust: "unauthenticated"}}
	}}
	useTetra(t, h, radio, ifaceProv{})
	id := h.submit(h.contact(directory.Address{Kind: directory.KindTETRA, Value: "2000", PrimaryRank: 1}, meshAddr(2)),
		"acked by a bridge", directory.StrategyOrderedFallback)
	h.work("tetra_0")
	h.drain()
	s := h.snap(id)
	if s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeAckedStation || s.Intent.LastTransition != "T9" {
		t.Fatalf("%s/%s/%s; want closed acked_station at T9", s.Intent.State, s.Intent.Outcome, s.Intent.LastTransition)
	}
	if h.mesh.sends() != 0 {
		t.Fatal("the next route was used after the acknowledgement")
	}
}

func TestIntentTETRA_NoAckIsUnknownAndTheNextRouteTakesOver(t *testing.T) {
	h := newIntentHarness(t)
	radio := &fakeAPRS{answer: func(gateway.IntentSend) gateway.IntentOutcome {
		return gateway.IntentOutcome{Class: gateway.IntentUnknown, Detail: "may have been sent: no acknowledgement"}
	}}
	useTetra(t, h, radio, ifaceProv{})
	id := h.submit(h.contact(directory.Address{Kind: directory.KindTETRA, Value: "2000", PrimaryRank: 1}, meshAddr(2)),
		"nobody acked", directory.StrategyOrderedFallback)
	h.work("tetra_0")
	h.drain()
	h.work("mesh_0")
	h.drain()
	if radio.count() != 1 || h.mesh.sends() != 1 {
		t.Fatalf("%d SDS, %d mesh sends; want one SDS, never resent, then the mesh", radio.count(), h.mesh.sends())
	}
	if s := h.snap(id); s.Intent.ActiveRound != 2 || s.Intent.LastTransition != "T8" {
		t.Fatalf("round %d at %s; want the second route at T8", s.Intent.ActiveRound, s.Intent.LastTransition)
	}

	h2 := newIntentHarness(t)
	radio2 := &fakeAPRS{answer: radio.answer}
	useTetra(t, h2, radio2, ifaceProv{})
	sub, err := h2.esc.Submit(h2.ctx, IntentRequest{ContactID: h2.contact(directory.Address{Kind: directory.KindTETRA, Value: "2000"}),
		Body: []byte("only tetra"), Strategy: directory.StrategyPrimaryOnly})
	if err != nil {
		t.Fatal(err)
	}
	h2.work("tetra_0")
	h2.drain()
	h2.clock.Advance(3 * time.Minute)
	h2.esc.Heartbeat()
	h2.drain()
	h2.work("tetra_0")
	h2.drain()
	if s := h2.snap(sub.Intents[0].ID); s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeMayHaveBeenSent || radio2.count() != 1 {
		t.Fatalf("%s/%s after %d SDS; want closed may_have_been_sent after one", s.Intent.State, s.Intent.Outcome, radio2.count())
	}
}

// A MeshSat Bridge acknowledged the SDS before the radio reported it
// failed (review 2026-10-18 14:53, TETRA-ACK1): the adapter returns the
// station's receipt with the radio's report in the detail, and the intent
// closes acked by a station at T9 without the next route.
func TestIntentTETRA_AnAckBeforeTheRadiosFailureReportCompletesIt(t *testing.T) {
	h := newIntentHarness(t)
	radio := &fakeAPRS{answer: func(s gateway.IntentSend) gateway.IntentOutcome {
		return gateway.IntentOutcome{Class: gateway.IntentAccepted, AcceptedBy: "radio",
			Detail: "a MeshSat Bridge on the TETRA group acknowledged the SDS; the radio had reported: tetra: the radio reports the SDS not sent (status 5, +CME ERROR: 42)",
			Receipt: &gateway.IntentReceipt{CorrKind: gateway.TetraIntentCorrKind, CorrValue: s.CorrValue, Peer: s.Destination,
				Source: "station", Event: "ack", Completes: database.OutcomeAckedStation, Trust: "unauthenticated"}}
	}}
	useTetra(t, h, radio, ifaceProv{})
	id := h.submit(h.contact(directory.Address{Kind: directory.KindTETRA, Value: "2000", PrimaryRank: 1}, meshAddr(2)),
		"acked before the report", directory.StrategyOrderedFallback)
	h.work("tetra_0")
	h.drain()
	h.clock.Advance(3 * time.Minute)
	h.esc.Heartbeat()
	h.drain()
	h.work("mesh_0")
	h.drain()
	s := h.snap(id)
	if s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeAckedStation || s.Intent.LastTransition != "T9" {
		t.Fatalf("%s/%s/%s; want closed acked_station at T9", s.Intent.State, s.Intent.Outcome, s.Intent.LastTransition)
	}
	if radio.count() != 1 || h.mesh.sends() != 0 {
		t.Fatalf("%d SDS, %d mesh sends; want one SDS and no fallback", radio.count(), h.mesh.sends())
	}
	kept := false
	for _, tx := range s.Transmissions {
		kept = kept || strings.Contains(tx.Detail, "status 5")
	}
	if !kept {
		t.Fatal("the radio's report is not kept on the transmission")
	}
}
