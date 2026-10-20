package database

import (
	"testing"
	"time"
)

func TestReviewR23FractionalWindowBoundaries(t *testing.T) {
	db := intentTestDB(t)
	a := statsIntent(t, db, "fractional", "MESHTASTIC", "mesh_0", 30, "hi")
	stamp := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`UPDATE message_deliveries SET created_at = ? WHERE id = ?`, stamp.Format("2006-01-02 15:04:05"), a); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		from, to time.Time
		want     int
	}{
		{"fractional_end_includes_whole_second", stamp.Add(-500 * time.Millisecond), stamp.Add(500 * time.Millisecond), 1},
		{"fractional_start_excludes_earlier_second", stamp.Add(500 * time.Millisecond), stamp.Add(1500 * time.Millisecond), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := db.DeliveryOutcomeStatsBetween(tc.from, tc.to)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("requested=[%s,%s) returned=[%s,%s) attempts=%d want=%d", tc.from.Format(time.RFC3339Nano), tc.to.Format(time.RFC3339Nano), st.Since, st.Until, st.Total.Attempts, tc.want)
			if st.Total.Attempts != tc.want {
				t.Errorf("fractional boundary changed membership")
			}
		})
	}
}

func TestReviewR23PruningSilentlyDropsRetainedAttempts(t *testing.T) {
	db := intentTestDB(t)
	a := statsIntent(t, db, "retention", "MESHTASTIC", "mesh_0", 30, "hi")
	finalize(t, db, handOff(t, db, a, "retention-corr"), ClassAccepted, "retention-corr")
	receipt(t, db, "retention-corr", OutcomeDeliveredDevice)
	_, err := db.ApplyIntentChange("retention", func(s *IntentSnapshot) *IntentChange {
		return &IntentChange{Transition: "T9", ExpectVersion: s.Intent.Version, SetOutcome: OutcomeDeliveredDevice, Close: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Add(-40 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	if _, err := db.Exec(`UPDATE message_deliveries SET created_at = ? WHERE id = ?`, stamp, a); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE delivery_intents SET created_at = ?, closed_at = ? WHERE id = 'retention'`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	from, to := time.Now().Add(-90*24*time.Hour), time.Now().Add(time.Hour)
	before, err := db.DeliveryOutcomeStatsBetween(from, to)
	if err != nil {
		t.Fatal(err)
	}
	n, err := db.PruneClosedIntents(30)
	if err != nil {
		t.Fatal(err)
	}
	after, err := db.DeliveryOutcomeStatsBetween(from, to)
	if err != nil {
		t.Fatal(err)
	}
	var retained int
	if err := db.Get(&retained, `SELECT count(*) FROM message_deliveries WHERE id = ? AND intent_id = 'retention'`, a); err != nil {
		t.Fatal(err)
	}
	t.Logf("before attempts=%d completed=%d; pruned intents=%d; retained attempts=%d; after attempts=%d completed=%d", before.Total.Attempts, before.Total.CompletedTotal, n, retained, after.Total.Attempts, after.Total.CompletedTotal)
	if after.Total.Attempts != retained {
		t.Fatal("requested window silently omits retained intent attempts after normal pruning")
	}
}

func TestReviewR23EvidenceLookupPlan(t *testing.T) {
	db := intentTestDB(t)
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT d.id,
        COALESCE((SELECT e.completes FROM delivery_evidence e WHERE e.attempt_id = d.id AND e.matched = 1
        AND e.ambiguous = 0 AND e.completes != '' ORDER BY e.id LIMIT 1), '')
        FROM message_deliveries d JOIN delivery_intents i ON i.id = d.intent_id
        WHERE d.created_at >= ? AND d.created_at < ?`, "2026-01-01 00:00:00", "2027-01-01 00:00:00")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		t.Log(detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
