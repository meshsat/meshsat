package database

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMigrations(t *testing.T) {
	db := testDB(t)

	var version int
	if err := db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != len(migrations) {
		t.Errorf("schema version: got %d, want %d", version, len(migrations))
	}

	// Verify dead_letters table exists
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM dead_letters").Scan(&count); err != nil {
		t.Fatalf("dead_letters table missing: %v", err)
	}
}

func TestDeadLetterLifecycle(t *testing.T) {
	db := testDB(t)

	payload := []byte{0x01, 0x02, 0x03}
	nextRetry := time.Now().Add(-time.Minute) // already due

	// Insert
	if err := db.InsertDeadLetter(42, payload, 3, nextRetry, "connection timeout", "test message"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Count
	count, err := db.CountPendingDeadLetters()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("count: got %d, want 1", count)
	}

	// Get pending
	pending, err := db.GetPendingDeadLetters(10)
	if err != nil {
		t.Fatalf("get pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending len: got %d, want 1", len(pending))
	}
	dl := pending[0]
	if dl.PacketID != 42 {
		t.Errorf("packet_id: got %d, want 42", dl.PacketID)
	}
	if dl.Retries != 0 {
		t.Errorf("retries: got %d, want 0", dl.Retries)
	}
	if dl.Status != "pending" {
		t.Errorf("status: got %q, want pending", dl.Status)
	}
	if string(dl.Payload) != string(payload) {
		t.Errorf("payload mismatch")
	}

	// Update retry
	nextRetry2 := time.Now().Add(5 * time.Minute)
	if err := db.UpdateDeadLetterRetry(dl.ID, nextRetry2, "still failing", 32); err != nil {
		t.Fatalf("update retry: %v", err)
	}

	// Should NOT appear in pending (next_retry is in future)
	pending2, err := db.GetPendingDeadLetters(10)
	if err != nil {
		t.Fatalf("get pending 2: %v", err)
	}
	if len(pending2) != 0 {
		t.Errorf("pending after reschedule: got %d, want 0", len(pending2))
	}

	// Mark sent
	if err := db.MarkDeadLetterSent(dl.ID); err != nil {
		t.Fatalf("mark sent: %v", err)
	}

	count, _ = db.CountPendingDeadLetters()
	if count != 0 {
		t.Errorf("count after mark sent: got %d, want 0", count)
	}
}

func TestDeadLetterLastMOStatus(t *testing.T) {
	db := testDB(t)

	nextRetry := time.Now().Add(-time.Minute)
	if err := db.InsertDeadLetter(50, []byte{0xAB}, 10, nextRetry, "initial", "test mo_status"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Verify default last_mo_status is -1
	pending, err := db.GetPendingDeadLetters(10)
	if err != nil {
		t.Fatalf("get pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending: got %d, want 1", len(pending))
	}
	if pending[0].LastMOStatus != -1 {
		t.Errorf("initial last_mo_status: got %d, want -1", pending[0].LastMOStatus)
	}

	// Update with failure mo_status=32
	retry1 := time.Now().Add(-30 * time.Second)
	if err := db.UpdateDeadLetterRetry(pending[0].ID, retry1, "mo_status=32", 32); err != nil {
		t.Fatalf("update retry: %v", err)
	}

	pending, _ = db.GetPendingDeadLetters(10)
	if len(pending) != 1 {
		t.Fatalf("pending after retry: got %d, want 1", len(pending))
	}
	if pending[0].LastMOStatus != 32 {
		t.Errorf("last_mo_status after failure: got %d, want 32", pending[0].LastMOStatus)
	}

	// Update with success mo_status=0
	retry2 := time.Now().Add(-10 * time.Second)
	if err := db.UpdateDeadLetterRetry(pending[0].ID, retry2, "mo_status=0", 0); err != nil {
		t.Fatalf("update retry 2: %v", err)
	}

	pending, _ = db.GetPendingDeadLetters(10)
	if len(pending) != 1 {
		t.Fatalf("pending after retry 2: got %d, want 1", len(pending))
	}
	if pending[0].LastMOStatus != 0 {
		t.Errorf("last_mo_status after success: got %d, want 0", pending[0].LastMOStatus)
	}
}

func TestDeadLetterExpire(t *testing.T) {
	db := testDB(t)

	nextRetry := time.Now().Add(-time.Minute)
	if err := db.InsertDeadLetter(99, []byte{0xFF}, 1, nextRetry, "timeout", ""); err != nil {
		t.Fatalf("insert: %v", err)
	}

	pending, _ := db.GetPendingDeadLetters(10)
	if len(pending) != 1 {
		t.Fatalf("pending: got %d, want 1", len(pending))
	}

	if err := db.ExpireDeadLetter(pending[0].ID, "max retries exhausted"); err != nil {
		t.Fatalf("expire: %v", err)
	}

	count, _ := db.CountPendingDeadLetters()
	if count != 0 {
		t.Errorf("count after expire: got %d, want 0", count)
	}
}

func TestDeadLetterPrune(t *testing.T) {
	db := testDB(t)

	// Insert and immediately mark sent
	nextRetry := time.Now().Add(-time.Minute)
	if err := db.InsertDeadLetter(1, []byte{0x01}, 3, nextRetry, "err", ""); err != nil {
		t.Fatalf("insert: %v", err)
	}
	pending, _ := db.GetPendingDeadLetters(10)
	db.MarkDeadLetterSent(pending[0].ID)

	// Backdate updated_at to make it prunable
	db.Exec("UPDATE dead_letters SET updated_at = datetime('now', '-10 days')")

	pruned, err := db.PruneDeadLetters(7)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 1 {
		t.Errorf("pruned: got %d, want 1", pruned)
	}
}

func TestDeadLetterPruneKeepsRecent(t *testing.T) {
	db := testDB(t)

	nextRetry := time.Now().Add(-time.Minute)
	db.InsertDeadLetter(1, []byte{0x01}, 3, nextRetry, "err", "")
	pending, _ := db.GetPendingDeadLetters(10)
	db.MarkDeadLetterSent(pending[0].ID)

	// Don't backdate — should NOT be pruned
	pruned, _ := db.PruneDeadLetters(7)
	if pruned != 0 {
		t.Errorf("pruned recent: got %d, want 0", pruned)
	}
}

func TestSchemaVersionIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Open twice — should not fail
	db1, err := New(dbPath)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	db1.Close()

	db2, err := New(dbPath)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer db2.Close()

	var version int
	db2.QueryRow("SELECT version FROM schema_version").Scan(&version)
	if version != len(migrations) {
		t.Errorf("version after reopen: got %d, want %d", version, len(migrations))
	}
}

// Ensure we don't accidentally import os in production code
var _ = os.DevNull

// --- MESHSAT-543 (S1-10) — precedence field round-trip -----------------

func TestDeliveryPrecedenceRoundTrip(t *testing.T) {
	db := testDB(t)
	for _, want := range []string{"Override", "Flash", "Immediate", "Priority", "Routine", "Deferred"} {
		id, err := db.InsertDelivery(MessageDelivery{
			MsgRef:     "msg-" + want,
			Channel:    "mesh_0",
			Status:     "queued",
			Precedence: want,
		})
		if err != nil {
			t.Fatalf("insert %s: %v", want, err)
		}
		got, err := db.GetDelivery(id)
		if err != nil {
			t.Fatalf("get %s: %v", want, err)
		}
		if got.Precedence != want {
			t.Errorf("precedence %q round-trip: got %q", want, got.Precedence)
		}
	}
}

func TestDeliveryPrecedenceDefaultsToRoutine(t *testing.T) {
	db := testDB(t)
	// Leaving Precedence empty on insert must produce 'Routine' via the schema default.
	id, err := db.InsertDelivery(MessageDelivery{
		MsgRef:  "msg-default",
		Channel: "mesh_0",
		Status:  "queued",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, _ := db.GetDelivery(id)
	if got.Precedence != "Routine" {
		t.Errorf("default precedence: got %q, want 'Routine'", got.Precedence)
	}
}

func TestDeliveryPrecedenceIndexExists(t *testing.T) {
	db := testDB(t)
	var name string
	err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_deliveries_precedence'`,
	).Scan(&name)
	if err != nil {
		t.Errorf("idx_deliveries_precedence: %v", err)
	}
}
