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

// MESHSAT-1744: how the last writer wrote is kept with the thread, and a
// writer over MQTT resets it.
func TestTAKChatThreadRemembersHowTheWriterWrote(t *testing.T) {
	db := testDB(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	const node = 0xa1b3c2ec
	if err := db.OpenTAKChatThreadVia(node, "meshsat-hub-op-~3f24d8f92959c061", "Anna", "sms", "01020304-0506-0708-090a-0b0c0d0e0f10", true, now); err != nil {
		t.Fatal(err)
	}
	uid, callsign, bearer, id, replyHere, ok, err := db.TAKChatThreadVia(node, now.Add(-time.Hour))
	if err != nil || !ok || uid != "meshsat-hub-op-~3f24d8f92959c061" || callsign != "Anna" || bearer != "sms" ||
		id != "01020304-0506-0708-090a-0b0c0d0e0f10" || !replyHere {
		t.Fatalf("thread = %q %q %q %q %v %v %v", uid, callsign, bearer, id, replyHere, ok, err)
	}
	// The plain read still works, and an older since hides it.
	if uid, _, ok, _ := db.TAKChatThread(node, now.Add(-time.Hour)); !ok || uid != "meshsat-hub-op-~3f24d8f92959c061" {
		t.Errorf("plain read %q %v", uid, ok)
	}
	if _, _, _, _, _, ok, _ := db.TAKChatThreadVia(node, now.Add(time.Minute)); ok {
		t.Error("a thread older than since was returned")
	}
	// A later writer over MQTT: the answer goes back over MQTT again.
	if err := db.OpenTAKChatThread(node, "ANDROID-1", "DISPATCH-1", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	uid, _, bearer, id, replyHere, ok, _ = db.TAKChatThreadVia(node, now.Add(-time.Hour))
	if !ok || uid != "ANDROID-1" || bearer != "" || id != "" || replyHere {
		t.Errorf("after an MQTT writer: %q %q %q %v", uid, bearer, id, replyHere)
	}
	if _, _, _, _, _, ok, _ := db.TAKChatThreadVia(node+1, now.Add(-time.Hour)); ok {
		t.Error("a thread leaked to another node")
	}
}
