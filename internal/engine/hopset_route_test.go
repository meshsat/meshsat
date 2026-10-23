package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/gateway"
	"meshsat/internal/lxmf"
	"meshsat/internal/transport"
)

// The hop set through the routing consumers it gates (design D-7): the
// delivery intents' round 1 and next route, a failover group's retry, and
// the evidence the worker and the mesh receipts feed it. [MESHSAT-1650]

// gatingHopSet makes a gating hop set over the harness's links, on a clock
// the test moves, and wires it as the dispatcher's sink and gate.
func gatingHopSet(t *testing.T, d *Dispatcher, list func() ([]database.Interface, error)) (*HopSetManager, *time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 23, 14, 0, 0, 0, time.UTC)
	m := NewHopSetManager(DefaultHopSetConfig(), list, HopOn)
	m.clock = func() time.Time { return now }
	d.SetHopSigns(m)
	d.SetRouteGate(m)
	return m, &now
}

func hopState(t *testing.T, m *HopSetManager, id string) HopLinkStatus {
	t.Helper()
	for _, l := range m.Status().Links {
		if l.InterfaceID == id {
			return l
		}
	}
	t.Fatalf("no link %s", id)
	return HopLinkStatus{}
}

// An excluded link is not round 1; a link the floor retains still is; a
// link re-admitted after its dwell is round 1 again.
func TestHopSetGatesTheIntents(t *testing.T) {
	h := newIntentHarness(t)
	m, now := gatingHopSet(t, h.d, h.db.GetAllInterfaces)
	c := h.contact(meshAddr(1), lxmfAddr(2))

	m.Negative("mesh_0", "delivery", "!0000abcd", "no_ack")
	m.Negative("mesh_0", "delivery", "!0000beef", "no_ack")
	*now = now.Add(11 * time.Second)
	if got := firstRowChannel(t, h, h.submit(c, "one", directory.StrategyOrderedFallback)); got != "lxmf_0" {
		t.Fatalf("mesh excluded: round 1 on %s, want lxmf_0", got)
	}

	// lxmf_0 earns two signs too, but excluding it would leave one usable
	// link (cellular_0): the floor of two retains it, and it is still tried.
	m.Negative("lxmf_0", "delivery", "a", "no_ack")
	m.Negative("lxmf_0", "delivery", "b", "no_ack")
	*now = now.Add(11 * time.Second)
	if s := hopState(t, m, "lxmf_0"); !s.Retained {
		t.Fatalf("lxmf_0 %s retained=%v, want retained by the floor", s.State, s.Retained)
	}
	if got := firstRowChannel(t, h, h.submit(c, "two", directory.StrategyOrderedFallback)); got != "lxmf_0" {
		t.Fatalf("lxmf retained: round 1 on %s, want lxmf_0", got)
	}

	// mesh hears traffic again and stays clean for the re-admit dwell.
	*now = now.Add(time.Minute)
	m.Positive("mesh_0", "rx")
	*now = now.Add(DefaultHopSetConfig().ReadmitDwell + time.Second)
	if s := hopState(t, m, "mesh_0"); s.State != HopAdmitted {
		t.Fatalf("mesh_0 %s, want re-admitted", s.State)
	}
	if got := firstRowChannel(t, h, h.submit(c, "three", directory.StrategyOrderedFallback)); got != "mesh_0" {
		t.Fatalf("mesh re-admitted: round 1 on %s, want mesh_0", got)
	}
}

// The no-ack retry's failover reads the hop set too: an excluded member is
// skipped (an ordinary retry instead), a re-admitted one is used again. The
// worker's no-ack is itself a sign against the link, keyed by destination.
func TestHopSetGatesTheRetryFailover(t *testing.T) {
	h, ruleID := noAckHarness(t)
	now := time.Date(2026, 10, 23, 14, 0, 0, 0, time.UTC)
	m := NewHopSetManager(DefaultHopSetConfig(), h.db.GetAllInterfaces, HopOn)
	m.clock = func() time.Time { return now }
	h.failover.SetRouteGate(m)
	w := noAckWorker(h)
	w.hop = &h.dispatch.hop
	h.dispatch.SetHopSigns(m)

	m.Negative("cellular_0", "radio", "not_submitted", "refused")
	m.Negative("cellular_0", "delivery", "+31600000001", "no_ack")
	now = now.Add(11 * time.Second)
	if s := hopState(t, m, "cellular_0"); s.State != HopExcluded {
		t.Fatalf("setup: cellular_0 %s, want excluded", s.State)
	}
	row := queueOnAPRS(t, h, ruleID, `["mesh_0"]`)
	row.Destination = "MSPRLX-10"
	w.handleFailure(row, errNoAckFromPeer)
	got, err := h.db.GetDelivery(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "retry" || len(deliveriesOn(t, h, "cellular_0")) != 0 {
		t.Fatalf("excluded member: status %q, %d moved, want an ordinary retry and none moved", got.Status, len(deliveriesOn(t, h, "cellular_0")))
	}
	if s := hopState(t, m, "aprs_0"); s.State != HopSuspect || len(s.Signs) != 1 || s.Signs[0].Key != "MSPRLX-10" {
		t.Fatalf("aprs_0 after the no-ack: %s %+v, want suspect on one delivery sign keyed MSPRLX-10", s.State, s.Signs)
	}

	now = now.Add(time.Minute)
	m.Positive("cellular_0", "rx")
	now = now.Add(DefaultHopSetConfig().ReadmitDwell + time.Second)
	row2 := queueOnAPRS(t, h, ruleID, `["mesh_0"]`)
	w.handleFailure(row2, errNoAckFromPeer)
	if n := len(deliveriesOn(t, h, "cellular_0")); n != 1 {
		t.Fatalf("re-admitted member: %d moved to cellular_0, want 1", n)
	}
}

// The mesh receipts feed the hop set: the destination's acknowledgement is a
// positive sign; our own radio giving up is a delivery sign keyed by the
// destination; a duty-cycle refusal is the radio's sign; a destination's own
// NAK is no sign.
func TestHopSetFedByMeshReceipts(t *testing.T) {
	h := newIntentHarness(t)
	m, _ := gatingHopSet(t, h.d, h.db.GetAllInterfaces)
	c := h.contact(meshAddr(1))
	send := func(text string) uint32 {
		h.submit(c, text, directory.StrategyOrderedFallback)
		h.work("mesh_0")
		return h.mesh.lastID()
	}

	h.ack(send("a"), annMesh, transport.RoutingErrorNoChannel) // the destination's own NAK
	if s := hopState(t, m, "mesh_0"); len(s.Signs) != 0 {
		t.Fatalf("destination NAK left signs %+v, want none", s.Signs)
	}
	h.ack(send("b"), "!0000aaaa", transport.RoutingErrorMaxRetransmit) // our radio gave up
	h.ack(send("c"), "!0000aaaa", transport.RoutingErrorDutyCycle)     // our radio refused
	s := hopState(t, m, "mesh_0")
	keys := map[string]bool{}
	for _, g := range s.Signs {
		keys[g.Source+":"+g.Key] = true
	}
	if !keys["delivery:"+annMesh] || !keys["radio:duty_cycle"] || len(keys) != 2 {
		t.Fatalf("signs %+v, want delivery:%s and radio:duty_cycle", s.Signs, annMesh)
	}
	h.ack(send("d"), annMesh, 0) // the destination's acknowledgement
	if s := hopState(t, m, "mesh_0"); s.LastPositive == nil || s.State != HopAdmitted {
		t.Fatalf("after an ack: %s positive=%v, want admitted with a positive sign", s.State, s.LastPositive)
	}
}

// nextRouteRig: a contact with LXMF first and the mesh next, under a gating
// hop set; LXMF refuses round 1 (not sent, a next route exists), so the
// walk's next route is the mesh's, read through the gate.
func nextRouteRig(t *testing.T) (*intentHarness, *HopSetManager, *time.Time, string) {
	t.Helper()
	h := newIntentHarness(t)
	m, now := gatingHopSet(t, h.d, h.db.GetAllInterfaces)
	return h, m, now, h.contact(lxmfAddr(0), meshAddr(1))
}

// runRound1 submits, lets LXMF refuse, evaluates, and returns round 2's
// channel ("" when there is none) and the intent's state.
func runRound1(t *testing.T, h *intentHarness, c, text string) (string, string) {
	t.Helper()
	id := h.submit(c, text, directory.StrategyOrderedFallback)
	h.lx.results = []error{lxmf.ErrStampCostTooHigh}
	h.work("lxmf_0")
	h.eval(id)
	s := h.snap(id)
	if r, ok := rowsByRound(s)[2]; ok {
		return r.Channel, s.Intent.State
	}
	return "", s.Intent.State
}

func excludeMeshHop(t *testing.T, m *HopSetManager, now *time.Time) {
	t.Helper()
	m.Negative("mesh_0", "delivery", "!0000abcd", "no_ack")
	m.Negative("mesh_0", "delivery", "!0000beef", "no_ack")
	*now = now.Add(11 * time.Second)
	if s := hopState(t, m, "mesh_0"); s.State != HopExcluded || s.Retained {
		t.Fatalf("setup: mesh_0 %s retained=%v, want excluded", s.State, s.Retained)
	}
}

// hopset_next_route_skips_excluded: the excluded mesh is not the next route,
// and a recovering one isn't either, until it is re-admitted.
func TestHopSetNextRouteSkipsExcluded(t *testing.T) {
	h, m, now, c := nextRouteRig(t)
	excludeMeshHop(t, m, now)
	if ch, st := runRound1(t, h, c, "one"); ch != "" || st != database.IntentClosed {
		t.Fatalf("mesh excluded: round 2 on %q, intent %s; want no round 2, closed", ch, st)
	}
	*now = now.Add(time.Second)
	m.Positive("mesh_0", "rx") // recovering
	if ch, _ := runRound1(t, h, c, "two"); ch != "" {
		t.Fatalf("mesh recovering: round 2 on %q, want none before re-admission", ch)
	}
}

// hopset_next_route_uses_retained: a mesh the floor keeps is the next route.
func TestHopSetNextRouteUsesRetained(t *testing.T) {
	h, m, now, c := nextRouteRig(t)
	m.SetAvailability(func(id string) (bool, bool) { return id != "cellular_0", true }) // two links there
	m.Negative("mesh_0", "delivery", "!0000abcd", "no_ack")
	m.Negative("mesh_0", "delivery", "!0000beef", "no_ack")
	*now = now.Add(11 * time.Second)
	if s := hopState(t, m, "mesh_0"); !s.Retained {
		t.Fatalf("mesh_0 %s retained=%v, want kept by the floor of two", s.State, s.Retained)
	}
	if ch, _ := runRound1(t, h, c, "one"); ch != "mesh_0" {
		t.Fatalf("mesh retained: round 2 on %q, want mesh_0", ch)
	}
}

// hopset_next_route_uses_readmitted: once the mesh is re-admitted it is the
// next route again.
func TestHopSetNextRouteUsesReadmitted(t *testing.T) {
	h, m, now, c := nextRouteRig(t)
	excludeMeshHop(t, m, now)
	*now = now.Add(time.Second)
	m.Positive("mesh_0", "rx")
	*now = now.Add(DefaultHopSetConfig().ReadmitDwell + time.Second)
	if s := hopState(t, m, "mesh_0"); s.State != HopAdmitted {
		t.Fatalf("mesh_0 %s, want re-admitted", s.State)
	}
	if ch, _ := runRound1(t, h, c, "one"); ch != "mesh_0" {
		t.Fatalf("mesh re-admitted: round 2 on %q, want mesh_0", ch)
	}
}

// intent_send_failure_feeds_hopset: a native intent send the radio refused
// (nothing left the device, a retry spent) is the radio's sign; a link that
// was down is none; a route whose receipt never came in time is a delivery
// sign keyed by its destination.
func TestHopSetFedByIntentSends(t *testing.T) {
	h := newIntentHarness(t)
	m, _ := gatingHopSet(t, h.d, h.db.GetAllInterfaces)
	h.workers["mesh_0"].hop = &h.d.hop
	c := h.contact(meshAddr(0), lxmfAddr(1))

	h.mesh.err = transport.ErrNotConnected
	h.submit(c, "down", directory.StrategyOrderedFallback)
	h.work("mesh_0")
	if s := hopState(t, m, "mesh_0"); len(s.Signs) != 0 {
		t.Fatalf("a down link gave signs %+v, want none", s.Signs)
	}

	h.mesh.err = fmt.Errorf("radio refused: %w", transport.ErrNotSubmitted)
	h.due()
	h.work("mesh_0")
	s := hopState(t, m, "mesh_0")
	if len(s.Signs) != 1 || s.Signs[0].Source != "radio" || s.Signs[0].Key != "not_submitted" {
		t.Fatalf("radio refusal: signs %+v, want one radio:not_submitted", s.Signs)
	}

	h2 := newIntentHarness(t)
	m2, _ := gatingHopSet(t, h2.d, h2.db.GetAllInterfaces)
	id := h2.submit(h2.contact(meshAddr(0), lxmfAddr(1)), "silent", directory.StrategyOrderedFallback)
	h2.work("mesh_0") // accepted by the radio; no answer ever comes
	h2.clock.Advance(121 * time.Second)
	h2.eval(id)
	s2 := hopState(t, m2, "mesh_0")
	if len(s2.Signs) != 1 || s2.Signs[0].Source != "delivery" || s2.Signs[0].Key != annMesh {
		t.Fatalf("no receipt in time: signs %+v, want one delivery:%s", s2.Signs, annMesh)
	}

	// A destination's own NAK that arrives after the receipt window still
	// says nothing about the band: no sign.
	h3 := newIntentHarness(t)
	m3, _ := gatingHopSet(t, h3.d, h3.db.GetAllInterfaces)
	h3.submit(h3.contact(meshAddr(0), lxmfAddr(1)), "late nak", directory.StrategyOrderedFallback)
	h3.work("mesh_0")
	h3.clock.Advance(121 * time.Second)
	h3.ack(h3.mesh.lastID(), annMesh, transport.RoutingErrorNoChannel)
	if s3 := hopState(t, m3, "mesh_0"); len(s3.Signs) != 0 {
		t.Fatalf("late destination NAK: signs %+v, want none", s3.Signs)
	}
}

// The legacy path raises the paid alarm at its forward, a real submission
// on the paid link, not before.
func TestHopSetPaidAlarmOnTheLegacyForward(t *testing.T) {
	h, _ := noAckHarness(t)
	now := time.Date(2026, 10, 23, 14, 0, 0, 0, time.UTC)
	m := NewHopSetManager(DefaultHopSetConfig(), h.db.GetAllInterfaces, HopOn)
	m.clock = func() time.Time { return now }
	var audits []string
	m.SetAudit(func(typ, id, _ string) { audits = append(audits, typ+" "+id) })
	h.dispatch.SetHopSigns(m)
	m.Negative("aprs_0", "delivery", "a", "no_ack")
	m.Negative("aprs_0", "delivery", "b", "no_ack")
	now = now.Add(11 * time.Second)
	if s := hopState(t, m, "aprs_0"); s.State != HopExcluded {
		t.Fatalf("setup: aprs_0 %s, want excluded", s.State)
	}
	row := database.MessageDelivery{MsgRef: "paid", Channel: "cellular_0", Status: "queued", Priority: 10,
		Payload: []byte("hello"), TextPreview: "hello", MaxRetries: 3, Visited: "[]", QoSLevel: 1, Destination: "+31600000001"}
	id, err := h.db.InsertDelivery(row)
	if err != nil {
		t.Fatal(err)
	}
	row.ID = id
	w := newTestWorker(h, "cellular_0", "cellular")
	w.hop = &h.dispatch.hop
	w.deliver(context.Background(), row)
	n := 0
	for _, a := range audits {
		if a == "hopset_alarm aprs_0" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("legacy paid forward: %d alarms (%v), want 1", n, audits)
	}
}

// unsent_paid_intent_does_not_raise_alarm: a paid attempt refused before its
// adapter is called raises no alarm; the same attempt sent later raises it,
// once.
func TestHopSetPaidAlarmOnlyOnARealSubmission(t *testing.T) {
	h := newIntentHarness(t)
	m, now := gatingHopSet(t, h.d, h.db.GetAllInterfaces)
	var audits []string
	m.SetAudit(func(typ, id, _ string) { audits = append(audits, typ+" "+id) })
	f := &fakeSMS{answer: gateway.IntentOutcome{Class: gateway.IntentAccepted, AcceptedBy: "carrier", Charged: true, ChargedUnits: 1}}
	useSMS(t, h, f)
	h.workers["cellular_0"].hop = &h.d.hop
	smsTariff(t, h, 3)
	excludeMeshHop(t, m, now) // a free link out of use

	sc := smsContact(h)
	sub, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: sc, Body: []byte("x"), Strategy: directory.StrategyPrimaryOnly, MaxCostCents: capOf(10)})
	if err != nil {
		t.Fatal(err)
	}
	f.setAdmit("not_now") // admitted at submission, refused at pre-send
	h.work("cellular_0")
	alarms := func() int {
		n := 0
		for _, a := range audits {
			if a == "hopset_alarm mesh_0" {
				n++
			}
		}
		return n
	}
	if f.count() != 0 || alarms() != 0 {
		t.Fatalf("refused before the call: %d sends, %d alarms; want 0 and 0", f.count(), alarms())
	}
	// A refusal at pre-send closes that PRIMARY_ONLY intent; the next one
	// is admitted and sent.
	f.setAdmit("")
	sub2, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: sc, Body: []byte("y"), Strategy: directory.StrategyPrimaryOnly, MaxCostCents: capOf(10)})
	if err != nil {
		t.Fatal(err)
	}
	h.work("cellular_0")
	if f.count() != 1 || alarms() != 1 {
		t.Fatalf("sent: %d sends, %d alarms; want 1 and 1 (intents %s, %s)", f.count(), alarms(), sub.Intents[0].ID, sub2.Intents[0].ID)
	}
}

// intent_non_bearer_refusal_is_not_radio_evidence: LXMF's local, destination
// and path failures leave no radio sign (R47-V3); the bearer's own refusal
// (a mesh ErrNotSubmitted) still is one.
func TestHopSetIntentNonBearerRefusalIsNotRadioEvidence(t *testing.T) {
	for _, e := range []error{lxmf.ErrNotPrepared, lxmf.ErrUnknownDestination, lxmf.ErrNoPath, lxmf.ErrLinkFailed,
		fmt.Errorf("lxmf: interface: %w", transport.ErrNotSubmitted)} {
		h := newIntentHarness(t)
		m, _ := gatingHopSet(t, h.d, h.db.GetAllInterfaces)
		h.workers["lxmf_0"].hop = &h.d.hop
		h.lx.results = []error{e}
		h.submit(h.contact(lxmfAddr(0), meshAddr(1)), "via lxmf", directory.StrategyOrderedFallback)
		if h.work("lxmf_0") == 0 || h.lx.n() != 1 {
			t.Fatalf("%v: the LXMF attempt did not run", e)
		}
		if s := hopState(t, m, "lxmf_0"); len(s.Signs) != 0 {
			t.Errorf("%v: lxmf_0 signs %+v, want none", e, s.Signs)
		}
	}
	h := newIntentHarness(t)
	m, _ := gatingHopSet(t, h.d, h.db.GetAllInterfaces)
	h.workers["mesh_0"].hop = &h.d.hop
	h.mesh.err = fmt.Errorf("radio refused: %w", transport.ErrNotSubmitted)
	h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "via mesh", directory.StrategyOrderedFallback)
	h.work("mesh_0")
	if s := hopState(t, m, "mesh_0"); len(s.Signs) != 1 || s.Signs[0].Source != "radio" {
		t.Fatalf("the radio's own refusal: signs %+v, want one radio sign", s.Signs)
	}
}

// unsent_paid_legacy_delivery_does_not_raise_alarm: a legacy paid delivery
// whose gateway is not found never reached a modem, so it raises no alarm
// (R47-V6); the next one that does reach its gateway raises one.
func TestHopSetUnsentPaidLegacyDeliveryDoesNotRaiseAlarm(t *testing.T) {
	h, _ := noAckHarness(t)
	h.addInterface(t, "iridium_0", "iridium", true)
	now := time.Date(2026, 10, 23, 14, 0, 0, 0, time.UTC)
	m := NewHopSetManager(DefaultHopSetConfig(), h.db.GetAllInterfaces, HopOn)
	m.clock = func() time.Time { return now }
	var audits []string
	m.SetAudit(func(typ, id, _ string) { audits = append(audits, typ+" "+id) })
	h.dispatch.SetHopSigns(m)
	m.Negative("aprs_0", "delivery", "a", "no_ack")
	m.Negative("aprs_0", "delivery", "b", "no_ack")
	now = now.Add(11 * time.Second)
	if s := hopState(t, m, "aprs_0"); s.State != HopExcluded {
		t.Fatalf("setup: aprs_0 %s, want excluded", s.State)
	}
	alarms := func() int {
		n := 0
		for _, a := range audits {
			if a == "hopset_alarm aprs_0" {
				n++
			}
		}
		return n
	}
	send := func(channel, chanType string) {
		row := database.MessageDelivery{MsgRef: "paid-" + channel, Channel: channel, Status: "queued", Priority: 10,
			Payload: []byte("hello"), TextPreview: "hello", MaxRetries: 3, Visited: "[]", QoSLevel: 1, Destination: "+31600000001"}
		id, err := h.db.InsertDelivery(row)
		if err != nil {
			t.Fatal(err)
		}
		row.ID = id
		w := newTestWorker(h, channel, chanType)
		w.hop = &h.dispatch.hop
		w.deliver(context.Background(), row)
	}
	send("iridium_0", "iridium")
	if n := alarms(); n != 0 {
		t.Fatalf("no gateway for iridium_0: %d alarms (%v), want none", n, audits)
	}
	send("cellular_0", "cellular")
	if n := alarms(); n != 1 {
		t.Fatalf("a forward to the cellular gateway: %d alarms (%v), want 1", n, audits)
	}
	// A gateway found by its type (the legacy lookup) is a submission too.
	h.addInterface(t, "sms_0", "sms", true)
	h.gwProv.gwSlice = append(h.gwProv.gwSlice, &mockGateway{typ: "sms_0"})
	now = now.Add(DefaultHopSetConfig().AlarmEvery + time.Second)
	send("sms_0", "sms")
	if n := alarms(); n != 2 {
		t.Fatalf("a forward to the gateway found by type: %d alarms (%v), want 2", n, audits)
	}
}

// gateway_oob_receive_clears_silence: a management frame the OOB handler
// consumes is still a frame heard on the link, so it ends that link's
// silence (R48-S2), and it is counted once.
func TestHopSetGatewayOOBReceiveClearsSilence(t *testing.T) {
	p, db, _, gw := newInboundAuthHarness(t, "aprs_0", "aprs", "[]")
	var mu sync.Mutex
	now := time.Date(2026, 10, 23, 14, 0, 0, 0, time.UTC)
	m := NewHopSetManager(DefaultHopSetConfig(), p.db.GetAllInterfaces, HopOn)
	m.clock = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	p.SetHopSigns(m)
	consumed := make(chan string, 1)
	p.SetOOBInbound(func(ctx context.Context, ifaceID, fromAddr, text string) bool {
		if text != "management frame" {
			return false
		}
		consumed <- ifaceID
		return true
	})
	for i := 0; i < 70; i++ {
		m.Positive("aprs_0", "rx")
		advance(time.Minute)
	}
	advance(10 * time.Minute)
	if s := hopState(t, m, "aprs_0"); len(s.Signs) != 1 || s.Signs[0].Source != "silence" {
		t.Fatalf("setup: aprs_0 signs %+v, want one silence sign", s.Signs)
	}
	m.mu.Lock()
	before := 0
	for _, r := range m.links["aprs_0"].rx {
		before += r.n
	}
	m.mu.Unlock()

	gw.ch <- gateway.InboundMessage{Source: "aprs", FromAddr: "MSPRLX-10", Text: "management frame"}
	select {
	case id := <-consumed:
		if id != "aprs_0" {
			t.Fatalf("OOB handler saw %q, want aprs_0", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the OOB handler never saw the frame")
	}
	s := hopState(t, m, "aprs_0")
	if len(s.Signs) != 0 || s.State != HopAdmitted {
		t.Fatalf("after a consumed management frame: %s signs %+v, want admitted with no silence sign", s.State, s.Signs)
	}
	m.mu.Lock()
	after := 0
	for _, r := range m.links["aprs_0"].rx {
		after += r.n
	}
	m.mu.Unlock()
	if after != before+1 {
		t.Fatalf("frames heard %d -> %d, want the frame counted once", before, after)
	}

	// An ordinary frame, which the OOB handler passes on, is counted once too.
	advance(time.Minute)
	if persisted, _ := deliver(t, p, db, gw, gateway.InboundMessage{Source: "aprs", FromAddr: "MSPRLX-10", Text: "hello"}); len(persisted) == 0 {
		t.Fatal("the ordinary frame was not persisted")
	}
	m.mu.Lock()
	final := 0
	for _, r := range m.links["aprs_0"].rx {
		final += r.n
	}
	m.mu.Unlock()
	if final != after+1 {
		t.Fatalf("frames heard %d -> %d after an ordinary frame, want it counted once", after, final)
	}
}
