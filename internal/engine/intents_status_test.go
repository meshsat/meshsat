package engine

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/transport"
)

// S13: cancel, and separately complete, an intent after a worker fetched
// its row but before the claim: the claim changes nothing, nothing is sent.
// With a row already in flight: its outcome is recorded, possible
// duplicate when another round also went out, no further attempt.
func TestIntentS13_FetchedRowAfterCancelOrCompletion(t *testing.T) {
	t.Run("cancel between fetch and claim", func(t *testing.T) {
		h := newIntentHarness(t)
		id := h.submit(h.contact(meshAddr(0)), "a", directory.StrategyPrimaryOnly)
		fetched, _ := h.db.GetPendingDeliveries("mesh_0", 10)
		if _, err := h.esc.Cancel(id, "test"); err != nil {
			t.Fatal(err)
		}
		h.workers["mesh_0"].deliver(h.ctx, fetched[0])
		s := h.snap(id)
		if h.mesh.attempts != 0 || s.Intent.Outcome != database.OutcomeCancelled || rowsByRound(s)[1].Status != database.AttemptCancelled {
			t.Fatalf("%d adapter calls, outcome %s, row %s", h.mesh.attempts, s.Intent.Outcome, rowsByRound(s)[1].Status)
		}
	})
	t.Run("completion between fetch and claim", func(t *testing.T) {
		h := newIntentHarness(t)
		id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "b", directory.StrategyOrderedFallback)
		h.work("mesh_0")
		pkt := h.mesh.lastID()
		h.eval(id)
		h.clock.Advance(121 * time.Second)
		h.eval(id) // T5: round 2 queued
		fetched, _ := h.db.GetPendingDeliveries("lxmf_0", 10)
		h.ack(pkt, annMesh, 0) // T9
		h.workers["lxmf_0"].deliver(h.ctx, fetched[0])
		if h.lx.n() != 0 {
			t.Fatal("a row retired by the completion was sent")
		}
	})
	t.Run("cancel while a row is in flight", func(t *testing.T) {
		h := newIntentHarness(t)
		id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "c", directory.StrategyOrderedFallback)
		h.work("mesh_0")
		h.eval(id)
		h.clock.Advance(121 * time.Second)
		h.eval(id) // T5
		h.lx.block, h.lx.entered = make(chan struct{}), make(chan struct{}, 1)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); h.work("lxmf_0") }()
		<-h.lx.entered // the adapter is running: the row is handed off
		if _, err := h.esc.Cancel(id, "test"); err != nil {
			t.Fatal(err)
		}
		in, _ := h.db.GetIntent(id)
		if in.State != database.IntentClosed || in.Outcome != database.OutcomeAcceptedUnconfirmed {
			t.Fatalf("at cancel: %s/%s; want closed, accepted_unconfirmed (round 1 was accepted)", in.State, in.Outcome)
		}
		v := BuildIntentView(h.snap(id))
		if !hasNote(v.Notes, "may still be sent on lxmf_0") {
			t.Fatalf("notes %v; want 'may still be sent on lxmf_0'", v.Notes)
		}
		close(h.lx.block)
		wg.Wait()
		h.drain()
		h.eval(id)
		s := h.snap(id)
		if r := rowsByRound(s)[2]; r.Status != database.AttemptAccepted {
			t.Fatalf("the in-flight row is %s; want its outcome recorded", r.Status)
		}
		if !s.Intent.PossibleDuplicate || s.Intent.Outcome != database.OutcomeAcceptedDestinationNode || s.Intent.LastTransition != "T14" {
			t.Fatalf("dup %v, %s by %s; want possible duplicate, the late proof upgrading the outcome (T14)",
				s.Intent.PossibleDuplicate, s.Intent.Outcome, s.Intent.LastTransition)
		}
		if len(s.Rows) != 2 {
			t.Fatalf("%d rows; want no further attempt", len(s.Rows))
		}
	})
}

func hasNote(notes []string, want string) bool {
	for _, n := range notes {
		if n == want {
			return true
		}
	}
	return false
}

func routeFacts(v IntentView, iface string) []string {
	for _, r := range v.Routes {
		if r.InterfaceID == iface {
			return r.Facts
		}
	}
	return nil
}

// S23: ALL_BEARERS with one sibling confirmed, then a cancel: the
// top-level outcome stays the confirmation and the cancel is listed under
// it. With one sibling confirmed and a paid sibling ending unknown: the
// confirmation stays, the "may have been sent" sibling is listed (C1).
func TestIntentS23_PerRouteFactsUnderTheOutcome(t *testing.T) {
	t.Run("confirmed, then cancel", func(t *testing.T) {
		h := newIntentHarness(t)
		id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "d", directory.StrategyAllBearers)
		h.work("lxmf_0")
		h.eval(id) // T10: outcome from the proof, the mesh sibling still queued
		if s := h.snap(id); s.Intent.State != database.IntentActive || s.Intent.Outcome != database.OutcomeAcceptedDestinationNode {
			t.Fatalf("after the proof: %s/%s; want active (siblings go on), the proof's outcome", s.Intent.State, s.Intent.Outcome)
		}
		if _, err := h.esc.Cancel(id, "test"); err != nil {
			t.Fatal(err)
		}
		s := h.snap(id)
		if s.Intent.Outcome != database.OutcomeAcceptedDestinationNode {
			t.Fatalf("outcome %s; want the confirmation kept", s.Intent.Outcome)
		}
		v := BuildIntentView(s)
		if f := routeFacts(v, "mesh_0"); !hasNote(f, "cancelled") {
			t.Fatalf("mesh facts %v; want cancelled", f)
		}
		if f := routeFacts(v, "lxmf_0"); !hasNote(f, "accepted by the recipient's Reticulum node (signed)") {
			t.Fatalf("lxmf facts %v", f)
		}
		h.work("mesh_0")
		if h.mesh.attempts != 0 {
			t.Fatal("a cancelled sibling was sent")
		}
	})
	t.Run("confirmed, and a paid sibling unknown", func(t *testing.T) {
		h := newIntentHarness(t)
		id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "e", directory.StrategyAllBearers)
		h.setPlanPaid(id, 1) // the mesh route, as a fixture
		h.work("lxmf_0")
		h.mesh.err = errExampleWrite
		h.work("mesh_0")
		h.eval(id)
		s := h.snap(id)
		if s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeAcceptedDestinationNode {
			t.Fatalf("%s/%s by %s; want closed with the confirmation (T7 does not close ALL_BEARERS)", s.Intent.State, s.Intent.Outcome, s.Intent.LastTransition)
		}
		if f := routeFacts(BuildIntentView(s), "mesh_0"); !hasNote(f, "may have been sent on mesh_0") {
			t.Fatalf("mesh facts %v", f)
		}
	})
}

var errExampleWrite = errorString("write /dev/ttyACM1: broken pipe")

type errorString string

func (e errorString) Error() string { return string(e) }

// S20: deadlines run on elapsed time. A wall jump of a day either way
// fires nothing. In a crash loop (a restart every 20 s) a 300 s queue
// allowance fires no later than 300 s and at most 10 s early per restart;
// an untrusted clock at boot counts no downtime.
func TestIntentS20_Clocks(t *testing.T) {
	t.Run("wall jumps", func(t *testing.T) {
		h := newIntentHarness(t)
		id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "f", directory.StrategyOrderedFallback)
		h.mesh.err = transport.ErrNotConnected
		h.work("mesh_0")
		for _, j := range []time.Duration{24 * time.Hour, -48 * time.Hour} {
			h.clock.JumpWall(j)
			h.esc.Heartbeat()
			if s := h.snap(id); s.Intent.ActiveRound != 1 || s.Intent.State != database.IntentActive {
				t.Fatalf("after a wall jump of %v: round %d, %s", j, s.Intent.ActiveRound, s.Intent.State)
			}
		}
	})
	t.Run("crash loop", func(t *testing.T) {
		h := newIntentHarness(t, func(c *EscalatorConfig) { c.QueueAllowS = 300; c.ExpiryAllowS = 7200 })
		id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "g", directory.StrategyOrderedFallback)
		h.mesh.err = transport.ErrNotConnected
		h.work("mesh_0")
		real, restarts := 0, 0
		fired := -1
		for real < 600 && fired < 0 {
			for i := 0; i < 2 && fired < 0; i++ {
				h.clock.Advance(10 * time.Second)
				real += 10
				h.esc.Heartbeat()
				if h.snap(id).Intent.ActiveRound == 2 {
					fired = real
				}
			}
			if fired < 0 {
				h.restart()
				restarts++
				if h.snap(id).Intent.ActiveRound == 2 {
					fired = real
				}
			}
		}
		if fired < 0 || fired > 300 || fired < 300-10*restarts {
			t.Fatalf("the 300 s allowance fired at %d s after %d restarts; want within [%d, 300]", fired, restarts, 300-10*restarts)
		}
	})
	t.Run("untrusted clock at boot counts no downtime", func(t *testing.T) {
		for _, trusted := range []bool{false, true} {
			h := newIntentHarness(t, func(c *EscalatorConfig) { c.ExpiryAllowS = 4 * 3600 })
			id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "h", directory.StrategyOrderedFallback)
			h.mesh.err = transport.ErrNotConnected
			h.work("mesh_0")
			h.clock.Advance(10 * time.Second)
			h.esc.Heartbeat()
			h.clock.Advance(1 * time.Hour) // down for an hour
			h.clock.mu.Lock()
			h.clock.trusted = trusted
			h.clock.mu.Unlock()
			h.restart()
			if got := h.snap(id).Intent.ActiveRound == 2; got != trusted {
				t.Fatalf("trusted=%v: advanced=%v; the downtime counts only on a trusted clock", trusted, got)
			}
		}
	})
}

// S14: idempotency. One submission per key; before the commit nothing
// exists, after it the payload and plan are there without the request; a
// retry returns the original IDs without resolving again; two concurrent
// retries get one submission; the same key with a changed payload or
// recipient is a conflict, nothing written or sent.
func TestIntentS14_Idempotency(t *testing.T) {
	h := newIntentHarness(t)
	c1 := h.contact(meshAddr(0))
	c2 := h.contact(lxmfAddr(0))
	req := IntentRequest{Source: "spa", IdempotencyKey: "k1", ContactID: c1, Body: []byte("once")}
	first, err := h.esc.Submit(h.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	resolves := h.resolver.resolves
	again, err := h.esc.Submit(h.ctx, req)
	if err != nil || !again.Existing || again.Submission.ID != first.Submission.ID || again.Intents[0].ID != first.Intents[0].ID {
		t.Fatalf("retry: %+v, %v", again, err)
	}
	if again.RowIDs[0][0] != first.RowIDs[0][0] || h.resolver.resolves != resolves {
		t.Fatalf("retry rows %v vs %v, resolver %d -> %d calls", again.RowIDs, first.RowIDs, resolves, h.resolver.resolves)
	}
	for name, changed := range map[string]IntentRequest{
		"payload":   {Source: "spa", IdempotencyKey: "k1", ContactID: c1, Body: []byte("twice")},
		"recipient": {Source: "spa", IdempotencyKey: "k1", ContactID: c2, Body: []byte("once")},
	} {
		if _, err := h.esc.Submit(h.ctx, changed); err != database.ErrIdempotencyConflict {
			t.Fatalf("changed %s: %v; want ErrIdempotencyConflict", name, err)
		}
	}
	// The same key from another source is another request.
	if other, err := h.esc.Submit(h.ctx, IntentRequest{Source: "hub", IdempotencyKey: "k1", ContactID: c1, Body: []byte("once")}); err != nil || other.Existing {
		t.Fatalf("another source: %+v %v", other, err)
	}
	var n int
	_ = h.db.QueryRow(`SELECT COUNT(*) FROM delivery_submissions`).Scan(&n)
	if n != 2 {
		t.Fatalf("%d submissions; want 2", n)
	}

	var wg sync.WaitGroup
	ids := make([]string, 4)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := h.esc.Submit(h.ctx, IntentRequest{Source: "api", IdempotencyKey: "k2", ContactID: c1, Body: []byte("race")})
			if err == nil {
				ids[i] = s.Submission.ID
			}
		}(i)
	}
	wg.Wait()
	for _, id := range ids {
		if id == "" || id != ids[0] {
			t.Fatalf("concurrent retries: %v; want one submission", ids)
		}
	}

	// A commit that fails leaves nothing: two round-1 rows with one
	// attempt number break the unique index inside the transaction.
	in := first.Intents[0]
	in.ID, in.SubmissionID = "fail-intent", "fail-sub"
	row := database.MessageDelivery{MsgRef: "x", Channel: "mesh_0", IntentID: in.ID, Round: 1, AttemptNo: 1, RouteIndex: 1, Payload: []byte("x")}
	_, _, err = h.db.CreateSubmission(database.DeliverySubmission{ID: "fail-sub", Source: "api", RequestSHA256: "f"},
		[]database.NewIntent{{Intent: in, FirstRows: []database.MessageDelivery{row, row}}})
	if err == nil {
		t.Fatal("a broken submission committed")
	}
	if s, _ := h.db.GetSubmission("fail-sub"); s != nil {
		t.Fatal("the failed submission exists")
	}
	if _, err := h.db.GetIntent("fail-intent"); err != database.ErrIntentNotFound {
		t.Fatalf("the failed intent: %v", err)
	}
	snap := h.snap(first.Intents[0].ID)
	if string(snap.Intent.Payload) != "once" || len(snap.Intent.RoutePlan) != 1 || snap.Intent.RoutePlan[0].Destination != annMesh {
		t.Fatalf("committed intent without its request: payload %q, plan %+v", snap.Intent.Payload, snap.Intent.RoutePlan)
	}
}

// S15: evidence completes only what it identifies. A proof means accepted
// by the recipient's Reticulum node, never more. Two transmissions under
// one packet id: an ACK naming it is ambiguous and completes nothing. An
// ACK naming no transmission is stored unmatched. A NAK ends the receipt
// wait on a free route (the next route goes at once).
func TestIntentS15_EvidenceMatching(t *testing.T) {
	t.Run("a proof is node acceptance", func(t *testing.T) {
		h := newIntentHarness(t)
		id := h.submit(h.contact(lxmfAddr(0)), "i", directory.StrategyPrimaryOnly)
		h.work("lxmf_0") // the receiver may reject the message after proving the packet
		h.eval(id)
		v := BuildIntentView(h.snap(id))
		if v.Outcome != database.OutcomeAcceptedDestinationNode || strings.Contains(v.Label, "delivered") {
			t.Fatalf("%s / %q", v.Outcome, v.Label)
		}
	})
	t.Run("one packet id on two transmissions", func(t *testing.T) {
		h := newIntentHarness(t)
		h.mesh.fixedID = 0x00c0ffee // a radio that picks its own ids, and picks one twice
		h.workers["mesh_0"].mesh = pickOwnIDMesh{m: h.mesh}
		c := h.contact(meshAddr(0))
		a := h.submit(c, "j", directory.StrategyPrimaryOnly)
		b := h.submit(c, "k", directory.StrategyPrimaryOnly)
		h.work("mesh_0")
		h.ack(0x00c0ffee, annMesh, 0)
		h.eval(a)
		h.eval(b)
		for _, id := range []string{a, b} {
			if s := h.snap(id); s.Intent.State != database.IntentActive || s.Intent.Outcome != database.OutcomePending {
				t.Fatalf("intent %s: %s/%s; an ambiguous ACK completes nothing", id, s.Intent.State, s.Intent.Outcome)
			}
		}
		var amb int
		_ = h.db.QueryRow(`SELECT COUNT(*) FROM delivery_evidence WHERE ambiguous = 1 AND intent_id IS NULL AND candidates LIKE '[%,%]'`).Scan(&amb)
		if amb != 1 {
			t.Fatalf("%d ambiguous evidence rows; want 1 with both candidates", amb)
		}
	})
	t.Run("an ACK for nothing we sent", func(t *testing.T) {
		h := newIntentHarness(t)
		h.ack(0x12345678, annMesh, 0)
		var n int
		_ = h.db.QueryRow(`SELECT COUNT(*) FROM delivery_evidence WHERE matched = 0 AND ambiguous = 0 AND intent_id IS NULL`).Scan(&n)
		if n != 1 {
			t.Fatalf("%d unmatched rows; want 1", n)
		}
	})
	t.Run("an ACK from a relay or our own radio completes nothing", func(t *testing.T) {
		h := newIntentHarness(t)
		id := h.submit(h.contact(meshAddr(0)), "l", directory.StrategyPrimaryOnly)
		h.work("mesh_0")
		h.ack(h.mesh.lastID(), "!0000aaaa", 0)
		if s := h.snap(id); s.Intent.State != database.IntentActive || len(s.Evidence) != 1 || s.Evidence[0].Event != "relay_ack" {
			t.Fatalf("%s, evidence %+v", s.Intent.State, s.Evidence)
		}
	})
	t.Run("a NAK moves on at once", func(t *testing.T) {
		h := newIntentHarness(t)
		id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "m", directory.StrategyOrderedFallback)
		h.work("mesh_0")
		h.eval(id)
		h.ack(h.mesh.lastID(), "!0000aaaa", 5) // MAX_RETRANSMIT from our own radio
		if s := h.snap(id); s.Intent.LastTransition != "T5" || s.Intent.ActiveRound != 2 {
			t.Fatalf("after a NAK: %s, round %d; want T5 into round 2", s.Intent.LastTransition, s.Intent.ActiveRound)
		}
	})
}

// S10: with the flag off a new send takes the legacy path, while an intent
// from before still ends under its one owner; with no owner at all an
// intent's row is never sent, and the legacy recovery never touches it.
func TestIntentS10_RollbackKeepsOneOwner(t *testing.T) {
	h := newIntentHarness(t)
	c := h.contact(meshAddr(0))
	id := h.submit(c, "before", directory.StrategyPrimaryOnly)

	h.start(false) // restarted with MESHSAT_DELIVERY_INTENTS=false, an intent still active
	res, err := h.d.SendToRecipient(h.ctx, RecipientRef{ContactID: c}, []byte("after"), SendOptions{})
	if err != nil || res.Intent != nil {
		t.Fatalf("flag off: %+v %v; want the legacy path", res.Intent, err)
	}
	legacy, _ := h.db.GetDelivery(res.DeliveryIDs[directory.KindMeshtastic][0])
	if legacy.IntentID != "" {
		t.Fatal("the legacy send became an intent attempt")
	}
	h.work("mesh_0")
	s := h.snap(id)
	pkt := txOf(s, rowsByRound(s)[1].ID)[0].CorrValue
	n, _ := strconv.ParseUint(pkt, 16, 32)
	h.ack(uint32(n), annMesh, 0)
	h.eval(id)
	if s := h.snap(id); s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeDeliveredDevice {
		t.Fatalf("the earlier intent: %s/%s; want it to finish under the Escalator", s.Intent.State, s.Intent.Outcome)
	}

	// No owner: the worker leaves the row; the legacy recovery skips it.
	h.start(true)
	id2 := h.submit(c, "orphan", directory.StrategyPrimaryOnly)
	before := h.mesh.attempts
	w := &DeliveryWorker{db: h.db, channelID: "mesh_0", mesh: h.mesh}
	dels, _ := h.db.GetPendingDeliveries("mesh_0", 10)
	for _, del := range dels {
		w.deliver(h.ctx, del)
	}
	if h.mesh.attempts != before {
		t.Fatal("an intent's row was sent with no Escalator")
	}
	row := rowsByRound(h.snap(id2))[1]
	_, _ = h.db.ClaimDeliveryForSending(row.ID)
	if n, _ := h.db.RecoverStaleDeliveries(); n != 0 {
		t.Fatalf("the legacy recovery took %d intent rows", n)
	}
	if n, _ := h.db.EndStaleSatelliteSends(); n != 0 {
		t.Fatalf("the satellite recovery took %d intent rows", n)
	}
	if err := h.db.CancelDelivery(row.ID); err == nil {
		t.Fatal("the queue's cancel took an intent's row")
	}
}

// S9: the SOS and the other legacy rows are untouched. A legacy row is
// claimed as before; an SOS leg's Hub frame keeps priority 0 and its
// recovery; no intent attempt is ever priority 0 or class hub_uplink, so
// none can take the SOS's budget exception.
func TestIntentS9_SOSAndLegacyRowsUnchanged(t *testing.T) {
	h := newIntentHarness(t)
	frameID, _, err := h.d.QueueDirectSendTo("mesh_0", "SOS", DirectSendOptions{Critical: true, Class: database.DeliveryClassHubUplink,
		MsgRef: "sos-1-hub", RetryForever: true})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := h.db.ClaimDeliveryForSending(frameID); err != nil || !ok {
		t.Fatalf("a legacy row is not claimed: %v %v", ok, err)
	}
	if n, _ := h.db.RecoverStaleDeliveries(); n != 1 {
		t.Fatalf("the SOS frame left sending was not recovered (%d)", n)
	}
	if f, _ := h.db.GetDelivery(frameID); f.Priority != 0 || f.Status != "retry" {
		t.Fatalf("SOS frame: priority %d, %s", f.Priority, f.Status)
	}
	id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "n", directory.StrategyAllBearers)
	for _, r := range h.snap(id).Rows {
		if r.Priority != 1 || r.Class == database.DeliveryClassHubUplink || r.Class == database.DeliveryClassSOS {
			t.Fatalf("intent row priority %d class %s", r.Priority, r.Class)
		}
	}
}
