package database

import (
	"errors"
	"testing"
	"time"
)

// [MESHSAT-1814] The ledger's personal data leaves with the retention: a
// row that ended past the cutoff keeps time, channel and status and loses
// payload, text, destination and error text; live rows and fresh rows are
// left alone; a scrubbed row cannot be retried.

func seedDelivery(t *testing.T, db *DB, status, ack string, age time.Duration, intentID string) int64 {
	t.Helper()
	when := time.Now().UTC().Add(-age).Format("2006-01-02 15:04:05")
	var ackVal, intentVal interface{}
	if ack != "" {
		ackVal = ack
	}
	if intentID != "" {
		intentVal = intentID
	}
	res, err := db.Exec(`INSERT INTO message_deliveries
		(msg_ref, channel, status, payload, text_preview, destination, last_error, ack_status, intent_id, created_at, updated_at)
		VALUES ('post-K7QW2X', 'iridium_0', ?, ?, 'Maria: we are safe', '+31612345678', 'sms to +31612345678 failed', ?, ?, ?, ?)`,
		status, []byte("@+31612345678 Maria: we are safe"), ackVal, intentVal, when, when)
	if err != nil {
		t.Fatalf("seed %s: %v", status, err)
	}
	id, _ := res.LastInsertId()
	return id
}

type scrubRow struct {
	Status      string  `db:"status"`
	Channel     string  `db:"channel"`
	Payload     []byte  `db:"payload"`
	TextPreview string  `db:"text_preview"`
	Destination string  `db:"destination"`
	LastError   string  `db:"last_error"`
	ScrubbedAt  *string `db:"scrubbed_at"`
}

func readScrubRow(t *testing.T, db *DB, id int64) scrubRow {
	t.Helper()
	var r scrubRow
	if err := db.Get(&r, `SELECT status, channel, payload, text_preview, destination, last_error, scrubbed_at FROM message_deliveries WHERE id = ?`, id); err != nil {
		t.Fatalf("read %d: %v", id, err)
	}
	return r
}

func TestScrubEndedDeliveriesRemovesTheTextAndKeepsTheRow(t *testing.T) {
	db := testDB(t)
	old := 31 * 24 * time.Hour
	ended := map[string]int64{}
	for _, st := range []string{"sent", "delivered", "dead", "expired", "denied", "failed"} {
		ended[st] = seedDelivery(t, db, st, "", old, "")
	}
	ackedSent := seedDelivery(t, db, "sent", "acked", old, "")
	liveQueued := seedDelivery(t, db, "queued", "", old, "")
	liveRetry := seedDelivery(t, db, "retry", "", old, "")
	liveSending := seedDelivery(t, db, "sending", "", old, "")
	pendingAck := seedDelivery(t, db, "sent", "pending", old, "")
	fresh := seedDelivery(t, db, "sent", "", 29*24*time.Hour, "")

	// An attempt of an intent that is still open is never touched, a closed
	// intent's attempt is.
	for _, in := range []struct{ id, state string }{{"intent-open", "active"}, {"intent-closed", "closed"}} {
		if _, err := db.Exec(`INSERT INTO delivery_intents (id, submission_id, payload, payload_sha256, effective_strategy, route_plan, state)
			VALUES (?, 'sub', x'00', 'h', 'BEST', '[]', ?)`, in.id, in.state); err != nil {
			t.Fatalf("seed intent: %v", err)
		}
	}
	openAttempt := seedDelivery(t, db, "accepted", "", old, "intent-open")
	closedAttempt := seedDelivery(t, db, "accepted", "", old, "intent-closed")

	n, err := db.ScrubEndedDeliveries(30)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(len(ended) + 2); n != want {
		t.Fatalf("scrubbed %d rows, want %d", n, want)
	}
	for st, id := range ended {
		r := readScrubRow(t, db, id)
		if r.Payload != nil || r.TextPreview != "" || r.Destination != "" || r.LastError != "" || r.ScrubbedAt == nil {
			t.Fatalf("%s row %d not scrubbed: %+v", st, id, r)
		}
		if r.Status != st || r.Channel != "iridium_0" {
			t.Fatalf("%s row %d lost its status or channel: %+v", st, id, r)
		}
	}
	for _, id := range []int64{ackedSent, closedAttempt} {
		if r := readScrubRow(t, db, id); r.ScrubbedAt == nil || r.Payload != nil {
			t.Fatalf("row %d should be scrubbed: %+v", id, r)
		}
	}
	for name, id := range map[string]int64{"queued": liveQueued, "retry": liveRetry, "sending": liveSending, "sent, ack pending": pendingAck, "fresh": fresh, "open intent's attempt": openAttempt} {
		r := readScrubRow(t, db, id)
		if r.ScrubbedAt != nil || string(r.Payload) == "" || r.TextPreview == "" || r.Destination == "" {
			t.Fatalf("%s row %d was touched: %+v", name, id, r)
		}
	}

	// A second run finds nothing new.
	if n, err := db.ScrubEndedDeliveries(30); err != nil || n != 0 {
		t.Fatalf("second run scrubbed %d (%v)", n, err)
	}

	// A scrubbed dead row is not requeued; an unscrubbed one is.
	if err := db.RetryDelivery(ended["dead"]); !errors.Is(err, ErrDeliveryScrubbed) {
		t.Fatalf("retry of a scrubbed row: %v", err)
	}
	unscrubbedDead := seedDelivery(t, db, "dead", "", time.Hour, "")
	if err := db.RetryDelivery(unscrubbedDead); err != nil {
		t.Fatalf("retry of a fresh dead row: %v", err)
	}
}

// The prune's cutoff is in the format created_at is written in, so a row
// newer than the cutoff on the cutoff's own day is kept.
func TestPruneOlderThanKeepsTheCutoffDaysNewerRows(t *testing.T) {
	db := testDB(t)
	at := func(age time.Duration) string { return time.Now().UTC().Add(-age).Format("2006-01-02 15:04:05") }
	keep := at(30*24*time.Hour - time.Minute)
	drop := at(30*24*time.Hour + time.Hour)
	for _, ts := range []string{keep, drop} {
		if _, err := db.Exec(`INSERT INTO positions (node_id, latitude, longitude, created_at) VALUES ('!00000001', 52.1, 4.4, ?)`, ts); err != nil {
			t.Fatalf("seed position: %v", err)
		}
	}
	if _, err := db.PruneOlderThan(30); err != nil {
		t.Fatal(err)
	}
	var total, kept int
	if err := db.Get(&total, `SELECT COUNT(*) FROM positions`); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&kept, `SELECT COUNT(*) FROM positions WHERE created_at = ?`, keep); err != nil {
		t.Fatal(err)
	}
	if total != 1 || kept != 1 {
		t.Fatalf("%d rows left, %d of them the one to keep (%s)", total, kept, keep)
	}
}
