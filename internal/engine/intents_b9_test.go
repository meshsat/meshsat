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

// B9: paid intent routes, priced with the interface's tariff, reserved
// against the intent's cap before the send, charged as the adapter reports.
// SMS through a fake billed adapter on cellular_0. [MESHSAT-1584]

type fakeSMS struct {
	mu     sync.Mutex
	sends  []gateway.IntentSend
	answer gateway.IntentOutcome
	onSend func(gateway.IntentSend)
	n      int
}

func (f *fakeSMS) Start(context.Context) error { return nil }
func (f *fakeSMS) Stop() error                 { return nil }
func (f *fakeSMS) Forward(context.Context, *transport.MeshMessage) error {
	return errors.New("not the intent path")
}
func (f *fakeSMS) Enqueue(*transport.MeshMessage) error              { return errors.New("not the intent path") }
func (f *fakeSMS) Receive() <-chan gateway.InboundMessage            { return nil }
func (f *fakeSMS) Status() gateway.GatewayStatus                     { return gateway.GatewayStatus{} }
func (f *fakeSMS) Type() string                                      { return "cellular" }
func (f *fakeSMS) IntentCorrKind() string                            { return gateway.SMSIntentCorrKind }
func (f *fakeSMS) SetIntentReceiptSink(func(gateway.IntentReceipt))  {}
func (f *fakeSMS) IntentAdmit(string, []byte, bool) (string, string) { return "", "" }
func (f *fakeSMS) IntentBilled() bool                                { return true }
func (f *fakeSMS) IntentNewCorr() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	return "ref" + string(rune('a'+f.n)), nil
}
func (f *fakeSMS) IntentSend(_ context.Context, s gateway.IntentSend) gateway.IntentOutcome {
	f.mu.Lock()
	f.sends = append(f.sends, s)
	on, a := f.onSend, f.answer
	f.mu.Unlock()
	if on != nil {
		on(s)
	}
	return a
}
func (f *fakeSMS) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sends) }

type ifaceProv map[string]gateway.Gateway

func (p ifaceProv) Gateways() []gateway.Gateway {
	var out []gateway.Gateway
	for _, g := range p {
		out = append(out, g)
	}
	return out
}
func (p ifaceProv) GatewayByInterfaceID(id string) gateway.Gateway { return p[id] }

func useSMS(t *testing.T, h *intentHarness, f *fakeSMS) {
	t.Helper()
	prov := ifaceProv{"cellular_0": f}
	h.d.gwProv = prov
	h.workers["cellular_0"] = &DeliveryWorker{db: h.db, channelID: "cellular_0", gwProv: prov, intents: h.esc,
		desc: channel.ChannelDescriptor{ID: "cellular_0", CanSend: true}}
}

func smsTariff(t *testing.T, h *intentHarness, priceMinor int64) database.Tariff {
	t.Helper()
	tr, err := h.db.InsertTariff(database.Tariff{InterfaceID: "cellular_0", UnitKind: "sms", MinUnits: 1, PriceMinor: priceMinor, Currency: "EUR"})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func smsContact(h *intentHarness) string {
	return h.contact(directory.Address{Kind: directory.KindSMS, Value: "+31612345678"})
}

func intentMoney(t *testing.T, h *intentHarness, id string) (reserved, incurred, possible int64) {
	t.Helper()
	in, err := h.db.GetIntent(id)
	if err != nil {
		t.Fatal(err)
	}
	return in.ReservedMinor, in.IncurredMinor, in.PossibleMinor
}

func capOf(c int) *int { return &c }

// No tariff: a paid route is never priced at zero, it is skipped.
func TestIntentB9_NoTariffSkipsThePaidRoute(t *testing.T) {
	h := newIntentHarness(t)
	f := &fakeSMS{}
	useSMS(t, h, f)
	_, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: smsContact(h), Body: []byte("x"), Strategy: directory.StrategyPrimaryOnly})
	var nr *NoRouteError
	if !errors.As(err, &nr) || nr.Plan[0].Reason != reasonNoPrice || !nr.Plan[0].Paid {
		t.Fatalf("no tariff: %v", err)
	}
}

// Priced at admission; the price is reserved before the call; an accepted,
// charged SMS is incurred and nothing stays reserved. It closes accepted,
// not confirmed, never delivered (S8).
func TestIntentB9_ReservedThenCharged(t *testing.T) {
	h := newIntentHarness(t)
	f := &fakeSMS{answer: gateway.IntentOutcome{Class: gateway.IntentAccepted, AcceptedBy: "carrier", Charged: true, ChargedUnits: 1}}
	useSMS(t, h, f)
	tr := smsTariff(t, h, 3)
	sub, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: smsContact(h), Body: []byte("pay for me"), Strategy: directory.StrategyPrimaryOnly, MaxCostCents: capOf(10)})
	if err != nil {
		t.Fatal(err)
	}
	id := sub.Intents[0].ID
	p := sub.Intents[0].RoutePlan[0]
	if !p.Admitted || !p.Paid || p.AdapterMode != modeSMS || p.EstUnits != 1 || p.EstMinor != 3 || p.TariffID != tr.ID || p.UnitKind != "sms" {
		t.Fatalf("plan %+v", p)
	}
	f.onSend = func(gateway.IntentSend) {
		if r, _, _ := intentMoney(t, h, id); r != 3 {
			t.Errorf("reserved %d during the call; want 3", r)
		}
	}
	h.work("cellular_0")
	h.drain()
	if r, inc, pos := intentMoney(t, h, id); r != 0 || inc != 3 || pos != 0 {
		t.Fatalf("reserved %d incurred %d possible %d; want 0, 3, 0", r, inc, pos)
	}
	tx := h.snap(id).Transmissions[0]
	if tx.ChargedMinor != 3 || tx.ChargedUnits != 1 || tx.ChargeState != "estimated" || tx.AcceptedBy != "carrier" {
		t.Fatalf("transmission %+v", tx)
	}
	h.clock.Advance(3 * time.Minute)
	h.esc.Heartbeat()
	h.drain()
	if s := h.snap(id); s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeAcceptedUnconfirmed {
		t.Fatalf("%s/%s; want closed accepted_unconfirmed", s.Intent.State, s.Intent.Outcome)
	}
}

// Unknown with no charge reported: the reservation stays counted as
// possible. Not sent: it is released.
func TestIntentB9_UnknownKeepsPossibleNotSentReleases(t *testing.T) {
	for _, tc := range []struct {
		name               string
		answer             gateway.IntentOutcome
		reserved, possible int64
	}{
		{"unknown", gateway.IntentOutcome{Class: gateway.IntentUnknown, Detail: "no answer from the network"}, 0, 3},
		{"not_sent", gateway.IntentOutcome{Class: gateway.IntentNotSent, Reason: "not_sent", Detail: "refused"}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newIntentHarness(t)
			useSMS(t, h, &fakeSMS{answer: tc.answer})
			smsTariff(t, h, 3)
			id := h.submit(smsContact(h), "x", directory.StrategyPrimaryOnly)
			h.work("cellular_0")
			h.drain()
			if r, inc, pos := intentMoney(t, h, id); r != tc.reserved || inc != 0 || pos != tc.possible {
				t.Fatalf("reserved %d incurred %d possible %d; want %d, 0, %d", r, inc, pos, tc.reserved, tc.possible)
			}
		})
	}
}

// A cap below one SMS: the route is not tried.
func TestIntentB9_CapBelowOneSubmission(t *testing.T) {
	h := newIntentHarness(t)
	useSMS(t, h, &fakeSMS{})
	smsTariff(t, h, 3)
	_, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: smsContact(h), Body: []byte("x"), Strategy: directory.StrategyPrimaryOnly, MaxCostCents: capOf(2)})
	var nr *NoRouteError
	if !errors.As(err, &nr) || nr.Plan[0].Reason != reasonBudget {
		t.Fatalf("cap 2 for a 3-cent SMS: %v", err)
	}
}

// The tariff goes up after admission beyond the cap: before the send the
// figure is priced again and refused as reprice, with no call; under the
// cap the new figure is the one reserved.
func TestIntentB9_RepriceBeforeTheSend(t *testing.T) {
	for _, tc := range []struct {
		name    string
		price   int64
		sends   int
		reason  string
		charged int64
	}{
		{"over_the_cap", 7, 0, "reprice", 0},
		{"under_the_cap", 5, 1, "", 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newIntentHarness(t)
			f := &fakeSMS{answer: gateway.IntentOutcome{Class: gateway.IntentAccepted, AcceptedBy: "carrier", Charged: true, ChargedUnits: 1}}
			useSMS(t, h, f)
			smsTariff(t, h, 3)
			sub, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: smsContact(h), Body: []byte("x"), Strategy: directory.StrategyPrimaryOnly, MaxCostCents: capOf(6)})
			if err != nil {
				t.Fatal(err)
			}
			id := sub.Intents[0].ID
			smsTariff(t, h, tc.price)
			h.work("cellular_0")
			h.drain()
			s := h.snap(id)
			if f.count() != tc.sends {
				t.Fatalf("%d sends; want %d", f.count(), tc.sends)
			}
			if tc.reason != "" && (s.Rows[0].Reason != tc.reason || len(s.Transmissions) != 0) {
				t.Fatalf("attempt %+v, transmissions %+v; want %s with none", s.Rows[0], s.Transmissions, tc.reason)
			}
			if _, inc, _ := intentMoney(t, h, id); inc != tc.charged {
				t.Fatalf("incurred %d; want %d", inc, tc.charged)
			}
		})
	}
}
