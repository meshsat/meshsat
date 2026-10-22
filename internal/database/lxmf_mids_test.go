package database

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func lxmfRow(text string) *Message {
	return &Message{FromNode: "lxmf:aa", ToNode: "lxmf:bb", PortNum: 1, PortNumName: "LXMF", DecodedText: text, Direction: "rx", Transport: "reticulum"}
}

func countRows(t *testing.T, db *DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.Get(&n, q, args...); err != nil {
		t.Fatal(err)
	}
	return n
}

// The message and its (origin, mid) are written together, once per origin;
// a failure writes neither (r40 review R40-R2). [MESHSAT-1641]
func TestInsertLXMFMessageOnce(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	for i, c := range []struct {
		origin, mid string
		want        bool
	}{
		{"aa", "m1", true}, {"aa", "m1", false}, {"bb", "m1", true}, {"aa", "m2", true},
	} {
		got, id, err := db.InsertLXMFMessageOnce(c.origin, c.mid, now, lxmfRow("hi"), "received")
		if err != nil || got != c.want || (got && id == 0) {
			t.Fatalf("%d: %v %d %v, want %v", i, got, id, err, c.want)
		}
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM messages WHERE portnum_name = 'LXMF'`); n != 3 {
		t.Fatalf("%d messages, want 3", n)
	}

	// The message insert fails: no claim is left behind.
	if _, err := db.Exec(`CREATE TRIGGER r40_fail BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT, 'test: inbox write fails'); END;`); err != nil {
		t.Fatal(err)
	}
	if got, _, err := db.InsertLXMFMessageOnce("cc", "m9", now, lxmfRow("x"), "received"); err == nil || got {
		t.Fatalf("a failed write answered %v %v", got, err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM lxmf_mids WHERE origin = 'cc'`); n != 0 {
		t.Fatalf("%d claims left by a failed write", n)
	}
	if _, err := db.Exec(`DROP TRIGGER r40_fail`); err != nil {
		t.Fatal(err)
	}
	if got, _, err := db.InsertLXMFMessageOnce("cc", "m9", now, lxmfRow("x"), "received"); err != nil || !got {
		t.Fatalf("the retry: %v %v", got, err)
	}

	// The horizon.
	if _, _, err := db.InsertLXMFMessageOnce("dd", "old", now.Add(-LXMFMidRetention-time.Minute), lxmfRow("o"), "received"); err != nil {
		t.Fatal(err)
	}
	if n, err := db.PruneLXMFMids(now); err != nil || n != 1 {
		t.Fatalf("pruned %d %v, want 1", n, err)
	}
	if got, _, _ := db.InsertLXMFMessageOnce("aa", "m1", now, lxmfRow("hi"), "received"); got {
		t.Fatal("a live id was forgotten")
	}
}

// Many deliveries of one (origin, mid) at once file one message.
func TestInsertLXMFMessageOnceConcurrent(t *testing.T) {
	db := testDB(t)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = db.InsertLXMFMessageOnce("aa", "same", time.Now(), lxmfRow("once"), "received")
		}()
	}
	wg.Wait()
	if n := countRows(t, db, `SELECT COUNT(*) FROM messages WHERE decoded_text = 'once'`); n != 1 {
		t.Fatalf("%d messages, want 1", n)
	}
}

func lxmfDelivery(t *testing.T, db *DB, intent string) int64 {
	t.Helper()
	id, err := db.InsertDelivery(MessageDelivery{Channel: "lxmf_0", Status: "queued", Class: DeliveryClassLXMF, Destination: "00112233445566778899aabbccddeeff", TextPreview: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if intent != "" {
		if _, err := db.Exec(`UPDATE message_deliveries SET intent_id = ? WHERE id = ?`, intent, id); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// A row's mid is a UUIDv7 given once: every later read, concurrent or after
// a reopen, answers the same (r40 review, decision 2).
func TestEnsureDeliveryMid(t *testing.T) {
	db := testDB(t)
	id := lxmfDelivery(t, db, "")
	var wg sync.WaitGroup
	got := make([]string, 12)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m, err := db.EnsureDeliveryMid(id)
			if err != nil {
				t.Error(err)
			}
			got[i] = m
		}(i)
	}
	wg.Wait()
	for _, m := range got {
		if m != got[0] {
			t.Fatalf("racing callers got %q and %q", got[0], m)
		}
	}
	u, err := uuid.Parse(got[0])
	if err != nil || u.Version() != 7 || u.Variant() != uuid.RFC4122 {
		t.Fatalf("%s is not a UUIDv7: %v", got[0], err)
	}
	if again, _ := db.EnsureDeliveryMid(id); again != got[0] {
		t.Fatalf("a later read gave %s", again)
	}
	if other, _ := db.EnsureDeliveryMid(lxmfDelivery(t, db, "")); other == got[0] {
		t.Fatal("two rows share a mid")
	}
	if _, err := db.EnsureDeliveryMid(99999); err == nil {
		t.Fatal("a missing row got a mid")
	}
}

// v81's backfill gives every LXMF row of no intent a mid once, and leaves an
// intent's rows and rows that have one alone.
func TestBackfillDeliveryMids(t *testing.T) {
	db := testDB(t)
	a := lxmfDelivery(t, db, "")
	b := lxmfDelivery(t, db, "01a14f31-6680-7a0b-8c0d-0e0f10111213")
	if err := db.backfillDeliveryMids(); err != nil {
		t.Fatal(err)
	}
	var ma, mb string
	_ = db.Get(&ma, `SELECT mid FROM message_deliveries WHERE id = ?`, a)
	_ = db.Get(&mb, `SELECT mid FROM message_deliveries WHERE id = ?`, b)
	if u, err := uuid.Parse(ma); err != nil || u.Version() != 7 {
		t.Fatalf("row of no intent: %q %v", ma, err)
	}
	if mb != "" {
		t.Fatalf("an intent's row got %q", mb)
	}
	if err := db.backfillDeliveryMids(); err != nil {
		t.Fatal(err)
	}
	var again string
	_ = db.Get(&again, `SELECT mid FROM message_deliveries WHERE id = ?`, a)
	if again != ma {
		t.Fatal("a second backfill changed a mid")
	}
	if m, _ := db.EnsureDeliveryMid(a); m != ma {
		t.Fatal("the send would use another mid than the backfill gave")
	}
}
