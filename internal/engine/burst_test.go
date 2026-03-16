package engine

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestBurstQueue_EnqueueAndFlush(t *testing.T) {
	db := testDB(t)
	bq := NewBurstQueue(db, 10, 5*time.Minute)

	if err := bq.Enqueue(BurstMessage{Payload: []byte("hello"), Priority: 1}); err != nil {
		t.Fatal(err)
	}
	if err := bq.Enqueue(BurstMessage{Payload: []byte("world"), Priority: 2}); err != nil {
		t.Fatal(err)
	}

	if bq.Pending() != 2 {
		t.Fatalf("expected 2 pending, got %d", bq.Pending())
	}

	data, count, err := bq.Flush(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2 packed, got %d", count)
	}
	if bq.Pending() != 0 {
		t.Fatalf("expected 0 pending after flush, got %d", bq.Pending())
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty payload")
	}
}

func TestBurstQueue_TLVRoundTrip(t *testing.T) {
	db := testDB(t)
	bq := NewBurstQueue(db, 10, 5*time.Minute)

	messages := [][]byte{
		[]byte("alpha"),
		[]byte("bravo"),
		[]byte("charlie"),
	}
	for _, m := range messages {
		if err := bq.Enqueue(BurstMessage{Payload: m, Priority: 1}); err != nil {
			t.Fatal(err)
		}
	}

	data, count, err := bq.Flush(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("expected 3 packed, got %d", count)
	}

	// Unpack and verify round-trip
	unpacked, err := UnpackBurst(data)
	if err != nil {
		t.Fatalf("UnpackBurst: %v", err)
	}
	if len(unpacked) != 3 {
		t.Fatalf("expected 3 unpacked messages, got %d", len(unpacked))
	}
	for i, m := range messages {
		if !bytes.Equal(unpacked[i], m) {
			t.Errorf("message %d: expected %q, got %q", i, m, unpacked[i])
		}
	}
}

func TestBurstQueue_SplitAtMTU(t *testing.T) {
	db := testDB(t)
	bq := NewBurstQueue(db, 100, 5*time.Minute)

	// Create messages that collectively exceed IridiumMTU (340 bytes)
	// Header = 3 bytes, each message = 2 + 100 = 102 bytes
	// 3 + 102*3 = 309 (fits), 3 + 102*4 = 411 (doesn't fit)
	bigPayload := make([]byte, 100)
	for i := range bigPayload {
		bigPayload[i] = byte(i)
	}

	for i := 0; i < 5; i++ {
		if err := bq.Enqueue(BurstMessage{Payload: bigPayload, Priority: i}); err != nil {
			t.Fatal(err)
		}
	}

	data, count, err := bq.Flush(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Should have packed only 3 messages (309 bytes fits within 340)
	if count != 3 {
		t.Fatalf("expected 3 packed within MTU, got %d", count)
	}
	if len(data) > IridiumMTU {
		t.Fatalf("burst payload %d bytes exceeds MTU %d", len(data), IridiumMTU)
	}

	// Remaining should be in queue
	if bq.Pending() != 2 {
		t.Fatalf("expected 2 remaining, got %d", bq.Pending())
	}

	// Verify unpacking
	unpacked, err := UnpackBurst(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(unpacked) != 3 {
		t.Fatalf("expected 3 unpacked, got %d", len(unpacked))
	}
}

func TestBurstQueue_ShouldFlush_MaxSize(t *testing.T) {
	db := testDB(t)
	bq := NewBurstQueue(db, 2, 1*time.Hour)

	if bq.ShouldFlush() {
		t.Fatal("should not flush when empty")
	}

	_ = bq.Enqueue(BurstMessage{Payload: []byte("a"), Priority: 1})
	if bq.ShouldFlush() {
		t.Fatal("should not flush with 1 of 2")
	}

	_ = bq.Enqueue(BurstMessage{Payload: []byte("b"), Priority: 1})
	if !bq.ShouldFlush() {
		t.Fatal("should flush at maxSize")
	}
}

func TestBurstQueue_ShouldFlush_MaxAge(t *testing.T) {
	db := testDB(t)
	bq := NewBurstQueue(db, 100, 1*time.Millisecond)

	_ = bq.Enqueue(BurstMessage{
		Payload:  []byte("old"),
		Priority: 1,
		QueuedAt: time.Now().Add(-1 * time.Second),
	})

	if !bq.ShouldFlush() {
		t.Fatal("should flush when maxAge exceeded")
	}
}

func TestBurstQueue_EmptyPayloadRejected(t *testing.T) {
	db := testDB(t)
	bq := NewBurstQueue(db, 10, 5*time.Minute)

	if err := bq.Enqueue(BurstMessage{Payload: nil, Priority: 1}); err == nil {
		t.Fatal("expected error for empty payload")
	}
}

func TestUnpackBurst_InvalidData(t *testing.T) {
	// Too short
	if _, err := UnpackBurst([]byte{0x42}); err == nil {
		t.Fatal("expected error for short data")
	}

	// Wrong type byte
	if _, err := UnpackBurst([]byte{0x00, 0x00, 0x00}); err == nil {
		t.Fatal("expected error for wrong type byte")
	}
}

func TestBurstQueue_PriorityOrdering(t *testing.T) {
	db := testDB(t)
	bq := NewBurstQueue(db, 10, 5*time.Minute)

	_ = bq.Enqueue(BurstMessage{Payload: []byte("low"), Priority: 1})
	_ = bq.Enqueue(BurstMessage{Payload: []byte("high"), Priority: 10})
	_ = bq.Enqueue(BurstMessage{Payload: []byte("mid"), Priority: 5})

	data, _, err := bq.Flush(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	unpacked, err := UnpackBurst(data)
	if err != nil {
		t.Fatal(err)
	}

	// Highest priority first
	if string(unpacked[0]) != "high" {
		t.Errorf("expected first message to be 'high', got %q", unpacked[0])
	}
	if string(unpacked[1]) != "mid" {
		t.Errorf("expected second message to be 'mid', got %q", unpacked[1])
	}
	if string(unpacked[2]) != "low" {
		t.Errorf("expected third message to be 'low', got %q", unpacked[2])
	}
}
