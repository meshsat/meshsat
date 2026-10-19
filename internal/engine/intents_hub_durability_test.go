package engine

import (
	"encoding/json"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/hubmsg"
	"meshsat/internal/hubroute"
)

// The Hub's evidence reaches kit-a's ledger exactly once whichever write fails (r8 review
// HM-R1): an event the ledger did not take stays kept and pending, the route applies it
// again (after a restart, without the Hub), a copy of it applies it too, the ledger records it
// once by its key, and an event that could not even be kept makes the operation owed a
// repeat, which the Hub answers with every event under the same ids. Each case runs for a
// completion, an expiry the recipient reports, and the Hub's refusal. [MESHSAT-1584]

// failOn makes one statement on kit-a's database fail until the returned func is called.
func failOn(t *testing.T, db *database.DB, ddl string) func() {
	t.Helper()
	if _, err := db.Exec(`CREATE TRIGGER test_fail ` + ddl + ` BEGIN SELECT RAISE(ABORT, 'test: write failed'); END`); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := db.Exec(`DROP TRIGGER test_fail`); err != nil {
			t.Fatal(err)
		}
	}
}

// restartA builds kit-a's route again over its database, as a process start does, with the
// startup recorder (main.go).
func (p *hubPair) restartA() *hubroute.Route {
	a := hubroute.New(p.h.db, &hubLink{hub: p.hub, principal: "bridge:kit-a"}, hubroute.Config{})
	rec := NewIntentReceiptRecorder(p.h.db)
	rec.SetNotify(p.h.esc.Notify)
	a.SetIntentEvidenceSink(rec.Apply)
	p.hub.mu.Lock()
	p.hub.routes["bridge:kit-a"] = a
	p.hub.mu.Unlock()
	p.a = a
	return a
}

// ledgerRows counts kit-a's evidence rows under a Hub event's key.
func (p *hubPair) ledgerRows(t *testing.T, eventID string) int {
	t.Helper()
	var n int
	if err := p.h.db.QueryRow(`SELECT COUNT(*) FROM delivery_evidence WHERE dedup_key = ?`, "hub_event:"+eventID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (p *hubPair) appliedAt(t *testing.T, eventID string) int64 {
	t.Helper()
	var at *int64
	if err := p.h.db.QueryRow(`SELECT applied_at FROM hub_op_events WHERE event_id = ?`, eventID).Scan(&at); err != nil {
		t.Fatalf("event %s not kept: %v", eventID, err)
	}
	if at == nil {
		return 0
	}
	return *at
}

// hubCase is one kind of evidence the ledger must take: what makes it, the evidence event it
// is in the ledger, its kept id, and what kit-a's intent then is.
type hubCase struct {
	name    string
	cfgB    hubroute.Config
	ledger  string // a LIKE pattern on delivery_evidence.event
	produce func(t *testing.T, p *hubPair) string
	settled func(t *testing.T, p *hubPair, id string)
}

func onlyOp(t *testing.T, p *hubPair) hubmsg.Submission {
	t.Helper()
	p.hub.mu.Lock()
	defer p.hub.mu.Unlock()
	if len(p.hub.ops) != 1 {
		t.Fatalf("%d operations at the Hub", len(p.hub.ops))
	}
	for _, s := range p.hub.ops {
		return s
	}
	return hubmsg.Submission{}
}

// eventOf returns the id of the event of kind the Hub published.
func eventOf(t *testing.T, p *hubPair, kind string) string {
	t.Helper()
	p.hub.mu.Lock()
	defer p.hub.mu.Unlock()
	for _, f := range p.hub.statuses {
		var e hubmsg.Event
		if json.Unmarshal(f.payload, &e) == nil && e.Kind == kind {
			return e.EventID
		}
	}
	t.Fatalf("the Hub published no %s", kind)
	return ""
}

func escalatedToMesh(t *testing.T, p *hubPair, id string) {
	t.Helper()
	p.h.work("mesh_0")
	p.h.drain()
	if s := p.h.snap(id); s.Intent.ActiveRound != 2 || p.h.mesh.sends() != 1 {
		t.Fatalf("round %d, %d mesh sends", s.Intent.ActiveRound, p.h.mesh.sends())
	}
}

func hubCases() []hubCase {
	return []hubCase{
		{name: "completed", ledger: "recipient_app_stored",
			produce: func(t *testing.T, p *hubPair) string {
				p.hub.pump(t)
				return eventOf(t, p, "recipient_app_stored")
			},
			settled: func(t *testing.T, p *hubPair, id string) {
				if s := p.h.snap(id); s.Intent.Outcome != database.OutcomeDeliveredApp {
					t.Fatalf("kit-a: %s/%s", s.Intent.State, s.Intent.Outcome)
				}
			}},
		{name: "expired", ledger: "nak:recipient_refused:%", cfgB: hubroute.Config{Now: func() time.Time { return time.Now().Add(10 * time.Minute) }},
			produce: func(t *testing.T, p *hubPair) string {
				p.hub.pump(t)
				return eventOf(t, p, "recipient_refused")
			},
			settled: escalatedToMesh},
		{name: "refusal", ledger: "dropped:hub_refusal:%",
			produce: func(t *testing.T, p *hubPair) string {
				p.hub.hold = true
				p.hub.pump(t) // admitted and delivered at the Hub, but held: kit-b never sees it
				p.hub.hold = false
				s := onlyOp(t, p)
				raw, _ := json.Marshal(hubmsg.Refusal{V: 1, Type: "refusal", OpID: s.OpID, Mid: s.Mid, Reason: "refused_unknown_recipient"})
				p.hub.publish("bridge:kit-a", "status", raw)
				return "refusal:" + s.OpID + ":refused_unknown_recipient"
			},
			settled: escalatedToMesh},
	}
}

// The ledger's write fails: the event stays kept and pending, nothing of it is in the ledger,
// and the intent waits. Then, with the fault gone: a restarted route applies it from what it
// kept, without the Hub; a copy of every frame after that records nothing more.
func TestHubEvidence_ALedgerWriteThatFailsIsAppliedAgainOnce(t *testing.T) {
	for _, c := range hubCases() {
		t.Run(c.name, func(t *testing.T) {
			p := newHubPair(t, c.cfgB)
			id := p.send(t, "evidence across a failed write")
			heal := failOn(t, p.h.db, `BEFORE INSERT ON delivery_evidence WHEN NEW.event LIKE '`+c.ledger+`'`)
			ev := c.produce(t, p)
			p.h.drain()
			if p.ledgerRows(t, ev) != 0 || p.appliedAt(t, ev) != 0 {
				t.Fatalf("the failed write left %d rows, applied_at %d", p.ledgerRows(t, ev), p.appliedAt(t, ev))
			}
			if s := p.h.snap(id); s.Intent.ActiveRound != 1 || s.Intent.Outcome == database.OutcomeDeliveredApp {
				t.Fatalf("decided on evidence the ledger never took: round %d %s", s.Intent.ActiveRound, s.Intent.Outcome)
			}
			heal()

			p.restartA().ApplyPending()
			p.h.drain()
			c.settled(t, p, id)
			if n := p.ledgerRows(t, ev); n != 1 || p.appliedAt(t, ev) == 0 {
				t.Fatalf("after the restart: %d rows, applied_at %d", n, p.appliedAt(t, ev))
			}
			p.hub.mu.Lock()
			sts := append([]hubFrame(nil), p.hub.statuses...)
			p.hub.mu.Unlock()
			for _, f := range sts {
				p.hub.replay(f)
			}
			p.a.ApplyPending()
			p.h.drain()
			if n := p.ledgerRows(t, ev); n != 1 {
				t.Fatalf("copies after recovery: %d rows", n)
			}
		})
	}
}

// The same failed write, recovered by the Hub's copy of the frame alone (no restart): the copy
// of a kept event the ledger does not have is applied, not dropped as a copy.
func TestHubEvidence_ACopyOfAPendingEventAppliesIt(t *testing.T) {
	for _, c := range hubCases() {
		t.Run(c.name, func(t *testing.T) {
			p := newHubPair(t, c.cfgB)
			id := p.send(t, "a copy applies it")
			heal := failOn(t, p.h.db, `BEFORE INSERT ON delivery_evidence WHEN NEW.event LIKE '`+c.ledger+`'`)
			ev := c.produce(t, p)
			p.h.drain()
			heal()
			p.hub.mu.Lock()
			sts := append([]hubFrame(nil), p.hub.statuses...)
			p.hub.mu.Unlock()
			for _, f := range sts {
				p.hub.replay(f)
			}
			p.h.drain()
			c.settled(t, p, id)
			if n := p.ledgerRows(t, ev); n != 1 || p.appliedAt(t, ev) == 0 {
				t.Fatalf("%d rows, applied_at %d", n, p.appliedAt(t, ev))
			}
		})
	}
}

// The ledger takes the event but the mark that it did fails (a stop between the two writes):
// applied again by the restarted route and by a copy, it is still one row in the ledger.
func TestHubEvidence_InTheLedgerButNotMarkedIsRecordedOnce(t *testing.T) {
	for _, c := range hubCases() {
		t.Run(c.name, func(t *testing.T) {
			p := newHubPair(t, c.cfgB)
			id := p.send(t, "applied, not marked")
			heal := failOn(t, p.h.db, `BEFORE UPDATE OF applied_at ON hub_op_events`)
			ev := c.produce(t, p)
			p.h.drain()
			if n := p.ledgerRows(t, ev); n != 1 || p.appliedAt(t, ev) != 0 {
				t.Fatalf("%d rows, applied_at %d", n, p.appliedAt(t, ev))
			}
			heal()
			p.restartA().ApplyPending()
			p.hub.mu.Lock()
			sts := append([]hubFrame(nil), p.hub.statuses...)
			p.hub.mu.Unlock()
			for _, f := range sts {
				p.hub.replay(f)
			}
			p.h.drain()
			c.settled(t, p, id)
			if n := p.ledgerRows(t, ev); n != 1 || p.appliedAt(t, ev) == 0 {
				t.Fatalf("applied again: %d rows, applied_at %d", n, p.appliedAt(t, ev))
			}
		})
	}
}

// The event cannot even be kept: the operation is owed a repeat, kit-a publishes its
// submission again unchanged once it can, the Hub answers with every event again, and the
// one that was lost reaches the ledger under the id the Hub gave it, once.
func TestHubEvidence_AnEventNotKeptIsAskedForAgain(t *testing.T) {
	for _, c := range hubCases() {
		if c.name == "refusal" {
			continue // a refusal is not an event the Hub stores, so a repeat does not bring it back; it is answered anew (next test)
		}
		t.Run(c.name, func(t *testing.T) {
			p := newHubPair(t, c.cfgB)
			id := p.send(t, "lost on the way in")
			kind := map[string]string{"completed": "recipient_app_stored", "expired": "recipient_refused"}[c.name]
			heal := failOn(t, p.h.db, `BEFORE INSERT ON hub_op_events WHEN NEW.kind = '`+kind+`'`)
			ev := c.produce(t, p)
			p.h.drain()
			s := onlyOp(t, p)
			op, err := p.h.db.GetHubOp(s.OpID)
			if err != nil || op == nil || op.ReplayAt == 0 {
				t.Fatalf("no repeat owed: %+v %v", op, err)
			}
			heal()
			p.restartA().RequestReplays()
			p.hub.pump(t)
			p.h.drain()
			c.settled(t, p, id)
			if n := p.ledgerRows(t, ev); n != 1 || p.appliedAt(t, ev) == 0 || p.hub.replays != 1 {
				t.Fatalf("%d rows, applied_at %d, %d replays", n, p.appliedAt(t, ev), p.hub.replays)
			}
			if op, _ := p.h.db.GetHubOp(s.OpID); op.ReplayAt != 0 {
				t.Fatal("still owed a repeat after it went out")
			}
			if n := storedRows(t, p.dbB); n > 1 {
				t.Fatalf("the repeat stored the message again at kit-b: %d", n)
			}
		})
	}
}

// A refusal the route could not keep: the repeat is a new submission to the Hub, answered
// again with the same refusal, kept and applied once under the same id.
func TestHubEvidence_ARefusalNotKeptIsAnsweredAgain(t *testing.T) {
	p := newHubPair(t, hubroute.Config{})
	id := p.send(t, "refused twice")
	heal := failOn(t, p.h.db, `BEFORE INSERT ON hub_op_events WHEN NEW.kind = 'hub_refusal'`)
	c := hubCases()[2]
	ev := c.produce(t, p)
	p.h.drain()
	heal()
	s := onlyOp(t, p)
	if op, _ := p.h.db.GetHubOp(s.OpID); op == nil || op.ReplayAt == 0 {
		t.Fatal("no repeat owed")
	}
	// The Hub that refused it refuses the repeat the same way.
	raw, _ := json.Marshal(hubmsg.Refusal{V: 1, Type: "refusal", OpID: s.OpID, Mid: s.Mid, Reason: "refused_unknown_recipient"})
	p.restartA().RequestReplays()
	p.hub.mu.Lock()
	p.hub.queue = nil // the repeat, answered here by the refusal
	p.hub.mu.Unlock()
	p.hub.publish("bridge:kit-a", "status", raw)
	p.h.drain()
	escalatedToMesh(t, p, id)
	if n := p.ledgerRows(t, ev); n != 1 {
		t.Fatalf("%d rows", n)
	}
}

func storedRows(t *testing.T, db *database.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE transport = ?`, database.HubTransport).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
