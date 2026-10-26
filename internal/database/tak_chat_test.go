package database

import (
	"testing"
	"time"
)

func TestTAKChatThreadIsTheLatestWriterForADay(t *testing.T) {
	db := testDB(t)
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	const node = 0xa1b3c3a4

	if _, _, ok, err := db.TAKChatThread(node, now.Add(-24*time.Hour)); err != nil || ok {
		t.Fatalf("a node nobody wrote to has a thread: ok=%v err=%v", ok, err)
	}
	if err := db.OpenTAKChatThread(node, "ANDROID-1", "DISPATCH-1", now); err != nil {
		t.Fatal(err)
	}
	if err := db.OpenTAKChatThread(node, "meshsat-hub-op-7", "Office", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	uid, callsign, ok, err := db.TAKChatThread(node, now.Add(-24*time.Hour))
	if err != nil || !ok || uid != "meshsat-hub-op-7" || callsign != "Office" {
		t.Fatalf("thread = %q %q %v %v, want the latest writer", uid, callsign, ok, err)
	}
	// Another node's thread is its own.
	if _, _, ok, _ := db.TAKChatThread(node+1, now.Add(-24*time.Hour)); ok {
		t.Fatal("a thread leaked to another node")
	}
	// Older than since: no longer an answer's way back.
	if _, _, ok, _ := db.TAKChatThread(node, now.Add(2*time.Minute)); ok {
		t.Fatal("a thread older than since was returned")
	}
}

func TestTAKChatThreadsArePrunedAfterAWeek(t *testing.T) {
	db := testDB(t)
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if err := db.OpenTAKChatThread(1, "u1", "C1", start); err != nil {
		t.Fatal(err)
	}
	if err := db.OpenTAKChatThread(2, "u2", "C2", start.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.Get(&n, `SELECT COUNT(*) FROM tak_chat_threads`); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows = %d, want the week-old thread pruned", n)
	}
}
