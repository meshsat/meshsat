package database

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// closeIntent closes an intent with an outcome through the ledger.
func closeIntent(t *testing.T, db *DB, id, outcome string) {
	t.Helper()
	if _, err := db.ApplyIntentChange(id, func(s *IntentSnapshot) *IntentChange {
		return &IntentChange{Transition: "T9", ExpectVersion: s.Intent.Version, SetOutcome: outcome, Close: true}
	}); err != nil {
		t.Fatal(err)
	}
}

// age moves an intent and its attempts back by d (a fixture: nothing in
// the ledger writes old times).
func age(t *testing.T, db *DB, id string, d time.Duration) {
	t.Helper()
	stamp := time.Now().UTC().Add(-d).Format("2006-01-02 15:04:05")
	if _, err := db.Exec(`UPDATE message_deliveries SET created_at = ? WHERE intent_id = ?`, stamp, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE delivery_intents SET created_at = ?,
		closed_at = CASE WHEN closed_at IS NULL THEN NULL ELSE ? END WHERE id = ?`, stamp, stamp, id); err != nil {
		t.Fatal(err)
	}
}

// statsJSON is the figures of a window without the coverage block.
func statsJSON(t *testing.T, st DeliveryOutcomeStats) string {
	t.Helper()
	st.Coverage = StatsCoverage{}
	b, _ := json.Marshal(st)
	return string(b)
}

// R23-R1: the retention prune keeps what the statistics need, so a window
// reads the same before and after it, for completed, failed and still
// active intents on both sides of the prune's boundary; history that was
// pruned without its facts (as before v78) is reported unavailable, never
// counted in a stage, and is told apart from an empty window and from a
// ledger never pruned. [MESHSAT-1584]
func TestDeliveryOutcomeStatsSurviveThePrune(t *testing.T) {
	db := intentTestDB(t)

	// Old (40 days) and closed: completed by a receipt, and failed.
	a := statsIntent(t, db, "old-done", "MESHTASTIC", "mesh_0", 30, "hi")
	finalize(t, db, handOff(t, db, a, "1"), ClassAccepted, "1")
	receipt(t, db, "1", OutcomeDeliveredDevice)
	closeIntent(t, db, "old-done", OutcomeDeliveredDevice)
	age(t, db, "old-done", 40*24*time.Hour)
	a = statsIntent(t, db, "old-failed", "SMS", "cellular_0", 120, "a longer text")
	gen := claim(t, db, a)
	if err := db.EndAttemptNotSent(a, gen, "offline", "", false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	closeIntent(t, db, "old-failed", OutcomeFailed)
	age(t, db, "old-failed", 40*24*time.Hour)
	// Old and still active: the prune leaves it.
	statsIntent(t, db, "old-active", "MESHTASTIC", "mesh_0", 30, "hi")
	age(t, db, "old-active", 40*24*time.Hour)
	// Recent and closed: inside the retention.
	a = statsIntent(t, db, "new-done", "MQTT", "hub_0", 60, "hi there")
	finalize(t, db, handOff(t, db, a, "2"), ClassAccepted, "2")
	receipt(t, db, "2", OutcomeDeliveredApp)
	closeIntent(t, db, "new-done", OutcomeDeliveredApp)

	from, to := time.Now().Add(-90*24*time.Hour), time.Now().Add(time.Hour)
	window := func() DeliveryOutcomeStats {
		t.Helper()
		st, err := db.DeliveryOutcomeStatsBetween(from, to)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	before := window()
	if !before.Coverage.Complete || before.Coverage.ArchivedAttempts != 0 || before.Total.Attempts != 4 ||
		before.Total.CompletedTotal != 2 || before.Total.NotSent != 1 || before.Total.Unresolved != 1 || before.Intents.Intents != 4 {
		t.Fatalf("before the prune: %+v", before)
	}

	n, err := db.PruneClosedIntents(30)
	if err != nil || n != 2 {
		t.Fatalf("pruned %d intents (%v); want the two old closed ones", n, err)
	}
	after := window()
	if statsJSON(t, after) != statsJSON(t, before) {
		t.Fatalf("the prune changed the window's figures:\n before %s\n after  %s", statsJSON(t, before), statsJSON(t, after))
	}
	if !after.Coverage.Complete || after.Coverage.ArchivedAttempts != 2 || after.Coverage.ArchivedIntents != 2 ||
		after.Coverage.UnavailableAttempts != 0 {
		t.Errorf("coverage after the prune: %+v", after.Coverage)
	}
	// Pruning again changes nothing, and the archive does not double.
	if _, err := db.PruneClosedIntents(30); err != nil {
		t.Fatal(err)
	}
	if again := window(); statsJSON(t, again) != statsJSON(t, before) || again.Coverage.ArchivedAttempts != 2 {
		t.Errorf("a second prune: %s %+v", statsJSON(t, again), again.Coverage)
	}

	// An empty window is complete with nothing in it.
	empty, err := db.DeliveryOutcomeStatsBetween(time.Now().Add(-400*24*time.Hour), time.Now().Add(-300*24*time.Hour))
	if err != nil || !empty.Coverage.Complete || empty.Total.Attempts != 0 || empty.Coverage.UnavailableAttempts != 0 {
		t.Errorf("an empty window: %+v %v", empty, err)
	}

	// History pruned before v78 kept no facts: delete an intent the way
	// the old prune did. Its attempt is unavailable, in no stage, and the
	// window says it is incomplete; the intent is not counted either.
	a = statsIntent(t, db, "pre-v78", "MESHTASTIC", "mesh_0", 30, "hi")
	finalize(t, db, handOff(t, db, a, "3"), ClassAccepted, "3")
	for _, q := range []string{`DELETE FROM delivery_evidence WHERE intent_id = 'pre-v78'`,
		`DELETE FROM delivery_transmissions WHERE intent_id = 'pre-v78'`, `DELETE FROM delivery_intents WHERE id = 'pre-v78'`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	lost := window()
	if lost.Coverage.Complete || lost.Coverage.AttemptsComplete || lost.Coverage.UnavailableAttempts != 1 ||
		lost.Coverage.UnavailableIntentsByFirstAttempt != 1 ||
		lost.Total.Attempts != 4 || lost.Intents.Intents != 4 {
		t.Errorf("history pruned without facts: coverage %+v, attempts %d, intents %d", lost.Coverage, lost.Total.Attempts, lost.Intents.Intents)
	}
	// A window that holds only kept history is complete.
	if old, _ := db.DeliveryOutcomeStatsBetween(time.Now().Add(-90*24*time.Hour), time.Now().Add(-20*24*time.Hour)); !old.Coverage.Complete ||
		old.Total.Attempts != 3 || old.Coverage.ArchivedAttempts != 2 {
		t.Errorf("the old part of the window: %+v %+v", old.Coverage, old.Total)
	}
}

// A ledger that is never pruned (retention off) reads complete, with
// nothing archived.
func TestDeliveryOutcomeStatsWithoutAPrune(t *testing.T) {
	db := intentTestDB(t)
	statsIntent(t, db, "kept", "MESHTASTIC", "mesh_0", 30, "hi")
	age(t, db, "kept", 400*24*time.Hour)
	st, err := db.DeliveryOutcomeStatsBetween(time.Now().Add(-500*24*time.Hour), time.Now())
	if err != nil || !st.Coverage.Complete || st.Coverage.ArchivedAttempts != 0 || st.Total.Attempts != 1 {
		t.Fatalf("%+v %v", st, err)
	}
}

// R23-R2: the window keeps the membership its bounds give, for attempts and
// intents alike, whatever fraction or offset the bounds carry; the answer
// states them as requested.
func TestDeliveryOutcomeStatsWindowBounds(t *testing.T) {
	db := intentTestDB(t)
	statsIntent(t, db, "at-noon", "MESHTASTIC", "mesh_0", 30, "hi")
	noon := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	stamp := noon.Format("2006-01-02 15:04:05")
	_, _ = db.Exec(`UPDATE message_deliveries SET created_at = ? WHERE intent_id = 'at-noon'`, stamp)
	_, _ = db.Exec(`UPDATE delivery_intents SET created_at = ? WHERE id = 'at-noon'`, stamp)
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	athens := time.FixedZone("EEST", 3*3600)
	for _, c := range []struct {
		name     string
		from, to time.Time
		want     int
	}{
		{"fractional end includes the whole second", noon.Add(-ms(500)), noon.Add(ms(500)), 1},
		{"fractional start excludes the earlier second", noon.Add(ms(500)), noon.Add(ms(1500)), 0},
		{"a subsecond window after it", noon.Add(ms(200)), noon.Add(ms(700)), 0},
		{"a subsecond window around it", noon.Add(-ms(100)), noon.Add(ms(100)), 1},
		{"an exact start is inclusive", noon, noon.Add(time.Second), 1},
		{"an exact end is exclusive", noon.Add(-time.Second), noon, 0},
		{"the same instants in another offset", noon.In(athens), noon.Add(time.Second).In(athens), 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, err := db.DeliveryOutcomeStatsBetween(c.from, c.to)
			if err != nil {
				t.Fatal(err)
			}
			if st.Total.Attempts != c.want || st.Intents.Intents != c.want {
				t.Errorf("[%s, %s): %d attempts, %d intents; want %d", st.Since, st.Until, st.Total.Attempts, st.Intents.Intents, c.want)
			}
			if st.Since != c.from.UTC().Format(time.RFC3339Nano) || st.Until != c.to.UTC().Format(time.RFC3339Nano) {
				t.Errorf("answered [%s, %s); asked [%s, %s)", st.Since, st.Until, c.from.Format(time.RFC3339Nano), c.to.Format(time.RFC3339Nano))
			}
		})
	}
}

// R23-R3: the window's attempts and each attempt's receipt are found by
// index, not by a scan.
func TestDeliveryOutcomeStatsQueryPlan(t *testing.T) {
	db := intentTestDB(t)
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT d.id,
		(SELECT COUNT(*) FROM delivery_transmissions t WHERE t.attempt_id = d.id AND t.state = 'handed_off'),
		COALESCE((SELECT e.completes FROM delivery_evidence e WHERE e.attempt_id = d.id AND e.matched = 1
			AND e.ambiguous = 0 AND e.completes != '' ORDER BY e.id LIMIT 1), '')
		FROM message_deliveries d JOIN delivery_intents i ON i.id = d.intent_id
		WHERE d.intent_id IS NOT NULL AND d.created_at >= ? AND d.created_at < ?`, "2026-01-01 00:00:00", "2027-01-01 00:00:00")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(plan, "\n")
	for _, want := range []string{"SEARCH d USING INDEX idx_message_deliveries_intent_created",
		"SEARCH e USING INDEX idx_delivery_evidence_attempt", "SEARCH t USING"} {
		if !strings.Contains(all, want) {
			t.Errorf("plan lacks %q:\n%s", want, all)
		}
	}
	for _, line := range plan {
		if strings.HasPrefix(line, "SCAN ") {
			t.Errorf("a scan in the plan: %s\n%s", line, all)
		}
	}
}

// TestDeliveryOutcomeStatsMeasured times a populated ledger (run with
// MESHSAT_STATS_MEASURE=1, in a capped container; not in CI).
func TestDeliveryOutcomeStatsMeasured(t *testing.T) {
	if os.Getenv("MESHSAT_STATS_MEASURE") == "" {
		t.Skip("MESHSAT_STATS_MEASURE not set")
	}
	db := intentTestDB(t)
	const intents, evidencePer = 10000, 3
	tx, err := db.Beginx()
	if err != nil {
		t.Fatal(err)
	}
	plan := `[{"route_index":1,"kind":"MESHTASTIC","interface_id":"mesh_0","est_wire_bytes":40,"admitted":true},` +
		`{"route_index":2,"kind":"SMS","interface_id":"cellular_0","est_wire_bytes":120,"admitted":true}]`
	for i := 0; i < intents; i++ {
		id := "m" + time.Duration(i).String()
		tx.MustExec(`INSERT INTO delivery_intents (id, submission_id, contact_id, precedence, class, payload, payload_sha256,
			requested_strategy, effective_strategy, route_plan, currency, state, outcome) VALUES (?, 's', 'c', 'Routine', 'message',
			x'6869', 'x', '', 'ALL_BEARERS', ?, 'EUR', 'closed', 'accepted_unconfirmed')`, id, plan)
		for r := 1; r <= 2; r++ {
			res := tx.MustExec(`INSERT INTO message_deliveries (msg_ref, channel, status, payload, intent_id, round, attempt_no, route_index)
				VALUES (?, 'mesh_0', 'accepted', x'6869', ?, 1, ?, ?)`, id, id, r, r)
			attempt, _ := res.LastInsertId()
			for e := 0; e < evidencePer; e++ {
				tx.MustExec(`INSERT INTO delivery_evidence (intent_id, attempt_id, source, event, matched) VALUES (?, ?, 'x', 'progress', 1)`, id, attempt)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	st, err := db.DeliveryOutcomeStatsBetween(time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	took := time.Since(start)
	if err != nil || st.Total.Attempts != 2*intents {
		t.Fatalf("%d attempts (%v)", st.Total.Attempts, err)
	}
	t.Logf("with the v78 indexes: %d intents, %d attempts, %d evidence rows: %v", intents, 2*intents, 2*intents*evidencePer, took)
	// The same query without them, for comparison.
	db.MustExec(`DROP INDEX idx_delivery_evidence_attempt`)
	db.MustExec(`DROP INDEX idx_message_deliveries_intent_created`)
	start = time.Now()
	if _, err := db.DeliveryOutcomeStatsBetween(time.Now().Add(-time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	t.Logf("without them: %v", time.Since(start))
}
