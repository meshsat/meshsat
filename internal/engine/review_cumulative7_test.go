package engine

// The review of 38f2e17 (18 Oct 2026 13:47 CEST): its cases as supplied
// (RADIO-R2's accepted to unknown). [MESHSAT-1584]

import (
	"errors"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/directory"
)

func TestReviewCumulative7_LateRefusalLeavesUnknownFallback(t *testing.T) {
	h := newIntentHarness(t)
	h.lx.results = []error{errors.New("reply lost after handoff")}
	id := h.submit(h.contact(meshAddr(1), lxmfAddr(2)), "unknown fallback", directory.StrategyOrderedFallback)
	h.work("mesh_0")
	h.drain()
	h.clock.Advance(121 * time.Second)
	h.esc.Heartbeat()
	h.drain()
	h.work("lxmf_0")
	h.drain()
	before := h.snap(id)
	if before.Intent.State != database.IntentClosed || before.Intent.Outcome != database.OutcomeAcceptedUnconfirmed || h.lx.n() != 1 {
		t.Fatalf("setup: %s/%s lxmf_sends=%d", before.Intent.State, before.Intent.Outcome, h.lx.n())
	}
	h.radioAnswer(h.mesh.lastID(), ownNode, 4, "NO_INTERFACE")
	s := h.snap(id)
	t.Logf("before=%s after=%s recomputed=%s state=%s mesh_sends=%d lxmf_sends=%d", before.Intent.Outcome, s.Intent.Outcome, closedOutcome(s), s.Intent.State, h.mesh.attempts, h.lx.n())
	if s.Intent.State != database.IntentClosed || h.mesh.attempts != 1 || h.lx.n() != 1 {
		t.Fatal("closed intent reopened or resent")
	}
	if s.Intent.Outcome != database.OutcomeMayHaveBeenSent {
		t.Fatalf("only an unknown fallback remains; headline=%s, want %s", s.Intent.Outcome, database.OutcomeMayHaveBeenSent)
	}
}

// The same late refusal with a stronger result on the fallback: LXMF's
// proof closed it accepted by the destination node, and a refusal of the
// mesh send never replaces a completion.
func TestReviewCumulative7_LateRefusalKeepsTheFallbacksProof(t *testing.T) {
	h := newIntentHarness(t)
	id := h.submit(h.contact(meshAddr(1), lxmfAddr(2)), "proof on the fallback", directory.StrategyOrderedFallback)
	h.work("mesh_0")
	h.drain()
	h.clock.Advance(121 * time.Second)
	h.esc.Heartbeat()
	h.drain()
	h.work("lxmf_0")
	h.drain()
	if s := h.snap(id); s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeAcceptedDestinationNode {
		t.Fatalf("setup: %s/%s", s.Intent.State, s.Intent.Outcome)
	}
	h.radioAnswer(h.mesh.lastID(), ownNode, 4, "NO_INTERFACE")
	if s := h.snap(id); s.Intent.Outcome != database.OutcomeAcceptedDestinationNode || h.mesh.attempts != 1 || h.lx.n() != 1 {
		t.Fatalf("after the late refusal: %s, %d mesh, %d lxmf", s.Intent.Outcome, h.mesh.attempts, h.lx.n())
	}
}

// Which weaker outcome may replace a stored one on a closed intent: only
// an acceptance or a possible send, and only by what the evidence leaves.
func TestReviewCumulative7_OnlyAnAcceptanceIsCorrectedDownward(t *testing.T) {
	all := []string{database.OutcomeDeliveredHandset, database.OutcomeDeliveredDevice, database.OutcomeAcceptedDestinationNode,
		database.OutcomeAckedStation, database.OutcomeAcceptedUnconfirmed, database.OutcomeMayHaveBeenSent,
		database.OutcomeFailed, database.OutcomeExpired, database.OutcomeCancelled}
	want := map[[2]string]bool{
		{database.OutcomeMayHaveBeenSent, database.OutcomeAcceptedUnconfirmed}: true,
		{database.OutcomeFailed, database.OutcomeAcceptedUnconfirmed}:          true,
		{database.OutcomeCancelled, database.OutcomeAcceptedUnconfirmed}:       true,
		{database.OutcomeExpired, database.OutcomeAcceptedUnconfirmed}:         true,
		{database.OutcomeFailed, database.OutcomeMayHaveBeenSent}:              true,
		{database.OutcomeCancelled, database.OutcomeMayHaveBeenSent}:           true,
		{database.OutcomeExpired, database.OutcomeMayHaveBeenSent}:             true,
	}
	for _, cur := range all {
		for _, next := range all {
			if got := correctsAcceptance(next, cur); got != want[[2]string{next, cur}] {
				t.Errorf("correctsAcceptance(%s, %s) = %t", next, cur, got)
			}
		}
	}
}
