package hubroute

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/hubmsg"
)

// fakeSession is this bridge's Hub session: kit-a. onAck, when set, runs at each answer's
// publish, before it is recorded (to look at the database at that moment).
type fakeSession struct {
	mu    sync.Mutex
	acks  []hubmsg.Ack
	onAck func(hubmsg.Ack)
}

func (f *fakeSession) PublishMsg(kind string, payload []byte, _ time.Duration) error {
	if kind != "ack" {
		return errors.New("unexpected " + kind)
	}
	var a hubmsg.Ack
	_ = json.Unmarshal(payload, &a)
	if f.onAck != nil {
		f.onAck(a)
	}
	f.mu.Lock()
	f.acks = append(f.acks, a)
	f.mu.Unlock()
	return nil
}
func (f *fakeSession) IsConnected() bool { return true }
func (f *fakeSession) Principal() string { return "bridge:kit-a" }

func (f *fakeSession) take() []hubmsg.Ack {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.acks
	f.acks = nil
	return a
}

func newDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.New(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func delivery(t *testing.T, now time.Time, op, mid, body string) []byte {
	t.Helper()
	s, err := hubmsg.NewSubmission(op, mid, "bridge:kit-a", body, now.Add(time.Hour).UnixMilli(), now.Add(2*time.Minute).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(hubmsg.Delivery{V: 1, OpID: s.OpID, Mid: s.Mid, From: "bridge:kit-b", To: s.To, ContentType: s.ContentType,
		MidExpiresAt: s.MidExpiresAt, ExpiresAt: s.ExpiresAt, ContentSHA256: s.ContentSHA256, Body: s.Body})
	return b
}

func handoffs(r *Route) *[]database.HubHandoff {
	var got []database.HubHandoff
	r.SetHandoff(func(h database.HubHandoff) error { got = append(got, h); return nil })
	return &got
}

// Committed, then answered, then handed on; a repeat is answered again and handed on never.
func TestADeliveryIsCommittedThenAnsweredThenHandedOn(t *testing.T) {
	db, sess := newDB(t), &fakeSession{}
	r := New(db, sess, Config{})
	got := handoffs(r)
	committed := false
	sess.onAck = func(a hubmsg.Ack) {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM hub_inbox WHERE op_id = ?`, a.OpID).Scan(&n)
		committed = n == 1
	}
	now := time.Now()
	d := delivery(t, now, hubmsg.NewV7(now), hubmsg.NewV7(now), "Water at the north gate")
	r.HandleIn(d)
	acks := sess.take()
	if len(acks) != 1 || acks[0].Result != "stored" || acks[0].From != "bridge:kit-b" || !committed {
		t.Fatalf("answers %+v, committed before the answer: %v", acks, committed)
	}
	if len(*got) != 0 {
		t.Fatal("handed on before the answer")
	}
	r.HandOff()
	if len(*got) != 1 || (*got)[0].From != "bridge:kit-b" || (*got)[0].Text != "Water at the north gate" {
		t.Fatalf("hand-offs %+v", *got)
	}
	r.HandleIn(d)
	r.HandOff()
	if acks := sess.take(); len(acks) != 1 || acks[0].Result != "stored" {
		t.Fatalf("a repeat: %+v", acks)
	}
	if len(*got) != 1 {
		t.Fatalf("a repeat was handed on again: %d", len(*got))
	}
}

// A process that died between the commit and the hand-off hands the message on at start.
func TestAHandOffSurvivesARestart(t *testing.T) {
	db, sess := newDB(t), &fakeSession{}
	now := time.Now()
	New(db, sess, Config{}).HandleIn(delivery(t, now, hubmsg.NewV7(now), hubmsg.NewV7(now), "before the crash"))
	again := New(db, sess, Config{})
	got := handoffs(again)
	again.HandOff()
	if len(*got) != 1 || (*got)[0].Text != "before the crash" {
		t.Fatalf("hand-offs after a restart %+v", *got)
	}
}

// A refusal is answered with its reason and stores nothing; a delivery for another bridge is
// neither stored nor answered.
func TestARefusalIsAnsweredAndADeliveryForSomeoneElseIsNot(t *testing.T) {
	db, sess := newDB(t), &fakeSession{}
	r := New(db, sess, Config{})
	got := handoffs(r)
	now := time.Now()
	var d hubmsg.Delivery
	_ = json.Unmarshal(delivery(t, now, hubmsg.NewV7(now), hubmsg.NewV7(now), "x"), &d)
	d.Body = "y"
	b, _ := json.Marshal(d)
	r.HandleIn(b)
	if acks := sess.take(); len(acks) != 1 || acks[0].Result != "refused" || acks[0].Reason != "digest_mismatch" {
		t.Fatalf("answers %+v", acks)
	}
	d.Body, d.To = "x", "bridge:kit-z"
	b, _ = json.Marshal(d)
	r.HandleIn(b)
	r.HandOff()
	if acks := sess.take(); len(acks) != 0 || len(*got) != 0 {
		t.Fatalf("answers %+v, hand-offs %+v", acks, *got)
	}
}

// With the clock not established the deadline is not judged: a delivery past it is stored.
func TestAnUnestablishedClockStoresAndMarksIt(t *testing.T) {
	db, sess := newDB(t), &fakeSession{}
	late := time.Now().Add(time.Hour)
	r := New(db, sess, Config{ClockEstablished: func() bool { return false }, Now: func() time.Time { return late }})
	now := time.Now()
	r.HandleIn(delivery(t, now, hubmsg.NewV7(now), hubmsg.NewV7(now), "x"))
	if acks := sess.take(); len(acks) != 1 || acks[0].Result != "stored" {
		t.Fatalf("answers %+v", acks)
	}
	var unjudged int
	_ = db.QueryRow(`SELECT unjudged FROM hub_inbox`).Scan(&unjudged)
	if unjudged != 1 {
		t.Fatal("not marked unjudged")
	}
	// The same, with the clock established, is refused.
	r2 := New(db, sess, Config{Now: func() time.Time { return late }})
	r2.HandleIn(delivery(t, now, hubmsg.NewV7(now), hubmsg.NewV7(now), "y"))
	if acks := sess.take(); len(acks) != 1 || acks[0].Reason != "expired_at_recipient" {
		t.Fatalf("answers %+v", acks)
	}
}

// IntentAdmit refuses what the Hub would not take, before anything is planned.
func TestTheRouteAdmitsOnlyWhatTheHubTakes(t *testing.T) {
	r := New(newDB(t), &fakeSession{}, Config{})
	for _, tc := range []struct {
		dest, reason string
		wire         []byte
		enc          bool
	}{
		{"bridge:kit-b", "", []byte("ok"), false},
		{"kit-b", "skipped_bad_address", []byte("ok"), false},
		{"bridge:kit-a", "skipped_self_address", []byte("ok"), false},
		{"bridge:kit-b", "skipped_encoding", []byte{0xff, 0xfe}, false},
		{"bridge:kit-b", "skipped_encoding", []byte("ok"), true},
		{"bridge:kit-b", "skipped_oversize", make([]byte, hubmsg.MaxBody+1), false},
	} {
		if got, _ := r.IntentAdmit(tc.dest, tc.wire, tc.enc); got != tc.reason {
			t.Errorf("%s %d bytes enc=%v: %q, want %q", tc.dest, len(tc.wire), tc.enc, got, tc.reason)
		}
	}
}
