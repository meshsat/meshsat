package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// forcedSat is a 9603 transport that runs the mailbox check itself, as
// DirectSatTransport does: it answers with a scripted outcome, remembers
// the context it ran on, and can hold the check until released.
type forcedSat struct {
	*fakeSat
	mu      sync.Mutex
	outcome transport.MailboxCheckOutcome
	calls   int
	ctxs    []context.Context
	started chan struct{}
	release chan struct{}
}

func newForcedSat(out transport.MailboxCheckOutcome) *forcedSat {
	return &forcedSat{fakeSat: &fakeSat{imei: "300434067943980"}, outcome: out, started: make(chan struct{}, 8)}
}

func (f *forcedSat) CheckMailboxNow(ctx context.Context) transport.MailboxCheckOutcome {
	f.mu.Lock()
	f.calls++
	f.ctxs = append(f.ctxs, ctx)
	release := f.release
	out := f.outcome
	f.mu.Unlock()
	f.started <- struct{}{}
	if release != nil {
		<-release
	}
	return out
}

func (f *forcedSat) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// ringSat is a transport without the forced check (the HAL's): its
// MailboxCheck answers as scripted.
type ringSat struct {
	*fakeSat
	mu        sync.Mutex
	result    *transport.SatResult
	err       error
	mt        []byte
	connected bool
	checks    int
}

func (r *ringSat) MailboxCheck(ctx context.Context) (*transport.SatResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks++
	if r.err != nil {
		return nil, r.err
	}
	res := *r.result
	return &res, nil
}

func (r *ringSat) Receive(ctx context.Context) ([]byte, error) { return r.mt, nil }

func (r *ringSat) GetStatus(ctx context.Context) (*transport.SatStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return &transport.SatStatus{Connected: r.connected, Type: "sbd"}, nil
}

func (r *ringSat) checkCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.checks
}

func testDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.New(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// gssRows returns the GSS session rows (value 1 = registered) of source.
func gssRows(t *testing.T, db *database.DB, source string) []float64 {
	t.Helper()
	pts, err := db.GetSignalHistoryRaw(source, 0, time.Now().Unix()+60, 100)
	if err != nil {
		t.Fatal(err)
	}
	var vals []float64
	for _, p := range pts {
		vals = append(vals, p.Value)
	}
	return vals
}

func checked(received, queued int) transport.MailboxResult {
	return transport.MailboxResult{Kind: transport.MailboxChecked, MOStatus: 0, Received: received, StillQueued: queued}
}

// The forced check hands over what it fetched and records a GSS session
// only when the modem answered one.
func TestSBDCheckMailboxNow_GSSOnlyForAnsweredSessions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		out      transport.MailboxCheckOutcome
		wantGSS  []float64
		wantMsgs []string
	}{
		{"checked with a message", transport.MailboxCheckOutcome{
			Result: checked(1, 2), Messages: [][]byte{[]byte("hello")}, SessionAnswered: true,
		}, []float64{1}, []string{"hello"}},
		{"no network", transport.MailboxCheckOutcome{
			Result:          transport.MailboxResult{Kind: transport.MailboxSessionFailed, MOStatus: 32},
			SessionAnswered: true,
		}, []float64{0}, nil},
		{"held", transport.MailboxCheckOutcome{
			Result: transport.MailboxResult{Kind: transport.MailboxHeld, Seconds: 42, MOStatus: -1},
		}, nil, nil},
		{"link lost after the free read", transport.MailboxCheckOutcome{
			Result:   transport.MailboxResult{Kind: transport.MailboxLinkLost, MOStatus: -1},
			Messages: [][]byte{[]byte("earlier")},
		}, nil, []string{"earlier"}},
		// The waiting MT could not be read, so no session was opened over it.
		{"no answer: the free read failed", transport.MailboxCheckOutcome{
			Result: transport.MailboxResult{Kind: transport.MailboxNoAnswer, MOStatus: -1},
		}, nil, nil},
		{"not connected", transport.MailboxCheckOutcome{
			Result: transport.MailboxResult{Kind: transport.MailboxNotConnected, MOStatus: -1},
		}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			sat := newForcedSat(tc.out)
			gw := NewSBDGateway(IridiumConfig{}, sat, db, nil)

			if got := gw.CheckMailboxNow(); got != tc.out.Result {
				t.Fatalf("result %+v, want %+v", got, tc.out.Result)
			}
			if got := gssRows(t, db, "sbd_gss"); len(got) != len(tc.wantGSS) || (len(got) == 1 && got[0] != tc.wantGSS[0]) {
				t.Fatalf("sbd_gss rows %v, want %v", got, tc.wantGSS)
			}
			if got := gssRows(t, db, "gss"); len(got) != len(tc.wantGSS) {
				t.Fatalf("gss rows %v, want %v", got, tc.wantGSS)
			}
			for _, want := range tc.wantMsgs {
				select {
				case msg := <-gw.Receive():
					if msg.Text != want {
						t.Fatalf("inbound %q, want %q", msg.Text, want)
					}
				case <-time.After(time.Second):
					t.Fatalf("message %q was not handed over", want)
				}
			}
			select {
			case msg := <-gw.Receive():
				t.Fatalf("unexpected inbound %+v", msg)
			default:
			}
		})
	}
}

// The ring-alert and poll path records a GSS session only when one ran: an
// empty result (no reason for a session, or the MT already in the modem)
// used to count as a successful registration, and a failed status check
// as a failed one.
func TestRingAlertMailbox_GSSOnlyWhenASessionRan(t *testing.T) {
	for _, tc := range []struct {
		name    string
		result  *transport.SatResult
		err     error
		wantGSS int
	}{
		{"no session: nothing called for one", &transport.SatResult{NoSession: true}, nil, 0},
		{"no session: the MT was in the modem", &transport.SatResult{MTStatus: 1, MTLength: 1, MTReceived: true, NoSession: true}, nil, 0},
		{"a session ran", &transport.SatResult{MOStatus: 0, MOMSN: 219}, nil, 1},
		{"a session failed", &transport.SatResult{MOStatus: 32}, nil, 1},
		{"the status check failed", nil, errors.New("SBDSX failed: read timeout"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			sat := &ringSat{fakeSat: &fakeSat{}, result: tc.result, err: tc.err, mt: []byte("hi"), connected: true}
			gw := NewSBDGateway(IridiumConfig{}, sat, db, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel() // ends any 30 s retry the path schedules
			gw.handleRingAlertWithRetry(ctx, 1)
			if got := len(gssRows(t, db, "sbd_gss")); got != tc.wantGSS {
				t.Fatalf("%d GSS rows, want %d", got, tc.wantGSS)
			}
		})
	}
}

// A transport that cannot force a session (the HAL's) is checked through
// its own mailbox check.
func TestSBDCheckMailboxNow_ThroughTheTransportsOwnCheck(t *testing.T) {
	db := testDB(t)
	sat := &ringSat{fakeSat: &fakeSat{}, result: &transport.SatResult{MOStatus: 0, MTStatus: 1, MTLength: 2, MTQueued: 1}, mt: []byte("hi"), connected: true}
	gw := NewSBDGateway(IridiumConfig{}, sat, db, nil)
	if got := gw.CheckMailboxNow(); got != checked(1, 1) {
		t.Fatalf("result %+v", got)
	}
	if n := len(gssRows(t, db, "sbd_gss")); n != 1 {
		t.Fatalf("%d GSS rows, want 1", n)
	}

	sat.result = &transport.SatResult{MOStatus: 32}
	if got := gw.CheckMailboxNow(); got.Kind != transport.MailboxSessionFailed || got.MOStatus != 32 {
		t.Fatalf("result %+v, want session_failed 32", got)
	}

	sat.connected = false
	before := sat.checkCount()
	if got := gw.CheckMailboxNow(); got.Kind != transport.MailboxNotConnected {
		t.Fatalf("result %+v, want not_connected", got)
	}
	if sat.checkCount() != before {
		t.Fatal("a disconnected modem was asked for a check")
	}
}

// eventLog collects the manager's mailbox events.
type eventLog struct {
	mu     sync.Mutex
	events []MailboxCheckState
	texts  []string
}

func (l *eventLog) emit(eventType, message string, data json.RawMessage) {
	if eventType != "mailbox" {
		return
	}
	var st MailboxCheckState
	if err := json.Unmarshal(data, &st); err != nil {
		panic(err)
	}
	l.mu.Lock()
	l.events = append(l.events, st)
	l.texts = append(l.texts, message)
	l.mu.Unlock()
}

func (l *eventLog) snapshot() ([]MailboxCheckState, []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]MailboxCheckState(nil), l.events...), append([]string(nil), l.texts...)
}

func waitMailboxDone(t *testing.T, m *Manager) MailboxCheckState {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st := m.GetMailboxCheck(); !st.Running && st.Result != nil {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the mailbox check did not finish")
	return MailboxCheckState{}
}

func mailboxManager(gws map[string]Gateway) *Manager {
	return &Manager{running: gws, runningByIface: map[string]Gateway{}}
}

// Before the first check there is no result; with no Iridium gateway the
// check fails with ErrNoIridiumGateway and its kept outcome is
// not_connected, announced like any other.
func TestStartMailboxCheck_NoGateway(t *testing.T) {
	m := mailboxManager(map[string]Gateway{"mqtt_0": &ctxFakeGateway{}})
	log := &eventLog{}
	m.SetEventDataEmitFunc(log.emit)

	raw, _ := json.Marshal(m.GetMailboxCheck())
	if string(raw) != `{"running":false,"result":null,"finished_at":null}` {
		t.Fatalf("state before the first check: %s", raw)
	}

	if err := m.StartMailboxCheck(); !errors.Is(err, ErrNoIridiumGateway) {
		t.Fatalf("err %v, want ErrNoIridiumGateway", err)
	}
	st := m.GetMailboxCheck()
	if st.Running || st.Result == nil || st.Result.Kind != transport.MailboxNotConnected || st.FinishedAt == nil {
		t.Fatalf("state %+v", st)
	}
	if _, err := time.Parse(time.RFC3339, *st.FinishedAt); err != nil {
		t.Fatalf("finished_at %q: %v", *st.FinishedAt, err)
	}
	events, texts := log.snapshot()
	if len(events) != 1 || events[0].Running || events[0].Result.Kind != transport.MailboxNotConnected {
		t.Fatalf("events %+v", events)
	}
	if texts[0] != "Mailbox check: the modem is not connected." {
		t.Fatalf("event text %q", texts[0])
	}
}

// With an SBD and an IMT gateway both running the check runs on the SBD
// one, whatever the map order; a second request while it runs is refused;
// the outcome is kept and announced on start and on end.
func TestStartMailboxCheck_SBDFirstConflictAndOutcome(t *testing.T) {
	for i := 0; i < 20; i++ { // map order varies from run to run
		sat := newForcedSat(transport.MailboxCheckOutcome{Result: checked(1, 2), Messages: [][]byte{[]byte("hello")}, SessionAnswered: true})
		sat.release = make(chan struct{})
		sbd := NewSBDGateway(IridiumConfig{}, sat, nil, nil)
		imtSat := &ringSat{fakeSat: &fakeSat{}, result: &transport.SatResult{NoSession: true}, connected: true}
		imt := NewIMTGateway(IridiumConfig{}, imtSat, nil, nil)
		m := mailboxManager(map[string]Gateway{"iridium_imt_0": imt, "iridium_0": sbd, "mqtt_0": &ctxFakeGateway{}})
		log := &eventLog{}
		m.SetEventDataEmitFunc(log.emit)

		if err := m.StartMailboxCheck(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-sat.started:
		case <-time.After(3 * time.Second):
			t.Fatal("the SBD gateway was not asked")
		}
		if !m.GetMailboxCheck().Running {
			t.Fatal("not running while the check runs")
		}
		if err := m.StartMailboxCheck(); !errors.Is(err, ErrMailboxCheckRunning) {
			t.Fatalf("second start: %v, want ErrMailboxCheckRunning", err)
		}
		close(sat.release)
		st := waitMailboxDone(t, m)
		if *st.Result != checked(1, 2) {
			t.Fatalf("result %+v", *st.Result)
		}
		if imtSat.checkCount() != 0 || sat.callCount() != 1 {
			t.Fatalf("imt checks %d, sbd checks %d", imtSat.checkCount(), sat.callCount())
		}
		events, texts := log.snapshot()
		if len(events) != 2 || !events[0].Running || events[0].Result != nil || events[0].FinishedAt != nil ||
			events[1].Running || *events[1].Result != checked(1, 2) || events[1].FinishedAt == nil {
			t.Fatalf("events %+v", events)
		}
		if texts[0] != "Mailbox check started" || texts[1] != "Mailbox check: 1 message received. 2 more waiting." {
			t.Fatalf("event texts %q", texts)
		}
	}
}

// A hold is an outcome, not an error.
func TestStartMailboxCheck_HeldIsAnOutcome(t *testing.T) {
	held := transport.MailboxResult{Kind: transport.MailboxHeld, Seconds: 97, MOStatus: -1}
	sat := newForcedSat(transport.MailboxCheckOutcome{Result: held})
	m := mailboxManager(map[string]Gateway{"iridium_0": NewSBDGateway(IridiumConfig{}, sat, nil, nil)})
	if err := m.StartMailboxCheck(); err != nil {
		t.Fatalf("a held check is not an error: %v", err)
	}
	if st := waitMailboxDone(t, m); *st.Result != held {
		t.Fatalf("result %+v", *st.Result)
	}
	// The next one starts again: the hold is the modem's, not the manager's.
	if err := m.StartMailboxCheck(); err != nil {
		t.Fatalf("next start: %v", err)
	}
	waitMailboxDone(t, m)
}

// The check runs on the gateway's own context, live after the caller is
// gone and ended by the gateway's Stop.
func TestStartMailboxCheck_RunsOnTheGatewaysContext(t *testing.T) {
	sat := newForcedSat(transport.MailboxCheckOutcome{Result: checked(0, 0), SessionAnswered: true})
	gw := NewSBDGateway(IridiumConfig{MailboxMode: "off"}, sat, nil, nil)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := mailboxManager(map[string]Gateway{"iridium_0": gw})
	if err := m.StartMailboxCheck(); err != nil {
		t.Fatal(err)
	}
	waitMailboxDone(t, m)
	sat.mu.Lock()
	ctx := sat.ctxs[0]
	sat.mu.Unlock()
	if ctx != gw.runContext() || ctx.Err() != nil {
		t.Fatalf("the check ran on %v (err %v), not on the gateway's live context", ctx, ctx.Err())
	}
	gw.Stop()
	if ctx.Err() == nil {
		t.Fatal("the gateway's Stop did not end the context the check runs on")
	}
}

// imtQueueSat is a 9704 transport holding app messages it was pushed.
type imtQueueSat struct {
	*ringSat
	queue [][]byte
}

func (s *imtQueueSat) ReceiveMessage() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return nil, nil
	}
	msg := s.queue[0]
	s.queue = s.queue[1:]
	return msg, nil
}

// Only an IMT gateway: no session is opened or recorded, and the messages
// the 9704 was pushed are handed over.
func TestStartMailboxCheck_IMTOnly(t *testing.T) {
	imtSat := &imtQueueSat{
		ringSat: &ringSat{fakeSat: &fakeSat{}, result: &transport.SatResult{NoSession: true}, connected: false},
		queue:   [][]byte{[]byte("one"), []byte("two")},
	}
	db := testDB(t)
	imt := NewIMTGateway(IridiumConfig{}, imtSat, db, nil)
	m := mailboxManager(map[string]Gateway{"iridium_imt_0": imt})
	if err := m.StartMailboxCheck(); err != nil {
		t.Fatal(err)
	}
	if st := waitMailboxDone(t, m); st.Result.Kind != transport.MailboxNotConnected {
		t.Fatalf("disconnected 9704: %+v", *st.Result)
	}
	imtSat.mu.Lock()
	imtSat.connected = true
	imtSat.mu.Unlock()
	if err := m.StartMailboxCheck(); err != nil {
		t.Fatal(err)
	}
	st := waitMailboxDone(t, m)
	if want := (transport.MailboxResult{Kind: transport.MailboxChecked, MOStatus: -1, Received: 2}); *st.Result != want {
		t.Fatalf("connected 9704: %+v, want %+v", *st.Result, want)
	}
	for _, want := range []string{"one", "two"} {
		if msg := <-imt.Receive(); msg.Text != want {
			t.Fatalf("inbound %q, want %q", msg.Text, want)
		}
	}
	if n := len(gssRows(t, db, "imt_gss")); n != 0 {
		t.Fatalf("%d GSS rows for a check that opened no session", n)
	}
}

// The event line in Android's words (CheckMailboxButton.kt).
func TestDescribeMailboxResult(t *testing.T) {
	for _, tc := range []struct {
		r    transport.MailboxResult
		want string
	}{
		{transport.MailboxResult{Kind: transport.MailboxHeld, Seconds: 12}, "Mailbox check held after a failed session: try again in 12 s."},
		{transport.MailboxResult{Kind: transport.MailboxSessionFailed, MOStatus: 32}, "Mailbox check: no network, the modem sees no satellite. No credit used."},
		{transport.MailboxResult{Kind: transport.MailboxSessionFailed, MOStatus: 18}, "Mailbox check: the session failed (status 18)."},
		{transport.MailboxResult{Kind: transport.MailboxNoAnswer}, "Mailbox check: the modem did not answer."},
		{transport.MailboxResult{Kind: transport.MailboxLinkLost}, "Mailbox check: the link to the modem dropped during the session; what it fetched is unknown."},
		{checked(0, 0), "Mailbox check: no new messages."},
		{checked(0, 4), "Mailbox check: no new messages. 4 more waiting."},
		{checked(3, 0), "Mailbox check: 3 messages received."},
	} {
		if got := describeMailboxResult(tc.r); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.r, got, tc.want)
		}
	}
}
