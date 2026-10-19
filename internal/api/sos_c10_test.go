package api

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/hubmsg"
	"meshsat/internal/hubreporter"
)

// The SOS under its own identity (HeMB-NG stage 2, C10, MESHSAT-1584): the contract is the
// Hub's docs/bridge-hub-protocol.md "SOS identity (C10)", the shared cases
// internal/hembfixtures/testdata/v1/sos.json.

// sosFakeHub is the Hub session for these tests: it records what the SOS publishes.
type sosFakeHub struct {
	mu     sync.Mutex
	up     bool
	alerts []hubreporter.SOSAlert
	events []hubreporter.SOSEvent
}

func (h *sosFakeHub) IsConnected() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.up
}

func (h *sosFakeHub) setUp(up bool) {
	h.mu.Lock()
	h.up = up
	h.mu.Unlock()
}

func (h *sosFakeHub) PublishSOSNow(a hubreporter.SOSAlert, _ hubreporter.DeviceSOS) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.up {
		return hubreporter.ErrNotConnected
	}
	h.alerts = append(h.alerts, a)
	return nil
}

func (h *sosFakeHub) PublishSOSEvent(ev hubreporter.SOSEvent, _ time.Duration) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.up {
		return hubreporter.ErrNotConnected
	}
	h.events = append(h.events, ev)
	return nil
}

func (h *sosFakeHub) count(kind string) (events, alerts int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.events {
		if e.Kind == kind {
			events++
		}
	}
	return events, len(h.alerts)
}

func (h *sosFakeHub) last(kind string) hubreporter.SOSEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.events) - 1; i >= 0; i-- {
		if h.events[i].Kind == kind {
			return h.events[i]
		}
	}
	return hubreporter.SOSEvent{}
}

// sosClock is a clock a test moves.
type sosClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *sosClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *sosClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// sosC10Server is sosLegsServer with a Hub session, a clock the test moves, and the Hub's
// caps as given.
func sosC10Server(t *testing.T, sbd *apiSat, sosV2, up bool) (*Server, *sosFakeHub, *sosClock) {
	t.Helper()
	s := sosLegsServer(t, sbd, nil)
	clk := &sosClock{t: time.Unix(1792330000, 123e6)}
	s.nowFn = clk.now
	hub := &sosFakeHub{up: up}
	s.sos.hub = hub
	if sosV2 {
		s.HubCapsReceived(hubreporter.HubCaps{V: 1, Caps: []string{hubreporter.CapSOSV2}})
	}
	t.Cleanup(stopSOS(s))
	return s, hub, clk
}

// eventually waits for cond, for 5 s at most.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: never", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func sosRunOf(t *testing.T, s *Server) sosRun {
	t.Helper()
	s.sos.mu.Lock()
	defer s.sos.mu.Unlock()
	if s.sos.run == nil {
		t.Fatal("no SOS kept")
	}
	return s.sos.run.copy()
}

// Without the Hub's sos_v2 the SOS goes as before: the 0x02 frame, and the Hub told the old
// way, "sent" at the broker's PUBACK. The run keeps its own id all the same.
func TestSOSC10_WithoutTheHubsCapsTheSOSGoesTheOldWay(t *testing.T) {
	s, hub, _ := sosC10Server(t, &apiSat{kind: "sbd", connected: true}, false, true)
	if !s.TriggerSOSWithText("hold", sosWords) {
		t.Fatal("refused")
	}
	st := waitSOS(t, s, "the satellite leg queued and the Hub told", func(st sosStatusAnswer) bool {
		h := legOf(st, "hub")
		return queued("satellite")(st) && h != nil && h.Status == "sent"
	})
	del, _ := s.db.GetDelivery(legOf(st, "satellite").DeliveryID)
	if header, _, err := hubreporter.DecodeSatUplink(del.Payload); err != nil || header.MsgType != hubreporter.SatMsgSOS {
		t.Fatalf("the frame is type %x (%v), want the 0x02", del.Payload[3], err)
	}
	if events, alerts := hub.count(hubreporter.SOSEventStart); events != 0 || alerts != 1 {
		t.Fatalf("%d C10 events, %d old-way alerts", events, alerts)
	}
	run := sosRunOf(t, s)
	if !hubmsg.ValidV7(run.SOSID) || !hubmsg.SOSIDValid(run.SOSID, "msa-flaneur", run.ID) {
		t.Fatalf("the run's id %q does not name its start %d", run.SOSID, run.ID)
	}
}

// Under sos_v2: the satellite frame is the 0x07 with the run's id and start, and fits one
// SMS as base64; the hub leg publishes the v2 start, again and again, and is "sending" until
// the Hub's ack, then "sent", with nothing published after. Nothing goes the old way.
func TestSOSC10_UnderSOSV2TheFramesAndTheHubLegCarryTheRunsIdentity(t *testing.T) {
	s, hub, clk := sosC10Server(t, &apiSat{kind: "sbd", connected: true}, true, true)
	if !s.TriggerSOSWithText("hold", sosWords) {
		t.Fatal("refused")
	}
	st := waitSOS(t, s, "the satellite leg queued and the start published", func(st sosStatusAnswer) bool {
		h := legOf(st, "hub")
		return queued("satellite")(st) && h != nil && h.Status == "sending"
	})
	run := sosRunOf(t, s)

	del, _ := s.db.GetDelivery(legOf(st, "satellite").DeliveryID)
	frame := del.Payload
	if frame[3] != hubreporter.SatMsgSOSStart || len(frame) > sosFrameSMSMax {
		t.Fatalf("the frame %x: type %x, %d bytes", frame, frame[3], len(frame))
	}
	id, _ := hubmsg.UUIDBytes(run.SOSID)
	tail := frame[len(frame)-24:]
	if string(tail[:16]) != string(id[:]) || int64(binary.BigEndian.Uint64(tail[16:])) != run.ID {
		t.Fatalf("the frame's identity %x, want %s and %d", tail, run.SOSID, run.ID)
	}
	if _, _, _, _, words, _, err := hubreporter.DecodeSatSOS(frame[4 : len(frame)-24]); err != nil || words != "SOS: Kyriakos needs help, position unknown" {
		t.Fatalf("the 0x02 body in the 0x07: %q %v", words, err)
	}
	if l := legOf(st, "satellite"); l.Frame != sosFrameV2 {
		t.Fatalf("the satellite leg is not marked v2: %+v", l)
	}

	ev := hub.last(hubreporter.SOSEventStart)
	if ev.SOSID != run.SOSID || ev.RunStartMs != run.ID || ev.Text != sosWords || ev.Timestamp != run.ID/1000 || ev.Reason != nil {
		t.Fatalf("the start event %+v", ev)
	}
	if _, alerts := hub.count(""); alerts != 0 {
		t.Fatal("the Hub was also told the old way")
	}

	// Published again until the ack.
	n, _ := hub.count(hubreporter.SOSEventStart)
	clk.add(time.Second)
	eventually(t, "the start published again", func() bool { m, _ := hub.count(hubreporter.SOSEventStart); return m > n })

	s.SOSAckReceived(hubreporter.SOSAck{V: 2, SOSID: run.SOSID, Kind: hubreporter.SOSEventStart, State: "active", Rev: 2})
	st = waitSOS(t, s, "the hub leg sent at the ack", func(st sosStatusAnswer) bool { return legOf(st, "hub").Status == "sent" })
	if h := legOf(st, "hub"); h.HubState != "active" || h.SentAt == "" || h.PublishedAt == "" {
		t.Fatalf("hub leg after the ack %+v", h)
	}
	time.Sleep(60 * time.Millisecond) // a worker round still under way publishes once more at most
	n, _ = hub.count(hubreporter.SOSEventStart)
	clk.add(time.Hour)
	time.Sleep(100 * time.Millisecond)
	if m, _ := hub.count(hubreporter.SOSEventStart); m != n {
		t.Fatalf("published %d more times after the ack", m-n)
	}
}

// An ack for another run changes nothing.
func TestSOSC10_AnAckForAnotherRunIsIgnored(t *testing.T) {
	s, _, _ := sosC10Server(t, &apiSat{kind: "sbd", connected: true}, true, true)
	if !s.TriggerSOSWithText("hold", sosWords) {
		t.Fatal("refused")
	}
	waitSOS(t, s, "the start published", func(st sosStatusAnswer) bool { h := legOf(st, "hub"); return h != nil && h.Status == "sending" })
	s.SOSAckReceived(hubreporter.SOSAck{V: 2, SOSID: hubmsg.NewSOSID(1), Kind: hubreporter.SOSEventStart, State: "active", Rev: 1})
	time.Sleep(50 * time.Millisecond)
	if h := legOf(sosStatus(t, s), "hub"); h.Status != "sending" {
		t.Fatalf("another run's ack ended this hub leg: %+v", h)
	}
}

// startWent puts the SOS on with its satellite start gone and the Hub's ack in, as on a kit
// that sent both.
func startWent(t *testing.T, s *Server) sosRun {
	t.Helper()
	if !s.TriggerSOSWithText("hold", sosWords) {
		t.Fatal("refused")
	}
	st := waitSOS(t, s, "the satellite leg queued", queued("satellite"))
	setRow(t, s, legOf(st, "satellite").DeliveryID, `status = 'sent'`)
	run := sosRunOf(t, s)
	return run
}

func cancelSOS(t *testing.T, s *Server) {
	t.Helper()
	if w := post(t, s, "/api/sos/cancel", `{"message":"Alarm cancelled: Kyriakos is safe and needs no help now."}`); w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
}

func cancelFrameRows(t *testing.T, s *Server, run sosRun) []database.MessageDelivery {
	t.Helper()
	dels, err := s.db.GetDeliveriesByMessage(run.ref("cancel:" + sosRouteSatellite))
	if err != nil {
		t.Fatal(err)
	}
	return dels
}

// The cancel under sos_v2 with the Hub's session up: the v2 cancel is published again and
// again until its ack; the 0x08 waits sosCancelAckGrace for that ack before it spends a
// credit on the satellite that carried the start; the ack stops the frame still waiting and
// ends the repeats.
func TestSOSC10_TheCancelRepeatsUntilItsAckAndTheFrameWaitsForIt(t *testing.T) {
	s, hub, clk := sosC10Server(t, &apiSat{kind: "sbd", connected: true}, true, true)
	run := startWent(t, s)
	s.SOSAckReceived(hubreporter.SOSAck{V: 2, SOSID: run.SOSID, Kind: hubreporter.SOSEventStart, State: "active", Rev: 1})
	cancelSOS(t, s)

	eventually(t, "the cancel published", func() bool { n, _ := hub.count(hubreporter.SOSEventCancel); return n > 0 })
	ev := hub.last(hubreporter.SOSEventCancel)
	if ev.SOSID != run.SOSID || ev.RunStartMs != run.ID || ev.Reason == nil || *ev.Reason != 0 || ev.Timestamp != clk.now().Unix() {
		t.Fatalf("the cancel event %+v", ev)
	}
	time.Sleep(60 * time.Millisecond)
	if rows := cancelFrameRows(t, s, run); len(rows) != 0 {
		t.Fatalf("the 0x08 was queued inside the grace, with the Hub's session up: %+v", rows)
	}

	n, _ := hub.count(hubreporter.SOSEventCancel)
	clk.add(sosCancelAckGrace + time.Second)
	eventually(t, "the cancel published again", func() bool { m, _ := hub.count(hubreporter.SOSEventCancel); return m > n })
	eventually(t, "the 0x08 queued after the grace", func() bool { return len(cancelFrameRows(t, s, run)) == 1 })
	row := cancelFrameRows(t, s, run)[0]
	if row.Channel != "iridium_0" || row.Class != database.DeliveryClassHubUplink || row.Priority != 0 || row.Status != "queued" {
		t.Fatalf("the cancel frame's row %+v", row)
	}
	f := row.Payload
	id, _ := hubmsg.UUIDBytes(run.SOSID)
	want := hubreporter.EncodeSatSOSCancel("msa-flaneur", id, run.ID, time.Unix(ev.Timestamp, 0), hubreporter.SOSCancelOperator)
	if string(f) != string(want) {
		t.Fatalf("the cancel frame\n got %x\nwant %x", f, want)
	}
	if !s.sosMayDeliver(row) {
		t.Fatal("the send gate holds the cancel frame back")
	}

	s.SOSAckReceived(hubreporter.SOSAck{V: 2, SOSID: run.SOSID, Kind: hubreporter.SOSEventCancel, State: "cancelled", Rev: 2})
	if status, lastErr := rowStatus(t, s, row.ID); status != "dead" || lastErr != "cancelled" {
		t.Fatalf("the waiting cancel frame after the ack: %s %q", status, lastErr)
	}
	eventually(t, "the follow loop ended", func() bool {
		s.sos.mu.Lock()
		defer s.sos.mu.Unlock()
		return s.sos.cancelLoopFor == 0
	})
	n, _ = hub.count(hubreporter.SOSEventCancel)
	clk.add(time.Hour)
	time.Sleep(60 * time.Millisecond)
	if m, _ := hub.count(hubreporter.SOSEventCancel); m != n {
		t.Fatal("the cancel was published after its ack")
	}
	if r := sosRunOf(t, s); r.HubCancelAckedAt == "" || !r.HubCancelV2 || r.HubCancelPublishedAt == "" {
		t.Fatalf("the run after the ack %+v", r)
	}
}

// With no Hub session the 0x08 goes at once on the bearer that carried the start, and after
// 24 h without the Hub's ack the cancel is no longer repeated: the frame still waiting stops.
func TestSOSC10_WithoutAHubSessionTheFrameGoesAtOnceForADayAtMost(t *testing.T) {
	s, hub, clk := sosC10Server(t, &apiSat{kind: "sbd", connected: true}, true, false)
	run := startWent(t, s)
	cancelSOS(t, s)
	eventually(t, "the 0x08 queued at once", func() bool { return len(cancelFrameRows(t, s, run)) == 1 })
	row := cancelFrameRows(t, s, run)[0]
	if n, _ := hub.count(hubreporter.SOSEventCancel); n != 0 {
		t.Fatal("published without a session")
	}
	clk.add(sosCancelFor + time.Minute)
	eventually(t, "the frame stopped after a day", func() bool { st, _ := rowStatus(t, s, row.ID); return st == "dead" })
	eventually(t, "the follow loop ended", func() bool {
		s.sos.mu.Lock()
		defer s.sos.mu.Unlock()
		return s.sos.cancelLoopFor == 0
	})
	hub.setUp(true)
	time.Sleep(60 * time.Millisecond)
	if n, _ := hub.count(hubreporter.SOSEventCancel); n != 0 {
		t.Fatal("the cancel was published after the day was over")
	}
}

// A start that never left (cancelled while it waited) owes the Hub nothing: no 0x08, no
// cancel event, whenever the session comes.
func TestSOSC10_ACancelBeforeTheStartWentOwesNothing(t *testing.T) {
	s, hub, _ := sosC10Server(t, &apiSat{kind: "sbd", connected: true}, true, false)
	if !s.TriggerSOSWithText("hold", sosWords) {
		t.Fatal("refused")
	}
	waitSOS(t, s, "the satellite leg queued", queued("satellite"))
	run := sosRunOf(t, s)
	cancelSOS(t, s)
	hub.setUp(true)
	time.Sleep(100 * time.Millisecond)
	if n, _ := hub.count(hubreporter.SOSEventCancel); n != 0 {
		t.Fatal("a cancel published for a start that never went")
	}
	if rows := cancelFrameRows(t, s, run); len(rows) != 0 {
		t.Fatalf("a 0x08 for a start that never went: %+v", rows)
	}
	if r := sosRunOf(t, s); r.HubCancelV2 {
		t.Fatal("the run says the Hub is owed a cancel")
	}
}

// A restart carries on the cancellation the Hub has not acked: a new server on the same
// database publishes it again and stops at the ack.
func TestSOSC10_ARestartCarriesOnTheCancel(t *testing.T) {
	s, hub, clk := sosC10Server(t, &apiSat{kind: "sbd", connected: true}, true, true)
	run := startWent(t, s)
	s.SOSAckReceived(hubreporter.SOSAck{V: 2, SOSID: run.SOSID, Kind: hubreporter.SOSEventStart, State: "active", Rev: 1})
	cancelSOS(t, s)
	eventually(t, "the cancel published", func() bool { n, _ := hub.count(hubreporter.SOSEventCancel); return n > 0 })
	hub.setUp(false) // the old process's session ends
	s.sos.mu.Lock()
	s.sos.run = &sosRun{ID: run.ID + 1} // the old process is gone: its loop ends
	s.sos.mu.Unlock()

	after := &Server{db: s.db, mesh: &sosMesh{}, gwManager: s.gwManager, nowFn: clk.now}
	reg := channel.NewRegistry()
	channel.RegisterDefaults(reg)
	after.SetDispatcher(engine.NewDispatcher(after.db, reg, after.gwManager, nil))
	after.SetHubBridgeID("msa-flaneur")
	hub2 := &sosFakeHub{up: true}
	after.sos.hub = hub2
	after.RestoreSOS()
	eventually(t, "the cancel published again after the restart", func() bool { n, _ := hub2.count(hubreporter.SOSEventCancel); return n > 0 })
	if ev := hub2.last(hubreporter.SOSEventCancel); ev.SOSID != run.SOSID {
		t.Fatalf("after the restart the cancel names %q, want %q", ev.SOSID, run.SOSID)
	}
	if !after.sosV2() {
		t.Fatal("the Hub's caps were not kept across the restart")
	}
	after.SOSAckReceived(hubreporter.SOSAck{V: 2, SOSID: run.SOSID, Kind: hubreporter.SOSEventCancel, State: "cancelled", Rev: 2})
	eventually(t, "the follow loop ended", func() bool {
		after.sos.mu.Lock()
		defer after.sos.mu.Unlock()
		return after.sos.cancelLoopFor == 0
	})
}

// A run kept from before C10 has no id: it takes the old-format one, the fixture's, and keeps
// it.
func TestSOSC10_ARunFromBeforeC10TakesTheOldFormatID(t *testing.T) {
	s := newTestServerWithDB(t)
	s.SetHubBridgeID("kit-a")
	raw, _ := json.Marshal(map[string]any{"id": 1792330000123, "started_at": "2026-10-19T10:06:40Z", "text": sosWords,
		"trigger": "hold", "planned": true, "cancelled_at": "2026-10-19T10:16:40Z"})
	if err := s.db.SetSystemConfig(sosRunKey, string(raw)); err != nil {
		t.Fatal(err)
	}
	s.RestoreSOS()
	run := sosRunOf(t, s)
	if run.SOSID != "862a1976-7edf-5287-b08d-3fa253439180" {
		t.Fatalf("the old run's id %q, want the fixture's", run.SOSID)
	}
	kept, _ := s.db.GetSystemConfig(sosRunKey)
	if !strings.Contains(kept, `"sos_id":"862a1976-7edf-5287-b08d-3fa253439180"`) {
		t.Fatalf("the id is not kept: %s", kept)
	}
}

// The Hub's caps are kept in the database: a Bridge with no Hub session still knows. A later
// hub/caps without sos_v2 turns it off.
func TestSOSC10_TheHubsCapsAreKept(t *testing.T) {
	s := newTestServerWithDB(t)
	if s.sosV2() {
		t.Fatal("sos_v2 before the Hub said anything")
	}
	s.HubCapsReceived(hubreporter.HubCaps{V: 1, Caps: []string{hubreporter.CapSOSV2}})
	after := &Server{db: s.db}
	if !after.sosV2() {
		t.Fatal("a new process forgot the Hub's caps")
	}
	after.HubCapsReceived(hubreporter.HubCaps{})
	if after.sosV2() || (&Server{db: s.db}).sosV2() {
		t.Fatal("caps without sos_v2 did not turn it off")
	}
}

// The 0x07 fits one SMS as base64 whatever the bridge id and the words: its words are cut
// (never a character in two), the identity never.
func TestSOSC10_TheStartFrameFitsOneSMS(t *testing.T) {
	s := newTestServerWithDB(t)
	s.HubCapsReceived(hubreporter.HubCaps{V: 1, Caps: []string{hubreporter.CapSOSV2}})
	for _, bridge := range []string{"msa-flaneur", "nllei01tesseract01", strings.Repeat("b", 40)} {
		s.SetHubBridgeID(bridge)
		run := sosRun{ID: 1792330000123, Text: "SOS: Κυριάκος Παπαδόπουλος needs help right now on the north face", SOSID: hubmsg.NewSOSID(1792330000123)}
		frame, v2 := s.sosFrame(run)
		if !v2 || len(frame) > sosFrameSMSMax || frame[3] != hubreporter.SatMsgSOSStart {
			t.Fatalf("%s: v2 %v, %d bytes", bridge, v2, len(frame))
		}
		_, _, _, _, words, _, err := hubreporter.DecodeSatSOS(frame[4 : len(frame)-24])
		if err != nil || words == "" || !utf8Valid(words) || !strings.HasPrefix(run.Text+", position unknown", words) {
			t.Fatalf("%s: words %q %v", bridge, words, err)
		}
	}
}

// sosSMSRecorder is the satellite fallback's SOS send for these tests: it queues each frame
// as the SMS to the Hub's number (as cmd/meshsat's hubUplinkSOSSender does) and keeps it.
type sosSMSRecorder struct {
	mu     sync.Mutex
	frames map[string][]byte // msg_ref -> frame
}

func (r *sosSMSRecorder) frame(ref string) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.frames[ref]
}

func withHubSMS(t *testing.T, s *Server) *sosSMSRecorder {
	t.Helper()
	rec := &sosSMSRecorder{frames: map[string][]byte{}}
	s.satFallback = hubreporter.NewSatFallback(hubreporter.SatFallbackConfig{BridgeID: "msa-flaneur",
		SOSSendFn: func(frame []byte, ref string) error {
			rec.mu.Lock()
			rec.frames[ref] = append([]byte(nil), frame...)
			rec.mu.Unlock()
			_, _, err := s.dispatcher.QueueDirectSendTo("cellular_0", base64Std(frame), engine.DirectSendOptions{
				Precedence: "Override", Class: database.DeliveryClassHubUplink, Critical: hubreporter.IsSatSOS(frame),
				Destination: "+3197010258258", MsgRef: ref})
			return err
		}})
	return rec
}

// Under sos_v2 the SMS to the Hub's number carries the 0x07 in one SMS, and once it went, the
// cancel's 0x08 follows by SMS, at once with no Hub session; the Hub's ack stops it if it
// has not gone.
func TestSOSC10_TheSMSToTheHubCarriesTheStartAndItsCancel(t *testing.T) {
	s, _, _ := sosC10Server(t, nil, true, false)
	rec := withHubSMS(t, s)
	if !s.TriggerSOSWithText("hold", sosWords) {
		t.Fatal("refused")
	}
	run := sosRunOf(t, s)
	ref := run.ref(sosRouteHubSMS)
	eventually(t, "the SMS to the Hub's number", func() bool { return rec.frame(ref) != nil })
	start := rec.frame(ref)
	if start[3] != hubreporter.SatMsgSOSStart || len(base64Std(start)) > 160 {
		t.Fatalf("the SMS frame: type %x, %d base64 characters", start[3], len(base64Std(start)))
	}
	dels, _ := s.db.GetDeliveriesByMessage(ref)
	if len(dels) != 1 || dels[0].Priority != 0 {
		t.Fatalf("the SMS start's rows %+v", dels)
	}
	setRow(t, s, dels[0].ID, `status = 'sent'`)

	cancelSOS(t, s)
	cref := run.ref("cancel:" + sosRouteHubSMS)
	eventually(t, "the cancel by SMS", func() bool { return rec.frame(cref) != nil })
	id, _ := hubmsg.UUIDBytes(run.SOSID)
	r := sosRunOf(t, s)
	at, _ := time.Parse(time.RFC3339, r.CancelledAt)
	if want := hubreporter.EncodeSatSOSCancel("msa-flaneur", id, run.ID, at, 0); string(rec.frame(cref)) != string(want) {
		t.Fatalf("the cancel by SMS\n got %x\nwant %x", rec.frame(cref), want)
	}
	cdels, _ := s.db.GetDeliveriesByMessage(cref)
	if len(cdels) != 1 || cdels[0].Priority != 0 || cdels[0].Status != "queued" {
		t.Fatalf("the cancel's SMS rows %+v", cdels)
	}
	s.SOSAckReceived(hubreporter.SOSAck{V: 2, SOSID: run.SOSID, Kind: hubreporter.SOSEventCancel, State: "cancelled", Rev: 2})
	if st, _ := rowStatus(t, s, cdels[0].ID); st != "dead" {
		t.Fatalf("the cancel's SMS after the ack: %s", st)
	}
}

// The Hub takes no cancel by SMS for a v5 id (anyone could work it out from the bridge id and
// the start), so the Bridge sends none; a v7 id's goes.
func TestSOSC10_NoCancelBySMSForAnOldFormatID(t *testing.T) {
	for _, c := range []struct {
		name string
		id   func(start int64) string
		want bool
	}{
		{"v5", func(start int64) string { return hubmsg.LegacySOSID("msa-flaneur", start) }, false},
		{"v7", hubmsg.NewSOSID, true},
	} {
		s, _, _ := sosC10Server(t, nil, true, false)
		rec := withHubSMS(t, s)
		const start = 1792330000123
		run := &sosRun{ID: start, SOSID: c.id(start), Text: sosWords, Planned: true, HubSMSDone: true, HubSMSFrame: sosFrameV2,
			CancelledAt: "2026-10-19T10:16:40Z", CancelText: "safe", HubCancelV2: true}
		s.sos.mu.Lock()
		s.sos.run = run
		s.sos.mu.Unlock()
		delID, _, err := s.dispatcher.QueueDirectSendTo("cellular_0", "x", engine.DirectSendOptions{Class: database.DeliveryClassHubUplink, MsgRef: run.ref(sosRouteHubSMS)})
		if err != nil {
			t.Fatal(err)
		}
		setRow(t, s, delID, `status = 'sent'`)
		s.sosQueueCancelFrames(start)
		if got := rec.frame(run.ref("cancel:"+sosRouteHubSMS)) != nil; got != c.want {
			t.Errorf("%s: a cancel by SMS %v, want %v", c.name, got, c.want)
		}
	}
}

func base64Std(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
