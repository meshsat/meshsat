package api

import (
	"errors"
	"sync"
	"testing"
	"time"

	"meshsat/internal/hubreporter"
)

// The r16 review's acceptance cases for C10-R4 and C10-R5 that the r16 fixes' own tests left
// out (MESHSAT-1584): a start publish that timed out after the Hub took the start, a restart
// while the first start publish was under way, and the last SOS's satellite start going out
// only after the next SOS began.

// timeoutHub takes the first start and then answers as a PUBACK that never came: the Hub has
// the start, the Bridge is told the publish failed.
type timeoutHub struct {
	*sosFakeHub
	once     sync.Once
	returned chan struct{}
}

func (h *timeoutHub) PublishSOSEvent(ev hubreporter.SOSEvent, wait time.Duration) error {
	err := h.sosFakeHub.PublishSOSEvent(ev, wait)
	if ev.Kind != hubreporter.SOSEventStart || err != nil {
		return err
	}
	first := false
	h.once.Do(func() { first = true })
	if !first {
		return nil
	}
	defer close(h.returned)
	return errors.New("injected: no PUBACK in time")
}

// R5: the first start publish failed after the Hub took it; a cancel after that owes the Hub
// the cancel, though the Bridge never saw the start reach the broker.
func TestR17AStartPublishThatTimedOutAfterTheHubTookItOwesTheCancel(t *testing.T) {
	s, _, clk := sosC10Server(t, nil, true, true)
	hub := &timeoutHub{sosFakeHub: &sosFakeHub{up: true}, returned: make(chan struct{})}
	s.sos.hub = hub
	if !s.TriggerSOSWithText("hold", sosWords) {
		t.Fatal("refused")
	}
	<-hub.returned
	if n, _ := hub.count(hubreporter.SOSEventStart); n != 1 {
		t.Fatalf("the Hub has %d starts, want 1", n)
	}
	run := sosRunOf(t, s)
	if l := run.leg(sosRouteHub); l == nil || l.PublishedAt != "" {
		t.Fatalf("the precondition: the Bridge saw the start reach the broker: %+v", l)
	}
	cancelSOS(t, s)
	if !sosRunOf(t, s).HubCancelV2 {
		t.Fatal("a start the Hub took, though its publish failed, owes the Hub no cancel")
	}
	clk.add(time.Second)
	eventually(t, "the cancel", func() bool { return hub.last(hubreporter.SOSEventCancel).SOSID == run.SOSID })
}

// R5: the process stops while the first start publish is under way. The new one finds the
// attempt kept, so a cancel made after the restart owes the Hub the cancel, with no publish of
// its own before it.
func TestR17ARestartInsideTheFirstStartPublishOwesTheCancel(t *testing.T) {
	s, _, clk := sosC10Server(t, nil, true, true)
	hub := &blockingHub{sosFakeHub: &sosFakeHub{up: true}, entered: make(chan struct{}), release: make(chan struct{})}
	s.sos.hub = hub
	if !s.TriggerSOSWithText("hold", sosWords) {
		t.Fatal("refused")
	}
	<-hub.entered
	run := sosRunOf(t, s)

	// A new process from what was kept; the old one's publish never returns.
	after := &Server{db: s.db, mesh: &sosMesh{}, gwManager: s.gwManager, nowFn: clk.now}
	after.SetDispatcher(s.dispatcher)
	after.SetHubBridgeID("msa-flaneur")
	hub2 := &sosFakeHub{up: false} // no session yet: the new process publishes nothing
	after.sos.hub = hub2
	after.RestoreSOS()
	t.Cleanup(stopSOS(after))
	t.Cleanup(func() { close(hub.release) })
	cancelSOS(t, after)
	if !sosRunOf(t, after).HubCancelV2 {
		t.Fatal("after a restart inside the start publish, the cancel owes the Hub nothing")
	}
	if n, _ := hub2.count(hubreporter.SOSEventStart); n != 0 {
		t.Fatalf("the precondition: the new process published %d starts", n)
	}
	hub2.setUp(true)
	clk.add(time.Second)
	eventually(t, "the cancel after the restart", func() bool { return hub2.last(hubreporter.SOSEventCancel).SOSID == run.SOSID })
}

// R4: A is cancelled while its satellite start is with the modem, A's cancel gets no ack, B
// starts, and only then does A's start go out. A's 0x08 still goes on the satellite once the
// grace is over; B stays active and owes no cancel.
func TestR17ASatelliteStartThatGoesAfterTheNextSOSStillGetsItsCancelFrame(t *testing.T) {
	s, hub, clk := sosC10Server(t, &apiSat{kind: "sbd", connected: true}, true, true)
	if !s.TriggerSOSWithText("hold", sosWords) {
		t.Fatal("refused")
	}
	st := waitSOS(t, s, "the satellite leg queued", queued("satellite"))
	satID := legOf(st, "satellite").DeliveryID
	setRow(t, s, satID, `status = 'sending'`) // with the modem
	a := sosRunOf(t, s)
	s.SOSAckReceived(hubreporter.SOSAck{V: 2, SOSID: a.SOSID, Kind: hubreporter.SOSEventStart, State: "active", Rev: 1})
	cancelSOS(t, s)
	eventually(t, "A's cancel published", func() bool { return hub.last(hubreporter.SOSEventCancel).SOSID == a.SOSID })

	if !s.TriggerSOSWithText("hold", "B") {
		t.Fatal("B refused")
	}
	b := sosRunOf(t, s)
	if b.ID == a.ID || b.SOSID == a.SOSID {
		t.Fatal("B is A")
	}
	setRow(t, s, satID, `status = 'sent'`) // A's start goes out, after B began
	clk.add(sosCancelAckGrace + time.Second)
	eventually(t, "A's 0x08 after the grace", func() bool { return len(cancelFrameRows(t, s, a)) == 1 })
	if rows := cancelFrameRows(t, s, b); len(rows) != 0 {
		t.Fatalf("B has a cancel frame: %+v", rows)
	}
	if st := sosStatus(t, s); !st.Active || st.ID != b.ID {
		t.Fatalf("B is not the active SOS: %+v", st)
	}
	if hub.last(hubreporter.SOSEventCancel).SOSID == b.SOSID {
		t.Fatal("a cancel was published for B")
	}
}
