package database

import (
	"strings"
	"testing"
	"time"
)

func TestReviewR25CoverageMigrationResume(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		name := "after_sql_before_hook"
		if fresh {
			name = "after_fresh_hook_before_version"
		}
		t.Run(name, func(t *testing.T) {
			db := ledgerAt(t, 78)
			if _, err := db.Exec(migrations[78]); err != nil {
				t.Fatal(err)
			}
			known := time.Now().UTC().Add(-49 * time.Hour).Truncate(time.Second)
			recorded := known.Add(24 * time.Hour)
			if _, err := db.Exec(`UPDATE delivery_history_coverage SET intents_known_from = ?, recorded_at = ? WHERE id = 1`,
				known.Format(ledgerStamp), recorded.Format(ledgerStamp)); err != nil {
				t.Fatal(err)
			}
			if fresh {
				if err := postMigration[79](db, 0); err != nil {
					t.Fatal(err)
				}
			}
			var before string
			const provenance = `SELECT provenance || '|' || intents_known_from || '|' || recorded_at FROM delivery_history_coverage WHERE id = 1`
			if err := db.Get(&before, provenance); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := db.migrate(); err != nil {
					t.Fatal(err)
				}
				var after string
				if err := db.Get(&after, provenance); err != nil {
					t.Fatal(err)
				}
				if after != before {
					t.Fatalf("resume %d moved provenance: before=%q after=%q", i, before, after)
				}
			}
			st := stats(t, db, known.Add(-time.Hour), recorded.Add(time.Hour))
			if st.Coverage.IntentsComplete != fresh || st.Coverage.Complete != fresh {
				t.Fatalf("unexpected resumed coverage: %+v", st.Coverage)
			}
			t.Logf("provenance preserved across resume and second start: %s; complete=%v", before, st.Coverage.Complete)
		})
	}
}

func TestReviewR25MissingCoverageFailsClosed(t *testing.T) {
	db := intentTestDB(t)
	if _, err := db.Exec(`DELETE FROM delivery_history_coverage WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	_, err := db.DeliveryOutcomeStatsBetween(time.Now().Add(-time.Hour), time.Now())
	if err == nil || !strings.Contains(err.Error(), "history coverage") {
		t.Fatalf("missing provenance must return an error: %v", err)
	}
	t.Logf("missing provenance returns error: %v", err)
}
