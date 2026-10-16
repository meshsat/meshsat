package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/lxmf"
	"meshsat/internal/transport"
)

// stopAt is a crash boundary inside one transmission (r3 D5, G1).
type stopAt int

const (
	afterClaim     stopAt = iota // (a) claimed, nothing authorized
	afterAuthorize               // (b) authorized, adapter never called
	afterHandoff                 // (c) handed off, outcome never recorded
)

// crashRound1 drives round 1's row of an intent to a boundary by the same
// steps the worker takes, then the process stops. withCall says the
// adapter was really called after the handoff (the radio sent it).
func (h *intentHarness) crashRound1(id string, at stopAt, withCall bool) (rowID int64, gen int) {
	h.t.Helper()
	row := rowsByRound(h.snap(id))[1]
	if ok, err := h.db.ClaimDeliveryForSending(row.ID); err != nil || !ok {
		h.t.Fatalf("claim: %v %v", ok, err)
	}
	fresh, _ := h.db.GetDelivery(row.ID)
	gen = fresh.ClaimGen
	if at == afterClaim {
		return row.ID, gen
	}
	txID, err := h.db.AuthorizeTransmission(row.ID, gen, "", 0, 0)
	if err != nil {
		h.t.Fatal(err)
	}
	if at == afterAuthorize {
		return row.ID, gen
	}
	if err := h.db.MarkTransmissionHandedOff(txID); err != nil {
		h.t.Fatal(err)
	}
	if withCall {
		_, _ = h.mesh.SendMessageID(context.Background(), transport.SendRequest{Text: "x", To: annMesh})
	}
	return row.ID, gen
}

// S5 (a)-(c): a stop after the claim or after the authorization is proven
// not sent: back to retry, the reservation released, sent once after the
// restart. A stop after the handoff is unknown: a free route moves on
// (possible duplicate), PRIMARY_ONLY closes "may have been sent", a paid
// route closes "may have been sent" with no automatic send (T7).
func TestIntentS5_CrashBoundaries(t *testing.T) {
	for _, at := range []stopAt{afterClaim, afterAuthorize} {
		t.Run(fmt.Sprintf("proven not sent at boundary %d", at), func(t *testing.T) {
			h := newIntentHarness(t)
			id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "p", directory.StrategyOrderedFallback)
			rowID, _ := h.crashRound1(id, at, false)
			h.restart()
			s := h.snap(id)
			r := rowsByRound(s)[1]
			if r.Status != "retry" {
				t.Fatalf("after restart the row is %s; want retry", r.Status)
			}
			for _, tx := range txOf(s, rowID) {
				if tx.State != database.TxDone || tx.OutcomeClass != database.ClassNotSent || tx.ReservedMinor != 0 {
					t.Fatalf("recovered transmission %+v; want done/not_sent, nothing reserved", tx)
				}
			}
			h.due()
			h.work("mesh_0")
			if h.mesh.sends() != 1 {
				t.Fatalf("sent %d times after the restart; want once", h.mesh.sends())
			}
			txs := txOf(h.snap(id), rowID)
			last := txs[len(txs)-1]
			if last.OutcomeClass != database.ClassAccepted || last.Gen != rowsByRound(h.snap(id))[1].ClaimGen {
				t.Fatalf("the send after restart: %+v", last)
			}
		})
	}
	cases := []struct {
		name     string
		strategy directory.Strategy
		paid     bool
		want     string // transition after the restart
	}{
		{"free route with a next route", directory.StrategyOrderedFallback, false, "T8"},
		{"free route, PRIMARY_ONLY", directory.StrategyPrimaryOnly, false, "T11"},
		{"paid route", directory.StrategyOrderedFallback, true, "T7"},
	}
	for _, c := range cases {
		t.Run("handed off: "+c.name, func(t *testing.T) {
			h := newIntentHarness(t)
			id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "q", c.strategy)
			if c.paid {
				h.setPlanPaid(id, 1)
			}
			rowID, _ := h.crashRound1(id, afterHandoff, true)
			h.restart()
			s := h.snap(id)
			if s.Intent.LastTransition != c.want {
				t.Fatalf("after the restart: %s; want %s", s.Intent.LastTransition, c.want)
			}
			txs := txOf(s, rowID)
			if len(txs) != 1 || txs[0].OutcomeClass != database.ClassUnknown {
				t.Fatalf("transmission %+v; want one, unknown", txs)
			}
			if r := rowsByRound(s)[1]; r.Status != database.AttemptUnknown {
				t.Fatalf("row %s; want unknown", r.Status)
			}
			if c.want != "T8" && (s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeMayHaveBeenSent) {
				t.Fatalf("%s/%s; want closed, may_have_been_sent", s.Intent.State, s.Intent.Outcome)
			}
			h.due()
			h.work("mesh_0")
			if h.mesh.sends() != 1 {
				t.Fatalf("mesh sent %d times in all; want only the call before the stop", h.mesh.sends())
			}
			if c.want == "T8" {
				h.work("lxmf_0")
				h.eval(id)
				s = h.snap(id)
				if !s.Intent.PossibleDuplicate || s.Intent.Outcome != database.OutcomeAcceptedDestinationNode {
					t.Fatalf("after round 2: dup %v, %s", s.Intent.PossibleDuplicate, s.Intent.Outcome)
				}
			} else if h.lx.n() != 0 {
				t.Fatal("a closed intent sent on its next route")
			}
		})
	}
}

// S5 (d) and (f): a second generation after a retryable, charged first
// one. Generation 1's outcome and charge stay and never describe
// generation 2; generation 2's boundaries behave as (a)-(c).
func TestIntentS5d_SecondGenerationAfterARetryableChargedFirst(t *testing.T) {
	h := newIntentHarness(t)
	id := h.submit(h.contact(meshAddr(0)), "r", directory.StrategyPrimaryOnly)
	row := rowsByRound(h.snap(id))[1]

	// Generation 1: reserved 5, charged 5 (confirmed), not sent, retryable.
	_, _ = h.db.ClaimDeliveryForSending(row.ID)
	tx1, err := h.db.AuthorizeTransmission(row.ID, 1, "test_unit", 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	_ = h.db.MarkTransmissionHandedOff(tx1)
	if _, err := h.db.FinalizeTransmission(tx1, database.TransmissionResult{Class: database.ClassNotSent, Retryable: true,
		ConsumeRetry: true, ChargedUnits: 1, ChargedMinor: 5, Confirmed: true, Detail: "refused after a billed attempt"}); err != nil {
		t.Fatal(err)
	}
	if r := rowsByRound(h.snap(id))[1]; r.Status != "retry" || r.Retries != 1 {
		t.Fatalf("after generation 1: %s, %d retries", r.Status, r.Retries)
	}
	// (f) the next claim is generation 2; then a stop after its handoff.
	_, _ = h.db.ClaimDeliveryForSending(row.ID)
	fresh, _ := h.db.GetDelivery(row.ID)
	if fresh.ClaimGen != 2 {
		t.Fatalf("claim_gen %d; want 2", fresh.ClaimGen)
	}
	tx2, err := h.db.AuthorizeTransmission(row.ID, 2, "test_unit", 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	_ = h.db.MarkTransmissionHandedOff(tx2)
	h.restart()

	s := h.snap(id)
	txs := txOf(s, row.ID)
	if len(txs) != 2 {
		t.Fatalf("%d transmissions", len(txs))
	}
	g1, g2 := txs[0], txs[1]
	if g1.Gen != 1 || g1.OutcomeClass != database.ClassNotSent || g1.ChargedMinor != 5 || g1.ChargeState != "confirmed" {
		t.Fatalf("generation 1 changed: %+v", g1)
	}
	if g2.Gen != 2 || g2.OutcomeClass != database.ClassUnknown || g2.PossibleMinor != 5 || g2.ReservedMinor != 0 {
		t.Fatalf("generation 2: %+v; want unknown, possible 5", g2)
	}
	if s.Intent.ReservedMinor != 0 || s.Intent.IncurredMinor != 5 || s.Intent.PossibleMinor != 5 {
		t.Fatalf("aggregates reserved %d incurred %d possible %d; want 0, 5, 5",
			s.Intent.ReservedMinor, s.Intent.IncurredMinor, s.Intent.PossibleMinor)
	}
	if s.Intent.Outcome != database.OutcomeMayHaveBeenSent || s.Intent.State != database.IntentClosed {
		t.Fatalf("%s/%s; want closed, may_have_been_sent (generation 1 does not certify generation 2)", s.Intent.State, s.Intent.Outcome)
	}
}

// S5 (e): a stop just before the finalization commit is unknown; just
// after it, the accepted result stays and the adapter is never called
// again; on generation 1 and on generation 2.
func TestIntentS5e_AroundTheFinalizationCommit(t *testing.T) {
	for _, gen2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("before the commit, generation 2=%v", gen2), func(t *testing.T) {
			h := newIntentHarness(t)
			id := h.submit(h.contact(meshAddr(0)), "s", directory.StrategyPrimaryOnly)
			if gen2 {
				h.mesh.err = transport.ErrNotConnected
				h.work("mesh_0")
				h.mesh.err = nil
			}
			rowID, gen := h.crashRound1(id, afterHandoff, true)
			h.restart()
			s := h.snap(id)
			txs := txOf(s, rowID)
			last := txs[len(txs)-1]
			if last.Gen != gen || last.OutcomeClass != database.ClassUnknown || s.Intent.Outcome != database.OutcomeMayHaveBeenSent {
				t.Fatalf("generation %d: %+v, outcome %s", gen, last, s.Intent.Outcome)
			}
			if gen2 && txs[0].OutcomeClass != database.ClassNotSent {
				t.Fatalf("generation 1 changed: %+v", txs[0])
			}
		})
		t.Run(fmt.Sprintf("after the commit, generation 2=%v", gen2), func(t *testing.T) {
			h := newIntentHarness(t)
			id := h.submit(h.contact(meshAddr(0)), "t", directory.StrategyPrimaryOnly)
			if gen2 {
				h.mesh.err = transport.ErrNotConnected
				h.work("mesh_0")
				h.mesh.err = nil
				h.due()
			}
			h.work("mesh_0")
			h.restart()
			h.due()
			h.work("mesh_0")
			s := h.snap(id)
			r := rowsByRound(s)[1]
			if r.Status != database.AttemptAccepted || h.mesh.sends() != 1 {
				t.Fatalf("row %s, %d sends; want accepted, 1", r.Status, h.mesh.sends())
			}
			txs := txOf(s, r.ID)
			if last := txs[len(txs)-1]; last.OutcomeClass != database.ClassAccepted || last.Gen != r.ClaimGen {
				t.Fatalf("finalized transmission %+v", last)
			}
		})
	}
}

// S22: settlement follows G2. PRIMARY_ONLY with one retry left: a
// retryable not_sent keeps the intent active and generation 2 runs once,
// its acceptance and receipt kept. With no retry left the row settles and
// the intent closes. ALL_BEARERS with every receipt in closes without a
// deadline. ORDERED_FALLBACK after a settled not_sent advances.
func TestIntentS22_RetryableNotSentIsNotSettled(t *testing.T) {
	t.Run("PRIMARY_ONLY, one retry left", func(t *testing.T) {
		h := newIntentHarness(t)
		id := h.submit(h.contact(lxmfAddr(0)), "u", directory.StrategyPrimaryOnly)
		_, _ = h.db.Exec(`UPDATE message_deliveries SET retries = 1 WHERE intent_id = ?`, id)
		h.lx.results = []error{lxmf.ErrNoPath, nil}
		h.work("lxmf_0")
		h.eval(id)
		s := h.snap(id)
		if s.Intent.State != database.IntentActive || rowsByRound(s)[1].Status != "retry" {
			t.Fatalf("after a retryable not_sent: %s, row %s; want active, retry", s.Intent.State, rowsByRound(s)[1].Status)
		}
		h.due()
		h.work("lxmf_0")
		h.eval(id)
		s = h.snap(id)
		r := rowsByRound(s)[1]
		txs := txOf(s, r.ID)
		if h.lx.n() != 2 || len(txs) != 2 || txs[1].Gen != 2 || txs[1].OutcomeClass != database.ClassAccepted {
			t.Fatalf("%d calls, transmissions %+v", h.lx.n(), txs)
		}
		if s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeAcceptedDestinationNode {
			t.Fatalf("%s/%s", s.Intent.State, s.Intent.Outcome)
		}
		if len(s.Evidence) != 1 || s.Evidence[0].TransmissionID != txs[1].ID {
			t.Fatalf("the receipt belongs to generation 2: %+v", s.Evidence)
		}
	})
	t.Run("PRIMARY_ONLY, no retry left", func(t *testing.T) {
		h := newIntentHarness(t)
		id := h.submit(h.contact(lxmfAddr(0)), "v", directory.StrategyPrimaryOnly)
		_, _ = h.db.Exec(`UPDATE message_deliveries SET retries = 2 WHERE intent_id = ?`, id)
		h.lx.results = []error{lxmf.ErrNoPath}
		h.work("lxmf_0")
		h.eval(id)
		s := h.snap(id)
		if rowsByRound(s)[1].Status != database.AttemptNotSent || s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeFailed {
			t.Fatalf("row %s, intent %s/%s; want not_sent, closed/failed", rowsByRound(s)[1].Status, s.Intent.State, s.Intent.Outcome)
		}
	})
	t.Run("ALL_BEARERS, every receipt in", func(t *testing.T) {
		h := newIntentHarness(t)
		id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "w", directory.StrategyAllBearers)
		h.work("mesh_0")
		h.work("lxmf_0")
		h.ack(h.mesh.lastID(), annMesh, 0)
		h.eval(id)
		s := h.snap(id)
		if s.Intent.State != database.IntentClosed || s.Intent.LastTransition != "T11" {
			t.Fatalf("%s by %s; want closed by T11 with no time passing", s.Intent.State, s.Intent.LastTransition)
		}
	})
	t.Run("ORDERED_FALLBACK, not_sent with a next route", func(t *testing.T) {
		h := newIntentHarness(t)
		id := h.submit(h.contact(lxmfAddr(0), meshAddr(1)), "x", directory.StrategyOrderedFallback)
		h.lx.results = []error{lxmf.ErrStampCostTooHigh}
		h.work("lxmf_0")
		h.eval(id)
		s := h.snap(id)
		if s.Intent.State != database.IntentActive || s.Intent.LastTransition != "T3" || rowsByRound(s)[2].Channel != "mesh_0" {
			t.Fatalf("%s by %s; want active after T3 onto the mesh", s.Intent.State, s.Intent.LastTransition)
		}
	})
}

// S21 and the legacy mapping (r3 D18): a publish whose acknowledgement was
// lost is unknown, never "not sent"; only an error that says nothing left
// is not sent. On the mesh a failed serial write is unknown, so the intent
// ends "may have been sent".
func TestIntentS21_LostAcknowledgementIsUnknown(t *testing.T) {
	if o := LegacyOutcome(errors.New("mqtt: publish timeout")); o.Class != database.ClassUnknown {
		t.Fatalf("publish timeout: %s; want unknown", o.Class)
	}
	if o := LegacyOutcome(fmt.Errorf("mqtt: %w", transport.ErrNotConnected)); o.Class != database.ClassNotSent || !o.Retryable {
		t.Fatalf("not connected: %+v; want retryable not_sent", o)
	}
	if o := LegacyOutcome(fmt.Errorf("x: %w", transport.ErrNotSubmitted)); o.Class != database.ClassNotSent {
		t.Fatalf("not submitted: %+v", o)
	}
	h := newIntentHarness(t)
	id := h.submit(h.contact(meshAddr(0)), "y", directory.StrategyPrimaryOnly)
	h.mesh.err = errors.New("write /dev/ttyACM1: input/output error")
	h.work("mesh_0")
	h.eval(id)
	if s := h.snap(id); s.Intent.Outcome != database.OutcomeMayHaveBeenSent || s.Intent.State != database.IntentClosed {
		t.Fatalf("%s/%s; want closed, may_have_been_sent", s.Intent.State, s.Intent.Outcome)
	}
}
