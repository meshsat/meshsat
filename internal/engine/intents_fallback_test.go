package engine

import (
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/transport"
)

// S3: route 1 stays offline past its queue allowance: skipped_offline, and
// route 2 is admitted at the allowance plus one tick, not before.
func TestIntentS3_OfflinePastQueueAllowanceMovesToTheNextRoute(t *testing.T) {
	h := newIntentHarness(t)
	id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "meet at the gate", directory.StrategyOrderedFallback)

	h.mesh.err = transport.ErrNotConnected
	h.work("mesh_0")
	s := h.snap(id)
	if r := rowsByRound(s)[1]; r.Status != "retry" || r.Retries != 0 {
		t.Fatalf("round 1 after a down link: %s, %d retries; want retry, 0 (a down link spends no retry)", r.Status, r.Retries)
	}

	h.clock.Advance(119 * time.Second)
	h.eval(id)
	if s := h.snap(id); s.Intent.ActiveRound != 1 || len(s.Rows) != 1 {
		t.Fatalf("at 119 s: round %d, %d rows; want round 1, 1 row", s.Intent.ActiveRound, len(s.Rows))
	}
	h.clock.Advance(1 * time.Second)
	h.eval(id)
	s = h.snap(id)
	if s.Intent.ActiveRound != 2 || s.Intent.LastTransition != "T4" {
		t.Fatalf("at 120 s: round %d after %s; want round 2 after T4", s.Intent.ActiveRound, s.Intent.LastTransition)
	}
	rows := rowsByRound(s)
	if rows[1].Status != database.AttemptSuperseded || rows[1].Reason != "skipped_offline" {
		t.Fatalf("round 1: %s/%s; want superseded/skipped_offline", rows[1].Status, rows[1].Reason)
	}
	if rows[2].Channel != "lxmf_0" || rows[2].Destination != annLXMF || rows[2].Status != "queued" {
		t.Fatalf("round 2: %s to %s, %s", rows[2].Channel, rows[2].Destination, rows[2].Status)
	}

	h.work("lxmf_0")
	h.eval(id)
	s = h.snap(id)
	if s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeAcceptedDestinationNode {
		t.Fatalf("after the proof: %s/%s; want closed/accepted_destination_node", s.Intent.State, s.Intent.Outcome)
	}
	if h.lx.n() != 1 || h.mesh.sends() != 0 {
		t.Fatalf("adapters: lxmf %d calls, mesh %d sends; want 1, 0", h.lx.n(), h.mesh.sends())
	}
}

// S11: round N's link is offline past its allowance, N+1 is admitted, then
// N's link comes back: N's row is superseded and its adapter gets nothing.
func TestIntentS11_SupersededRowNeverSendsWhenItsLinkReturns(t *testing.T) {
	h := newIntentHarness(t)
	id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "status?", directory.StrategyOrderedFallback)
	h.mesh.err = transport.ErrNotConnected
	h.work("mesh_0")
	h.clock.Advance(121 * time.Second)
	h.eval(id)
	if h.snap(id).Intent.ActiveRound != 2 {
		t.Fatal("round 2 not admitted")
	}
	h.mesh.err = nil
	h.due()
	h.work("mesh_0")
	h.clock.Advance(30 * time.Second)
	h.eval(id)
	if h.mesh.sends() != 0 {
		t.Fatalf("the superseded round's adapter sent %d times; want 0", h.mesh.sends())
	}
	if r := rowsByRound(h.snap(id))[1]; r.Status != database.AttemptSuperseded {
		t.Fatalf("round 1 is %s; want superseded", r.Status)
	}
}

// S4: a receipt and the fallback race. In every order exactly one of
// close-by-receipt and advance commits first; the evidence row exists
// either way; an attempt N+1 that went out stays recorded.
func TestIntentS4_ReceiptRacingTheFallback(t *testing.T) {
	t.Run("advance first, then the receipt retires round 2", func(t *testing.T) {
		h := newIntentHarness(t)
		id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "a", directory.StrategyOrderedFallback)
		h.work("mesh_0")
		pkt := h.mesh.lastID()
		h.eval(id) // stamps the acceptance
		h.clock.Advance(121 * time.Second)
		h.eval(id)
		if s := h.snap(id); s.Intent.LastTransition != "T5" || s.Intent.ActiveRound != 2 {
			t.Fatalf("want T5 into round 2, got %s round %d", s.Intent.LastTransition, s.Intent.ActiveRound)
		}
		h.ack(pkt, annMesh, 0)
		s := h.snap(id)
		if s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeDeliveredDevice || s.Intent.LastTransition != "T9" {
			t.Fatalf("after the late round-1 ACK: %s/%s by %s", s.Intent.State, s.Intent.Outcome, s.Intent.LastTransition)
		}
		if r := rowsByRound(s)[2]; r.Status != database.AttemptSuperseded {
			t.Fatalf("round 2 is %s; want superseded", r.Status)
		}
		if len(s.Evidence) != 1 || !s.Evidence[0].Matched || s.Evidence[0].Completes != database.OutcomeDeliveredDevice {
			t.Fatalf("evidence %+v", s.Evidence)
		}
		h.work("lxmf_0")
		if h.lx.n() != 0 {
			t.Fatal("the retired round 2 was sent")
		}
	})
	t.Run("receipt first: no advance", func(t *testing.T) {
		h := newIntentHarness(t)
		id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "b", directory.StrategyOrderedFallback)
		h.work("mesh_0")
		h.ack(h.mesh.lastID(), annMesh, 0)
		h.clock.Advance(500 * time.Second)
		h.eval(id)
		s := h.snap(id)
		if s.Intent.LastTransition != "T9" || len(s.Rows) != 1 {
			t.Fatalf("want T9 and one row, got %s and %d rows", s.Intent.LastTransition, len(s.Rows))
		}
	})
	t.Run("round 2 went out before the round-1 receipt: both recorded, possible duplicate", func(t *testing.T) {
		h := newIntentHarness(t)
		id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "c", directory.StrategyOrderedFallback)
		h.work("mesh_0")
		pkt := h.mesh.lastID()
		h.eval(id)
		h.clock.Advance(121 * time.Second)
		h.eval(id)
		h.work("lxmf_0")
		h.eval(id)
		h.ack(pkt, annMesh, 0) // late, after the close
		s := h.snap(id)
		if s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeAcceptedDestinationNode {
			t.Fatalf("%s/%s", s.Intent.State, s.Intent.Outcome)
		}
		if !s.Intent.PossibleDuplicate {
			t.Fatal("two copies went out; possible_duplicate not set")
		}
		if len(s.Evidence) != 2 {
			t.Fatalf("%d evidence rows; want the proof and the late ACK", len(s.Evidence))
		}
		if r := rowsByRound(s)[2]; r.Status != database.AttemptAccepted {
			t.Fatalf("round 2 is %s; want accepted (recorded)", r.Status)
		}
	})
}

// S12: a round-1 receipt after round 2 is admitted closes the intent (T9)
// and retires unclaimed work in every round; replayed deadlines and row
// outcomes schedule nothing, no round 3, in both orders, and the same
// result comes from persisted evidence alone after a restart.
func TestIntentS12_StaleEventsScheduleNothing(t *testing.T) {
	for _, order := range []string{"receipt-then-replay", "replay-then-receipt"} {
		t.Run(order, func(t *testing.T) {
			h := newIntentHarness(t)
			id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "x", directory.StrategyOrderedFallback)
			h.work("mesh_0")
			pkt := h.mesh.lastID()
			h.eval(id)
			h.clock.Advance(121 * time.Second)
			h.eval(id) // T5: round 2
			replay := func() {
				h.clock.Advance(200 * time.Second) // round 1's allowance long gone
				h.eval(id)
				h.esc.Notify(id)
				h.eval(id)
			}
			if order == "receipt-then-replay" {
				h.ack(pkt, annMesh, 0)
				replay()
			} else {
				replay() // round 2's queue allowance passes too; no route 3 exists
				h.ack(pkt, annMesh, 0)
			}
			s := h.snap(id)
			if s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeDeliveredDevice {
				t.Fatalf("%s/%s by %s", s.Intent.State, s.Intent.Outcome, s.Intent.LastTransition)
			}
			if len(s.Rows) != 2 {
				t.Fatalf("%d rows; want 2 (no round 3)", len(s.Rows))
			}
			v := s.Intent.Version
			h.restart()
			h.eval(id)
			if s2 := h.snap(id); s2.Intent.Version != v || s2.Intent.Outcome != s.Intent.Outcome {
				t.Fatalf("after restart: version %d->%d, outcome %s", v, s2.Intent.Version, s2.Intent.Outcome)
			}
		})
	}
	t.Run("from persisted evidence only", func(t *testing.T) {
		h := newIntentHarness(t)
		id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "y", directory.StrategyOrderedFallback)
		h.work("mesh_0")
		pkt := h.mesh.lastID()
		h.esc.ready.Store(false) // the evidence is stored, nothing is decided
		h.ack(pkt, annMesh, 0)
		if h.snap(id).Intent.State != database.IntentActive {
			t.Fatal("decided while not ready")
		}
		h.restart()
		s := h.snap(id)
		if s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeDeliveredDevice {
			t.Fatalf("recovery from the evidence: %s/%s", s.Intent.State, s.Intent.Outcome)
		}
	})
}
