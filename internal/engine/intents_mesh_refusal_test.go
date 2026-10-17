package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/transport"
)

// Our own radio's refusal (found live, 17 Oct 2026 22:19 UTC): a DM to a
// node whose public key tesseract's radio did not have came back as
// ROUTING_APP from = to = our node with PKI_SEND_FAIL_PUBLIC_KEY ("Error=39,
// return NAK and drop packet"), and the intent closed "accepted, not
// confirmed" though nothing went on air. Such a refusal is "dropped": not
// sent, the next route takes over, and with none the intent fails.
// [MESHSAT-1584]

const ownNode = 0x0000aaaa // the harness radio's own node (To in h.ack)

func (h *intentHarness) radioAnswer(id, from uint32, reason uint32, name string) {
	h.esc.MeshRoutingReceipt("mesh_0", transport.MeshMessage{From: from, To: ownNode, RequestID: id,
		PortNum: int(transport.PortNumRouting), Routing: &transport.RoutingInfo{ErrorReason: reason, ErrorName: name}})
	h.drain()
}

func factsOfRoute(v IntentView, i int) string { return strings.Join(v.Routes[i].Facts, "; ") }

func TestIntentMesh_OwnRadioRefusalIsNotSent(t *testing.T) {
	for _, tc := range []struct {
		reason uint32
		name   string
	}{{39, "PKI_SEND_FAIL_PUBLIC_KEY"}, {7, "TOO_LARGE"}, {34, "PKI_FAILED"}, {6, "NO_CHANNEL"}, {4, "NO_INTERFACE"}, {38, "RATE_LIMIT_EXCEEDED"}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newIntentHarness(t)
			id := h.submit(h.contact(meshAddr(0)), "refused", directory.StrategyPrimaryOnly)
			h.work("mesh_0")
			h.radioAnswer(h.mesh.lastID(), ownNode, tc.reason, tc.name)
			h.clock.Advance(121 * time.Second)
			h.esc.Heartbeat()
			h.drain()
			s := h.snap(id)
			if s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeFailed {
				t.Fatalf("%s/%s; want closed failed: the radio sent nothing", s.Intent.State, s.Intent.Outcome)
			}
			facts := factsOfRoute(BuildIntentView(s), 0)
			if !strings.Contains(facts, "not sent: the radio on mesh_0 refused it ("+tc.name+")") || strings.Contains(facts, "accepted by") {
				t.Fatalf("facts %q", facts)
			}
		})
	}
}

// With a next route the refusal hands over at once, and there is no
// possible duplicate: only one route sent anything.
func TestIntentMesh_OwnRadioRefusalHandsOverToTheNextRoute(t *testing.T) {
	h := newIntentHarness(t)
	id := h.submit(h.contact(meshAddr(1), lxmfAddr(2)), "refused then lxmf", directory.StrategyOrderedFallback)
	h.work("mesh_0")
	h.radioAnswer(h.mesh.lastID(), ownNode, 39, "PKI_SEND_FAIL_PUBLIC_KEY")
	h.work("lxmf_0")
	h.drain()
	s := h.snap(id)
	if h.lx.n() != 1 || s.Intent.Outcome != database.OutcomeAcceptedDestinationNode || s.Intent.PossibleDuplicate {
		t.Fatalf("%d LXMF sends, %s, duplicate %v; want LXMF at once, its proof, no duplicate", h.lx.n(), s.Intent.Outcome, s.Intent.PossibleDuplicate)
	}
}

// The same codes from a relay, and MAX_RETRANSMIT from our own radio, mean
// the packet left us: still accepted, not confirmed.
func TestIntentMesh_ARelaysCodeOrNoAckIsStillSent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		from   uint32
		reason uint32
		ename  string
	}{
		{"a relay's duty-cycle limit", 0x0000beef, 9, "DUTY_CYCLE_LIMIT"},
		{"our radio's MAX_RETRANSMIT", ownNode, 5, "MAX_RETRANSMIT"},
		// The radio checks the duty cycle on every copy and retries after
		// it: a copy may have gone before, or go after (review RADIO-R1).
		{"our radio's duty-cycle limit", ownNode, 9, "DUTY_CYCLE_LIMIT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newIntentHarness(t)
			id := h.submit(h.contact(meshAddr(0)), "sent", directory.StrategyPrimaryOnly)
			h.work("mesh_0")
			h.radioAnswer(h.mesh.lastID(), tc.from, tc.reason, tc.ename)
			h.clock.Advance(121 * time.Second)
			h.esc.Heartbeat()
			h.drain()
			if s := h.snap(id); s.Intent.Outcome != database.OutcomeAcceptedUnconfirmed {
				t.Fatalf("%s; want accepted_unconfirmed: it left us", s.Intent.Outcome)
			}
		})
	}
}

// The refusal can come back before the send is finalized (the radio NAKs
// at once): matched later from the stored answer, the same result.
type refusingRadio struct {
	transport.MeshTransport
	esc *Escalator
}

func (m *refusingRadio) SendMessageWithID(_ context.Context, _ transport.SendRequest, id uint32) error {
	m.esc.MeshRoutingReceipt("mesh_0", transport.MeshMessage{From: ownNode, To: ownNode, RequestID: id,
		PortNum: int(transport.PortNumRouting), Routing: &transport.RoutingInfo{ErrorReason: 39, ErrorName: "PKI_SEND_FAIL_PUBLIC_KEY"}})
	return nil
}
func (m *refusingRadio) SendMessage(context.Context, transport.SendRequest) error { return nil }

func TestIntentMesh_OwnRadioRefusalBeforeFinalization(t *testing.T) {
	h := newIntentHarness(t)
	h.workers["mesh_0"].mesh = &refusingRadio{esc: h.esc}
	id := h.submit(h.contact(meshAddr(0)), "refused early", directory.StrategyPrimaryOnly)
	h.work("mesh_0")
	h.drain()
	h.clock.Advance(121 * time.Second)
	h.esc.Heartbeat()
	h.drain()
	if s := h.snap(id); s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeFailed {
		t.Fatalf("%s/%s; want closed failed", s.Intent.State, s.Intent.Outcome)
	}
}

// An encode-path refusal (6, 7, 34, 39) more than the first-copy window
// after the handoff may be about a retransmission, after a copy went: not
// proof (RADIO-R1). A handoff-path refusal (4, 38) only ever refuses the
// handoff, so it counts whenever it comes.
func TestIntentMesh_ALateRefusalOnlyCountsForTheHandoffPath(t *testing.T) {
	for _, tc := range []struct {
		reason uint32
		name   string
		want   string
	}{
		{39, "PKI_SEND_FAIL_PUBLIC_KEY", database.OutcomeAcceptedUnconfirmed},
		{7, "TOO_LARGE", database.OutcomeAcceptedUnconfirmed},
		{38, "RATE_LIMIT_EXCEEDED", database.OutcomeFailed},
		{4, "NO_INTERFACE", database.OutcomeFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newIntentHarness(t)
			id := h.submit(h.contact(meshAddr(0)), "late answer", directory.StrategyPrimaryOnly)
			h.work("mesh_0")
			h.drain()
			if _, err := h.db.Exec(`UPDATE delivery_transmissions SET handoff_at = datetime('now', '-10 seconds') WHERE intent_id = ?`, id); err != nil {
				t.Fatal(err)
			}
			h.radioAnswer(h.mesh.lastID(), ownNode, tc.reason, tc.name)
			h.clock.Advance(121 * time.Second)
			h.esc.Heartbeat()
			h.drain()
			if s := h.snap(id); s.Intent.Outcome != tc.want {
				t.Fatalf("%s 10 s after the handoff: %s; want %s", tc.name, s.Intent.Outcome, tc.want)
			}
		})
	}
}

// RADIO-R2's further cases: an intent that expired with a send it believed
// accepted becomes expired; a refusal whose notice was lost is corrected
// after a restart; a stronger completion from another route stays.
func TestIntentMesh_LateRefusalAfterExpiryRestartAndAnotherRoute(t *testing.T) {
	t.Run("expiry", func(t *testing.T) {
		h := newIntentHarness(t, func(c *EscalatorConfig) { c.ExpiryAllowS = 60 })
		id := h.submit(h.contact(meshAddr(0)), "expires", directory.StrategyPrimaryOnly)
		h.work("mesh_0")
		h.drain()
		h.clock.Advance(61 * time.Second)
		h.esc.Heartbeat()
		h.drain()
		if s := h.snap(id); s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeAcceptedUnconfirmed || s.Intent.LastTransition != "T12" {
			t.Fatalf("setup: %s/%s by %s", s.Intent.State, s.Intent.Outcome, s.Intent.LastTransition)
		}
		h.radioAnswer(h.mesh.lastID(), ownNode, 39, "PKI_SEND_FAIL_PUBLIC_KEY")
		if s := h.snap(id); s.Intent.Outcome != database.OutcomeExpired || h.mesh.attempts != 1 {
			t.Fatalf("%s, %d sends; want expired, nothing sent again", s.Intent.Outcome, h.mesh.attempts)
		}
	})
	t.Run("restart", func(t *testing.T) {
		h := newIntentHarness(t)
		id := h.submit(h.contact(meshAddr(0)), "restart", directory.StrategyPrimaryOnly)
		h.work("mesh_0")
		h.drain()
		h.clock.Advance(121 * time.Second)
		h.esc.Heartbeat()
		h.drain()
		h.esc.MeshRoutingReceipt("mesh_0", transport.MeshMessage{From: ownNode, To: ownNode, RequestID: h.mesh.lastID(),
			PortNum: int(transport.PortNumRouting), Routing: &transport.RoutingInfo{ErrorReason: 39, ErrorName: "PKI_SEND_FAIL_PUBLIC_KEY"}})
		for len(h.esc.notify) > 0 {
			<-h.esc.notify // lost with the stop
		}
		h.restart()
		h.esc.Heartbeat()
		if s := h.snap(id); s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeFailed || h.mesh.attempts != 1 {
			t.Fatalf("%s/%s, %d sends; want closed failed after the restart, nothing sent again", s.Intent.State, s.Intent.Outcome, h.mesh.attempts)
		}
	})
	t.Run("another route completed", func(t *testing.T) {
		h := newIntentHarness(t)
		id := h.submit(h.contact(meshAddr(1), lxmfAddr(2)), "lxmf proved", directory.StrategyOrderedFallback)
		h.work("mesh_0")
		h.drain()
		packet := h.mesh.lastID()
		h.radioAnswer(packet, ownNode, 5, "MAX_RETRANSMIT") // no ACK: LXMF takes over
		h.work("lxmf_0")
		h.drain()
		if s := h.snap(id); s.Intent.Outcome != database.OutcomeAcceptedDestinationNode || s.Intent.State != database.IntentClosed {
			t.Fatalf("setup: %s/%s", s.Intent.State, s.Intent.Outcome)
		}
		h.radioAnswer(packet, ownNode, 39, "PKI_SEND_FAIL_PUBLIC_KEY")
		if s := h.snap(id); s.Intent.Outcome != database.OutcomeAcceptedDestinationNode {
			t.Fatalf("%s; the LXMF proof must stay", s.Intent.Outcome)
		}
	})
}

// A refusal and the destination's ACK for the same packet contradict each
// other; the ACK wins (something was received, so something was sent), and
// the route is not shown as not sent (review RADIO-R1).
func TestIntentMesh_ADestinationAckOutweighsARefusal(t *testing.T) {
	h := newIntentHarness(t)
	id := h.submit(h.contact(meshAddr(0)), "both answers", directory.StrategyPrimaryOnly)
	h.work("mesh_0")
	h.drain()
	packet := h.mesh.lastID()
	h.radioAnswer(packet, ownNode, 39, "PKI_SEND_FAIL_PUBLIC_KEY")
	h.ack(packet, annMesh, 0)
	h.drain()
	s := h.snap(id)
	facts := factsOfRoute(BuildIntentView(s), 0)
	if s.Intent.Outcome != database.OutcomeDeliveredDevice || strings.Contains(facts, "not sent") {
		t.Fatalf("%s, facts %q; want delivered_device and no not-sent fact", s.Intent.Outcome, facts)
	}
	if dropped := droppedTransmissions(s); len(dropped) != 0 {
		t.Fatalf("dropped %v; the ACK outweighs the refusal", dropped)
	}
}
