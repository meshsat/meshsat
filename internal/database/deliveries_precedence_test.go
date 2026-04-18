package database

import (
	"testing"
)

// TestGetPendingDeliveries_PrecedenceOrder — the queue is drained
// in STANAG 4406 precedence order, with the legacy priority as the
// tiebreaker inside each precedence bucket. [MESHSAT-546 / S2-03]
func TestGetPendingDeliveries_PrecedenceOrder(t *testing.T) {
	db := testDB(t)
	mk := func(msgRef, precedence string, priority int) int64 {
		id, err := db.InsertDelivery(MessageDelivery{
			MsgRef:     msgRef,
			Channel:    "mesh_0",
			Status:     "queued",
			Priority:   priority,
			Precedence: precedence,
		})
		if err != nil {
			t.Fatalf("insert %s: %v", msgRef, err)
		}
		return id
	}
	// Insert in deliberately scrambled order.
	r := mk("routine", "Routine", 1)
	d := mk("deferred", "Deferred", 0)
	i := mk("immediate", "Immediate", 2)
	f := mk("flash", "Flash", 1)
	p := mk("priority", "Priority", 2)
	o := mk("override", "Override", 1)

	rows, err := db.GetPendingDeliveries("mesh_0", 100)
	if err != nil {
		t.Fatalf("GetPendingDeliveries: %v", err)
	}
	gotOrder := make([]int64, len(rows))
	for k, row := range rows {
		gotOrder[k] = row.ID
	}
	wantOrder := []int64{o, f, i, p, r, d}
	for k := range wantOrder {
		if gotOrder[k] != wantOrder[k] {
			t.Errorf("pos %d: got id=%d msg_ref=%q, want id=%d",
				k, gotOrder[k], rows[k].MsgRef, wantOrder[k])
		}
	}
}

// TestWeakestEvictable_PrecedenceThenPriority — inside a saturated
// queue, the most-evictable row is the lowest-precedence one (then
// highest priority number). Deferred > Routine > Priority etc.
// Priority=0 rows are protected.
func TestWeakestEvictable_PrecedenceThenPriority(t *testing.T) {
	db := testDB(t)
	mk := func(msgRef, precedence string, priority int) int64 {
		id, err := db.InsertDelivery(MessageDelivery{
			MsgRef:     msgRef,
			Channel:    "mesh_0",
			Status:     "queued",
			Priority:   priority,
			Precedence: precedence,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	mk("p0-flash", "Flash", 0) // never evictable (priority 0)
	_ = mk("flash", "Flash", 1)
	def := mk("deferred-low", "Deferred", 2)
	_ = mk("routine", "Routine", 1)

	c, err := db.WeakestEvictable("mesh_0")
	if err != nil {
		t.Fatalf("WeakestEvictable: %v", err)
	}
	if c.ID != def {
		t.Errorf("weakest: got id=%d, want id=%d (deferred priority 2)", c.ID, def)
	}
	if c.Precedence != "Deferred" {
		t.Errorf("precedence: got %q", c.Precedence)
	}
	if c.PrecedenceRank != 5 {
		t.Errorf("rank: got %d, want 5", c.PrecedenceRank)
	}
}

// TestWeakestEvictable_AllP0 — when every queued row is P0 (never
// evictable), the helper returns sql.ErrNoRows (wrapped).
func TestWeakestEvictable_AllP0(t *testing.T) {
	db := testDB(t)
	_, _ = db.InsertDelivery(MessageDelivery{
		MsgRef:     "p0-only",
		Channel:    "mesh_0",
		Status:     "queued",
		Priority:   0,
		Precedence: "Routine",
	})
	_, err := db.WeakestEvictable("mesh_0")
	if err == nil {
		t.Error("expected no-row error when every queued delivery is P0")
	}
}

// TestEvictDelivery_MarksDead — specific-ID eviction flips the
// target row to status='dead' with the provided reason.
func TestEvictDelivery_MarksDead(t *testing.T) {
	db := testDB(t)
	id, _ := db.InsertDelivery(MessageDelivery{
		MsgRef:     "to-evict",
		Channel:    "mesh_0",
		Status:     "queued",
		Priority:   1,
		Precedence: "Routine",
	})
	n, err := db.EvictDelivery(id, "test reason")
	if err != nil {
		t.Fatalf("EvictDelivery: %v", err)
	}
	if n != 1 {
		t.Errorf("rows affected: got %d, want 1", n)
	}
	got, _ := db.GetDelivery(id)
	if got.Status != "dead" {
		t.Errorf("status: got %q, want 'dead'", got.Status)
	}
	if got.LastError != "test reason" {
		t.Errorf("last_error: got %q", got.LastError)
	}
	// Idempotent — second eviction on a dead row does nothing.
	n2, _ := db.EvictDelivery(id, "again")
	if n2 != 0 {
		t.Errorf("second evict should be no-op; got %d rows", n2)
	}
}

// TestEvictLowestPriority_NowPrecedenceAware — queue has Routine P2
// and Deferred P1; the Deferred row is evicted first even though
// its priority int is numerically lower.
func TestEvictLowestPriority_NowPrecedenceAware(t *testing.T) {
	db := testDB(t)
	routine, _ := db.InsertDelivery(MessageDelivery{
		MsgRef: "routine-p2", Channel: "mesh_0", Status: "queued",
		Priority: 2, Precedence: "Routine",
	})
	deferred, _ := db.InsertDelivery(MessageDelivery{
		MsgRef: "deferred-p1", Channel: "mesh_0", Status: "queued",
		Priority: 1, Precedence: "Deferred",
	})

	n, err := db.EvictLowestPriority("mesh_0")
	if err != nil {
		t.Fatalf("EvictLowestPriority: %v", err)
	}
	if n != 1 {
		t.Errorf("evicted rows: %d", n)
	}
	evictedRow, _ := db.GetDelivery(deferred)
	if evictedRow.Status != "dead" {
		t.Errorf("Deferred row should be dead, got %q", evictedRow.Status)
	}
	routineRow, _ := db.GetDelivery(routine)
	if routineRow.Status == "dead" {
		t.Error("Routine-P2 row should NOT be evicted — Deferred is weaker by precedence")
	}
}
