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
	}{{39, "PKI_SEND_FAIL_PUBLIC_KEY"}, {9, "DUTY_CYCLE_LIMIT"}, {7, "TOO_LARGE"}, {34, "PKI_FAILED"}} {
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
