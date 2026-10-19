package database

import (
	"path/filepath"
	"testing"
	"time"

	"meshsat/internal/hubmsg"
)

// What hub_msg keeps survives the database closing between any two writes (r8 review HM-R1,
// HM-R2): an event kept but not applied is still pending after a reopen, the ledger keeps an
// answer once by its key across a reopen, an owed repeat is still owed, and the work a
// hand-off made is still listed until the hand-off is marked done. [MESHSAT-1584]

func reopen(t *testing.T, path string, db *DB) *DB {
	t.Helper()
	if db != nil {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	db, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestHubDurability_PendingEventsAndTheLedgerKeySurviveAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kit.db")
	db := reopen(t, path, nil)
	now := time.Now()
	op := HubOp{OpID: hubmsg.NewV7(now), Mid: hubmsg.NewV7(now), Recipient: "bridge:kit-b", IntentID: "i-1",
		ExpiresAt: now.Add(2 * time.Minute).UnixMilli(), CreatedAt: now.UnixMilli(), Submission: `{"v":1}`}
	if err := db.PutHubOp(op); err != nil {
		t.Fatal(err)
	}
	ev := hubmsg.Event{OpID: op.OpID, EventID: hubmsg.NewV7(now), Rev: 2, Class: "completed", Kind: "recipient_app_stored",
		Authority: "recipient", At: now.UTC().Format(time.RFC3339)}
	rejected := ev
	rejected.EventID, rejected.Kind = hubmsg.NewV7(now), "hub_forwarded"
	if _, err := db.AddHubOpEvent(ev, true, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddHubOpEvent(rejected, false, now); err != nil {
		t.Fatal(err)
	}

	db = reopen(t, path, db)
	pend, err := db.PendingHubOpEvents(10)
	if err != nil || len(pend) != 1 || pend[0].Event.EventID != ev.EventID || !pend[0].Valid || pend[0].Applied {
		t.Fatalf("pending after a reopen: %+v %v", pend, err)
	}
	if got, err := db.GetHubOp(op.OpID); err != nil || got == nil || got.Submission != op.Submission {
		t.Fatalf("the submission was not kept: %+v %v", got, err)
	}

	key := "hub_event:" + ev.EventID
	first, err := db.RecordEvidence(DeliveryEvidence{Source: "recipient_app", Event: "recipient_app_stored", CorrKind: "hub_op",
		CorrValue: op.OpID, CorrScope: "hub_0", DedupKey: key}, 0, nil)
	if err != nil || first.Repeat {
		t.Fatalf("first: %+v %v", first, err)
	}
	db = reopen(t, path, db)
	again, err := db.RecordEvidence(DeliveryEvidence{Source: "recipient_app", Event: "recipient_app_stored", CorrKind: "hub_op",
		CorrValue: op.OpID, CorrScope: "hub_0", DedupKey: key}, 0, nil)
	if err != nil || !again.Repeat || again.ID != first.ID {
		t.Fatalf("again after a reopen: %+v %v (first %d)", again, err, first.ID)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM delivery_evidence WHERE corr_value = ?`, op.OpID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("ledger rows: %d %v", n, err)
	}
	// Evidence with no key is not deduplicated: two answers are two rows.
	for i := 0; i < 2; i++ {
		if _, err := db.RecordEvidence(DeliveryEvidence{Source: "station", Event: "ack", CorrKind: "aprs", CorrValue: "A1", CorrScope: "aprs_0"}, 0, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM delivery_evidence WHERE corr_value = 'A1'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("keyless rows: %d %v", n, err)
	}

	if err := db.MarkHubOpEventApplied(ev.EventID, now); err != nil {
		t.Fatal(err)
	}
	db = reopen(t, path, db)
	if pend, err := db.PendingHubOpEvents(10); err != nil || len(pend) != 0 {
		t.Fatalf("applied but still pending: %+v %v", pend, err)
	}
	if k, err := db.AddHubOpEvent(ev, true, now); err != nil || k.Added || !k.Applied {
		t.Fatalf("a copy of an applied event: %+v %v", k, err)
	}
}

// An owed repeat stays owed until the operation is settled, however often it goes out; it is
// listed only inside the bound, and past it the operation is listed for giving up. A process
// start lists every operation not settled, owed or not.
func TestHubDurability_AnOwedRepeatStaysOwedUntilTheOperationSettles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kit.db")
	db := reopen(t, path, nil)
	now := time.Now()
	op := HubOp{OpID: hubmsg.NewV7(now), Mid: hubmsg.NewV7(now), Recipient: "bridge:kit-b", IntentID: "i-1",
		ExpiresAt: now.Add(2 * time.Minute).UnixMilli(), CreatedAt: now.UnixMilli(), Submission: `{"v":1}`}
	quiet := op
	quiet.OpID, quiet.Mid = hubmsg.NewV7(now), hubmsg.NewV7(now)
	for _, o := range []HubOp{op, quiet} {
		if err := db.PutHubOp(o); err != nil {
			t.Fatal(err)
		}
	}
	if ops, err := db.HubOpsOwedReplay(now, time.Hour, 10); err != nil || len(ops) != 0 {
		t.Fatalf("owed before anything failed: %+v %v", ops, err)
	}
	if ops, err := db.UnsettledHubOps(now, time.Hour, 10); err != nil || len(ops) != 2 {
		t.Fatalf("not settled: %+v %v", ops, err)
	}
	if err := db.WantHubOpReplay(op.OpID, now); err != nil {
		t.Fatal(err)
	}
	if err := db.WantHubOpReplay(op.OpID, now.Add(time.Second)); err != nil { // first owed stays
		t.Fatal(err)
	}
	db = reopen(t, path, db)
	ops, err := db.HubOpsOwedReplay(now, time.Hour, 10)
	if err != nil || len(ops) != 1 || ops[0].ReplayAt != now.UnixMilli() {
		t.Fatalf("owed after a reopen: %+v %v", ops, err)
	}
	if err := db.SettleHubOpReplay(op.OpID); err != nil { // not settled: nothing clears it
		t.Fatal(err)
	}
	if n, err := db.SettleHubOpReplays(); err != nil || n != 0 {
		t.Fatalf("cleared %d unsettled: %v", n, err)
	}
	if ops, _ := db.HubOpsOwedReplay(now, time.Hour, 10); len(ops) != 1 {
		t.Fatalf("cleared without settling: %+v", ops)
	}
	later := time.UnixMilli(op.ExpiresAt).Add(time.Hour + time.Second)
	if ops, _ := db.HubOpsOwedReplay(later, time.Hour, 10); len(ops) != 0 {
		t.Fatalf("owed past the bound: %+v", ops)
	}
	if ops, _ := db.UnsettledHubOpsPastBound(later, time.Hour, 10); len(ops) != 2 {
		t.Fatalf("past the bound: %+v", ops)
	}
	// A forwarded event does not settle it; a completion does, and the mark goes.
	for i, ev := range []hubmsg.Event{
		{OpID: op.OpID, EventID: hubmsg.NewV7(now), Rev: 1, Class: "forwarded", Kind: "hub_forwarded", Authority: "hub"},
		{OpID: op.OpID, EventID: hubmsg.NewV7(now), Rev: 2, Class: "completed", Kind: "recipient_app_stored", Authority: "recipient"},
	} {
		if _, err := db.AddHubOpEvent(ev, true, now); err != nil {
			t.Fatal(err)
		}
		settled, _ := db.HubOpSettled(op.OpID)
		if settled != (i == 1) {
			t.Fatalf("after %s settled=%v", ev.Class, settled)
		}
	}
	if n, err := db.SettleHubOpReplays(); err != nil || n != 1 {
		t.Fatalf("settled cleared %d %v", n, err)
	}
	if got, _ := db.GetHubOp(op.OpID); got.ReplayAt != 0 {
		t.Fatal("a settled operation still owed")
	}
	if ops, _ := db.UnsettledHubOps(now, time.Hour, 10); len(ops) != 1 || ops[0].OpID != quiet.OpID {
		t.Fatalf("not settled after the completion: %+v", ops)
	}
	// Given up is not recovered: the owed mark stays, and it is listed nowhere again.
	if err := db.WantHubOpReplay(quiet.OpID, now); err != nil {
		t.Fatal(err)
	}
	if err := db.GiveUpHubOpReplay(quiet.OpID, later); err != nil {
		t.Fatal(err)
	}
	db = reopen(t, path, db)
	got, _ := db.GetHubOp(quiet.OpID)
	if got.ReplayAt == 0 || got.ReplayGivenUpAt != later.UnixMilli() {
		t.Fatalf("given up: %+v", got)
	}
	for _, list := range []func() ([]HubOp, error){
		func() ([]HubOp, error) { return db.HubOpsOwedReplay(now, time.Hour, 10) },
		func() ([]HubOp, error) { return db.UnsettledHubOps(now, time.Hour, 10) },
		func() ([]HubOp, error) { return db.UnsettledHubOpsPastBound(later, time.Hour, 10) },
	} {
		if ops, err := list(); err != nil || len(ops) != 0 {
			t.Fatalf("a given-up operation listed: %+v %v", ops, err)
		}
	}
}

// The work a hand-off made is listed with it in one transaction, survives a reopen, is made
// once however often it is asked for, and goes with the mark that the hand-off is done.
func TestHubDurability_HandoffWorkIsListedOnceUntilTheHandoffIsDone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kit.db")
	db := reopen(t, path, nil)
	origin := HubHandoffOrigin(7)
	del := MessageDelivery{MsgRef: "r", Channel: "iridium_imt_0", Status: "queued", Payload: []byte("x"), MaxRetries: 3}
	id, created, err := db.InsertDeliveryOnce(origin, "rule:1/iridium_imt_0/", del)
	if err != nil || !created || id == 0 {
		t.Fatalf("%d %v %v", id, created, err)
	}
	db = reopen(t, path, db)
	again, created, err := db.InsertDeliveryOnce(origin, "rule:1/iridium_imt_0/", del)
	if err != nil || created || again != id {
		t.Fatalf("made again after a reopen: %d %v %v", again, created, err)
	}
	if err := db.NoteDispatchWork(origin, "rule:2/bond1/"); err != nil {
		t.Fatal(err)
	}
	done, err := db.DispatchWorkFor(origin)
	if err != nil || len(done) != 2 || !done["rule:1/iridium_imt_0/"] || !done["rule:2/bond1/"] {
		t.Fatalf("%v %v", done, err)
	}
	if other, _ := db.DispatchWorkFor(HubHandoffOrigin(70)); len(other) != 0 {
		t.Fatalf("another message's list: %v", other)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM message_deliveries`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rows %d %v", rows, err)
	}
	// A failed row write leaves no line: the work is still to make.
	if _, err := db.Exec(`CREATE TRIGGER fail_row BEFORE INSERT ON message_deliveries BEGIN SELECT RAISE(ABORT, 'test'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.InsertDeliveryOnce(origin, "rule:3/cellular_0/", del); err == nil {
		t.Fatal("a failed write reported success")
	}
	if _, err := db.Exec(`DROP TRIGGER fail_row`); err != nil {
		t.Fatal(err)
	}
	if done, _ := db.DispatchWorkFor(origin); done["rule:3/cellular_0/"] {
		t.Fatal("work listed whose row was never written")
	}

	if _, err := db.Exec(`INSERT INTO hub_inbox_handoff (message_id, state, created_at) VALUES (7, 'pending', 0)`); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkHubHandoffDone(7, time.Now()); err != nil {
		t.Fatal(err)
	}
	db = reopen(t, path, db)
	if done, _ := db.DispatchWorkFor(origin); len(done) != 0 {
		t.Fatalf("the list outlived its hand-off: %v", done)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM hub_inbox_handoff WHERE message_id = 7`).Scan(&state); err != nil || state != "done" {
		t.Fatalf("%s %v", state, err)
	}
}

// v72 takes the events kept before it as applied (the code of their day applied them), so an
// upgrade does not apply old evidence a second time.
func TestHubDurability_V72TakesEarlierEventsAsApplied(t *testing.T) {
	db := openStaged(t, 71)
	if _, err := db.Exec(`INSERT INTO hub_op_events (event_id, op_id, rev, class, kind, reason, authority, at, valid, received_at)
		VALUES ('e-1', 'op-1', 1, 'completed', 'recipient_app_stored', '', 'recipient', '', 1, 1234)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(migrations[71]); err != nil {
		t.Fatalf("v72: %v", err)
	}
	var applied int64
	if err := db.QueryRow(`SELECT applied_at FROM hub_op_events WHERE event_id = 'e-1'`).Scan(&applied); err != nil || applied != 1234 {
		t.Fatalf("applied_at %d %v", applied, err)
	}
}
