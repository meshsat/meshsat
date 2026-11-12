package database

import (
	"testing"
	"time"
)

// The public demo forgets everything an hour old: a post with its replies
// and its delivery row, a board line, a phone's counters. Younger rows stay,
// and a stamp with a fraction compares as a time, not as a string.
// [MESHSAT-1845]
func TestPurgePostOlderThan(t *testing.T) {
	db := testDB(t)
	now := time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)
	cut := now.Add(-time.Hour)
	post := func(code string, at time.Time) {
		t.Helper()
		id, err := db.InsertDelivery(MessageDelivery{MsgRef: "demo-" + code, Channel: "iridium_demo", Status: "sent",
			Priority: 1, TextPreview: "(demo)", Visited: "[]", QoSLevel: 1})
		if err != nil {
			t.Fatal(err)
		}
		m := &PostMessage{Code: code, Session: "s", Recipient: "+447700900001", Text: "safe", Class: PostClassRoutine,
			Status: PostStatusQueued, Lang: "en", DeliveryID: id, CreatedAt: at}
		if err := db.InsertPostMessage(m); err != nil {
			t.Fatal(err)
		}
		if err := db.InsertPostReply(&PostReply{Code: code, From: "+447700900001", Text: "ok", ReceivedAt: at.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	post("OLD222", cut.Add(-time.Minute))
	post("NEW222", cut.Add(time.Minute))
	// Just after the cut with a fraction: as strings "...17:00:00.5Z" sorts
	// before "...17:00:00Z", as times it is younger and must stay.
	post("FRA222", cut.Add(500*time.Millisecond))
	for _, l := range []struct {
		name string
		at   time.Time
	}{{"Old Line", cut.Add(-2 * time.Minute)}, {"New Line", cut.Add(2 * time.Minute)}} {
		e := &PostBoardEntry{Name: l.name, Status: "safe", CreatedAt: l.at, ExpiresAt: now.Add(time.Hour)}
		if err := db.InsertPostBoardEntry(e, l.name); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := db.TakePostSlot("ip:old", cut.Add(-3*time.Minute), 3, 10); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := db.TakePostSlot("ip:new", cut.Add(3*time.Minute), 3, 10); err != nil || !ok {
		t.Fatal(ok, err)
	}
	before, _ := db.CountPostRows()
	if before != 8 {
		t.Fatalf("rows before: %d, want 3 posts + 3 replies + 2 lines", before)
	}

	c, err := db.PurgePostOlderThan(cut)
	if err != nil {
		t.Fatal(err)
	}
	if c.Messages != 1 || c.Replies != 1 || c.Board != 1 || c.Sessions != 1 {
		t.Fatalf("purged %+v, want one of each", c)
	}
	if m, _ := db.GetPostMessage("OLD222"); m != nil {
		t.Error("the old post is still there")
	}
	for _, code := range []string{"NEW222", "FRA222"} {
		if m, _ := db.GetPostMessage(code); m == nil {
			t.Errorf("%s was purged", code)
		}
	}
	var deliveries int
	if err := db.QueryRow(`SELECT COUNT(*) FROM message_deliveries WHERE msg_ref LIKE 'demo-%'`).Scan(&deliveries); err != nil || deliveries != 2 {
		t.Errorf("delivery rows left: %d (%v), want 2", deliveries, err)
	}
	if n, _ := db.CountPostRows(); n != 5 {
		t.Errorf("rows after: %d, want 2 posts + 2 replies + 1 line", n)
	}
	if s, _ := db.GetPostSession("ip:old"); s != nil {
		t.Error("the old phone's counters are still there")
	}
	if s, _ := db.GetPostSession("ip:new"); s == nil {
		t.Error("the young phone's counters were purged")
	}
}
