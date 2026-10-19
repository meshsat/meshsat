package engine

// The r10 review's reproduction of HM-R5, kept as a regression, unchanged but for gofmt: an
// expiry kept before a completion that could not be kept took the operation for settled, and
// the completion was never asked for again. On 64a3990a both variants fail. [MESHSAT-1584]

import (
	"meshsat/internal/database"
	"meshsat/internal/hubroute"
	"testing"
)

func TestReviewR10CompletionLostAfterExpiry(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "warm"
		if restart {
			name = "reopen"
		}
		t.Run(name, func(t *testing.T) {
			p := newHubPair(t, hubroute.Config{})
			id := p.send(t, "late completion must recover")
			p.hub.hold = true
			p.hub.pump(t)
			p.hub.hold = false
			s := onlyOp(t, p)
			p.hub.publish("bridge:kit-a", "status", p.hub.event(s, "hub_expired", "expired", "hub", "expired"))
			p.h.drain()
			heal := failOn(t, p.h.db, `BEFORE INSERT ON hub_op_events WHEN NEW.kind='recipient_app_stored'`)
			p.hub.publish("bridge:kit-a", "status", p.hub.event(s, "recipient_app_stored", "completed", "recipient", ""))
			ev := eventOf(t, p, "recipient_app_stored")
			if p.owedSince(t) == 0 {
				t.Fatal("missing initial owed marker")
			}
			heal()
			if restart {
				p.reopenA(t)
			}
			p.a.RequestReplays()
			p.hub.pump(t)
			p.a.ApplyPending()
			p.h.drain()
			t.Logf("outcome=%s replays=%d completion_rows=%d owed=%d", p.h.snap(id).Intent.Outcome, p.hub.replays, p.ledgerRows(t, ev), p.owedSince(t))
			if p.h.snap(id).Intent.Outcome != database.OutcomeDeliveredApp {
				t.Error("completion was not recovered")
			}
			// Positive control: the same retained Hub event, delivered directly, still raises the outcome.
			for _, f := range p.hub.statuses {
				p.hub.replay(f)
			}
			p.h.drain()
			if p.h.snap(id).Intent.Outcome != database.OutcomeDeliveredApp || p.ledgerRows(t, ev) != 1 {
				t.Fatal("positive control failed")
			}
		})
	}
}
