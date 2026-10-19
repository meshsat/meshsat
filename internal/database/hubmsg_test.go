package database

import (
	"testing"
	"time"

	"meshsat/internal/hubmsg"
	"meshsat/internal/hubmsg/hubmsgtest"
)

// The release's inbox cases (C3) against the database's inbox, one transaction per commit.
func TestFixtureInboxInTheDatabase(t *testing.T) {
	hubmsgtest.RunInboxCases(t, hubmsgtest.LoadInboxCases(t), func(t testing.TB) func(hubmsg.Delivery, time.Time, bool, int) (hubmsg.Answer, error) {
		db := testDB(t.(*testing.T))
		return db.CommitHubDelivery
	})
}

func hubDelivery(t *testing.T, now time.Time, op, mid, from, body string) hubmsg.Delivery {
	t.Helper()
	s, err := hubmsg.NewSubmission(op, mid, "bridge:kit-b", body, now.Add(time.Hour).UnixMilli(), now.Add(10*time.Minute).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return hubmsg.Delivery{V: 1, OpID: s.OpID, Mid: s.Mid, From: from, To: s.To, ContentType: s.ContentType,
		MidExpiresAt: s.MidExpiresAt, ExpiresAt: s.ExpiresAt, ContentSHA256: s.ContentSHA256, Body: s.Body}
}

// A stored message is a messages row and one pending hand-off, in the commit's transaction;
// a second operation of the same message, and a repeat, store nothing more.
func TestAHubDeliveryStoresTheMessageAndItsHandOffOnce(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	mid := hubmsg.NewV7(now)
	d1 := hubDelivery(t, now, hubmsg.NewV7(now), mid, "bridge:kit-a", "Water at the north gate")
	d2 := hubDelivery(t, now, hubmsg.NewV7(now), mid, "bridge:kit-a", "Water at the north gate")
	for _, d := range []hubmsg.Delivery{d1, d1, d2, d2} {
		if a, err := db.CommitHubDelivery(d, now, true, 0); err != nil || a.Result != "stored" {
			t.Fatalf("%+v %v", a, err)
		}
	}
	var msgs, inbox int
	_ = db.QueryRow(`SELECT COUNT(*) FROM messages WHERE transport = ?`, HubTransport).Scan(&msgs)
	_ = db.QueryRow(`SELECT COUNT(*) FROM hub_inbox`).Scan(&inbox)
	if msgs != 1 || inbox != 2 {
		t.Fatalf("messages %d, inbox rows %d", msgs, inbox)
	}
	hs, err := db.PendingHubHandoffs(0)
	if err != nil || len(hs) != 1 || hs[0].From != "bridge:kit-a" || hs[0].Text != "Water at the north gate" {
		t.Fatalf("hand-offs %+v %v", hs, err)
	}
	if err := db.MarkHubHandoffDone(hs[0].MessageID, now); err != nil {
		t.Fatal(err)
	}
	if hs, _ := db.PendingHubHandoffs(0); len(hs) != 0 {
		t.Fatalf("a done hand-off is pending: %+v", hs)
	}
	// The same mid from another origin is another message.
	if a, _ := db.CommitHubDelivery(hubDelivery(t, now, hubmsg.NewV7(now), mid, "bridge:kit-c", "Water at the north gate"), now, true, 0); !a.StoredNow {
		t.Fatalf("another origin: %+v", a)
	}
	// Past its retention an entry goes; the message row stays (it is the bridge's history).
	if n, err := db.PruneHubInbox(time.UnixMilli(hubmsg.RetainedUntil(d1.ExpiresAt) + 1)); err != nil || n != 3 {
		t.Fatalf("pruned %d %v", n, err)
	}
}

// A refused delivery writes nothing at all.
func TestARefusedHubDeliveryWritesNothing(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	d := hubDelivery(t, now, hubmsg.NewV7(now), hubmsg.NewV7(now), "bridge:kit-a", "x")
	d.Body = "y"
	if a, _ := db.CommitHubDelivery(d, now, true, 0); a.Result != "refused" || a.Reason != "digest_mismatch" {
		t.Fatalf("%+v", a)
	}
	var n int
	_ = db.QueryRow(`SELECT (SELECT COUNT(*) FROM messages) + (SELECT COUNT(*) FROM hub_inbox) + (SELECT COUNT(*) FROM hub_inbox_handoff)`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows written", n)
	}
}

// The sender keeps each event once by its id, valid or rejected, and forgets an operation
// with its events after the recipient's retention.
func TestHubOpEventsAreKeptOncePerID(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	op := HubOp{OpID: hubmsg.NewV7(now), Mid: hubmsg.NewV7(now), Recipient: "bridge:kit-b", IntentID: "i-1",
		ExpiresAt: now.Add(2 * time.Minute).UnixMilli(), CreatedAt: now.UnixMilli()}
	if err := db.PutHubOp(op); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetHubOp(op.OpID); err != nil || got == nil || *got != op {
		t.Fatalf("%+v %v", got, err)
	}
	if got, err := db.GetHubOp(hubmsg.NewV7(now)); err != nil || got != nil {
		t.Fatalf("an unknown op: %+v %v", got, err)
	}
	ev := hubmsg.Event{V: 1, OpID: op.OpID, Mid: op.Mid, Recipient: op.Recipient, EventID: hubmsg.NewV7(now), Rev: 1,
		Class: "forwarded", Kind: "hub_forwarded", Authority: "hub", At: now.UTC().Format(time.RFC3339)}
	for i, want := range []bool{true, false} {
		if k, err := db.AddHubOpEvent(ev, true, now); err != nil || k.Added != want || !k.Valid || k.Applied || k.Event.EventID != ev.EventID {
			t.Fatalf("copy %d: %+v %v", i+1, k, err)
		}
	}
	evs, valid, err := db.HubOpEvents(op.OpID)
	if err != nil || len(evs) != 1 || !valid[0] || evs[0].EventID != ev.EventID {
		t.Fatalf("%+v %v %v", evs, valid, err)
	}
	if n, err := db.PruneHubOps(time.UnixMilli(hubmsg.RetainedUntil(op.ExpiresAt) - 1)); err != nil || n != 0 {
		t.Fatalf("pruned early: %d %v", n, err)
	}
	if n, err := db.PruneHubOps(time.UnixMilli(hubmsg.RetainedUntil(op.ExpiresAt) + 1)); err != nil || n != 1 {
		t.Fatalf("pruned %d %v", n, err)
	}
	if evs, _, _ := db.HubOpEvents(op.OpID); len(evs) != 0 {
		t.Fatalf("events outlived their op: %d", len(evs))
	}
}
