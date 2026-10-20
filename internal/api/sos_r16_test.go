package api

import (
	"sync"
	"testing"
	"time"

	"meshsat/internal/hubreporter"
)

// The r16 review's C10 findings on the Bridge (MESHSAT-1584): R3 a cancel frame whose enqueue
// failed is owed until a row exists, per bearer, never two; R4 a new SOS does not end the
// last one's owed cancel; R5 a cancel made while the first start publish is under way owes
// the Hub the cancel, and so does a start's ack that comes after the local cancel.

func failCancelInserts(t *testing.T, s *Server, route string) func() {
	t.Helper()
	if _, err := s.db.Exec(`CREATE TRIGGER review_fail_cancel BEFORE INSERT ON message_deliveries WHEN NEW.msg_ref LIKE '%cancel:` +
		route + `' BEGIN SELECT RAISE(FAIL, 'injected: enqueue failed'); END`); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := s.db.Exec(`DROP TRIGGER review_fail_cancel`); err != nil {
			t.Fatal(err)
		}
	}
}

func rowsUnder(t *testing.T, s *Server, ref string) int {
	t.Helper()
	dels, err := s.db.GetDeliveriesByMessage(ref)
	if err != nil {
		t.Fatal(err)
	}
	return len(dels)
}

// R3: the enqueue fails, the frame stays owed, and once the database takes inserts again the
// loop queues exactly one.
func TestR16AFailedCancelEnqueueIsTriedAgain(t *testing.T) {
	s, _, clk := sosC10Server(t, &apiSat{kind: "sbd", connected: true}, true, false)
	run := startWent(t, s)
	recover := failCancelInserts(t, s, "satellite")
	cancelSOS(t, s)
	time.Sleep(100 * time.Millisecond) // a few rounds of the loop, each one failing
	ref := run.ref("cancel:" + sosRouteSatellite)
	if n := rowsUnder(t, s, ref); n != 0 || sosRunOf(t, s).FrameCancelsDone {
		t.Fatalf("while the insert fails: %d rows, done %v", n, sosRunOf(t, s).FrameCancelsDone)
	}
	recover()
	clk.add(time.Second)
	eventually(t, "the frame queued once the database takes it", func() bool { return rowsUnder(t, s, ref) == 1 })
	eventually(t, "done", func() bool { return sosRunOf(t, s).FrameCancelsDone })
	s.sosQueueCancelFrames(run.ID)
	if n := rowsUnder(t, s, ref); n != 1 {
		t.Fatalf("%d frames for one bearer", n)
	}
}

// R3: the reviewer's reproduction, as given, with the fix's semantics: after a failed first
// attempt and the trigger removed, the normal path queues the frame.
func TestReviewR16FailedCancelEnqueueIsRetried(t *testing.T) {
	s, _, _ := sosC10Server(t, &apiSat{kind: "sbd", connected: true}, true, false)
	run := startWent(t, s)
	recover := failCancelInserts(t, s, "satellite")
	cancelSOS(t, s)
	time.Sleep(60 * time.Millisecond)
	recover()
	s.sosQueueCancelFrames(run.ID)
	rows := cancelFrameRows(t, s, run)
	if len(rows) == 0 {
		t.Fatalf("cancel lost after transient enqueue failure: FrameCancelsDone=%v, rows=%d", sosRunOf(t, s).FrameCancelsDone, len(rows))
	}
}

// R3: a crash after the enqueue and before the run's update: the row is there, the run says
// not done; the next round counts the row and queues no second frame.
func TestR16ARowFromBeforeACrashIsNotQueuedTwice(t *testing.T) {
	s, _, _ := sosC10Server(t, &apiSat{kind: "sbd", connected: true}, true, false)
	run := startWent(t, s)
	cancelSOS(t, s)
	ref := run.ref("cancel:" + sosRouteSatellite)
	eventually(t, "the frame", func() bool { return rowsUnder(t, s, ref) == 1 })
	stopSOSLoops(t, s, run.ID)
	s.sosUpdate(run.ID, func(r *sosRun) { r.FrameCancelsDone, r.CancelLegs = false, nil }) // the update was lost
	s.sosQueueCancelFrames(run.ID)
	if n := rowsUnder(t, s, ref); n != 1 || !sosRunOf(t, s).FrameCancelsDone {
		t.Fatalf("%d rows, done %v", n, sosRunOf(t, s).FrameCancelsDone)
	}
}

// stopSOSLoops waits for the run's cancel loop to stop, after its work is done.
func stopSOSLoops(t *testing.T, s *Server, id int64) {
	t.Helper()
	s.sosUpdate(id, func(r *sosRun) { r.HubCancelAckedAt = "2026-10-20T00:00:00Z" })
	eventually(t, "the loop ends", func() bool { return !s.sosCancelLoopRunning(id) })
	s.sosUpdate(id, func(r *sosRun) { r.HubCancelAckedAt = "" })
}

// R3: two bearers carried the start; one enqueue succeeds, the other fails. The first is not
// queued again, the second is once the database takes it.
func TestR16APartialSuccessAcrossTwoBearers(t *testing.T) {
	s, _, clk := sosC10Server(t, &apiSat{kind: "sbd", connected: true}, true, false)
	withHubSMS(t, s)
	run := startWent(t, s)
	eventually(t, "the SMS start", func() bool { return rowsUnder(t, s, run.ref(sosRouteHubSMS)) == 1 })
	dels, _ := s.db.GetDeliveriesByMessage(run.ref(sosRouteHubSMS))
	setRow(t, s, dels[0].ID, `status = 'sent'`)
	recover := failCancelInserts(t, s, "hubsms")
	cancelSOS(t, s)
	sat, sms := run.ref("cancel:"+sosRouteSatellite), run.ref("cancel:"+sosRouteHubSMS)
	eventually(t, "the satellite frame", func() bool { return rowsUnder(t, s, sat) == 1 })
	time.Sleep(60 * time.Millisecond)
	if rowsUnder(t, s, sms) != 0 || sosRunOf(t, s).FrameCancelsDone {
		t.Fatal("done with a bearer still owed")
	}
	recover()
	clk.add(time.Second)
	eventually(t, "the SMS frame", func() bool { return rowsUnder(t, s, sms) == 1 })
	eventually(t, "done", func() bool { return sosRunOf(t, s).FrameCancelsDone })
	if n := rowsUnder(t, s, sat); n != 1 {
		t.Fatalf("%d satellite frames", n)
	}
}

// R4: the reviewer's reproduction: A's cancel is published and not acked, B starts inside
// the grace; A's cancel is still repeated, and A is still kept.
func TestReviewR16NewRunRetainsPreviousCancel(t *testing.T) {
	s, hub, clk := sosC10Server(t, &apiSat{kind: "sbd", connected: true}, true, true)
	old := startWent(t, s)
	s.SOSAckReceived(hubreporter.SOSAck{V: 2, SOSID: old.SOSID, Kind: hubreporter.SOSEventStart, State: "active", Rev: 1})
	cancelSOS(t, s)
	eventually(t, "first cancel", func() bool { n, _ := hub.count(hubreporter.SOSEventCancel); return n > 0 })
	before, _ := hub.count(hubreporter.SOSEventCancel)
	if !s.TriggerSOSWithText("hold", "second run") {
		t.Fatal("new run refused")
	}
	clk.add(time.Minute)
	eventually(t, "the old cancel again", func() bool { n, _ := hub.count(hubreporter.SOSEventCancel); return n > before })
	if _, _, exists := s.sosSnapshot(old.ID); !exists {
		t.Fatal("the old run is no longer kept")
	}
}

// R4: A is cancelled and its cancel does not reach the Hub; B starts; the connection comes
// back. A is cancelled at the Hub (its cancel published and acked), B stays active, and A's
// satellite frame is queued after the grace. After a restart A is followed again.
func TestR16AnOwedCancelOutlivesTheNextSOSAndARestart(t *testing.T) {
	s, hub, clk := sosC10Server(t, &apiSat{kind: "sbd", connected: true}, true, false)
	a := startWent(t, s)
	cancelSOS(t, s) // no session: A's frame queued at once
	eventually(t, "A's frame", func() bool { return len(cancelFrameRows(t, s, a)) == 1 })
	if !s.TriggerSOSWithText("hold", "B") {
		t.Fatal("B refused")
	}
	b := sosRunOf(t, s)
	if b.ID == a.ID || b.SOSID == a.SOSID {
		t.Fatal("B is A")
	}
	hub.setUp(true)
	clk.add(time.Second)
	eventually(t, "A's cancel published", func() bool {
		ev := hub.last(hubreporter.SOSEventCancel)
		return ev.SOSID == a.SOSID
	})
	if st := sosStatus(t, s); !st.Active || st.ID != b.ID {
		t.Fatalf("B is not the active SOS: %+v", st)
	}

	// A restart before A's ack: a new process follows A again.
	hub.setUp(false)
	after := &Server{db: s.db, mesh: &sosMesh{}, gwManager: s.gwManager, nowFn: clk.now}
	after.SetDispatcher(s.dispatcher)
	after.SetHubBridgeID("msa-flaneur")
	hub2 := &sosFakeHub{up: true}
	after.sos.hub = hub2
	stopSOS(s)()
	after.RestoreSOS()
	t.Cleanup(stopSOS(after))
	eventually(t, "A's cancel after the restart", func() bool { return hub2.last(hubreporter.SOSEventCancel).SOSID == a.SOSID })
	after.SOSAckReceived(hubreporter.SOSAck{V: 2, SOSID: a.SOSID, Kind: hubreporter.SOSEventCancel, State: "cancelled", Rev: 2})
	eventually(t, "A forgotten once acked", func() bool { _, _, ok := after.sosSnapshot(a.ID); return !ok })
	if _, active, ok := after.sosSnapshot(b.ID); !ok || !active {
		t.Fatal("B is not active after A's ack")
	}
}

// blockingHub holds the first start publish until released, as a broker that has the start
// and has not answered yet.
type blockingHub struct {
	*sosFakeHub
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (h *blockingHub) PublishSOSEvent(ev hubreporter.SOSEvent, wait time.Duration) error {
	if ev.Kind == hubreporter.SOSEventStart {
		first := false
		h.once.Do(func() { first = true })
		if first {
			close(h.entered)
			<-h.release
		}
	}
	return h.sosFakeHub.PublishSOSEvent(ev, wait)
}

// R5: the cancel lands while the first start publish is under way: the Hub is owed the
// cancel and gets it, whatever the publish answered.
func TestR16ACancelDuringTheFirstStartPublishOwesTheCancel(t *testing.T) {
	s, _, clk := sosC10Server(t, nil, true, true)
	hub := &blockingHub{sosFakeHub: &sosFakeHub{up: true}, entered: make(chan struct{}), release: make(chan struct{})}
	s.sos.hub = hub
	if !s.TriggerSOSWithText("hold", sosWords) {
		t.Fatal("refused")
	}
	<-hub.entered
	run := sosRunOf(t, s)
	if l := run.leg(sosRouteHub); l == nil || l.Frame != sosFrameV2 {
		t.Fatalf("the attempt is not kept before the publish: %+v", l)
	}
	cancelSOS(t, s)
	if !sosRunOf(t, s).HubCancelV2 {
		t.Fatal("the cancel made during the publish owes the Hub nothing")
	}
	close(hub.release)
	clk.add(time.Second)
	eventually(t, "the cancel", func() bool { n, _ := hub.count(hubreporter.SOSEventCancel); return n > 0 })
}

// R5: the start's ack comes after the local cancel, on a run whose cancel owed nothing (it
// knew of no start leaving): it owes the cancel then.
func TestR16AStartsAckAfterTheCancelOwesTheCancel(t *testing.T) {
	s, hub, _ := sosC10Server(t, nil, true, true)
	hub.setUp(false) // nothing published: the cancel owes nothing yet
	if !s.TriggerSOSWithText("hold", sosWords) {
		t.Fatal("refused")
	}
	waitSOS(t, s, "the hub leg planned", func(st sosStatusAnswer) bool { return legOf(st, "hub") != nil })
	run := sosRunOf(t, s)
	stopSOS(s)() // the worker made no publish attempt: no start known to have left
	s.sosUpdate(run.ID, func(r *sosRun) {
		if l := r.leg(sosRouteHub); l != nil {
			l.Frame = ""
		}
	})
	s.sos.mu.Lock()
	s.sos.active = true
	s.sos.mu.Unlock()
	cancelSOS(t, s)
	if sosRunOf(t, s).HubCancelV2 {
		t.Fatal("the precondition: the cancel already owes the Hub")
	}
	hub.setUp(true)
	s.SOSAckReceived(hubreporter.SOSAck{V: 2, SOSID: run.SOSID, Kind: hubreporter.SOSEventStart, State: "active", Rev: 1})
	if !sosRunOf(t, s).HubCancelV2 {
		t.Fatal("the late start ack did not make the cancel owed")
	}
	eventually(t, "the cancel", func() bool { n, _ := hub.count(hubreporter.SOSEventCancel); return n > 0 })
}
