package database

import "testing"

// The startup cleanup kills a max_retries=0 row at 15 retries, but not the
// legs of the SOS kept in the database (sos_run): an SOS leg waits for its
// link as long as the SOS is on, and its cancellation legs must still go
// once it is cancelled. Legs of an earlier SOS and other retry-forever rows
// are cleaned up as before. [MESHSAT-1581]
func TestCancelRunawayDeliveries_KeepsTheSOSLegs(t *testing.T) {
	db := testDB(t)
	if err := db.SetSystemConfig("sos_run", `{"id":1760000000000,"started_at":"2026-10-16T01:00:00Z","text":"SOS"}`); err != nil {
		t.Fatal(err)
	}
	insert := func(ref string, retries, maxRetries int) int64 {
		t.Helper()
		id, err := db.InsertDelivery(MessageDelivery{MsgRef: ref, Channel: "iridium_imt_0", Status: "retry", Priority: 0,
			TextPreview: "x", MaxRetries: maxRetries, Visited: "[]", QoSLevel: 1})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE message_deliveries SET retries = ? WHERE id = ?`, retries, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	kept := map[string]int64{
		"satellite leg":     insert("sos-1760000000000-satellite", 40, 0),
		"mesh leg":          insert("sos-1760000000000-mesh", 15, 0),
		"cancellation leg":  insert("sos-1760000000000-cancel:satellite", 22, 0),
		"under the limit":   insert("plain-under", 14, 0),
		"bounded, retrying": insert("plain-bounded", 2, 3),
	}
	killed := map[string]int64{
		"an earlier SOS leg":        insert("sos-1750000000000-satellite", 40, 0),
		"another retry-forever row": insert("plain-forever", 15, 0),
		"bounded, spent":            insert("plain-spent", 3, 3),
		"same digits, longer id":    insert("sos-17600000000001-mesh", 40, 0),
	}

	n, err := db.CancelRunawayDeliveries(15)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(killed)) {
		t.Errorf("cancelled %d, want %d", n, len(killed))
	}
	for name, id := range kept {
		if d, err := db.GetDelivery(id); err != nil || d.Status != "retry" {
			t.Errorf("%s: %q, %v; want it kept", name, d.Status, err)
		}
	}
	for name, id := range killed {
		if d, err := db.GetDelivery(id); err != nil || d.Status != "dead" {
			t.Errorf("%s: %q, %v; want it cleaned up", name, d.Status, err)
		}
	}
}

// With no SOS kept, or one that does not read, nothing is exempt.
func TestCancelRunawayDeliveries_NoSOSKept(t *testing.T) {
	for _, kept := range []string{"", "not json", `{"id":0}`} {
		db := testDB(t)
		if kept != "" {
			if err := db.SetSystemConfig("sos_run", kept); err != nil {
				t.Fatal(err)
			}
		}
		id, err := db.InsertDelivery(MessageDelivery{MsgRef: "sos-1760000000000-mesh", Channel: "mesh_0", Status: "retry",
			TextPreview: "x", MaxRetries: 0, Visited: "[]", QoSLevel: 1})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE message_deliveries SET retries = 20 WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
		if n, err := db.CancelRunawayDeliveries(15); err != nil || n != 1 {
			t.Errorf("sos_run %q: cancelled %d, %v; want 1", kept, n, err)
		}
	}
}

// An earlier run still owed its cancellation (sos_prev_runs, r16 review C10-R4) keeps its
// cancel frames through the cleanup too; a run no longer kept does not.
func TestCancelRunawayDeliveries_KeepsAnEarlierRunsOwedCancel(t *testing.T) {
	db := testDB(t)
	if err := db.SetSystemConfig("sos_run", `{"id":1760000000000,"text":"SOS"}`); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSystemConfig("sos_prev_runs", `[{"id":1759000000000,"text":"SOS","cancelled_at":"2026-10-16T01:00:00Z"}]`); err != nil {
		t.Fatal(err)
	}
	insert := func(ref string) int64 {
		t.Helper()
		id, err := db.InsertDelivery(MessageDelivery{MsgRef: ref, Channel: "iridium_imt_0", Status: "retry", Priority: 0,
			TextPreview: "x", MaxRetries: 0, Visited: "[]", QoSLevel: 1})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE message_deliveries SET retries = 40 WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	prevCancel := insert("sos-1759000000000-cancel:satellite")
	current := insert("sos-1760000000000-cancel:satellite")
	gone := insert("sos-1758000000000-cancel:satellite")
	if _, err := db.CancelRunawayDeliveries(15); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		id   int64
		want string
	}{"the earlier run's owed cancel": {prevCancel, "retry"}, "the current run's": {current, "retry"}, "a run no longer kept": {gone, "dead"}} {
		if d, err := db.GetDelivery(c.id); err != nil || d.Status != c.want {
			t.Errorf("%s: %q, %v; want %s", name, d.Status, err, c.want)
		}
	}
}
