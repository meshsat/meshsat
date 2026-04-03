package hubreporter

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const createOutboxTable = `
CREATE TABLE IF NOT EXISTS hub_outbox (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	topic       TEXT NOT NULL,
	payload     TEXT NOT NULL,
	qos         INTEGER NOT NULL DEFAULT 1,
	retry_count INTEGER NOT NULL DEFAULT 0,
	created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_hub_outbox_created ON hub_outbox(created_at);
`

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(createOutboxTable); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestEnqueueAndPending(t *testing.T) {
	db := newTestDB(t)
	ob := NewOutbox(db, 10000, 7*24*time.Hour)

	n, err := ob.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 pending, got %d", n)
	}

	if err := ob.Enqueue("test/topic1", []byte(`{"msg":"hello"}`), 1); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := ob.Enqueue("test/topic2", []byte(`{"msg":"world"}`), 0); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	n, err = ob.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 pending, got %d", n)
	}
}

func TestReplayFIFOOrder(t *testing.T) {
	db := newTestDB(t)
	ob := NewOutbox(db, 10000, 7*24*time.Hour)

	messages := []string{"first", "second", "third"}
	for _, m := range messages {
		if err := ob.Enqueue("test/order", []byte(m), 1); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	var received []string
	n, err := ob.Replay(context.Background(), func(topic string, payload []byte, qos byte) error {
		received = append(received, string(payload))
		return nil
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if n != 3 {
		t.Fatalf("expected 3 replayed, got %d", n)
	}
	for i, want := range messages {
		if received[i] != want {
			t.Errorf("message %d: got %q, want %q", i, received[i], want)
		}
	}

	// All messages should be deleted after successful replay
	pending, _ := ob.Pending()
	if pending != 0 {
		t.Errorf("expected 0 pending after replay, got %d", pending)
	}
}

func TestReplayDeletesSuccessful(t *testing.T) {
	db := newTestDB(t)
	ob := NewOutbox(db, 10000, 7*24*time.Hour)

	if err := ob.Enqueue("test/a", []byte("msg-a"), 1); err != nil {
		t.Fatal(err)
	}
	if err := ob.Enqueue("test/b", []byte("msg-b"), 1); err != nil {
		t.Fatal(err)
	}

	// Fail on "msg-a", succeed on "msg-b"
	n, err := ob.Replay(context.Background(), func(topic string, payload []byte, qos byte) error {
		if string(payload) == "msg-a" {
			return fmt.Errorf("simulated failure")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 replayed, got %d", n)
	}

	// msg-a should still be pending (with retry_count=1), msg-b deleted
	pending, _ := ob.Pending()
	if pending != 1 {
		t.Errorf("expected 1 pending after partial replay, got %d", pending)
	}

	var retryCount int
	db.QueryRow(`SELECT retry_count FROM hub_outbox WHERE payload = 'msg-a'`).Scan(&retryCount)
	if retryCount != 1 {
		t.Errorf("expected retry_count=1, got %d", retryCount)
	}
}

func TestReplaySkipsAfterMaxRetries(t *testing.T) {
	db := newTestDB(t)
	ob := NewOutbox(db, 10000, 7*24*time.Hour)

	if err := ob.Enqueue("test/doomed", []byte("doomed"), 1); err != nil {
		t.Fatal(err)
	}

	// Set retry_count to maxRetries (5)
	if _, err := db.Exec(`UPDATE hub_outbox SET retry_count = 5`); err != nil {
		t.Fatal(err)
	}

	publishCalled := false
	n, err := ob.Replay(context.Background(), func(topic string, payload []byte, qos byte) error {
		publishCalled = true
		return nil
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 replayed, got %d", n)
	}
	if publishCalled {
		t.Error("publishFn should not have been called for expired message")
	}

	// Message should have been deleted
	pending, _ := ob.Pending()
	if pending != 0 {
		t.Errorf("expected 0 pending after skip, got %d", pending)
	}
}

func TestCleanupByAge(t *testing.T) {
	db := newTestDB(t)
	ob := NewOutbox(db, 10000, 1*time.Hour) // 1 hour max age

	// Insert an "old" message with a timestamp 2 hours ago.
	// Use UTC and subtract 1 extra second to avoid precision edge cases.
	oldTime := time.Now().UTC().Add(-2 * time.Hour).Format("2006-01-02 15:04:05")
	if _, err := db.Exec(`INSERT INTO hub_outbox (topic, payload, qos, created_at) VALUES (?, ?, ?, ?)`,
		"test/old", "old-msg", 1, oldTime); err != nil {
		t.Fatal(err)
	}
	// Insert a "new" message
	if err := ob.Enqueue("test/new", []byte("new-msg"), 1); err != nil {
		t.Fatal(err)
	}

	if err := ob.Cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	pending, _ := ob.Pending()
	if pending != 1 {
		t.Errorf("expected 1 pending after age cleanup, got %d", pending)
	}

	// Verify the remaining message is the new one
	var payload string
	db.QueryRow(`SELECT payload FROM hub_outbox`).Scan(&payload)
	if payload != "new-msg" {
		t.Errorf("expected new-msg to survive, got %q", payload)
	}
}

func TestCleanupBySize(t *testing.T) {
	db := newTestDB(t)
	ob := NewOutbox(db, 3, 7*24*time.Hour) // max 3 messages

	for i := 0; i < 5; i++ {
		// Use distinct timestamps to ensure ordering
		ts := time.Now().Add(time.Duration(i) * time.Second).Format("2006-01-02 15:04:05")
		if _, err := db.Exec(`INSERT INTO hub_outbox (topic, payload, qos, created_at) VALUES (?, ?, ?, ?)`,
			"test/size", fmt.Sprintf("msg-%d", i), 1, ts); err != nil {
			t.Fatal(err)
		}
	}

	if err := ob.Cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	pending, _ := ob.Pending()
	if pending != 3 {
		t.Errorf("expected 3 pending after size cleanup, got %d", pending)
	}

	// Verify the 3 newest messages survived (msg-2, msg-3, msg-4)
	rows, err := db.Query(`SELECT payload FROM hub_outbox ORDER BY created_at ASC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var payloads []string
	for rows.Next() {
		var p string
		rows.Scan(&p)
		payloads = append(payloads, p)
	}
	expected := []string{"msg-2", "msg-3", "msg-4"}
	for i, want := range expected {
		if i >= len(payloads) || payloads[i] != want {
			t.Errorf("position %d: got %q, want %q", i, payloads[i], want)
		}
	}
}

func TestStatsReturnsCorrectInfo(t *testing.T) {
	db := newTestDB(t)
	ob := NewOutbox(db, 10000, 7*24*time.Hour)

	// Empty stats
	stats, err := ob.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Pending != 0 {
		t.Errorf("expected 0 pending, got %d", stats.Pending)
	}
	if stats.Oldest != nil {
		t.Errorf("expected nil oldest, got %v", stats.Oldest)
	}
	if stats.Replayed != 0 {
		t.Errorf("expected 0 replayed, got %d", stats.Replayed)
	}

	// Add messages and replay some
	if err := ob.Enqueue("test/a", []byte("a"), 1); err != nil {
		t.Fatal(err)
	}
	if err := ob.Enqueue("test/b", []byte("b"), 1); err != nil {
		t.Fatal(err)
	}

	ob.Replay(context.Background(), func(topic string, payload []byte, qos byte) error {
		if string(payload) == "a" {
			return nil // success
		}
		return fmt.Errorf("fail")
	})

	stats, err = ob.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Pending != 1 {
		t.Errorf("expected 1 pending, got %d", stats.Pending)
	}
	if stats.Replayed != 1 {
		t.Errorf("expected 1 replayed, got %d", stats.Replayed)
	}
	if stats.Oldest == nil {
		t.Error("expected non-nil oldest timestamp")
	}
}

func TestOldestTimestampNilWhenEmpty(t *testing.T) {
	db := newTestDB(t)
	ob := NewOutbox(db, 10000, 7*24*time.Hour)

	ts, err := ob.OldestTimestamp()
	if err != nil {
		t.Fatalf("oldest: %v", err)
	}
	if ts != nil {
		t.Errorf("expected nil timestamp for empty outbox, got %v", ts)
	}
}

func TestPublishOrQueueWhenDisconnectedWithOutbox(t *testing.T) {
	db := newTestDB(t)
	ob := NewOutbox(db, 10000, 7*24*time.Hour)

	r := NewHubReporter(
		ReporterConfig{HubURL: "tcp://hub:1883", BridgeID: "test"},
		func() BridgeBirth { return BridgeBirth{} },
		func() BridgeHealth { return BridgeHealth{} },
	)
	r.SetOutbox(ob)

	// Reporter is not connected — publish should queue to outbox
	if err := r.PublishDevicePosition("!aabb", DevicePosition{Lat: 52.0, Lon: 4.5}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := r.PublishDeviceSOS(DeviceSOS{DeviceID: "!aabb", Type: "triggered"}); err != nil {
		t.Fatalf("publish SOS: %v", err)
	}

	n, _ := ob.Pending()
	if n != 2 {
		t.Errorf("expected 2 queued messages, got %d", n)
	}
}

func TestPublishOrQueueWithoutOutboxDropsSilently(t *testing.T) {
	r := NewHubReporter(
		ReporterConfig{HubURL: "tcp://hub:1883", BridgeID: "test"},
		func() BridgeBirth { return BridgeBirth{} },
		func() BridgeHealth { return BridgeHealth{} },
	)
	// No outbox set, not connected — should return nil (legacy drop behavior)
	err := r.PublishDevicePosition("!aabb", DevicePosition{Lat: 52.0, Lon: 4.5})
	if err != nil {
		t.Errorf("expected nil error for silent drop, got: %v", err)
	}
}

func TestNewOutboxDefaults(t *testing.T) {
	db := newTestDB(t)

	// Zero values should get defaults
	ob := NewOutbox(db, 0, 0)
	if ob.maxSize != defaultMaxSize {
		t.Errorf("maxSize: got %d, want %d", ob.maxSize, defaultMaxSize)
	}
	if ob.maxAge != defaultMaxAge {
		t.Errorf("maxAge: got %v, want %v", ob.maxAge, defaultMaxAge)
	}
}
