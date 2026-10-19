package hubroute

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/hubmsg"
)

// A start asks once for every operation without a completion, however many there are, a
// batch per pass, without another restart; a publish that fails or a pass off the Hub holds
// the walk where it is; an operation asked and quiet is not asked again; and the owed are
// asked in turn, so none waits behind a first batch that stays owed (r10 review HM-R6).
// [MESHSAT-1584]

// walkSession records each repeat by its operation; failAt fails the n-th publish (1-based)
// once, and down takes the session off the Hub.
type walkSession struct {
	asked   map[string]int
	publish int
	failAt  int
	down    bool
}

func (w *walkSession) IsConnected() bool { return !w.down }
func (w *walkSession) Principal() string { return "bridge:kit-a" }
func (w *walkSession) PublishMsg(kind string, b []byte, _ time.Duration) error {
	if kind != "out" {
		return errors.New("unexpected " + kind)
	}
	w.publish++
	if w.publish == w.failAt {
		return errors.New("no PUBACK")
	}
	var s hubmsg.Submission
	_ = json.Unmarshal(b, &s)
	w.asked[s.OpID]++
	return nil
}

// putOps stores n operations with kept submissions, inside the bound, no events.
func putOps(t *testing.T, db *database.DB, now time.Time, n int) []string {
	t.Helper()
	var ids []string
	for i := 0; i < n; i++ {
		id := hubmsg.NewV7(now.Add(time.Duration(i) * time.Millisecond))
		sub, err := hubmsg.NewSubmission(id, hubmsg.NewV7(now), "bridge:kit-b", "recover", now.Add(2*time.Hour).UnixMilli(), now.Add(time.Hour).UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(sub)
		if err := db.PutHubOp(database.HubOp{OpID: id, Mid: sub.Mid, Recipient: "bridge:kit-b", ExpiresAt: sub.ExpiresAt,
			CreatedAt: now.UnixMilli() + int64(i%3), Submission: string(raw)}); err != nil { // created_at ties: the op id orders them
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestTheStartAsksForEveryOperationABatchAPass(t *testing.T) {
	db, now := newDB(t), time.Now()
	ids := putOps(t, db, now, 250)
	w := &walkSession{asked: map[string]int{}, failAt: 150}
	r := New(db, w, Config{Now: func() time.Time { return now }})
	w.down = true
	r.RequestReplays()
	r.RequestReplays()
	if len(w.asked) != 0 {
		t.Fatalf("asked %d off the Hub", len(w.asked))
	}
	w.down = false
	for pass := 1; pass <= 4; pass++ {
		r.RequestReplays()
	}
	for _, id := range ids {
		if w.asked[id] != 1 {
			t.Fatalf("operation %s asked %d times", id, w.asked[id])
		}
	}
	if w.publish != 251 { // 250 and the one whose PUBACK did not come, asked again at the next pass
		t.Fatalf("%d publishes", w.publish)
	}
	// Asked and quiet: nothing more, however many passes.
	for pass := 0; pass < 5; pass++ {
		r.RequestReplays()
	}
	if w.publish != 251 {
		t.Fatalf("quiet operations asked again: %d publishes", w.publish)
	}
}

// The reviewer's case at the first batch's edge, with the unresolved first batch still
// unresolved when the walk goes on: every one is asked, the 201st included.
func TestTheStartWalksPastAnUnresolvedFirstBatch(t *testing.T) {
	db, now := newDB(t), time.Now()
	ids := putOps(t, db, now, 201)
	w := &walkSession{asked: map[string]int{}}
	r := New(db, w, Config{Now: func() time.Time { return now }})
	r.RequestReplays()
	if len(w.asked) != startBatch {
		t.Fatalf("first pass asked %d", len(w.asked))
	}
	r.RequestReplays()
	r.RequestReplays()
	if len(w.asked) != len(ids) || w.asked[ids[len(ids)-1]] != 1 {
		t.Fatalf("asked %d of %d", len(w.asked), len(ids))
	}
}

// 120 operations owed, more than a pass takes: every one is asked within three passes, though
// none of the first 50 is resolved, and a fourth pass comes back to the first in turn.
func TestTheOwedAreAskedInTurn(t *testing.T) {
	db, now := newDB(t), time.Now()
	w := &walkSession{asked: map[string]int{}}
	clock := now
	r := New(db, w, Config{Now: func() time.Time { return clock }})
	r.RequestReplays() // the start's walk, over nothing: done
	ids := putOps(t, db, now, 120)
	for i, id := range ids {
		if err := db.WantHubOpReplay(id, "e-"+id, now.Add(time.Duration(i)*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	for pass := 1; pass <= 3; pass++ {
		r.RequestReplays()
		if want := min(pass*owedBatch, len(ids)); len(w.asked) != want {
			t.Fatalf("pass %d: %d asked, want %d", pass, len(w.asked), want)
		}
	}
	clock = now.Add(time.Minute) // past the 30 s wait of a Hub that has not answered
	r.RequestReplays()
	if w.asked[ids[0]] != 2 || w.asked[ids[len(ids)-1]] != 1 {
		t.Fatalf("the round came back to %d / the last %d", w.asked[ids[0]], w.asked[ids[len(ids)-1]])
	}
}
