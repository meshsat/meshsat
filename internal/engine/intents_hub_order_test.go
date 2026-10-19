package engine

import (
	"encoding/json"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/hubmsg"
	"meshsat/internal/hubroute"
)

// A weaker terminal does not end the recovery of a stronger event that was lost (r10 review
// HM-R5): C6 orders completed > failed > expired and lets a completion follow either (the
// Hub records a stored answer that comes after its expiry), so kit-a keeps asking for a lost
// completion whatever weaker outcome it kept, and a copy of an older terminal ends nothing.
// Every case ends with delivered to the app and one matched ledger row under the completion's
// own event id. [MESHSAT-1584]

// weaker publishes to kit-a, with nothing handed on to kit-b, the weaker terminal the Hub
// recorded first.
func (p *hubPair) weaker(t *testing.T, kind string) hubmsg.Submission {
	t.Helper()
	p.hub.hold = true
	p.hub.pump(t) // admitted and delivered at the Hub, kit-b sees nothing
	p.hub.hold = false
	s := onlyOp(t, p)
	switch kind {
	case "expired":
		p.hub.publish("bridge:kit-a", "status", p.hub.event(s, "hub_expired", "expired", "hub", "expired"))
	case "failed":
		p.hub.publish("bridge:kit-a", "status", p.hub.event(s, "hub_refused", "failed", "hub", "refused_unknown_recipient"))
	}
	p.h.drain()
	return s
}

// frameOf returns the status frame of kind the Hub published.
func (p *hubPair) frameOf(t *testing.T, kind string) hubFrame {
	t.Helper()
	p.hub.mu.Lock()
	defer p.hub.mu.Unlock()
	for _, f := range p.hub.statuses {
		var e hubmsg.Event
		if json.Unmarshal(f.payload, &e) == nil && e.Kind == kind {
			return f
		}
	}
	t.Fatalf("the Hub published no %s", kind)
	return hubFrame{}
}

func (p *hubPair) completionMatched(t *testing.T, id, ev string) {
	t.Helper()
	if s := p.h.snap(id); s.Intent.Outcome != database.OutcomeDeliveredApp {
		t.Fatalf("kit-a: %s/%s", s.Intent.State, s.Intent.Outcome)
	}
	var n int
	if err := p.h.db.QueryRow(`SELECT COUNT(*) FROM delivery_evidence WHERE dedup_key = ? AND matched = 1`, "hub_event:"+ev).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d matched completion rows: %v", n, err)
	}
	if p.owedSince(t) != 0 {
		t.Fatal("still owed once the completion is kept")
	}
}

// After an expiry or a Hub failure is kept, the completion is lost: recovered in the same
// process, after a reopen, and after a reopen when the owed mark was lost too.
func TestHubOrder_ACompletionLostAfterAWeakerTerminalIsRecovered(t *testing.T) {
	for _, kind := range []string{"expired", "failed"} {
		for _, recovery := range []string{"warm", "reopen", "both writes fail, then reopen"} {
			t.Run(kind+"/"+recovery, func(t *testing.T) {
				p := newHubPair(t, hubroute.Config{})
				id := p.send(t, "a completion after a weaker outcome")
				s := p.weaker(t, kind)
				heal := failOn(t, p.h.db, `BEFORE INSERT ON hub_op_events WHEN NEW.kind = 'recipient_app_stored'`)
				markFails := recovery == "both writes fail, then reopen"
				if markFails {
					if _, err := p.h.db.Exec(`CREATE TRIGGER test_fail_mark BEFORE UPDATE OF replay_at ON hub_ops BEGIN SELECT RAISE(ABORT, 'test'); END`); err != nil {
						t.Fatal(err)
					}
				}
				p.hub.publish("bridge:kit-a", "status", p.hub.event(s, "recipient_app_stored", "completed", "recipient", ""))
				ev := eventOf(t, p, "recipient_app_stored")
				if owed := p.owedSince(t) != 0; owed == markFails {
					t.Fatalf("owed=%v with the mark failing=%v", owed, markFails)
				}
				heal()
				if markFails {
					if _, err := p.h.db.Exec(`DROP TRIGGER test_fail_mark`); err != nil {
						t.Fatal(err)
					}
				}
				a := p.a
				if recovery != "warm" {
					a = p.reopenA(t)
				}
				a.RequestReplays()
				p.hub.pump(t)
				a.ApplyPending()
				p.h.drain()
				p.completionMatched(t, id, ev)
				if p.hub.replays != 1 {
					t.Fatalf("%d repeats", p.hub.replays)
				}
			})
		}
	}
}

// A copy of the older terminal comes while the completion still cannot be written, and again
// after the fault is gone: it ends nothing, and the repeat brings the completion.
func TestHubOrder_AnOlderTerminalCopyEndsNothing(t *testing.T) {
	p := newHubPair(t, hubroute.Config{})
	id := p.send(t, "old copies")
	s := p.weaker(t, "expired")
	heal := failOn(t, p.h.db, `BEFORE INSERT ON hub_op_events WHEN NEW.kind = 'recipient_app_stored'`)
	p.hub.publish("bridge:kit-a", "status", p.hub.event(s, "recipient_app_stored", "completed", "recipient", ""))
	ev := eventOf(t, p, "recipient_app_stored")
	expiry := p.frameOf(t, "hub_expired")
	p.hub.replay(expiry) // while the completion still cannot be written
	if p.owedSince(t) == 0 {
		t.Fatal("a copy of the expiry ended the obligation of the lost completion")
	}
	heal()
	p.hub.replay(expiry) // after
	if p.owedSince(t) == 0 {
		t.Fatal("a copy of the expiry ended it after the fault")
	}
	a := p.routeA(&hubLink{hub: p.hub, principal: "bridge:kit-a"}, hubroute.Config{})
	a.RequestReplays()
	p.hub.pump(t)
	p.h.drain()
	p.completionMatched(t, id, ev)
}

// Only the completion is kept, the forwarded event before it was lost: the completion ends
// that obligation too, since nothing a replay could bring raises a completion.
func TestHubOrder_ACompletionEndsEveryObligation(t *testing.T) {
	p := newHubPair(t, hubroute.Config{})
	id := p.send(t, "forwarded lost")
	heal := failOn(t, p.h.db, `BEFORE INSERT ON hub_op_events WHEN NEW.kind = 'hub_forwarded'`)
	p.hub.pump(t)
	p.h.drain()
	heal()
	ev := eventOf(t, p, "recipient_app_stored")
	p.completionMatched(t, id, ev)
	if m, _ := p.h.db.HubOpMissing(onlyOp(t, p).OpID); len(m) != 0 {
		t.Fatalf("still missing: %v", m)
	}
}

// The same with the obligation held only in memory (its mark could not be written either) in
// a process that already made its start's walk: the copy of the expiry still ends nothing.
func TestHubOrder_AnOlderTerminalCopyEndsNothingInMemory(t *testing.T) {
	p := newHubPair(t, hubroute.Config{})
	a := p.routeA(&hubLink{hub: p.hub, principal: "bridge:kit-a"}, hubroute.Config{})
	a.RequestReplays() // the start's walk, over nothing yet: done
	id := p.send(t, "old copies, held in memory")
	s := p.weaker(t, "expired")
	heal := failOn(t, p.h.db, `BEFORE INSERT ON hub_op_events WHEN NEW.kind = 'recipient_app_stored'`)
	if _, err := p.h.db.Exec(`CREATE TRIGGER test_fail_mark BEFORE UPDATE OF replay_at ON hub_ops BEGIN SELECT RAISE(ABORT, 'test'); END`); err != nil {
		t.Fatal(err)
	}
	p.hub.publish("bridge:kit-a", "status", p.hub.event(s, "recipient_app_stored", "completed", "recipient", ""))
	ev := eventOf(t, p, "recipient_app_stored")
	p.hub.replay(p.frameOf(t, "hub_expired")) // the expiry again
	heal()
	if _, err := p.h.db.Exec(`DROP TRIGGER test_fail_mark`); err != nil {
		t.Fatal(err)
	}
	if p.owedSince(t) != 0 {
		t.Fatal("the mark was kept")
	}
	a.RequestReplays()
	p.hub.pump(t)
	p.h.drain()
	p.completionMatched(t, id, ev)
}
