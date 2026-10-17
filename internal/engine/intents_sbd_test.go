package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/gateway"
	"meshsat/internal/transport"
)

// The SBD route (a 9603, paid) end to end: the real SBD gateway's intent
// adapter over a modem whose sessions answer as scripted. No kit carries a
// 9603 since 1 Oct 2026, so this and the transport's emulator are the
// evidence. [MESHSAT-1584]

type sbdAnswer struct {
	res *transport.SatResult
	err error
}

// sbdModem answers each session in turn (the last answer repeats) and
// records every MO it was handed, text or binary.
type sbdModem struct {
	mu      sync.Mutex
	answers []sbdAnswer
	mos     []string
}

func (m *sbdModem) answer(mo string) (*transport.SatResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mos = append(m.mos, mo)
	a := m.answers[0]
	if len(m.answers) > 1 {
		m.answers = m.answers[1:]
	}
	return a.res, a.err
}
func (m *sbdModem) count() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.mos) }
func (m *sbdModem) Subscribe(context.Context) (<-chan transport.SatEvent, error) {
	return nil, errors.New("not in this test")
}
func (m *sbdModem) Send(_ context.Context, data []byte) (*transport.SatResult, error) {
	return m.answer(string(data))
}
func (m *sbdModem) SendText(_ context.Context, s string) (*transport.SatResult, error) {
	return m.answer(s)
}
func (m *sbdModem) Receive(context.Context) ([]byte, error) { return nil, nil }
func (m *sbdModem) MailboxCheck(context.Context) (*transport.SatResult, error) {
	return &transport.SatResult{}, nil
}
func (m *sbdModem) GetSignal(context.Context) (*transport.SignalInfo, error) {
	return &transport.SignalInfo{}, nil
}
func (m *sbdModem) GetSignalFast(context.Context) (*transport.SignalInfo, error) {
	return &transport.SignalInfo{}, nil
}
func (m *sbdModem) GetStatus(context.Context) (*transport.SatStatus, error) {
	return &transport.SatStatus{Connected: true, Type: "sbd"}, nil
}
func (m *sbdModem) GetFirmwareVersion(context.Context) (string, error) { return "test", nil }
func (m *sbdModem) Close() error                                       { return nil }

const sbdDest = "300434067943980"

var sbdTaken = sbdAnswer{res: &transport.SatResult{MOStatus: 0, MOMSN: 12}}

// useSBD puts iridium_0 on the harness with the real SBD gateway over
// modem, its messages recorded as going to sbdDest, and a tariff of 12
// cents per 50 bytes.
func useSBD(t *testing.T, h *intentHarness, modem *sbdModem, prov ifaceProv, cfg gateway.IridiumConfig) {
	t.Helper()
	i := database.Interface{ID: "iridium_0", ChannelType: "iridium", Label: "iridium_0", Enabled: true,
		Config: "{}", IngressTransforms: "[]", EgressTransforms: "[]"}
	if err := h.db.InsertInterface(&i); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.InsertTariff(database.Tariff{InterfaceID: "iridium_0", UnitKind: "sbd_credit", UnitBytes: 50,
		MinUnits: 1, PriceMinor: 12, Currency: "EUR"}); err != nil {
		t.Fatal(err)
	}
	cfg.MODestination = sbdDest
	prov["iridium_0"] = gateway.NewSBDGateway(cfg, modem, h.db, nil)
	h.d.gwProv = prov
	h.workers["iridium_0"] = &DeliveryWorker{db: h.db, channelID: "iridium_0", gwProv: prov, intents: h.esc,
		desc: channel.ChannelDescriptor{ID: "iridium_0", CanSend: true}}
}

func sbdContact(h *intentHarness, addr string) string {
	return h.contact(directory.Address{Kind: directory.KindIridiumSBD, Value: addr})
}

// Taken by the Iridium gateway: priced on the MO the provider bills (the
// compact frame for a text over 120 characters), reserved, charged as an
// estimate, and closed accepted, not confirmed.
func TestIntentSBD_TakenByTheIridiumGateway(t *testing.T) {
	h := newIntentHarness(t)
	modem := &sbdModem{answers: []sbdAnswer{sbdTaken}}
	useSBD(t, h, modem, ifaceProv{}, gateway.IridiumConfig{})
	body := strings.Repeat("s", 130) // over 120 characters: the compact frame, 10 + 1 + 130 = 141 bytes, 3 credits
	sub, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: sbdContact(h, sbdDest), Body: []byte(body),
		Strategy: directory.StrategyPrimaryOnly, MaxCostCents: capOf(40)})
	if err != nil {
		t.Fatal(err)
	}
	id := sub.Intents[0].ID
	p := sub.Intents[0].RoutePlan[0]
	if !p.Admitted || !p.Paid || p.AdapterMode != modeSBD || p.UnitKind != "sbd_credit" || p.EstUnits != 3 || p.EstMinor != 36 || p.InterfaceID != "iridium_0" {
		t.Fatalf("plan %+v", p)
	}
	h.work("iridium_0")
	h.drain()
	if modem.count() != 1 || len(modem.mos[0]) != 141 || !strings.HasSuffix(modem.mos[0], body) {
		t.Fatalf("MOs %d, first %d B", modem.count(), len(modem.mos[0]))
	}
	tx := h.snap(id).Transmissions[0]
	if tx.AcceptedBy != "satellite_gateway" || tx.ChargedUnits != 3 || tx.ChargedMinor != 36 || tx.ChargeState != "estimated" || tx.CorrKind != gateway.SBDIntentCorrKind {
		t.Fatalf("transmission %+v", tx)
	}
	if r, inc, pos := intentMoney(t, h, id); r != 0 || inc != 36 || pos != 0 {
		t.Fatalf("reserved %d incurred %d possible %d", r, inc, pos)
	}
	h.clock.Advance(3 * time.Minute)
	h.esc.Heartbeat()
	h.drain()
	if s := h.snap(id); s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeAcceptedUnconfirmed {
		t.Fatalf("%s/%s; want closed accepted_unconfirmed", s.Intent.State, s.Intent.Outcome)
	}
}

// The frame, not the wire, is priced: a 90-byte wire that is not plain
// text (a version byte) becomes a 101-byte compact frame, three credits
// where the wire alone would be two.
func TestIntentSBD_TheFrameIsWhatIsPriced(t *testing.T) {
	h := newIntentHarness(t)
	modem := &sbdModem{answers: []sbdAnswer{sbdTaken}}
	useSBD(t, h, modem, ifaceProv{}, gateway.IridiumConfig{})
	wire := "\x01" + strings.Repeat("Q", 89) // a version byte: not plain text, so the compact frame: 101 bytes
	sub, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: sbdContact(h, sbdDest), Body: []byte(wire), Strategy: directory.StrategyPrimaryOnly})
	if err != nil {
		t.Fatal(err)
	}
	if p := sub.Intents[0].RoutePlan[0]; p.EstUnits != 3 || p.EstMinor != 36 {
		t.Fatalf("plan %+v; want the 101-byte frame priced, 3 credits", p)
	}
	h.work("iridium_0")
	h.drain()
	if tx := h.snap(sub.Intents[0].ID).Transmissions[0]; tx.ChargedUnits != 3 || tx.ChargedMinor != 36 || tx.AuthorizedMinor != 36 {
		t.Fatalf("transmission %+v; the reservation and the charge are the frame's", tx)
	}
}

// Another SBD address is not a route, and a text the frame would cut is
// not one either. Nothing is written or sent.
func TestIntentSBD_OnlyTheRecordedDestinationAndWhatFitsWhole(t *testing.T) {
	h := newIntentHarness(t)
	modem := &sbdModem{answers: []sbdAnswer{sbdTaken}}
	useSBD(t, h, modem, ifaceProv{}, gateway.IridiumConfig{})
	for _, tc := range []struct {
		addr, body, reason string
	}{
		{"300434067943981", "x", reasonUnsupported},
		{sbdDest, strings.Repeat("x", 256), reasonOversize},
	} {
		_, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: sbdContact(h, tc.addr), Body: []byte(tc.body), Strategy: directory.StrategyPrimaryOnly})
		var nr *NoRouteError
		if !errors.As(err, &nr) || nr.Plan[0].Reason != tc.reason {
			t.Fatalf("%s, %d B: %v; want %s", tc.addr, len(tc.body), err, tc.reason)
		}
	}
	if modem.count() != 0 {
		t.Fatal("an MO went")
	}
}

// A session that may have delivered the MO (status 13, an RF drop, a lost
// answer) is paid for as possible and never sent again, across heartbeats
// and a restart (r3 D17); the intent closes "may have been sent".
func TestIntentSBD_MayHaveLeftIsNeverResent(t *testing.T) {
	for _, a := range []sbdAnswer{
		{res: &transport.SatResult{MOStatus: 13}},
		{res: &transport.SatResult{MOStatus: 18}},
		{err: errors.New("SBDIX failed: read timeout")},
		{res: &transport.SatResult{MOStatus: 0}, err: fmt.Errorf("SBDIX failed: %w", transport.ErrOutcomeUnknown)},
	} {
		h := newIntentHarness(t)
		modem := &sbdModem{answers: []sbdAnswer{a}}
		useSBD(t, h, modem, ifaceProv{}, gateway.IridiumConfig{})
		id := h.submit(sbdContact(h, sbdDest), "x", directory.StrategyPrimaryOnly)
		h.work("iridium_0")
		h.drain()
		for i := 0; i < 3; i++ {
			h.clock.Advance(5 * time.Minute)
			h.esc.Heartbeat()
			h.drain()
			h.due()
			h.work("iridium_0")
		}
		h.restart()
		h.workers["iridium_0"] = &DeliveryWorker{db: h.db, channelID: "iridium_0", gwProv: h.d.gwProv, intents: h.esc,
			desc: channel.ChannelDescriptor{ID: "iridium_0", CanSend: true}}
		h.due()
		h.work("iridium_0")
		if modem.count() != 1 {
			t.Fatalf("%+v: %d MOs; an MO that may have left was sent again", a, modem.count())
		}
		if _, inc, pos := intentMoney(t, h, id); inc != 0 || pos != 12 {
			t.Fatalf("%+v: incurred %d possible %d; want 0, 12", a, inc, pos)
		}
		if s := h.snap(id); s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeMayHaveBeenSent {
			t.Fatalf("%+v: %s/%s; want closed may_have_been_sent", a, s.Intent.State, s.Intent.Outcome)
		}
	}
}

// No network service (32): nothing left, nothing charged, tried again; a
// refusal that would repeat (15, access denied) moves on to the next route.
func TestIntentSBD_NotSentThenTheNextRoute(t *testing.T) {
	h := newIntentHarness(t)
	modem := &sbdModem{answers: []sbdAnswer{
		{res: &transport.SatResult{MOStatus: 32}},
		{res: &transport.SatResult{MOStatus: 15}},
	}}
	sms := &fakeSMS{answer: gateway.IntentOutcome{Class: gateway.IntentAccepted, AcceptedBy: "carrier", Charged: true, ChargedUnits: 1}}
	prov := ifaceProv{"cellular_0": sms}
	useSBD(t, h, modem, prov, gateway.IridiumConfig{})
	h.workers["cellular_0"] = &DeliveryWorker{db: h.db, channelID: "cellular_0", gwProv: prov, intents: h.esc,
		desc: channel.ChannelDescriptor{ID: "cellular_0", CanSend: true}}
	smsTariff(t, h, 3)
	id := h.submit(h.contact(directory.Address{Kind: directory.KindIridiumSBD, Value: sbdDest, PrimaryRank: 1},
		directory.Address{Kind: directory.KindSMS, Value: "+31612345678", PrimaryRank: 2}), "x", directory.StrategyOrderedFallback)
	h.work("iridium_0")
	h.drain()
	if _, inc, pos := intentMoney(t, h, id); inc != 0 || pos != 0 {
		t.Fatalf("no network: incurred %d possible %d", inc, pos)
	}
	if sms.count() != 0 {
		t.Fatal("fell back after a retryable not-sent")
	}
	h.due()
	h.work("iridium_0")
	h.drain()
	if modem.count() != 2 {
		t.Fatalf("%d MOs; want the retry", modem.count())
	}
	h.work("cellular_0")
	h.drain()
	if sms.count() != 1 {
		t.Fatalf("%d SMS after access denied; want the next route", sms.count())
	}
	if _, inc, _ := intentMoney(t, h, id); inc != 3 {
		t.Fatalf("incurred %d; only the SMS costs", inc)
	}
}

// The gateway's own credit budget is the send-time check (r3 S6): a send it
// cannot afford never reaches the modem, and the next route takes over.
func TestIntentSBD_AnExhaustedCreditBudgetMovesOn(t *testing.T) {
	h := newIntentHarness(t)
	modem := &sbdModem{answers: []sbdAnswer{sbdTaken}}
	sms := &fakeSMS{answer: gateway.IntentOutcome{Class: gateway.IntentAccepted, AcceptedBy: "carrier", Charged: true, ChargedUnits: 1}}
	prov := ifaceProv{"cellular_0": sms}
	useSBD(t, h, modem, prov, gateway.IridiumConfig{DailyBudget: 2})
	h.workers["cellular_0"] = &DeliveryWorker{db: h.db, channelID: "cellular_0", gwProv: prov, intents: h.esc,
		desc: channel.ChannelDescriptor{ID: "cellular_0", CanSend: true}}
	smsTariff(t, h, 3)
	if err := h.db.InsertCreditUsage(nil, 2, nil); err != nil {
		t.Fatal(err)
	}
	h.submit(h.contact(directory.Address{Kind: directory.KindIridiumSBD, Value: sbdDest, PrimaryRank: 1},
		directory.Address{Kind: directory.KindSMS, Value: "+31612345678", PrimaryRank: 2}), "x", directory.StrategyOrderedFallback)
	h.work("iridium_0")
	h.drain()
	h.work("cellular_0")
	h.drain()
	if modem.count() != 0 || sms.count() != 1 {
		t.Fatalf("%d MOs, %d SMS; want the budget to refuse the MO and the SMS to go", modem.count(), sms.count())
	}
}
