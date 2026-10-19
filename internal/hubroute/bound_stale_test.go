package hubroute

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/gateway"
	"meshsat/internal/hubmsg"
)

// The bound reads the evidence, not the marks (r11 review HM-R7): an event kept by now is not
// missing, whether its strike could be written or not, after a reopen too, and whether the
// obligation was in the database or only in memory. What is really missing (a named event
// not kept, an unnamed one, no outcome at all) is still given up, once. [MESHSAT-1584]

// unknownSink counts the hub_outcome_unknown receipts per operation.
func unknownSink(r *Route) map[string]int {
	got := map[string]int{}
	r.SetIntentEvidenceSink(func(rc gateway.IntentReceipt) error {
		if rc.Event == "hub_outcome_unknown" {
			got[rc.CorrValue]++
		}
		return nil
	})
	return got
}

func expiredEvent(op *database.HubOp, at time.Time) []byte {
	raw, _ := json.Marshal(hubmsg.Event{V: 1, OpID: op.OpID, Mid: op.Mid, Recipient: op.Recipient, EventID: hubmsg.NewV7(at),
		Class: "expired", Kind: "hub_expired", Authority: "hub", Reason: "expired", At: at.UTC().Format(time.RFC3339)})
	return raw
}

func trigger(t *testing.T, db *database.DB, name, ddl string) func() {
	t.Helper()
	if _, err := db.Exec(`CREATE TRIGGER ` + name + ` ` + ddl + ` BEGIN SELECT RAISE(ABORT, 'test'); END`); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := db.Exec(`DROP TRIGGER ` + name); err != nil {
			t.Fatal(err)
		}
	}
}

// keptButNotStruck leaves an operation whose expiry is kept while its missing row, naming
// that expiry, stays: the insert failed once, the copy was kept, the strike failed.
func keptButNotStruck(t *testing.T, db *database.DB, r *Route, op *database.HubOp, now time.Time) (healStrike func()) {
	t.Helper()
	raw := expiredEvent(op, now)
	heal := trigger(t, db, "fail_insert", `BEFORE INSERT ON hub_op_events`)
	r.HandleStatus(raw)
	heal()
	healStrike = trigger(t, db, "fail_strike", `BEFORE DELETE ON hub_op_missing`)
	r.HandleStatus(raw)
	if m, _ := db.HubOpMissing(op.OpID); len(m) != 1 {
		t.Fatalf("missing rows %v", m)
	}
	return healStrike
}

// The strike still fails at the bound: nothing is given up, the owed listing does not list
// it, and once the strike can be written the row goes.
func TestTheBoundDoesNotGiveUpAKeptEventWhoseStrikeStillFails(t *testing.T) {
	db, now := newDB(t), time.Now()
	op, _ := db.GetHubOp(putOps(t, db, now, 1)[0])
	clock := now
	r := New(db, &walkSession{asked: map[string]int{}}, Config{Now: func() time.Time { return clock }})
	unknown := unknownSink(r)
	healStrike := keptButNotStruck(t, db, r, op, now)
	if owed, _ := db.HubOpsOwedReplay(now, ReplayWithin, database.HubOpCursor{}, 10); len(owed) != 0 {
		t.Fatalf("a kept event listed as owed: %+v", owed)
	}
	clock = time.UnixMilli(op.ExpiresAt).Add(ReplayWithin + time.Second)
	r.RequestReplays()
	r.RequestReplays()
	saved, _ := db.GetHubOp(op.OpID)
	if unknown[op.OpID] != 0 || saved.ReplayGivenUpAt != 0 {
		t.Fatalf("a kept event given up while its strike fails: %d unknown, given up %d", unknown[op.OpID], saved.ReplayGivenUpAt)
	}
	healStrike()
	r.RequestReplays()
	saved, _ = db.GetHubOp(op.OpID)
	if m, _ := db.HubOpMissing(op.OpID); len(m) != 0 || saved.ReplayAt != 0 || saved.ReplayGivenUpAt != 0 || unknown[op.OpID] != 0 {
		t.Fatalf("after the strike: missing %v, owed %d, given up %d, %d unknown", m, saved.ReplayAt, saved.ReplayGivenUpAt, unknown[op.OpID])
	}
}

// The stale row survives a reopen of the database, and its strike still fails there: the new
// process does not give it up.
func TestTheBoundDoesNotGiveUpAKeptEventAfterAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kit.db")
	db, err := database.New(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	op, _ := db.GetHubOp(putOps(t, db, now, 1)[0])
	r := New(db, &walkSession{asked: map[string]int{}}, Config{})
	unknownSink(r)
	keptButNotStruck(t, db, r, op, now)
	if err := db.Close(); err != nil { // the strike's trigger is in the file: it still fails after the reopen
		t.Fatal(err)
	}
	db, err = database.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	late := time.UnixMilli(op.ExpiresAt).Add(ReplayWithin + time.Second)
	again := New(db, &walkSession{asked: map[string]int{}}, Config{Now: func() time.Time { return late }})
	unknown := unknownSink(again)
	again.RequestReplays()
	saved, _ := db.GetHubOp(op.OpID)
	if unknown[op.OpID] != 0 || saved.ReplayGivenUpAt != 0 {
		t.Fatalf("after a reopen: %d unknown, given up %d", unknown[op.OpID], saved.ReplayGivenUpAt)
	}
}

// An obligation held only in memory, whose event another route instance kept since: at the
// bound it is struck, not given up.
func TestTheBoundDoesNotGiveUpAMemoryObligationKeptElsewhere(t *testing.T) {
	db, now := newDB(t), time.Now()
	op, _ := db.GetHubOp(putOps(t, db, now, 1)[0])
	clock := now
	r1 := New(db, &walkSession{asked: map[string]int{}}, Config{Now: func() time.Time { return clock }})
	unknown := unknownSink(r1)
	raw := expiredEvent(op, now)
	healInsert := trigger(t, db, "fail_insert", `BEFORE INSERT ON hub_op_events`)
	healMark := trigger(t, db, "fail_mark", `BEFORE UPDATE OF replay_at ON hub_ops`)
	r1.HandleStatus(raw) // neither the event nor its mark: r1 holds it in memory
	healInsert()
	healMark()
	if m, _ := db.HubOpMissing(op.OpID); len(m) != 0 {
		t.Fatalf("the mark was written: %v", m)
	}
	r2 := New(db, &walkSession{asked: map[string]int{}}, Config{})
	unknownSink(r2)
	r2.HandleStatus(raw) // kept by another instance
	clock = time.UnixMilli(op.ExpiresAt).Add(ReplayWithin + time.Second)
	r1.RequestReplays()
	saved, _ := db.GetHubOp(op.OpID)
	if unknown[op.OpID] != 0 || saved.ReplayGivenUpAt != 0 {
		t.Fatalf("a memory obligation kept elsewhere given up: %d unknown, given up %d", unknown[op.OpID], saved.ReplayGivenUpAt)
	}
}

// What is really missing is still given up, once each: a named event not kept, an unnamed one
// (carried over from before v74) with a weaker outcome kept, no outcome at all, and a named
// event held only in memory. A kept event with a stale row beside them is not.
func TestTheBoundStillGivesUpWhatIsReallyMissing(t *testing.T) {
	db, now := newDB(t), time.Now()
	ids := putOps(t, db, now, 5)
	named, unnamed, nothing, stale, inMemory := ids[0], ids[1], ids[2], ids[3], ids[4]
	clock := now
	r := New(db, &walkSession{asked: map[string]int{}}, Config{Now: func() time.Time { return clock }})
	unknown := unknownSink(r)
	if err := db.WantHubOpReplay(named, "lost-event", now); err != nil {
		t.Fatal(err)
	}
	if err := db.WantHubOpReplay(unnamed, database.MissingAnyEvent, now); err != nil {
		t.Fatal(err)
	}
	opU, _ := db.GetHubOp(unnamed)
	r.HandleStatus(expiredEvent(opU, now)) // a weaker outcome kept beside the unnamed obligation
	opS, _ := db.GetHubOp(stale)
	healStrike := keptButNotStruck(t, db, r, opS, now)
	defer healStrike()
	opM, _ := db.GetHubOp(inMemory)
	healMark := trigger(t, db, "fail_mark", `BEFORE UPDATE OF replay_at ON hub_ops`)
	healInsert := trigger(t, db, "fail_insert", `BEFORE INSERT ON hub_op_events`)
	r.HandleStatus(expiredEvent(opM, now))
	healInsert()
	healMark()
	clock = time.UnixMilli(opU.ExpiresAt).Add(ReplayWithin + time.Second)
	r.RequestReplays()
	r.RequestReplays()
	for id, want := range map[string]int{named: 1, unnamed: 1, nothing: 1, inMemory: 1, stale: 0} {
		saved, _ := db.GetHubOp(id)
		if unknown[id] != want || (saved.ReplayGivenUpAt != 0) != (want == 1) {
			t.Fatalf("%s: %d unknown (want %d), given up %d", id, unknown[id], want, saved.ReplayGivenUpAt)
		}
	}
	for _, id := range []string{named, unnamed} {
		if saved, _ := db.GetHubOp(id); saved.ReplayAt == 0 {
			t.Fatalf("%s: given up cleared its owed mark", id)
		}
	}
}
