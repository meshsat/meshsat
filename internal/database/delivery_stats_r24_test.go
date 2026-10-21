package database

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

// r24 review of S3-3: the statistics' history survives the retention prune
// exactly, from one snapshot, with one cutoff, and says what it cannot
// know. [MESHSAT-1584]

const ledgerStamp = "2006-01-02 15:04:05"

// ledgerAt is a ledger migrated to version v, as an older bridge left it;
// db.migrate() then upgrades it as a start of this one does.
func ledgerAt(t *testing.T, v int) *DB {
	t.Helper()
	db := openStaged(t, v)
	if _, err := db.Exec(`UPDATE schema_version SET version = ?`, v); err != nil {
		t.Fatal(err)
	}
	return db
}

// pruneBeforeV78 is the retention prune as it was before v78: it kept no
// facts.
func pruneBeforeV78(t *testing.T, db *DB, days int) {
	t.Helper()
	cutoff := "-" + strconv.Itoa(days) + " days"
	for _, q := range []string{
		`DELETE FROM delivery_evidence WHERE intent_id IN
			(SELECT id FROM delivery_intents WHERE state = 'closed' AND closed_at < datetime('now', ?))`,
		`DELETE FROM delivery_transmissions WHERE intent_id IN
			(SELECT id FROM delivery_intents WHERE state = 'closed' AND closed_at < datetime('now', ?))`,
		`DELETE FROM delivery_intents WHERE state = 'closed' AND closed_at < datetime('now', ?)`,
		`DELETE FROM delivery_evidence WHERE intent_id IS NULL AND received_at < datetime('now', ?)`,
		`DELETE FROM delivery_submissions WHERE NOT EXISTS
			(SELECT 1 FROM delivery_intents i WHERE i.submission_id = delivery_submissions.id)
			AND created_at < datetime('now', ?)`,
	} {
		if _, err := db.Exec(q, cutoff); err != nil {
			t.Fatal(err)
		}
	}
}

// setTimes puts an intent's creation, its attempts and its close at fixed
// instants (a fixture: nothing in the ledger writes old times).
func setTimes(t *testing.T, db *DB, id string, created, attempts, closed time.Time) {
	t.Helper()
	if _, err := db.Exec(`UPDATE message_deliveries SET created_at = ? WHERE intent_id = ?`,
		attempts.UTC().Format(ledgerStamp), id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE delivery_intents SET created_at = ?, closed_at = ? WHERE id = ?`,
		created.UTC().Format(ledgerStamp), closed.UTC().Format(ledgerStamp), id); err != nil {
		t.Fatal(err)
	}
}

// completedIntent writes an intent whose one attempt was accepted and
// completed by a receipt, then closed; returns the attempt.
func completedIntent(t *testing.T, db *DB, id string) int64 {
	t.Helper()
	a := statsIntent(t, db, id, "MESHTASTIC", "mesh_0", 30, "hi")
	finalize(t, db, handOff(t, db, a, id+"-corr"), ClassAccepted, id+"-corr")
	receipt(t, db, id+"-corr", OutcomeDeliveredDevice)
	closeIntent(t, db, id, OutcomeDeliveredDevice)
	return a
}

func stats(t *testing.T, db *DB, since, until time.Time) DeliveryOutcomeStats {
	t.Helper()
	st, err := db.DeliveryOutcomeStatsBetween(since, until)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// wholeJSON is the whole answer, coverage included.
func wholeJSON(st DeliveryOutcomeStats) string {
	b, _ := json.Marshal(st)
	return string(b)
}

// R24-R1: a kept attempt stays in every window it was in, to the second,
// within a day and across midnight; the prune copies the ledger's own
// created_at.
func TestDeliveryStatsArchivedTimestampsKeepTheirWindows(t *testing.T) {
	db := intentTestDB(t)
	day := time.Now().UTC().Add(-41 * 24 * time.Hour).Truncate(24 * time.Hour)
	noon, late, midnight := day.Add(12*time.Hour), day.Add(24*time.Hour-time.Second), day.Add(24*time.Hour)
	points := map[string]time.Time{"noon": noon, "late": late, "midnight": midnight}
	for id, at := range points {
		if id == "noon" {
			completedIntent(t, db, id)
		} else {
			statsIntent(t, db, id, "MESHTASTIC", "mesh_0", 30, "hi")
			closeIntent(t, db, id, OutcomeFailed)
		}
		setTimes(t, db, id, at, at, at)
	}
	half := 500 * time.Millisecond
	windows := []struct {
		name         string
		since, until time.Time
		want         int
	}{
		{"noon, exact second", noon, noon.Add(time.Second), 1},
		{"noon, fractional", noon.Add(-half), noon.Add(half), 1},
		{"noon, just after", noon.Add(half), noon.Add(time.Second + half), 0},
		{"within the day", noon, late, 1},
		{"late, exact second", late, late.Add(time.Second), 1},
		{"late, fractional", late.Add(-half), late.Add(half), 1},
		{"midnight, exact second", midnight, midnight.Add(time.Second), 1},
		{"across midnight, fractional", late.Add(half), midnight.Add(half), 1},
		{"across midnight, both", late, midnight.Add(time.Second), 2},
		{"the whole day", day, day.Add(24 * time.Hour), 2},
		{"the next day's first second only", midnight, midnight.Add(half), 1},
	}
	before := map[string]DeliveryOutcomeStats{}
	for _, w := range windows {
		st := stats(t, db, w.since, w.until)
		if st.Total.Attempts != w.want || st.Intents.Intents != w.want || !st.Coverage.Complete {
			t.Fatalf("%s before the prune: %d attempts, %d intents, coverage %+v; want %d", w.name,
				st.Total.Attempts, st.Intents.Intents, st.Coverage, w.want)
		}
		before[w.name] = st
	}
	if n, err := db.PruneClosedIntents(30); err != nil || n != 3 {
		t.Fatalf("prune: %d %v", n, err)
	}
	for _, w := range windows {
		after := stats(t, db, w.since, w.until)
		if statsJSON(t, after) != statsJSON(t, before[w.name]) || !after.Coverage.Complete ||
			after.Coverage.ArchivedAttempts != w.want || after.Coverage.ArchivedIntents != w.want {
			t.Errorf("%s after the prune:\n before %s\n after  %s %+v", w.name, statsJSON(t, before[w.name]),
				statsJSON(t, after), after.Coverage)
		}
	}
	// What is kept is what the ledger stores, character for character.
	var mismatched int
	if err := db.Get(&mismatched, `SELECT COUNT(*) FROM delivery_attempt_facts f JOIN message_deliveries d ON d.id = f.attempt_id
		WHERE CAST(f.created_at AS TEXT) != CAST(d.created_at AS TEXT)`); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.Get(&raw, `SELECT CAST(created_at AS TEXT) FROM delivery_attempt_facts WHERE intent_id = 'late'`); err != nil {
		t.Fatal(err)
	}
	if mismatched != 0 || raw != late.Format(ledgerStamp) {
		t.Errorf("kept created_at: %d differ from the ledger; late kept as %q", mismatched, raw)
	}
}

// R24-R1: v79 gives the rows v78 wrote in the driver's form back their
// ledger value, and the window query still uses the index.
func TestV79RepairsKeptTimestamps(t *testing.T) {
	db := ledgerAt(t, 78)
	at := time.Now().UTC().Add(-40 * 24 * time.Hour).Truncate(24 * time.Hour).Add(23*time.Hour + 50*time.Minute + 19*time.Second)
	a := statsIntent(t, db, "kept", "MESHTASTIC", "mesh_0", 30, "hi")
	closeIntent(t, db, "kept", OutcomeFailed)
	setTimes(t, db, "kept", at, at, at)
	// What v78's prune wrote: the facts with created_at as the driver handed
	// it back, then the deletes. A second row's attempt row is gone.
	gone := at.Add(-5 * 24 * time.Hour)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO delivery_attempt_facts (attempt_id, intent_id, created_at, bearer, interface_id, planned_bytes, status)
			VALUES (?, 'kept', ?, 'MESHTASTIC', 'mesh_0', 30, 'queued')`, []any{a, at.Format(time.RFC3339)}},
		{`INSERT INTO delivery_attempt_facts (attempt_id, intent_id, created_at, bearer, interface_id, planned_bytes, status)
			VALUES (999999, 'gone', ?, 'MESHTASTIC', 'mesh_0', 30, 'accepted')`, []any{gone.Format(time.RFC3339)}},
		{`INSERT INTO delivery_intent_facts (intent_id, created_at, state, outcome)
			SELECT id, created_at, state, outcome FROM delivery_intents WHERE id = 'kept'`, nil},
		{`DELETE FROM delivery_intents WHERE id = 'kept'`, nil},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	var raw string
	if err := db.Get(&raw, `SELECT CAST(created_at AS TEXT) FROM delivery_attempt_facts WHERE attempt_id = ?`, a); err != nil ||
		!strings.Contains(raw, "T") {
		t.Fatalf("setup: kept as %q (%v)", raw, err)
	}
	if err := db.migrate(); err != nil {
		t.Fatal(err)
	}
	var source, repaired, fallback string
	if err := db.Get(&source, `SELECT CAST(created_at AS TEXT) FROM message_deliveries WHERE id = ?`, a); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&repaired, `SELECT CAST(created_at AS TEXT) FROM delivery_attempt_facts WHERE attempt_id = ?`, a); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&fallback, `SELECT CAST(created_at AS TEXT) FROM delivery_attempt_facts WHERE attempt_id = 999999`); err != nil {
		t.Fatal(err)
	}
	if repaired != source || source != at.Format(ledgerStamp) || fallback != gone.Format(ledgerStamp) {
		t.Errorf("repair: source %q, repaired %q, without its attempt row %q", source, repaired, fallback)
	}
	var plan []struct {
		ID      int    `db:"id"`
		Parent  int    `db:"parent"`
		NotUsed int    `db:"notused"`
		Detail  string `db:"detail"`
	}
	if err := db.Select(&plan, `EXPLAIN QUERY PLAN SELECT attempt_id, intent_id, bearer, interface_id, planned_bytes, status,
		completes, handed_off, retries FROM delivery_attempt_facts WHERE created_at >= ? AND created_at < ?`,
		statsSecond(at), statsSecond(at.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 || !strings.Contains(plan[0].Detail, "USING INDEX idx_delivery_attempt_facts_created") {
		t.Errorf("kept attempts by window: %+v", plan)
	}
	st := stats(t, db, at, at.Add(time.Second))
	if st.Total.Attempts != 1 || st.Coverage.ArchivedAttempts != 1 || !st.Coverage.AttemptsComplete ||
		st.Intents.Intents != 1 || st.Coverage.ArchivedIntents != 1 || st.Coverage.LedgerHistory != "upgraded" {
		t.Errorf("the repaired attempt's second: %+v %+v", st.Total, st.Coverage)
	}
	var version int
	if err := db.Get(&version, `SELECT version FROM schema_version`); err != nil || version != len(migrations) {
		t.Errorf("schema version %d (%v), want %d", version, err, len(migrations))
	}
}

// R24-R2: a prune that commits while the statistics are being read is in
// all of the answer or none of it, wherever it lands, and the prune itself
// (a write) is not blocked by the read.
func TestDeliveryStatsReadOneSnapshot(t *testing.T) {
	boundaries := []string{
		"FROM message_deliveries d JOIN delivery_intents i", // the live attempts, the first read
		"FROM delivery_attempt_facts WHERE created_at",      // the kept attempts
		"SELECT COUNT(*) FROM message_deliveries d",         // the unavailable attempts
		"SELECT COUNT(*) FROM (SELECT MIN(d.created_at)",    // the unavailable intents
		"FROM delivery_intent_facts WHERE created_at",       // the intents
		"FROM delivery_history_coverage",                    // what is known of the intents
	}
	for i, marker := range boundaries {
		t.Run(marker, func(t *testing.T) {
			reader, writer, h := reviewR24DBs(t)
			completedIntent(t, writer, "moving")
			age(t, writer, "moving", 40*24*time.Hour)
			from, to := time.Now().Add(-90*24*time.Hour), time.Now().Add(time.Hour)
			before := stats(t, reader, from, to)
			hit := false
			h.query = func(q string) {
				if !strings.Contains(q, marker) {
					return
				}
				h.query = nil
				hit = true
				if n, err := writer.PruneClosedIntents(30); err != nil || n != 1 {
					t.Fatalf("prune during the read: %d %v", n, err)
				}
			}
			during := stats(t, reader, from, to)
			after := stats(t, reader, from, to)
			if !hit {
				t.Fatal("the prune did not run during the read")
			}
			saw := "mixed"
			switch wholeJSON(during) {
			case wholeJSON(before):
				saw = "before"
			case wholeJSON(after):
				saw = "after"
			}
			t.Logf("prune before read %d: the answer is the %s snapshot; attempts %d completed %d intents %d archived %d/%d complete %v",
				i+1, saw, during.Total.Attempts, during.Total.CompletedTotal, during.Intents.Intents,
				during.Coverage.ArchivedAttempts, during.Coverage.ArchivedIntents, during.Coverage.Complete)
			if saw == "mixed" || during.Total.Attempts != 1 || during.Total.CompletedTotal != 1 || during.Intents.Intents != 1 ||
				during.Coverage.ArchivedAttempts != during.Coverage.ArchivedIntents || !during.Coverage.Complete {
				t.Errorf("during %s\nbefore %s\nafter  %s", wholeJSON(during), wholeJSON(before), wholeJSON(after))
			}
			if after.Coverage.ArchivedAttempts != 1 || statsJSON(t, after) != statsJSON(t, before) {
				t.Errorf("after the prune: %s", wholeJSON(after))
			}
		})
	}
}

// R24-R3: an intent that comes of age while the prune runs is left for the
// next one, whichever statement the clock passes it at; what is deleted
// was kept first; and a failed keep deletes nothing.
func TestPruneClosedIntentsOneCohort(t *testing.T) {
	boundaries := []string{
		"INSERT OR IGNORE INTO delivery_attempt_facts", // between keeping attempts and the rest
		"INSERT OR IGNORE INTO delivery_intent_facts",  // between the attempts' and the intents' facts
		"DELETE FROM delivery_evidence WHERE intent_id IN",
		"DELETE FROM delivery_intents",
	}
	for _, marker := range boundaries {
		t.Run(marker, func(t *testing.T) {
			t.Parallel()
			pruner, writer, h := reviewR24DBs(t)
			completedIntent(t, writer, "old")
			age(t, writer, "old", 40*24*time.Hour)
			edge := completedIntent(t, writer, "edge")
			var now string
			if err := writer.Get(&now, `SELECT datetime('now')`); err != nil {
				t.Fatal(err)
			}
			nowTime, err := time.Parse(ledgerStamp, now)
			if err != nil {
				t.Fatal(err)
			}
			boundary := nowTime.Add(2 * time.Second)
			at := boundary.Add(-30 * 24 * time.Hour)
			setTimes(t, writer, "edge", at, at, at)
			hit := false
			h.exec = func(q string) {
				if !strings.Contains(q, marker) {
					return
				}
				h.exec = nil
				hit = true
				if d := time.Until(boundary.Add(1200 * time.Millisecond)); d > 0 {
					time.Sleep(d)
				}
			}
			n, err := pruner.PruneClosedIntents(30)
			if err != nil || n != 1 || !hit {
				t.Fatalf("prune: %d %v, delayed %v", n, err, hit)
			}
			var live, edgeFacts, edgeIntentFacts, oldFacts, unkept int
			for _, q := range []struct {
				dst  *int
				sql  string
				args []any
			}{
				{&live, `SELECT COUNT(*) FROM delivery_intents WHERE id = 'edge'`, nil},
				{&edgeFacts, `SELECT COUNT(*) FROM delivery_attempt_facts WHERE attempt_id = ?`, []any{edge}},
				{&edgeIntentFacts, `SELECT COUNT(*) FROM delivery_intent_facts WHERE intent_id = 'edge'`, nil},
				{&oldFacts, `SELECT (SELECT COUNT(*) FROM delivery_attempt_facts WHERE intent_id = 'old') +
					(SELECT COUNT(*) FROM delivery_intent_facts WHERE intent_id = 'old')`, nil},
				// No deleted intent leaves an attempt that was not kept.
				{&unkept, `SELECT COUNT(*) FROM message_deliveries d WHERE d.intent_id IS NOT NULL
					AND NOT EXISTS (SELECT 1 FROM delivery_intents i WHERE i.id = d.intent_id)
					AND NOT EXISTS (SELECT 1 FROM delivery_attempt_facts f WHERE f.attempt_id = d.id)`, nil},
			} {
				if err := writer.Get(q.dst, q.sql, q.args...); err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("edge intent live=%d attempt_facts=%d intent_facts=%d; old kept rows=%d; unkept attempts=%d",
				live, edgeFacts, edgeIntentFacts, oldFacts, unkept)
			if live != 1 || edgeFacts != 0 || edgeIntentFacts != 0 || oldFacts != 2 || unkept != 0 {
				t.Fatal("the prune's cohort moved")
			}
			// The next prune takes it, with its facts.
			if n, err := pruner.PruneClosedIntents(30); err != nil || n != 1 {
				t.Fatalf("the next prune: %d %v", n, err)
			}
			st := stats(t, writer, time.Now().Add(-90*24*time.Hour), time.Now().Add(time.Hour))
			if st.Total.Attempts != 2 || st.Total.CompletedTotal != 2 || st.Intents.Intents != 2 ||
				st.Coverage.ArchivedAttempts != 2 || !st.Coverage.Complete {
				t.Errorf("after both prunes: %s", wholeJSON(st))
			}
		})
	}
	t.Run("a failed keep deletes nothing", func(t *testing.T) {
		db := intentTestDB(t)
		completedIntent(t, db, "old")
		age(t, db, "old", 40*24*time.Hour)
		if _, err := db.Exec(`CREATE TRIGGER refuse_keep BEFORE INSERT ON delivery_intent_facts
			BEGIN SELECT RAISE(ABORT, 'keep refused'); END`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.PruneClosedIntents(30); err == nil || !strings.Contains(err.Error(), "keep refused") {
			t.Fatalf("prune: %v", err)
		}
		var live, evidence, facts int
		_ = db.Get(&live, `SELECT COUNT(*) FROM delivery_intents WHERE id = 'old'`)
		_ = db.Get(&evidence, `SELECT COUNT(*) FROM delivery_evidence WHERE intent_id = 'old'`)
		_ = db.Get(&facts, `SELECT COUNT(*) FROM delivery_attempt_facts`)
		if live != 1 || evidence != 1 || facts != 0 {
			t.Errorf("after a failed keep: intent %d, evidence %d, attempt facts %d", live, evidence, facts)
		}
	})
}

// R24-R4: what is known of the intents. A ledger made after v78 is known
// from the start; an upgraded one from a day before its upgrade, and a
// window that starts earlier says its intents may be incomplete, without
// inventing how many are missing.
func TestDeliveryStatsIntentHistory(t *testing.T) {
	old := func() (time.Time, time.Time) { return time.Now().Add(-90 * 24 * time.Hour), time.Now().Add(time.Hour) }
	knownFrom := func(t *testing.T, st DeliveryOutcomeStats) time.Time {
		t.Helper()
		k, err := time.Parse(time.RFC3339, st.Coverage.IntentsKnownFrom)
		if err != nil {
			t.Fatalf("intents_known_from %q: %v", st.Coverage.IntentsKnownFrom, err)
		}
		if d := time.Since(k) - 24*time.Hour; d < -time.Minute || d > time.Minute {
			t.Errorf("intents_known_from %s is not a day before the upgrade", k)
		}
		return k
	}

	t.Run("a fresh empty ledger is known and empty", func(t *testing.T) {
		db := intentTestDB(t)
		st := stats(t, db, time.Now().Add(-400*24*time.Hour), time.Now().Add(time.Hour))
		c := st.Coverage
		if !c.Complete || !c.AttemptsComplete || !c.IntentsComplete || c.IntentsKnownFrom != "" || c.LedgerHistory != "fresh" ||
			st.Total.Attempts != 0 || st.Intents.Intents != 0 {
			t.Errorf("%s", wholeJSON(st))
		}
	})

	t.Run("a zero-attempt intent pruned after v79 is kept", func(t *testing.T) {
		db := intentTestDB(t)
		newTestIntent(t, db, "zero", 0, nil)
		if _, err := db.ApplyIntentChange("zero", func(s *IntentSnapshot) *IntentChange {
			return &IntentChange{Transition: "T2", ExpectVersion: s.Intent.Version, SetOutcome: OutcomeFailed, Close: true}
		}); err != nil {
			t.Fatal(err)
		}
		age(t, db, "zero", 40*24*time.Hour)
		from, to := old()
		before := stats(t, db, from, to)
		if n, err := db.PruneClosedIntents(30); err != nil || n != 1 {
			t.Fatalf("prune: %d %v", n, err)
		}
		after := stats(t, db, from, to)
		if statsJSON(t, after) != statsJSON(t, before) || after.Intents.Intents != 1 || after.Intents.ByOutcome[OutcomeFailed] != 1 ||
			after.Coverage.ArchivedIntents != 1 || !after.Coverage.Complete {
			t.Errorf("before %s\nafter %s", wholeJSON(before), wholeJSON(after))
		}
	})

	t.Run("an upgraded ledger that lost a zero-attempt intent says so", func(t *testing.T) {
		db := ledgerAt(t, 77)
		newTestIntent(t, db, "zero", 0, nil)
		if _, err := db.ApplyIntentChange("zero", func(s *IntentSnapshot) *IntentChange {
			return &IntentChange{Transition: "T2", ExpectVersion: s.Intent.Version, SetOutcome: OutcomeFailed, Close: true}
		}); err != nil {
			t.Fatal(err)
		}
		age(t, db, "zero", 40*24*time.Hour)
		pruneBeforeV78(t, db, 30)
		if err := db.migrate(); err != nil {
			t.Fatal(err)
		}
		from, to := old()
		st := stats(t, db, from, to)
		c := st.Coverage
		if c.Complete || c.IntentsComplete || !c.AttemptsComplete || c.LedgerHistory != "upgraded" || st.Intents.Intents != 0 ||
			c.UnavailableAttempts != 0 || c.UnavailableIntentsByFirstAttempt != 0 {
			t.Errorf("%s", wholeJSON(st))
		}
		k := knownFrom(t, st)
		if recent := stats(t, db, k, to); !recent.Coverage.Complete {
			t.Errorf("from intents_known_from on: %s", wholeJSON(recent))
		}
		if early := stats(t, db, k.Add(-time.Second), to); early.Coverage.IntentsComplete {
			t.Errorf("a second before intents_known_from: %s", wholeJSON(early))
		}
	})

	t.Run("an upgraded ledger that lost nothing cannot prove it before the boundary", func(t *testing.T) {
		db := ledgerAt(t, 77)
		completedIntent(t, db, "kept")
		age(t, db, "kept", 40*24*time.Hour)
		if err := db.migrate(); err != nil {
			t.Fatal(err)
		}
		from, to := old()
		before := stats(t, db, from, to)
		if before.Coverage.IntentsComplete || before.Coverage.Complete || !before.Coverage.AttemptsComplete ||
			before.Intents.Intents != 1 || before.Total.Attempts != 1 {
			t.Errorf("%s", wholeJSON(before))
		}
		k := knownFrom(t, before)
		if recent := stats(t, db, k.Add(12*time.Hour), to); !recent.Coverage.Complete {
			t.Errorf("after the boundary: %s", wholeJSON(recent))
		}
		// A prune after the upgrade keeps what it removes.
		if n, err := db.PruneClosedIntents(30); err != nil || n != 1 {
			t.Fatalf("prune: %d %v", n, err)
		}
		if after := stats(t, db, from, to); statsJSON(t, after) != statsJSON(t, before) || after.Coverage.ArchivedIntents != 1 {
			t.Errorf("after the prune: %s", wholeJSON(after))
		}
	})

	t.Run("an intent and its first attempt in different windows", func(t *testing.T) {
		created, attempted, closed := time.Now().Add(-50*24*time.Hour), time.Now().Add(-45*24*time.Hour), time.Now().Add(-44*24*time.Hour)
		w1 := func(db *DB) DeliveryOutcomeStats {
			return stats(t, db, created.Add(-time.Hour), created.Add(time.Hour))
		}
		w2 := func(db *DB) DeliveryOutcomeStats {
			return stats(t, db, attempted.Add(-time.Hour), attempted.Add(time.Hour))
		}

		// Lost before v78, upgraded after.
		db := ledgerAt(t, 77)
		a := statsIntent(t, db, "split", "MESHTASTIC", "mesh_0", 30, "hi")
		finalize(t, db, handOff(t, db, a, "split-corr"), ClassAccepted, "split-corr")
		closeIntent(t, db, "split", OutcomeDeliveredDevice)
		setTimes(t, db, "split", created, attempted, closed)
		pruneBeforeV78(t, db, 30)
		if err := db.migrate(); err != nil {
			t.Fatal(err)
		}
		s1, s2 := w1(db), w2(db)
		if s1.Total.Attempts != 0 || s1.Intents.Intents != 0 || !s1.Coverage.AttemptsComplete || s1.Coverage.IntentsComplete ||
			s1.Coverage.Complete || s1.Coverage.UnavailableIntentsByFirstAttempt != 0 {
			t.Errorf("the intent's window, lost: %s", wholeJSON(s1))
		}
		if s2.Total.Attempts != 0 || s2.Coverage.UnavailableAttempts != 1 || s2.Coverage.UnavailableIntentsByFirstAttempt != 1 ||
			s2.Coverage.AttemptsComplete || s2.Coverage.Complete || s2.Intents.Intents != 0 {
			t.Errorf("the attempt's window, lost: %s", wholeJSON(s2))
		}

		// The same history on a ledger made after v79, pruned by it.
		db = intentTestDB(t)
		a = statsIntent(t, db, "split", "MESHTASTIC", "mesh_0", 30, "hi")
		finalize(t, db, handOff(t, db, a, "split-corr"), ClassAccepted, "split-corr")
		closeIntent(t, db, "split", OutcomeDeliveredDevice)
		setTimes(t, db, "split", created, attempted, closed)
		if n, err := db.PruneClosedIntents(30); err != nil || n != 1 {
			t.Fatalf("prune: %d %v", n, err)
		}
		s1, s2 = w1(db), w2(db)
		if s1.Intents.Intents != 1 || s1.Coverage.ArchivedIntents != 1 || s1.Total.Attempts != 0 || !s1.Coverage.Complete {
			t.Errorf("the intent's window, kept: %s", wholeJSON(s1))
		}
		if s2.Total.Attempts != 1 || s2.Total.Accepted != 1 || s2.Coverage.ArchivedAttempts != 1 || s2.Intents.Intents != 0 ||
			!s2.Coverage.Complete {
			t.Errorf("the attempt's window, kept: %s", wholeJSON(s2))
		}
	})

	t.Run("which ledgers are fresh", func(t *testing.T) {
		for v, want := range map[int]string{61: "fresh", 62: "upgraded", 78: "upgraded"} {
			db := ledgerAt(t, v)
			if err := db.migrate(); err != nil {
				t.Fatal(err)
			}
			var got string
			if err := db.Get(&got, `SELECT provenance FROM delivery_history_coverage WHERE id = 1`); err != nil || got != want {
				t.Errorf("a ledger at v%d upgraded: %q (%v), want %s", v, got, err, want)
			}
		}
	})
}
