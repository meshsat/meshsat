package transport

import (
	"encoding/json"
	"testing"
	"time"
)

// deliverMOResult stands in for readerLoop: it hands a helper mo_result to
// whoever is waiting on messageOriginateStatus, exactly as the dispatch does.
func deliverMOResult(t *testing.T, c *jsprConn, st jsprMOStatus) {
	t.Helper()
	body, _ := json.Marshal(st)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.pendingMu.Lock()
		pr, ok := c.pending["messageOriginateStatus"]
		if ok {
			delete(c.pending, "messageOriginateStatus")
		}
		c.pendingMu.Unlock()
		if ok {
			pr.ch <- jsprResponse{Code: jsprCodeOK, Target: "messageOriginateStatus", JSON: body}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("nobody was waiting for the MO result")
}

// The result of an earlier send must not settle this one. Before the check,
// a late mo_ack_received for message 22 booked message 23 as delivered while
// the helper had not even started it. [MESHSAT-1282]
func TestAwaitHelperMOResult_SkipsAnotherSendsResult(t *testing.T) {
	c := &jsprConn{pending: make(map[string]*pendingRequest)}
	type out struct {
		status string
		err    error
	}
	done := make(chan out, 1)
	go func() {
		s, err := c.awaitHelperMOResult(23, 5*time.Second)
		done <- out{s, err}
	}()

	deliverMOResult(t, c, jsprMOStatus{MessageID: 22, FinalMOStatus: "mo_ack_received", RequestReference: 22})
	select {
	case o := <-done:
		t.Fatalf("send 23 was settled by send 22's result: %+v", o)
	case <-time.After(50 * time.Millisecond):
	}

	deliverMOResult(t, c, jsprMOStatus{MessageID: 23, FinalMOStatus: "message_cancelled_pre_transit", RequestReference: 23})
	select {
	case o := <-done:
		if o.err != nil || o.status != "message_cancelled_pre_transit" {
			t.Fatalf("got %+v, want send 23's own status", o)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("send 23's own result was not taken")
	}
}

// A helper older than the reference tag sends no request_reference; its
// result is still accepted.
func TestAwaitHelperMOResult_AcceptsUntaggedResult(t *testing.T) {
	c := &jsprConn{pending: make(map[string]*pendingRequest)}
	done := make(chan string, 1)
	go func() {
		s, _ := c.awaitHelperMOResult(7, 5*time.Second)
		done <- s
	}()
	deliverMOResult(t, c, jsprMOStatus{MessageID: 9, FinalMOStatus: "mo_ack_received"})
	select {
	case s := <-done:
		if s != "mo_ack_received" {
			t.Fatalf("status %q", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("untagged result not accepted")
	}
}

func TestAwaitHelperMOResult_TimesOutAndClearsPending(t *testing.T) {
	c := &jsprConn{pending: make(map[string]*pendingRequest)}
	if _, err := c.awaitHelperMOResult(1, 30*time.Millisecond); err == nil {
		t.Fatal("want a timeout error")
	}
	if len(c.pending) != 0 {
		t.Fatal("a timed-out wait left its pending request behind")
	}
}

// The helper must answer before Go stops listening: its satellite wait plus
// the 20 s it may spend settling a cancel has to fit inside jsprMOTimeout.
func TestHelperBudgetFitsInsideMOTimeout(t *testing.T) {
	const settle = 20 * time.Second
	if jsprMOHelperBudget+settle >= jsprMOTimeout {
		t.Fatalf("helper budget %s + settle %s does not fit inside jsprMOTimeout %s", jsprMOHelperBudget, settle, jsprMOTimeout)
	}
}
