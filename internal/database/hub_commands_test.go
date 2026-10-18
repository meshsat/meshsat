package database

import (
	"testing"
	"time"
)

// A Hub command's request id is claimed once a day: a repeat while it runs
// gets nothing, a repeat after it finished gets the stored reply, and a day
// later the id is new again. [MESHSAT-1584]
func TestHubCommandLogClaimsOnceAndKeepsTheReply(t *testing.T) {
	db := testDB(t)
	t0 := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	if fresh, prev, err := db.ClaimHubCommand("req-1", "send_text", t0); err != nil || !fresh || prev != "" {
		t.Fatalf("first claim: %t %q %v", fresh, prev, err)
	}
	if fresh, prev, err := db.ClaimHubCommand("req-1", "send_text", t0.Add(time.Second)); err != nil || fresh || prev != "" {
		t.Fatalf("while it runs: %t %q %v", fresh, prev, err)
	}
	if err := db.FinishHubCommand("req-1", `{"status":"ok"}`); err != nil {
		t.Fatal(err)
	}
	if fresh, prev, err := db.ClaimHubCommand("req-1", "send_text", t0.Add(time.Hour)); err != nil || fresh || prev != `{"status":"ok"}` {
		t.Fatalf("after it finished: %t %q %v", fresh, prev, err)
	}
	if fresh, _, err := db.ClaimHubCommand("req-2", "ping", t0.Add(time.Hour)); err != nil || !fresh {
		t.Fatalf("another id: %t %v", fresh, err)
	}
	if fresh, _, err := db.ClaimHubCommand("req-1", "send_text", t0.Add(HubCommandWindow+time.Minute)); err != nil || !fresh {
		t.Fatalf("a day later: %t %v", fresh, err)
	}
}
