package database

import (
	"testing"
	"time"
)

// A Hub command's request id is claimed once a day: a repeat while it runs
// in this process gets nothing, a repeat after it finished gets the stored
// reply, a claim an earlier process never finished is run again by the next
// one, other arguments under the same id are a conflict, and a day later the
// id is new again. [MESHSAT-1584]
func TestHubCommandLogClaimsOnceAndKeepsTheReply(t *testing.T) {
	db := testDB(t)
	t0 := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	claim := func(id, args, run string, at time.Time) HubCommandClaim {
		t.Helper()
		c, err := db.ClaimHubCommand(id, "send_text", args, run, at)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if c := claim("req-1", "a1", "run-A", t0); !c.Fresh || c.Response != "" || c.Conflict {
		t.Fatalf("first claim: %+v", c)
	}
	if c := claim("req-1", "a1", "run-A", t0.Add(time.Second)); c.Fresh || c.Response != "" || c.Conflict {
		t.Fatalf("while it runs in this process: %+v", c)
	}
	// The process died before it finished; the next one runs it.
	if c := claim("req-1", "a1", "run-B", t0.Add(time.Minute)); !c.Fresh || c.Conflict {
		t.Fatalf("after a restart: %+v", c)
	}
	if err := db.FinishHubCommand("req-1", "run-A", "a1", `{"status":"stale run"}`); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishHubCommand("req-1", "run-B", "a1", `{"status":"ok"}`); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishHubCommand("req-1", "run-B", "a1", `{"status":"overwritten"}`); err != nil {
		t.Fatal(err)
	}
	if c := claim("req-1", "a1", "run-C", t0.Add(time.Hour)); c.Fresh || c.Response != `{"status":"ok"}` {
		t.Fatalf("after it finished: %+v", c)
	}
	if c := claim("req-1", "a2", "run-C", t0.Add(time.Hour)); !c.Conflict || c.Fresh {
		t.Fatalf("other arguments under the same id: %+v", c)
	}
	if c := claim("req-2", "a1", "run-C", t0.Add(time.Hour)); !c.Fresh {
		t.Fatalf("another id: %+v", c)
	}
	if c := claim("req-1", "a2", "run-C", t0.Add(HubCommandWindow+time.Hour)); !c.Fresh || c.Conflict {
		t.Fatalf("a day later: %+v", c)
	}
}

// A reply is stored only under the claim it belongs to: the same id and run
// with other arguments finish nothing (review 2026-10-18 19:34, FA-R3).
func TestHubCommandReplyNeedsTheClaimsArguments(t *testing.T) {
	db := testDB(t)
	t0 := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	if c, err := db.ClaimHubCommand("req-1", "send_text", "a1", "run-A", t0); err != nil || !c.Fresh {
		t.Fatalf("claim: %+v %v", c, err)
	}
	if err := db.FinishHubCommand("req-1", "run-A", "a2", `{"status":"error"}`); err != nil {
		t.Fatal(err)
	}
	if c, _ := db.ClaimHubCommand("req-1", "send_text", "a1", "run-A", t0.Add(time.Second)); c.Response != "" {
		t.Fatalf("a reply for other arguments was stored: %q", c.Response)
	}
	if err := db.FinishHubCommand("req-1", "run-A", "a1", `{"status":"ok"}`); err != nil {
		t.Fatal(err)
	}
	if c, _ := db.ClaimHubCommand("req-1", "send_text", "a1", "run-B", t0.Add(time.Minute)); c.Response != `{"status":"ok"}` {
		t.Fatalf("the claim's own reply: %q", c.Response)
	}
}
