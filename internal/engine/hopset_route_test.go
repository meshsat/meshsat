package engine

import (
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/directory"
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
	m := NewHopSetManager(DefaultHopSetConfig(), list, true)
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
	*now = now.Add(5*time.Minute + time.Second)
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
	m := NewHopSetManager(DefaultHopSetConfig(), h.db.GetAllInterfaces, true)
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
	now = now.Add(5*time.Minute + time.Second)
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
