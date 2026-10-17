package engine

import (
	"context"
	"errors"
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

// The IMT route (the kit's 9704, paid) end to end: the real IMT gateway's
// intent adapter over a modem that says how far each MO got. [MESHSAT-1584]

// imtModem is a 9704 for the IMT gateway: SubmitMO answers in turn from
// answers (the last one repeats), legacy sends are counted.
type imtModem struct {
	mu      sync.Mutex
	answers []transport.IMTSubmit
	mos     [][]byte
	legacy  int
}

func (m *imtModem) SubmitMO(_ context.Context, data []byte) (transport.IMTSubmit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mos = append(m.mos, data)
	a := m.answers[0]
	if len(m.answers) > 1 {
		m.answers = m.answers[1:]
	}
	return a, nil
}
func (m *imtModem) count() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.mos) }

func (m *imtModem) Subscribe(context.Context) (<-chan transport.SatEvent, error) {
	return nil, errors.New("not in this test")
}
func (m *imtModem) Send(context.Context, []byte) (*transport.SatResult, error) {
	m.mu.Lock()
	m.legacy++
	m.mu.Unlock()
	return &transport.SatResult{}, nil
}
func (m *imtModem) SendText(ctx context.Context, s string) (*transport.SatResult, error) {
	return m.Send(ctx, []byte(s))
}
func (m *imtModem) Receive(context.Context) ([]byte, error) { return nil, nil }
func (m *imtModem) MailboxCheck(context.Context) (*transport.SatResult, error) {
	return &transport.SatResult{}, nil
}
func (m *imtModem) GetSignal(context.Context) (*transport.SignalInfo, error) {
	return &transport.SignalInfo{}, nil
}
func (m *imtModem) GetSignalFast(context.Context) (*transport.SignalInfo, error) {
	return &transport.SignalInfo{}, nil
}
func (m *imtModem) GetStatus(context.Context) (*transport.SatStatus, error) {
	return &transport.SatStatus{Connected: true, Type: "imt"}, nil
}
func (m *imtModem) GetFirmwareVersion(context.Context) (string, error) { return "test", nil }
func (m *imtModem) Close() error                                       { return nil }

const imtDest = "300258060512710"

// useIMT puts iridium_imt_0 on the harness with the real IMT gateway over
// modem, its messages recorded as going to imtDest, and a tariff of 17
// cents per 50 bytes.
func useIMT(t *testing.T, h *intentHarness, modem *imtModem, prov ifaceProv) {
	t.Helper()
	i := database.Interface{ID: "iridium_imt_0", ChannelType: "iridium_imt", Label: "iridium_imt_0", Enabled: true,
		Config: "{}", IngressTransforms: "[]", EgressTransforms: "[]"}
	if err := h.db.InsertInterface(&i); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.InsertTariff(database.Tariff{InterfaceID: "iridium_imt_0", UnitKind: "imt_credit", UnitBytes: 50,
		MinUnits: 1, PriceMinor: 17, Currency: "EUR"}); err != nil {
		t.Fatal(err)
	}
	prov["iridium_imt_0"] = gateway.NewIMTGateway(gateway.IridiumConfig{MODestination: imtDest}, modem, nil, nil)
	h.d.gwProv = prov
	h.workers["iridium_imt_0"] = &DeliveryWorker{db: h.db, channelID: "iridium_imt_0", gwProv: prov, intents: h.esc,
		desc: channel.ChannelDescriptor{ID: "iridium_imt_0", CanSend: true}}
}

func imtContact(h *intentHarness, addr string) string {
	return h.contact(directory.Address{Kind: directory.KindIridiumIMT, Value: addr})
}

// Acknowledged by the network: priced per 50 bytes, reserved, charged as an
// estimate, accepted by the satellite gateway, and closed accepted, not
// confirmed: nothing at the device says more.
func TestIntentIMT_AcknowledgedByTheNetwork(t *testing.T) {
	h := newIntentHarness(t)
	modem := &imtModem{answers: []transport.IMTSubmit{{Phase: transport.IMTAccepted, Status: "mo_ack_received"}}}
	useIMT(t, h, modem, ifaceProv{})
	body := strings.Repeat("s", 60) // two credits of 50 bytes
	sub, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: imtContact(h, imtDest), Body: []byte(body),
		Strategy: directory.StrategyPrimaryOnly, MaxCostCents: capOf(40)})
	if err != nil {
		t.Fatal(err)
	}
	id := sub.Intents[0].ID
	p := sub.Intents[0].RoutePlan[0]
	if !p.Admitted || !p.Paid || p.AdapterMode != modeIMT || p.UnitKind != "imt_credit" || p.EstUnits != 2 || p.EstMinor != 34 || p.InterfaceID != "iridium_imt_0" {
		t.Fatalf("plan %+v", p)
	}
	h.work("iridium_imt_0")
	h.drain()
	if modem.count() != 1 || string(modem.mos[0]) != body || modem.legacy != 0 {
		t.Fatalf("MOs %d (%q), legacy %d", modem.count(), modem.mos, modem.legacy)
	}
	tx := h.snap(id).Transmissions[0]
	if tx.AcceptedBy != "satellite_gateway" || tx.ChargedUnits != 2 || tx.ChargedMinor != 34 || tx.ChargeState != "estimated" || tx.CorrKind != gateway.IMTIntentCorrKind {
		t.Fatalf("transmission %+v", tx)
	}
	if r, inc, pos := intentMoney(t, h, id); r != 0 || inc != 34 || pos != 0 {
		t.Fatalf("reserved %d incurred %d possible %d", r, inc, pos)
	}
	h.clock.Advance(3 * time.Minute)
	h.esc.Heartbeat()
	h.drain()
	if s := h.snap(id); s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeAcceptedUnconfirmed {
		t.Fatalf("%s/%s; want closed accepted_unconfirmed", s.Intent.State, s.Intent.Outcome)
	}
}

// Another IMT address is not a route: the 9704 would deliver the message to
// the recorded destination instead. Nothing is written or sent.
func TestIntentIMT_OnlyTheRecordedDestination(t *testing.T) {
	h := newIntentHarness(t)
	modem := &imtModem{answers: []transport.IMTSubmit{{Phase: transport.IMTAccepted}}}
	useIMT(t, h, modem, ifaceProv{})
	_, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: imtContact(h, "300258060902280"), Body: []byte("x"), Strategy: directory.StrategyPrimaryOnly})
	var nr *NoRouteError
	if !errors.As(err, &nr) || nr.Plan[0].Reason != reasonUnsupported {
		t.Fatalf("another modem: %v", err)
	}
	if modem.count() != 0 {
		t.Fatal("an MO went")
	}
}

// An MO that may have left is paid for as possible and never sent again,
// across heartbeats and a restart (r3 D17); the intent closes "may have
// been sent".
func TestIntentIMT_MayHaveLeftIsNeverResent(t *testing.T) {
	h := newIntentHarness(t)
	modem := &imtModem{answers: []transport.IMTSubmit{{Phase: transport.IMTUnknown, Status: "message_transfer_timeout"}}}
	useIMT(t, h, modem, ifaceProv{})
	id := h.submit(imtContact(h, imtDest), "x", directory.StrategyPrimaryOnly)
	h.work("iridium_imt_0")
	h.drain()
	for i := 0; i < 3; i++ {
		h.clock.Advance(5 * time.Minute)
		h.esc.Heartbeat()
		h.drain()
		h.due()
		h.work("iridium_imt_0")
	}
	h.restart()
	h.workers["iridium_imt_0"] = &DeliveryWorker{db: h.db, channelID: "iridium_imt_0", gwProv: h.d.gwProv, intents: h.esc,
		desc: channel.ChannelDescriptor{ID: "iridium_imt_0", CanSend: true}}
	h.due()
	h.work("iridium_imt_0")
	if modem.count() != 1 {
		t.Fatalf("%d MOs; an MO that may have left was sent again", modem.count())
	}
	if _, inc, pos := intentMoney(t, h, id); inc != 0 || pos != 17 {
		t.Fatalf("incurred %d possible %d; want 0, 17", inc, pos)
	}
	if s := h.snap(id); s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeMayHaveBeenSent {
		t.Fatalf("%s/%s; want closed may_have_been_sent", s.Intent.State, s.Intent.Outcome)
	}
}

// No sky (the helper cancelled before transit): nothing left, nothing is
// charged, and the attempt is tried again; a refusal that would repeat (an
// invalid subscription) moves on to the next route at once.
func TestIntentIMT_NotSentThenTheNextRoute(t *testing.T) {
	h := newIntentHarness(t)
	modem := &imtModem{answers: []transport.IMTSubmit{
		{Phase: transport.IMTNotSent, Retry: true, Status: "message_cancelled_pre_transit"},
		{Phase: transport.IMTNotSent, Status: "subscription_invalid"},
	}}
	sms := &fakeSMS{answer: gateway.IntentOutcome{Class: gateway.IntentAccepted, AcceptedBy: "carrier", Charged: true, ChargedUnits: 1}}
	prov := ifaceProv{"cellular_0": sms}
	useIMT(t, h, modem, prov)
	h.workers["cellular_0"] = &DeliveryWorker{db: h.db, channelID: "cellular_0", gwProv: prov, intents: h.esc,
		desc: channel.ChannelDescriptor{ID: "cellular_0", CanSend: true}}
	smsTariff(t, h, 3)
	id := h.submit(h.contact(directory.Address{Kind: directory.KindIridiumIMT, Value: imtDest, PrimaryRank: 1},
		directory.Address{Kind: directory.KindSMS, Value: "+31612345678", PrimaryRank: 2}), "x", directory.StrategyOrderedFallback)
	h.work("iridium_imt_0")
	h.drain()
	if _, inc, pos := intentMoney(t, h, id); inc != 0 || pos != 0 {
		t.Fatalf("no sky: incurred %d possible %d", inc, pos)
	}
	if sms.count() != 0 {
		t.Fatal("fell back after a retryable not-sent")
	}
	h.due()
	h.work("iridium_imt_0")
	h.drain()
	if modem.count() != 2 {
		t.Fatalf("%d MOs; want the retry", modem.count())
	}
	h.work("cellular_0")
	h.drain()
	if sms.count() != 1 {
		t.Fatalf("%d SMS after an invalid subscription; want the next route", sms.count())
	}
	if _, inc, _ := intentMoney(t, h, id); inc != 3 {
		t.Fatalf("incurred %d; only the SMS costs", inc)
	}
}

// IMT as the next route: the SMS is refused for good, and the Escalator
// moves on to the 9704, which the network acknowledges.
func TestIntentIMT_AsTheNextRoute(t *testing.T) {
	h := newIntentHarness(t)
	modem := &imtModem{answers: []transport.IMTSubmit{{Phase: transport.IMTAccepted, Status: "mo_ack_received"}}}
	sms := &fakeSMS{answer: gateway.IntentOutcome{Class: gateway.IntentNotSent, Reason: "not_sent", Detail: "refused by the network"}}
	prov := ifaceProv{"cellular_0": sms}
	useIMT(t, h, modem, prov)
	h.workers["cellular_0"] = &DeliveryWorker{db: h.db, channelID: "cellular_0", gwProv: prov, intents: h.esc,
		desc: channel.ChannelDescriptor{ID: "cellular_0", CanSend: true}}
	smsTariff(t, h, 3)
	id := h.submit(h.contact(directory.Address{Kind: directory.KindSMS, Value: "+31612345678", PrimaryRank: 1},
		directory.Address{Kind: directory.KindIridiumIMT, Value: imtDest, PrimaryRank: 2}), "x", directory.StrategyOrderedFallback)
	h.work("cellular_0")
	h.drain()
	if sms.count() != 1 {
		t.Fatalf("%d SMS", sms.count())
	}
	h.work("iridium_imt_0")
	h.drain()
	if modem.count() != 1 {
		t.Fatalf("%d MOs; want the 9704 to take over", modem.count())
	}
	if _, inc, _ := intentMoney(t, h, id); inc != 17 {
		t.Fatalf("incurred %d; want the one MO", inc)
	}
}

// The live run of 17 Oct 2026 on parallax, replayed: the 9704 has no sky, its
// one attempt takes longer than the queue allowance and ends cancelled
// before transit; nothing is charged, the reservation is released, and the
// SMS takes over (T4). The status says what happened, not "offline".
func TestIntentIMT_NoSkyPastTheQueueAllowance(t *testing.T) {
	h := newIntentHarness(t)
	modem := &imtModem{answers: []transport.IMTSubmit{{Phase: transport.IMTNotSent, Retry: true, Status: "message_cancelled_pre_transit"}}}
	sms := &fakeSMS{answer: gateway.IntentOutcome{Class: gateway.IntentAccepted, AcceptedBy: "carrier", Charged: true, ChargedUnits: 1}}
	prov := ifaceProv{"cellular_0": sms}
	useIMT(t, h, modem, prov)
	h.workers["cellular_0"] = &DeliveryWorker{db: h.db, channelID: "cellular_0", gwProv: prov, intents: h.esc,
		desc: channel.ChannelDescriptor{ID: "cellular_0", CanSend: true}}
	smsTariff(t, h, 3)
	id := h.submit(h.contact(directory.Address{Kind: directory.KindIridiumIMT, Value: imtDest},
		directory.Address{Kind: directory.KindSMS, Value: "+31612345678"}), "x", directory.StrategyOrderedFallback)
	h.work("iridium_imt_0") // 150 s on the modem, then cancelled before transit
	h.clock.Advance(151 * time.Second)
	h.esc.Heartbeat()
	h.drain()
	h.work("cellular_0")
	h.drain()
	if modem.count() != 1 || sms.count() != 1 {
		t.Fatalf("%d MOs, %d SMS; want one each", modem.count(), sms.count())
	}
	if r, inc, pos := intentMoney(t, h, id); r != 0 || inc != 3 || pos != 0 {
		t.Fatalf("reserved %d incurred %d possible %d; want 0, 3, 0", r, inc, pos)
	}
	s := h.snap(id)
	if s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeAcceptedUnconfirmed {
		t.Fatalf("%s/%s", s.Intent.State, s.Intent.Outcome)
	}
	v := BuildIntentView(s)
	facts := strings.Join(v.Routes[0].Facts, "; ")
	if !strings.Contains(facts, "not sent within its queue allowance") || strings.Contains(facts, "offline") {
		t.Fatalf("IMT route facts %q", facts)
	}
}

// A stale or contradictory result from the modem side (an ack the helper
// says came for a payload it never supplied) never makes the ledger record
// acceptance or a charge: the transmission is unknown, its price possible,
// nothing is resent, and the intent closes "may have been sent", not
// "accepted". [review IMT-R1]
func TestIntentIMT_AContradictoryAckIsNeverAcceptance(t *testing.T) {
	h := newIntentHarness(t)
	modem := &imtModem{answers: []transport.IMTSubmit{{Phase: transport.IMTUnknown, Status: "mo_ack_received", Inconsistent: true}}}
	useIMT(t, h, modem, ifaceProv{})
	id := h.submit(imtContact(h, imtDest), "x", directory.StrategyPrimaryOnly)
	h.work("iridium_imt_0")
	h.drain()
	s := h.snap(id)
	tx := s.Transmissions[0]
	if tx.OutcomeClass != database.ClassUnknown || tx.AcceptedBy != "" || tx.ChargedMinor != 0 || !strings.Contains(tx.Detail, "contradicts itself") {
		t.Fatalf("transmission %+v", tx)
	}
	if _, inc, pos := intentMoney(t, h, id); inc != 0 || pos != 17 {
		t.Fatalf("incurred %d possible %d; want 0, 17", inc, pos)
	}
	h.clock.Advance(5 * time.Minute)
	h.esc.Heartbeat()
	h.drain()
	h.due()
	h.work("iridium_imt_0")
	if modem.count() != 1 {
		t.Fatalf("%d MOs", modem.count())
	}
	if s := h.snap(id); s.Intent.Outcome != database.OutcomeMayHaveBeenSent {
		t.Fatalf("outcome %s; want may_have_been_sent", s.Intent.Outcome)
	}
}
