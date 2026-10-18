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
	if err := db.FinishHubCommand("req-1", `{"status":"ok"}`); err != nil {
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
