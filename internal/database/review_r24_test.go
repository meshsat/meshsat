package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"modernc.org/sqlite"
)

type reviewR24Hook struct {
	query func(string)
	exec  func(string)
}

type reviewR24Driver struct {
	base driver.Driver
	hook *reviewR24Hook
}

func (d *reviewR24Driver) Open(name string) (driver.Conn, error) {
	c, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &reviewR24Conn{Conn: c, hook: d.hook}, nil
}

type reviewR24Conn struct {
	driver.Conn
	hook *reviewR24Hook
}

func (c *reviewR24Conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.hook.query != nil {
		c.hook.query(query)
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c *reviewR24Conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.hook.exec != nil {
		c.hook.exec(query)
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

var reviewR24DriverID uint64

func reviewR24DBs(t *testing.T) (*DB, *DB, *reviewR24Hook) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "review.db")
	writer, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	h := &reviewR24Hook{}
	name := fmt.Sprintf("review-r24-%d", atomic.AddUint64(&reviewR24DriverID, 1))
	sql.Register(name, &reviewR24Driver{base: &sqlite.Driver{}, hook: h})
	conn, err := sqlx.Open(name, "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = conn.Close() })
	return &DB{DB: conn}, writer, h
}

func TestReviewR24PruneBetweenStatsReads(t *testing.T) {
	reader, writer, h := reviewR24DBs(t)
	a := statsIntent(t, writer, "moving", "MESHTASTIC", "mesh_0", 30, "hi")
	finalize(t, writer, handOff(t, writer, a, "moving-corr"), ClassAccepted, "moving-corr")
	receipt(t, writer, "moving-corr", OutcomeDeliveredDevice)
	closeIntent(t, writer, "moving", OutcomeDeliveredDevice)
	age(t, writer, "moving", 40*24*time.Hour)
	from, to := time.Now().Add(-90*24*time.Hour), time.Now().Add(time.Hour)
	before, err := reader.DeliveryOutcomeStatsBetween(from, to)
	if err != nil {
		t.Fatal(err)
	}
	hit := false
	h.query = func(q string) {
		if !strings.Contains(q, "FROM delivery_attempt_facts WHERE created_at") {
			return
		}
		h.query = nil
		hit = true
		n, err := writer.PruneClosedIntents(30)
		if err != nil || n != 1 {
			t.Fatalf("prune: %d %v", n, err)
		}
	}
	during, err := reader.DeliveryOutcomeStatsBetween(from, to)
	if err != nil {
		t.Fatal(err)
	}
	after, err := reader.DeliveryOutcomeStatsBetween(from, to)
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("interleaving hook did not fire")
	}
	t.Logf("before=%d during=%d after=%d; during completed=%d intents=%d archived=%d complete=%v", before.Total.Attempts, during.Total.Attempts, after.Total.Attempts, during.Total.CompletedTotal, during.Intents.Intents, during.Coverage.ArchivedAttempts, during.Coverage.Complete)
	if during.Total.Attempts != 1 || during.Total.CompletedTotal != 1 {
		t.Fatal("one attempt counted twice while pruning")
	}
}

// Adapted for r25: the review modelled the pre-v78 prune on a ledger made
// at the head version, which v79 knows to be fresh (its tables were made
// after v78, and its only deleter keeps facts first), so the review's own
// acceptance asks for a genuine upgrade fixture. Here the ledger is at v77
// when the intent is made, closed and deleted the old way, and is then
// upgraded as a start of this bridge does. The steps and the assertion are
// the review's; before is counted in SQL because v77 has no statistics.
func TestReviewR24LegacyZeroAttemptCoverage(t *testing.T) {
	db := ledgerAt(t, 77)
	newTestIntent(t, db, "zero-attempt", 0, nil)
	_, err := db.ApplyIntentChange("zero-attempt", func(s *IntentSnapshot) *IntentChange {
		return &IntentChange{Transition: "T2", ExpectVersion: s.Intent.Version, SetOutcome: OutcomeFailed, Close: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	age(t, db, "zero-attempt", 40*24*time.Hour)
	from, to := time.Now().Add(-90*24*time.Hour), time.Now().Add(time.Hour)
	var before int
	if err := db.Get(&before, `SELECT COUNT(*) FROM delivery_intents WHERE created_at >= ? AND created_at < ?`,
		statsSecond(from), statsSecond(to)); err != nil {
		t.Fatal(err)
	}
	// This fixture models the pre-v78 prune, which kept no intent facts.
	for _, q := range []string{
		`DELETE FROM delivery_evidence WHERE intent_id='zero-attempt'`,
		`DELETE FROM delivery_transmissions WHERE intent_id='zero-attempt'`,
		`DELETE FROM delivery_intents WHERE id='zero-attempt'`,
		`DELETE FROM delivery_submissions WHERE id='sub-zero-attempt'`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.migrate(); err != nil {
		t.Fatal(err)
	}
	after, err := db.DeliveryOutcomeStatsBetween(from, to)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("before intents=%d; after intents=%d complete=%v intents_complete=%v intents_known_from=%s unavailable_attempts=%d unavailable_intents_by_first_attempt=%d", before, after.Intents.Intents, after.Coverage.Complete, after.Coverage.IntentsComplete, after.Coverage.IntentsKnownFrom, after.Coverage.UnavailableAttempts, after.Coverage.UnavailableIntentsByFirstAttempt)
	if after.Coverage.Complete {
		t.Fatal("pruned zero-attempt intent is indistinguishable from known-empty history")
	}
}

func TestReviewR24PruneCutoffDoesNotMove(t *testing.T) {
	pruner, writer, h := reviewR24DBs(t)
	a := statsIntent(t, writer, "boundary", "MESHTASTIC", "mesh_0", 30, "hi")
	finalize(t, writer, handOff(t, writer, a, "boundary-corr"), ClassAccepted, "boundary-corr")
	receipt(t, writer, "boundary-corr", OutcomeDeliveredDevice)
	closeIntent(t, writer, "boundary", OutcomeDeliveredDevice)
	var now string
	if err := writer.Get(&now, `SELECT datetime('now')`); err != nil {
		t.Fatal(err)
	}
	nowTime, err := time.Parse("2006-01-02 15:04:05", now)
	if err != nil {
		t.Fatal(err)
	}
	boundary := nowTime.Add(3 * time.Second)
	stamp := boundary.Add(-30 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	if _, err := writer.Exec(`UPDATE delivery_intents SET created_at=?,closed_at=? WHERE id='boundary'`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(`UPDATE message_deliveries SET created_at=? WHERE id=?`, stamp, a); err != nil {
		t.Fatal(err)
	}
	hit := false
	h.exec = func(q string) {
		if !strings.Contains(q, "DELETE FROM delivery_evidence WHERE intent_id IN") {
			return
		}
		h.exec = nil
		hit = true
		// Delay a SQL step until a fixed eligibility boundary has passed.
		if d := time.Until(boundary.Add(1200 * time.Millisecond)); d > 0 {
			time.Sleep(d)
		}
	}
	n, err := pruner.PruneClosedIntents(30)
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("cutoff hook did not fire")
	}
	var facts, live int
	if err := writer.Get(&facts, `SELECT count(*) FROM delivery_attempt_facts WHERE attempt_id=?`, a); err != nil {
		t.Fatal(err)
	}
	if err := writer.Get(&live, `SELECT count(*) FROM delivery_intents WHERE id='boundary'`); err != nil {
		t.Fatal(err)
	}
	st, err := writer.DeliveryOutcomeStatsBetween(time.Now().Add(-90*24*time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("pruned=%d live=%d attempt_facts=%d counted_attempts=%d unavailable_attempts=%d", n, live, facts, st.Total.Attempts, st.Coverage.UnavailableAttempts)
	if live == 0 && facts == 0 {
		t.Fatal("moving cutoff deleted an intent that was not archived")
	}
}

func TestReviewR24ArchivedAttemptKeepsItsWindow(t *testing.T) {
	db := intentTestDB(t)
	statsIntent(t, db, "archive-window", "MESHTASTIC", "mesh_0", 30, "hi")
	closeIntent(t, db, "archive-window", OutcomeFailed)
	stamp := time.Now().UTC().Add(-40 * 24 * time.Hour).Truncate(time.Second)
	stored := stamp.Format("2006-01-02 15:04:05")
	if _, err := db.Exec(`UPDATE message_deliveries SET created_at=? WHERE intent_id='archive-window'`, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE delivery_intents SET created_at=?,closed_at=? WHERE id='archive-window'`, stored, stored); err != nil {
		t.Fatal(err)
	}
	before, err := db.DeliveryOutcomeStatsBetween(stamp, stamp.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.PruneClosedIntents(30); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.Get(&raw, `SELECT CAST(created_at AS TEXT) FROM delivery_attempt_facts WHERE intent_id='archive-window'`); err != nil {
		t.Fatal(err)
	}
	after, err := db.DeliveryOutcomeStatsBetween(stamp, stamp.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("source_timestamp=%q archived_timestamp=%q before=%d after=%d after_intents=%d complete=%v", stored, raw, before.Total.Attempts, after.Total.Attempts, after.Intents.Intents, after.Coverage.Complete)
	if after.Total.Attempts != before.Total.Attempts {
		t.Fatal("archiving changed a timestamp's window membership")
	}
}
