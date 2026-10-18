package engine

// The review of 972104e (18 Oct 2026 01:11 CEST), RADIO-R1 and RADIO-R2,
// its cases as supplied. [MESHSAT-1584]

import (
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/directory"
)

func TestReviewCumulative6_LateOwnRadioRefusalCorrectsOutcome(t *testing.T) {
	for _, cancel := range []bool{true, false} {
		name := "after_receipt_window"
		want := database.OutcomeFailed
		if cancel {
			name = "after_cancel"
			want = database.OutcomeCancelled
		}
		t.Run(name, func(t *testing.T) {
			h := newIntentHarness(t)
			id := h.submit(h.contact(meshAddr(0)), "refused late", directory.StrategyPrimaryOnly)
			h.work("mesh_0")
			h.drain()
			if cancel {
				if _, err := h.esc.Cancel(id, "review"); err != nil {
					t.Fatal(err)
				}
			} else {
				h.clock.Advance(121 * time.Second)
				h.esc.Heartbeat()
				h.drain()
			}
			before := h.snap(id)
			if before.Intent.State != database.IntentClosed || before.Intent.Outcome != database.OutcomeAcceptedUnconfirmed {
				t.Fatalf("setup: %s/%s", before.Intent.State, before.Intent.Outcome)
			}
			h.radioAnswer(h.mesh.lastID(), ownNode, 39, "PKI_SEND_FAIL_PUBLIC_KEY")
			s := h.snap(id)
			facts := factsOfRoute(BuildIntentView(s), 0)
			t.Logf("before=%s after=%s state=%s rule=%s facts=%q sends=%d", before.Intent.Outcome, s.Intent.Outcome, s.Intent.State, outcomeRule(s), facts, h.mesh.attempts)
			if s.Intent.State != database.IntentClosed || h.mesh.attempts != 1 {
				t.Fatal("closed intent reopened or resent")
			}
			if s.Intent.Outcome != want {
				t.Fatalf("late proven non-send leaves outcome=%s; want %s", s.Intent.Outcome, want)
			}
		})
	}
}

func TestReviewCumulative6_DutyLimitDoesNotErasePossibleDelivery(t *testing.T) {
	h := newIntentHarness(t)
	id := h.submit(h.contact(meshAddr(1), lxmfAddr(2)), "duty then proof", directory.StrategyOrderedFallback)
	h.work("mesh_0")
	h.drain()
	packet := h.mesh.lastID()
	h.radioAnswer(packet, ownNode, 9, "DUTY_CYCLE_LIMIT")
	h.work("lxmf_0")
	h.drain()
	h.ack(packet, annMesh, 0)
	h.drain()
	s := h.snap(id)
	facts := factsOfRoute(BuildIntentView(s), 0)
	proofs := 0
	for _, e := range s.Evidence {
		if e.Matched && e.Completes != "" {
			proofs++
		}
	}
	t.Logf("mesh_sends=%d lxmf_sends=%d matched_completion_evidence=%d possible_duplicate=%t mesh_facts=%q", h.mesh.attempts, h.lx.n(), proofs, s.Intent.PossibleDuplicate, facts)
	if proofs != 2 || h.lx.n() != 1 {
		t.Fatal("setup: both routes need matched completion evidence")
	}
	if !s.Intent.PossibleDuplicate {
		t.Fatal("both routes confirmed but the own-radio duty-limit NAK still erases the duplicate")
	}
}
