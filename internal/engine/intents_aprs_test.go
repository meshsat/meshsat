package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/gateway"
	"meshsat/internal/transport"
)

// Stage 1b, APRS through the gateway adapter contract. [MESHSAT-1584]

type fakeAPRS struct {
	mu          sync.Mutex
	sends       []gateway.IntentSend
	answer      func(s gateway.IntentSend) gateway.IntentOutcome
	onSend      func(s gateway.IntentSend)
	sink        func(gateway.IntentReceipt)
	admitReason string
	next        int
}

func (f *fakeAPRS) Start(context.Context) error { return nil }
func (f *fakeAPRS) Stop() error                 { return nil }
func (f *fakeAPRS) Forward(context.Context, *transport.MeshMessage) error {
	return errors.New("not the intent path")
}
func (f *fakeAPRS) Enqueue(*transport.MeshMessage) error   { return errors.New("not the intent path") }
func (f *fakeAPRS) Receive() <-chan gateway.InboundMessage { return nil }
func (f *fakeAPRS) Status() gateway.GatewayStatus          { return gateway.GatewayStatus{} }
func (f *fakeAPRS) Type() string                           { return "aprs" }
func (f *fakeAPRS) IntentCorrKind() string                 { return gateway.APRSIntentCorrKind }
func (f *fakeAPRS) SetIntentReceiptSink(fn func(gateway.IntentReceipt)) {
	f.mu.Lock()
	f.sink = fn
	f.mu.Unlock()
}
func (f *fakeAPRS) IntentAdmit(string, []byte, bool) (string, string) { return f.admitReason, "fake" }
func (f *fakeAPRS) IntentNewCorr() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	return string(rune('A'+f.next)) + "0001", nil
}
func (f *fakeAPRS) IntentSend(_ context.Context, s gateway.IntentSend) gateway.IntentOutcome {
	f.mu.Lock()
	f.sends = append(f.sends, s)
	on, ans := f.onSend, f.answer
	f.mu.Unlock()
	if on != nil {
		on(s)
	}
	if ans != nil {
		return ans(s)
	}
	return gateway.IntentOutcome{Class: gateway.IntentAccepted, AcceptedBy: "radio"}
}
func (f *fakeAPRS) late(r gateway.IntentReceipt) {
	f.mu.Lock()
	sink := f.sink
	f.mu.Unlock()
	sink(r)
}
func (f *fakeAPRS) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sends) }

type fakeGwProv struct{ gw gateway.Gateway }

func (p *fakeGwProv) Gateways() []gateway.Gateway {
	if p.gw == nil {
		return nil
	}
	return []gateway.Gateway{p.gw}
}
func (p *fakeGwProv) GatewayByInterfaceID(id string) gateway.Gateway {
	if id == "aprs_0" && p.gw != nil {
		return p.gw
	}
	return nil
}

func useAPRS(t *testing.T, h *intentHarness, f *fakeAPRS) *fakeGwProv {
	t.Helper()
	if _, err := h.db.GetInterface("aprs_0"); err != nil {
		if err := h.db.InsertInterface(&database.Interface{ID: "aprs_0", ChannelType: "aprs", Label: "aprs", Enabled: true,
			Config: "{}", IngressTransforms: "[]", EgressTransforms: "[]"}); err != nil {
			t.Fatal(err)
		}
	}
	prov := &fakeGwProv{}
	if f != nil {
		prov.gw = f
	}
	h.d.gwProv = prov
	h.workers["aprs_0"] = &DeliveryWorker{db: h.db, channelID: "aprs_0", gwProv: prov, intents: h.esc,
		desc: channel.ChannelDescriptor{ID: "aprs_0", CanSend: true}}
	return prov
}

const aprsDest = "PA3ENC-7"

func aprsContact(h *intentHarness) string {
	return h.contact(directory.Address{Kind: directory.KindAPRS, Value: aprsDest})
}

func ackReceipt(corr, from, completes, event string) *gateway.IntentReceipt {
	return &gateway.IntentReceipt{CorrKind: gateway.APRSIntentCorrKind, CorrValue: corr, Scope: "aprs_0", Peer: from,
		Source: "station", Event: event, Completes: completes, Trust: "unauthenticated"}
}

// Admitted as an APRS route; the message id is chosen, stored with the
// handoff before the gateway has it, and is the one the gateway sends; the
// addressed station's ack within the call completes delivered_device.
func TestIntentAPRS_PreparedIDAndAckInTheCall(t *testing.T) {
	h := newIntentHarness(t)
	f := &fakeAPRS{}
	useAPRS(t, h, f)
	f.onSend = func(s gateway.IntentSend) {
		var state, kind, value string
		if err := h.db.QueryRow(`SELECT state, corr_kind, corr_value FROM delivery_transmissions ORDER BY id DESC LIMIT 1`).Scan(&state, &kind, &value); err != nil ||
			state != "handed_off" || kind != gateway.APRSIntentCorrKind || value != s.CorrValue || s.CorrValue == "" {
			t.Errorf("at the send: %s %s %q (%v); want handed_off with the id the gateway got (%q)", state, kind, value, err, s.CorrValue)
		}
	}
	f.answer = func(s gateway.IntentSend) gateway.IntentOutcome {
		return gateway.IntentOutcome{Class: gateway.IntentAccepted, AcceptedBy: "radio", Receipt: ackReceipt(s.CorrValue, aprsDest, database.OutcomeDeliveredDevice, "ack")}
	}
	id := h.submit(aprsContact(h), "meet at the gate", directory.StrategyPrimaryOnly)
	in, _ := h.db.GetIntent(id)
	if p := in.RoutePlan[0]; !p.Admitted || p.AdapterMode != modeAPRS || p.InterfaceID != "aprs_0" || p.Destination != aprsDest || p.CompletionRule != database.OutcomeDeliveredDevice {
		t.Fatalf("plan %+v", p)
	}
	h.work("aprs_0")
	h.drain()
	s := h.snap(id)
	if s.Intent.Outcome != database.OutcomeDeliveredDevice || s.Intent.State != database.IntentClosed || f.count() != 1 {
		t.Fatalf("%s/%s after %d sends; evidence %+v", s.Intent.State, s.Intent.Outcome, f.count(), s.Evidence)
	}
	if f.sends[0].Destination != aprsDest || f.sends[0].Text != "meet at the gate" || f.sends[0].Encrypted {
		t.Fatalf("send %+v", f.sends[0])
	}
}

// No answer within the call; the ack comes later through the sink and
// completes the intent; an answer for an id no kept transmission carries
// records nothing.
func TestIntentAPRS_LateAckThroughTheSink(t *testing.T) {
	h := newIntentHarness(t)
	f := &fakeAPRS{}
	useAPRS(t, h, f)
	id := h.submit(aprsContact(h), "late ack", directory.StrategyPrimaryOnly)
	h.work("aprs_0")
	h.drain()
	if s := h.snap(id); s.Intent.State != database.IntentActive || s.Transmissions[0].OutcomeClass != database.ClassAccepted {
		t.Fatalf("before the ack: %s/%s %+v", s.Intent.State, s.Intent.Outcome, s.Transmissions)
	}
	f.late(*ackReceipt("ZZZZZ", aprsDest, database.OutcomeDeliveredDevice, "ack"))
	var n int
	_ = h.db.QueryRow(`SELECT COUNT(*) FROM delivery_evidence`).Scan(&n)
	if n != 0 {
		t.Fatalf("an answer for an id no kept transmission carries was recorded (%d rows)", n)
	}
	f.late(*ackReceipt(f.sends[0].CorrValue, aprsDest, database.OutcomeDeliveredDevice, "ack"))
	h.drain()
	if s := h.snap(id); s.Intent.Outcome != database.OutcomeDeliveredDevice {
		t.Fatalf("after the late ack: %s/%s %+v", s.Intent.State, s.Intent.Outcome, s.Evidence)
	}
}

// A reject completes nothing: once the receipt allowance passes the intent
// closes accepted, not confirmed.
func TestIntentAPRS_RejectIsNotDelivered(t *testing.T) {
	h := newIntentHarness(t)
	f := &fakeAPRS{answer: func(s gateway.IntentSend) gateway.IntentOutcome {
		return gateway.IntentOutcome{Class: gateway.IntentAccepted, AcceptedBy: "radio", Receipt: ackReceipt(s.CorrValue, aprsDest, "", "rej")}
	}}
	useAPRS(t, h, f)
	id := h.submit(aprsContact(h), "rejected", directory.StrategyPrimaryOnly)
	h.work("aprs_0")
	h.drain()
	h.clock.Advance(3 * time.Minute)
	h.esc.Heartbeat()
	h.drain()
	if s := h.snap(id); s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeAcceptedUnconfirmed {
		t.Fatalf("%s/%s; want closed accepted_unconfirmed", s.Intent.State, s.Intent.Outcome)
	}
}

// Admission: the gateway's refusal skips the route; no running gateway is
// offline. Before the send: a gateway that went away refuses the attempt
// with no transmission and no call.
func TestIntentAPRS_AdmissionAndPreSend(t *testing.T) {
	h := newIntentHarness(t)
	f := &fakeAPRS{admitReason: reasonOversize}
	prov := useAPRS(t, h, f)
	_, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: aprsContact(h), Body: []byte("x"), Strategy: directory.StrategyPrimaryOnly})
	var nr *NoRouteError
	if !errors.As(err, &nr) || nr.Plan[0].Reason != reasonOversize {
		t.Fatalf("oversize at the gateway: %v", err)
	}
	prov.gw = nil
	c2 := h.contact(directory.Address{Kind: directory.KindAPRS, Value: "PD0ABC-1"})
	if _, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: c2, Body: []byte("x"), Strategy: directory.StrategyPrimaryOnly}); !errors.As(err, &nr) || nr.Plan[0].Reason != reasonOffline {
		t.Fatalf("no gateway: %v", err)
	}
	f.admitReason = ""
	prov.gw = f
	id := h.submit(c2, "gone before the send", directory.StrategyPrimaryOnly)
	prov.gw = nil
	h.work("aprs_0")
	s := h.snap(id)
	if len(s.Transmissions) != 0 || f.count() != 0 || s.Rows[0].Reason != reasonOffline {
		t.Fatalf("transmissions %+v, %d sends, attempt %+v", s.Transmissions, f.count(), s.Rows[0])
	}
}
