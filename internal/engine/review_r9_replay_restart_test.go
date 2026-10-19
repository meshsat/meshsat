package engine

// The r9 review's reproductions of HM-R4, kept as regressions, unchanged but for gofmt: a
// repeat owed only in memory is lost with the process, and a repeat going out cleared an
// obligation raised while it was in flight. On ace79651 both fail as the review describes.
// [MESHSAT-1584]

import (
	"meshsat/internal/database"
	"meshsat/internal/hubroute"
	"testing"
	"time"
)

// The database rejects both receipt persistence and its replay-needed marker.
// The retained submission must provide recovery after the process loses RAM.
func TestReviewR9UnpersistedReplaySurvivesRestart(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "same_process_control"
		if restart {
			name = "route_restart"
		}
		t.Run(name, func(t *testing.T) {
			p := newHubPair(t, hubroute.Config{})
			id := p.send(t, "recover after database outage")
			heal := failOn(t, p.h.db, `BEFORE INSERT ON hub_op_events WHEN NEW.kind='recipient_app_stored'`)
			if _, err := p.h.db.Exec(`CREATE TRIGGER review_fail_replay BEFORE UPDATE OF replay_at ON hub_ops BEGIN SELECT RAISE(ABORT,'review database write failure'); END`); err != nil {
				t.Fatal(err)
			}
			ev := hubCases()[0].produce(t, p)
			p.h.drain()
			op := onlyOp(t, p)
			saved, err := p.h.db.GetHubOp(op.OpID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.ReplayAt != 0 {
				t.Fatal("fault did not prevent durable marker")
			}
			heal()
			if _, err = p.h.db.Exec(`DROP TRIGGER review_fail_replay`); err != nil {
				t.Fatal(err)
			}
			r := p.a
			if restart {
				r = p.restartA()
			}
			r.ApplyPending()
			r.RequestReplays()
			p.hub.pump(t)
			p.h.drain()
			got := p.h.snap(id).Intent.Outcome
			t.Logf("outcome=%s replays=%d completion ledger rows=%d", got, p.hub.replays, p.ledgerRows(t, ev))
			if got != database.OutcomeDeliveredApp {
				t.Errorf("retained submission did not recover lost completion after restart: %s", got)
			}
		})
	}
}

// A fast replay response can arrive before PublishMsg returns its broker acknowledgment.
type reviewReplayLink struct {
	*hubLink
	after func()
}

func (l *reviewReplayLink) PublishMsg(kind string, payload []byte, wait time.Duration) error {
	err := l.hubLink.PublishMsg(kind, payload, wait)
	if err == nil && kind == "out" {
		l.after()
	}
	return err
}
func TestReviewR9RepeatFailureMustRemainOwed(t *testing.T) {
	p := newHubPair(t, hubroute.Config{})
	id := p.send(t, "database still unavailable during replay")
	heal := failOn(t, p.h.db, `BEFORE INSERT ON hub_op_events WHEN NEW.kind='recipient_app_stored'`)
	ev := hubCases()[0].produce(t, p)
	p.h.drain()
	op := onlyOp(t, p)
	link := &reviewReplayLink{hubLink: &hubLink{hub: p.hub, principal: "bridge:kit-a"}, after: func() { p.hub.pump(t) }}
	r := hubroute.New(p.h.db, link, hubroute.Config{})
	rec := NewIntentReceiptRecorder(p.h.db)
	rec.SetNotify(p.h.esc.Notify)
	r.SetIntentEvidenceSink(rec.Apply)
	p.hub.mu.Lock()
	p.hub.routes["bridge:kit-a"] = r
	p.hub.mu.Unlock()
	// Completion fails a second time while its replay publication is in flight.
	r.RequestReplays()
	stored, err := p.h.db.GetHubOp(op.OpID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ReplayAt == 0 {
		t.Error("publishing the replay cleared a newly owed repeat while the completion still could not be stored")
	}
	heal()
	r.RequestReplays()
	r.ApplyPending()
	p.h.drain()
	got := p.h.snap(id).Intent.Outcome
	t.Logf("outcome=%s replays=%d completion rows=%d", got, p.hub.replays, p.ledgerRows(t, ev))
	if got != database.OutcomeDeliveredApp {
		t.Errorf("after database recovery got %s want delivered_app", got)
	}
}
