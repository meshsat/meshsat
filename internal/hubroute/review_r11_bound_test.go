package hubroute

// The r11 review's reproduction of HM-R7, kept as a regression, unchanged but for gofmt: an
// expiry kept by a copy whose missing row could not be struck was given up at the bound as
// an unknown outcome. On fb7d4d29 it fails as the review describes. [MESHSAT-1584]

import (
	"encoding/json"
	"meshsat/internal/gateway"
	"meshsat/internal/hubmsg"
	"testing"
	"time"
)

func TestReviewR11RecoveredExpiryIsNotGivenUp(t *testing.T) {
	db, now := newDB(t), time.Now()
	ids := putOps(t, db, now, 1)
	op, err := db.GetHubOp(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	clock := now
	w := &walkSession{asked: map[string]int{}}
	r := New(db, w, Config{Now: func() time.Time { return clock }})
	unknown := 0
	r.SetIntentEvidenceSink(func(rc gateway.IntentReceipt) error {
		if rc.Event == "hub_outcome_unknown" {
			unknown++
		}
		return nil
	})
	ev := hubmsg.Event{V: 1, OpID: op.OpID, Mid: op.Mid, Recipient: op.Recipient, EventID: hubmsg.NewV7(now), Class: "expired", Kind: "hub_expired", Authority: "hub", Reason: "expired", At: now.UTC().Format(time.RFC3339)}
	raw, _ := json.Marshal(ev)
	if _, err = db.Exec(`CREATE TRIGGER review_insert BEFORE INSERT ON hub_op_events BEGIN SELECT RAISE(ABORT,'insert unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	r.HandleStatus(raw)
	if _, err = db.Exec(`DROP TRIGGER review_insert`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TRIGGER review_delete BEFORE DELETE ON hub_op_missing BEGIN SELECT RAISE(ABORT,'cleanup unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	r.HandleStatus(raw)
	kept, err := db.HubOpEventKept(ev.EventID)
	if err != nil || !kept {
		t.Fatal("recovery did not keep the event", err)
	}
	if _, err = db.Exec(`DROP TRIGGER review_delete`); err != nil {
		t.Fatal(err)
	}
	clock = time.UnixMilli(op.ExpiresAt).Add(ReplayWithin + time.Second)
	r.RequestReplays()
	saved, err := db.GetHubOp(op.OpID)
	if err != nil {
		t.Fatal(err)
	}
	missing, err := db.HubOpMissing(op.OpID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("kept=%v unknown=%d given_up=%d owed=%d missing=%d", kept, unknown, saved.ReplayGivenUpAt, saved.ReplayAt, len(missing))
	if unknown != 0 || saved.ReplayGivenUpAt != 0 {
		t.Error("recovered expiry was falsely reported as unknown and given up")
	}
}
