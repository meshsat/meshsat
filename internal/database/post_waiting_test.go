package database

import "testing"

// A Community Post send is tried until it leaves or the operator closes the
// post, so the startup cleanup leaves the send a queued post waits on alone
// however many tries it took; a post's earlier send (it failed and was sent
// again) and any other retry-forever row are cleaned up as before. Closing
// stops the waiting sends, and only those: a send in a satellite session
// right now finishes, a sent post stays sent. [MESHSAT-1807]
func TestWaitingPostSendsOutliveTheCleanupUntilStopped(t *testing.T) {
	db := testDB(t)
	row := func(ref, status string, retries int) int64 {
		t.Helper()
		id, err := db.InsertDelivery(MessageDelivery{MsgRef: ref, Channel: "iridium_0", Status: status, Priority: 1,
			TextPreview: "x", MaxRetries: 0, Visited: "[]", QoSLevel: 1})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE message_deliveries SET retries = ? WHERE id = ?`, retries, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	post := func(code, status string, delivery int64) {
		t.Helper()
		m := &PostMessage{Code: code, Session: "s", Recipient: "+31612345678", Text: "water", Class: PostClassRoutine,
			Status: status, Lang: "en", DeliveryID: delivery}
		if err := db.InsertPostMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	waiting := row("post-waiting", "retry", 40)
	post("WAIT22", PostStatusQueued, waiting)
	inSession := row("post-in-session", "sending", 3)
	post("BUSY22", PostStatusQueued, inSession)
	sent := row("post-sent", "sent", 2)
	post("SENT22", PostStatusSent, sent)
	earlier := row("post-earlier", "retry", 20) // a failed post's first send, still marked retry
	post("FAIL22", PostStatusFailed, earlier)
	other := row("plain-forever", "retry", 15)

	n, err := db.CancelRunawayDeliveries(15)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("cleanup cancelled %d, want 2 (the failed post's earlier send and the plain row)", n)
	}
	for id, want := range map[int64]string{waiting: "retry", earlier: "dead", other: "dead"} {
		if d, _ := db.GetDelivery(id); d == nil || d.Status != want {
			t.Errorf("delivery %d after the cleanup: %+v, want %s", id, d, want)
		}
	}

	stopped, err := db.CancelWaitingPostSends(PostSendStoppedClosed)
	if err != nil || stopped != 1 {
		t.Fatalf("stopped %d, %v; want the one waiting send", stopped, err)
	}
	if d, _ := db.GetDelivery(waiting); d.Status != "dead" || d.LastError != PostSendStoppedClosed {
		t.Errorf("the waiting send: %s %q", d.Status, d.LastError)
	}
	for code, want := range map[string]string{"WAIT22": PostStatusFailed, "BUSY22": PostStatusQueued, "SENT22": PostStatusSent} {
		if m, _ := db.GetPostMessage(code); m == nil || m.Status != want {
			t.Errorf("post %s: %+v, want %s", code, m, want)
		}
	}
	if d, _ := db.GetDelivery(inSession); d.Status != "sending" {
		t.Errorf("the send in a session was touched: %s", d.Status)
	}
	if again, _ := db.CancelWaitingPostSends(PostSendStoppedClosed); again != 0 {
		t.Errorf("stopping twice stopped %d more", again)
	}
}

// The purge stops a send still waiting before it deletes the post, or the
// text would leave for a post that no longer exists. [MESHSAT-1807]
func TestPurgeStopsTheWaitingSend(t *testing.T) {
	db := testDB(t)
	id, err := db.InsertDelivery(MessageDelivery{MsgRef: "post-w", Channel: "iridium_0", Status: "queued", Priority: 1,
		TextPreview: "x", Visited: "[]", QoSLevel: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertPostMessage(&PostMessage{Code: "PURG22", Session: "s", Recipient: "+31612345678", Text: "water",
		Class: PostClassRoutine, Status: PostStatusQueued, Lang: "en", DeliveryID: id}); err != nil {
		t.Fatal(err)
	}
	c, err := db.PurgePost()
	if err != nil || c.Messages != 1 || c.Stopped != 1 {
		t.Fatalf("purge = %+v, %v", c, err)
	}
	if d, _ := db.GetDelivery(id); d.Status != "dead" || d.LastError != PostSendStoppedPurged {
		t.Errorf("the waiting send after the purge: %s %q", d.Status, d.LastError)
	}
}
