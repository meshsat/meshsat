package database

import (
	"errors"
	"testing"
	"time"
)

const sqliteTime = "2006-01-02 15:04:05"

// deadlineRow inserts d, queued at priority 1 unless it says otherwise.
func deadlineRow(t *testing.T, db *DB, d MessageDelivery) int64 {
	t.Helper()
	if d.Status == "" {
		d.Status = "queued"
	}
	if d.Priority == 0 {
		d.Priority = 1
	}
	id, err := db.InsertDelivery(d)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func setDeliveryColumns(t *testing.T, db *DB, id int64, sets string, args ...interface{}) {
	t.Helper()
	if _, err := db.Exec(`UPDATE message_deliveries SET `+sets+` WHERE id = ?`, append(args, id)...); err != nil {
		t.Fatal(err)
	}
}

// The queue's cancel stops a delivery that has not gone out, held ones too
// (as MeshSat Android's cancelWaiting), and leaves one being sent or done
// alone. A held delivery could not be cancelled before, so it went out when
// its link came back. [MESHSAT-1430]
func TestCancelDelivery_TakesQueuedRetryAndHeld(t *testing.T) {
	db := testDB(t)
	for _, tc := range []struct {
		status string
		ok     bool
	}{
		{"queued", true}, {"retry", true}, {"held", true},
		{"sending", false}, {"sent", false}, {"delivered", false}, {"dead", false}, {"expired", false},
	} {
		id := deadlineRow(t, db, MessageDelivery{MsgRef: "m-" + tc.status, Channel: "iridium_0", Status: tc.status})
		if tc.status == "held" {
			setDeliveryColumns(t, db, id, `held_at = datetime('now')`)
		}
		err := db.CancelDelivery(id)
		if (err == nil) != tc.ok {
			t.Fatalf("%s: cancel error %v, want ok %v", tc.status, err, tc.ok)
		}
		del, _ := db.GetDelivery(id)
		if tc.ok && (del.Status != "dead" || del.LastError != "cancelled") {
			t.Fatalf("%s: cancelled as %s %q, want dead cancelled", tc.status, del.Status, del.LastError)
		}
		if !tc.ok && del.Status != tc.status {
			t.Fatalf("%s: became %s", tc.status, del.Status)
		}
		var heldAt *string
		if err := db.QueryRow(`SELECT held_at FROM message_deliveries WHERE id = ?`, id).Scan(&heldAt); err != nil || (tc.ok && heldAt != nil) {
			t.Fatalf("%s: held_at %v (%v) after the cancel", tc.status, heldAt, err)
		}
	}
}

// OpenDeliveryByPreview finds the oldest delivery of that class and exact
// preview on that channel that has not finished (queued, retry or held
// before its deadline, or sending), and nothing else; WaitingDeliveryIDs
// lists those that can still be cancelled, on every channel. [MESHSAT-1430]
func TestOpenAndWaitingDeliveriesByPreview(t *testing.T) {
	db := testDB(t)
	const preview = "Alarm test: position report to the Hub"
	past := time.Now().UTC().Add(-time.Minute).Format(sqliteTime)
	future := time.Now().UTC().Add(29 * time.Minute).Format(sqliteTime)
	test := MessageDelivery{MsgRef: "t", Channel: "iridium_0", TextPreview: preview, Class: DeliveryClassHubUplink}

	if open, err := db.OpenDeliveryByPreview("iridium_0", DeliveryClassHubUplink, preview); err != nil || open != nil {
		t.Fatalf("empty queue: %v %v", open, err)
	}
	// Not open: finished, past the deadline, another channel, class or preview.
	for _, d := range []MessageDelivery{
		{MsgRef: "sent", Status: "sent"}, {MsgRef: "expired", Status: "expired"}, {MsgRef: "cancelled", Status: "dead"},
		{MsgRef: "late", ExpiresAt: &past}, {MsgRef: "late-held", Status: "held", ExpiresAt: &past},
		{MsgRef: "imt", Channel: "iridium_imt_0"}, {MsgRef: "text", Class: DeliveryClassMessage}, {MsgRef: "frame", TextPreview: "hub uplink frame, 31 B"},
	} {
		row := test
		row.MsgRef, row.Status, row.ExpiresAt = d.MsgRef, d.Status, d.ExpiresAt
		if d.Channel != "" {
			row.Channel = d.Channel
		}
		if d.Class != "" {
			row.Class = d.Class
		}
		if d.TextPreview != "" {
			row.TextPreview = d.TextPreview
		}
		deadlineRow(t, db, row)
	}
	if open, err := db.OpenDeliveryByPreview("iridium_0", DeliveryClassHubUplink, preview); err != nil || open != nil {
		t.Fatalf("nothing open, found %+v (%v)", open, err)
	}

	for _, status := range []string{"sending", "held", "retry", "queued"} {
		row := test
		row.MsgRef, row.Status, row.ExpiresAt = "open-"+status, status, &future
		id := deadlineRow(t, db, row)
		open, err := db.OpenDeliveryByPreview("iridium_0", DeliveryClassHubUplink, preview)
		if err != nil || open == nil {
			t.Fatalf("%s: nothing found (%v)", status, err)
		}
		if status == "sending" && open.ID != id {
			t.Fatalf("found %d, want the one being sent, %d", open.ID, id)
		}
		if status != "sending" && open.MsgRef != "open-sending" {
			t.Fatalf("found %s, want the oldest open one", open.MsgRef)
		}
	}

	ids, err := db.WaitingDeliveryIDs(DeliveryClassHubUplink, preview)
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, id := range ids {
		del, _ := db.GetDelivery(id)
		refs = append(refs, del.MsgRef)
	}
	want := []string{"late", "late-held", "imt", "open-held", "open-retry", "open-queued"}
	if len(refs) != len(want) {
		t.Fatalf("waiting %v, want %v", refs, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("waiting %v, want %v", refs, want)
		}
	}
}

// Unholding pauses a rule delivery's TTL for the time it was held, and
// leaves a direct send's deadline as it is: the direct row, held for three
// days past its deadline, stays out of the worker's reach and expires. The
// rule row gets its three days back. [MESHSAT-1430]
func TestUnhold_KeepsADirectSendsDeadline(t *testing.T) {
	db := testDB(t)
	threeDaysAgo := time.Now().UTC().Add(-72 * time.Hour)
	deadline := threeDaysAgo.Add(30 * time.Minute).Format(sqliteTime)
	ruleID := int64(7)

	direct := deadlineRow(t, db, MessageDelivery{MsgRef: "direct", Channel: "iridium_0", TTLSeconds: 1800, ExpiresAt: &deadline, Class: DeliveryClassHubUplink})
	rule := deadlineRow(t, db, MessageDelivery{MsgRef: "rule", Channel: "iridium_0", RuleID: &ruleID, TTLSeconds: 1800, ExpiresAt: &deadline})
	if n, err := db.HoldDeliveriesForChannel("iridium_0"); err != nil || n != 2 {
		t.Fatalf("held %d (%v)", n, err)
	}
	for _, id := range []int64{direct, rule} {
		setDeliveryColumns(t, db, id, `held_at = ?`, threeDaysAgo.Add(time.Minute).Format(sqliteTime))
	}
	if n, err := db.UnholdDeliveriesForChannel("iridium_0"); err != nil || n != 2 {
		t.Fatalf("unheld %d (%v)", n, err)
	}

	d, _ := db.GetDelivery(direct)
	if d.Status != "queued" || d.ExpiresAt == nil || *d.ExpiresAt != deadline {
		t.Fatalf("direct send after the hold: %s, expires %v, want queued with its deadline %s", d.Status, d.ExpiresAt, deadline)
	}
	r, _ := db.GetDelivery(rule)
	if exp, err := time.Parse(sqliteTime, *r.ExpiresAt); err != nil || !exp.After(time.Now().UTC()) {
		t.Fatalf("rule delivery after the hold expires %s (%v), want its TTL paused while held", *r.ExpiresAt, err)
	}

	pending, err := db.GetPendingDeliveries("iridium_0", 10)
	if err != nil || len(pending) != 1 || pending[0].ID != rule {
		t.Fatalf("pending %+v (%v), want the rule delivery only", pending, err)
	}
	if n, err := db.ExpireDeliveries(); err != nil || n != 1 {
		t.Fatalf("expired %d (%v), want the direct send", n, err)
	}
	if d, _ := db.GetDelivery(direct); d.Status != "expired" {
		t.Fatalf("direct send is %s, want expired", d.Status)
	}
}

// The worker's claim marks a row 'sending' only while it is queued or
// waiting for a retry, and clears the last error as the old mark did; a row
// in any other state is left as it is. [MESHSAT-1430]
func TestClaimDeliveryForSending_OnlyWhatIsStillQueued(t *testing.T) {
	db := testDB(t)
	for _, tc := range []struct {
		status string
		ok     bool
	}{
		{"queued", true}, {"retry", true},
		{"held", false}, {"sending", false}, {"sent", false}, {"delivered", false},
		{"dead", false}, {"expired", false}, {"denied", false},
	} {
		id := deadlineRow(t, db, MessageDelivery{MsgRef: "c-" + tc.status, Channel: "iridium_0", Status: tc.status})
		setDeliveryColumns(t, db, id, `last_error = 'mo_status=32'`)
		claimed, err := db.ClaimDeliveryForSending(id)
		if err != nil || claimed != tc.ok {
			t.Fatalf("%s: claimed %v (%v), want %v", tc.status, claimed, err, tc.ok)
		}
		del, _ := db.GetDelivery(id)
		switch {
		case tc.ok && (del.Status != "sending" || del.LastError != ""):
			t.Fatalf("%s: after the claim %s %q, want sending with no error", tc.status, del.Status, del.LastError)
		case !tc.ok && (del.Status != tc.status || del.LastError != "mo_status=32"):
			t.Fatalf("%s: became %s %q", tc.status, del.Status, del.LastError)
		}
	}
}

// EndSendingDeliveries gives only the matching rows being sent, above
// priority 0, a deadline of now: not the SOS's own frame (priority 0), not
// a waiting test, not another class or preview. [MESHSAT-1430]
func TestEndSendingDeliveries(t *testing.T) {
	db := testDB(t)
	const preview = "Alarm test: position report to the Hub"
	future := time.Now().UTC().Add(29 * time.Minute).Format(sqliteTime)
	row := func(ref, status, class, prev string, priority int) int64 {
		id, err := db.InsertDelivery(MessageDelivery{MsgRef: ref, Channel: "iridium_0", Status: status, Class: class,
			TextPreview: prev, Priority: priority, TTLSeconds: 1800, ExpiresAt: &future})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	onModem := row("on-modem", "sending", DeliveryClassHubUplink, preview, 1)
	untouched := []int64{
		row("sos-frame", "sending", DeliveryClassHubUplink, preview, 0),
		row("waiting", "queued", DeliveryClassHubUplink, preview, 1),
		row("text", "sending", DeliveryClassMessage, preview, 1),
		row("fallback-frame", "sending", DeliveryClassHubUplink, "hub uplink frame, 31 B", 1),
	}
	before := time.Now().UTC().Add(-time.Second)
	if n, err := db.EndSendingDeliveries(DeliveryClassHubUplink, preview); err != nil || n != 1 {
		t.Fatalf("ended %d (%v), want the one on the modem", n, err)
	}
	del, _ := db.GetDelivery(onModem)
	if exp, err := time.Parse(sqliteTime, *del.ExpiresAt); err != nil || exp.Before(before.Truncate(time.Second)) || exp.After(time.Now().UTC()) || del.Status != "sending" {
		t.Fatalf("the test on the modem: %s, expires %s (%v), want still sending with a deadline of now", del.Status, *del.ExpiresAt, err)
	}
	for _, id := range untouched {
		if d, _ := db.GetDelivery(id); d.ExpiresAt == nil || *d.ExpiresAt != future {
			t.Fatalf("%s: expires %v, want untouched", d.MsgRef, d.ExpiresAt)
		}
	}
}

// RetryDelivery never re-queues the SOS's Hub frame (class hub_uplink at
// priority 0, dead or failed): the credit budget does not hold it back and
// the Hub would raise the SOS again with the frame's old time. Everything
// else dead or failed is re-queued as before. [MESHSAT-1431]
func TestRetryDelivery_NeverTheSOSFrame(t *testing.T) {
	db := testDB(t)
	for _, tc := range []struct {
		name, status, class string
		priority            int
		wantErr             error
		requeued            bool
	}{
		{"the SOS frame, dead", "dead", DeliveryClassHubUplink, 0, ErrSOSFrameNotRetried, false},
		{"the SOS frame, failed", "failed", DeliveryClassHubUplink, 0, ErrSOSFrameNotRetried, false},
		{"a position frame, dead", "dead", DeliveryClassHubUplink, 1, nil, true},
		{"a text at priority 0, dead", "dead", DeliveryClassMessage, 0, nil, true},
	} {
		id, err := db.InsertDelivery(MessageDelivery{MsgRef: tc.name, Channel: "iridium_0", Status: tc.status, Class: tc.class, Priority: tc.priority})
		if err != nil {
			t.Fatal(err)
		}
		err = db.RetryDelivery(id)
		if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
			t.Fatalf("%s: %v, want %v", tc.name, err, tc.wantErr)
		}
		if del, _ := db.GetDelivery(id); (del.Status == "queued") != tc.requeued {
			t.Fatalf("%s: now %s", tc.name, del.Status)
		}
	}
	// Not dead or failed: the ordinary refusal, not the SOS one.
	id, _ := db.InsertDelivery(MessageDelivery{MsgRef: "queued-sos", Channel: "iridium_0", Status: "queued", Class: DeliveryClassHubUplink, Priority: 0})
	if err := db.RetryDelivery(id); err == nil || errors.Is(err, ErrSOSFrameNotRetried) {
		t.Fatalf("a queued SOS frame: %v", err)
	}
}
