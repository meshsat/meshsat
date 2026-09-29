package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/gateway"
	"meshsat/internal/transport"
)

// apiSat is a satellite transport for the mailbox and signal endpoints:
// fixed readings, and a scripted mailbox check that can be held until the
// test releases it.
type apiSat struct {
	mu        sync.Mutex
	kind      string // sbd | imt
	connected bool
	bars      int // AT+CSQ
	fastBars  int // AT+CSQF
	outcome   transport.MailboxCheckOutcome
	release   chan struct{}
	ran       chan context.Context
	ctxErrEnd error // the check's context error when it ended
	checks    int   // the forced check
	polls     int   // MailboxCheck
}

func (s *apiSat) Subscribe(context.Context) (<-chan transport.SatEvent, error) {
	return nil, errors.New("no events in this test")
}
func (s *apiSat) Send(context.Context, []byte) (*transport.SatResult, error) {
	return nil, errors.New("not in this test")
}
func (s *apiSat) SendText(context.Context, string) (*transport.SatResult, error) {
	return nil, errors.New("not in this test")
}
func (s *apiSat) Receive(context.Context) ([]byte, error) { return nil, nil }
func (s *apiSat) MailboxCheck(context.Context) (*transport.SatResult, error) {
	s.mu.Lock()
	s.polls++
	s.mu.Unlock()
	return &transport.SatResult{NoSession: true}, nil
}
func (s *apiSat) GetSignal(context.Context) (*transport.SignalInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &transport.SignalInfo{Bars: s.bars, Timestamp: "2026-09-29T10:00:00Z", Assessment: signalAssessment(s.bars), Source: s.kind}, nil
}
func (s *apiSat) GetSignalFast(context.Context) (*transport.SignalInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &transport.SignalInfo{Bars: s.fastBars, Timestamp: "2026-09-29T10:00:00Z", Assessment: signalAssessment(s.fastBars), Source: s.kind}, nil
}
func (s *apiSat) GetStatus(context.Context) (*transport.SatStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &transport.SatStatus{Connected: s.connected, Type: s.kind, IMEI: "300434067943980"}, nil
}
func (s *apiSat) GetFirmwareVersion(context.Context) (string, error) { return "TA19002", nil }
func (s *apiSat) Close() error                                       { return nil }

// CheckMailboxNow makes apiSat a 9603 that runs the check itself.
func (s *apiSat) CheckMailboxNow(ctx context.Context) transport.MailboxCheckOutcome {
	s.mu.Lock()
	s.checks++
	out, release, ran := s.outcome, s.release, s.ran
	s.mu.Unlock()
	if ran != nil {
		ran <- ctx
	}
	if release != nil {
		<-release
	}
	s.mu.Lock()
	s.ctxErrEnd = ctx.Err()
	s.mu.Unlock()
	return out
}

func (s *apiSat) counts() (checks, polls int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checks, s.polls
}

type mailboxEvents struct {
	mu     sync.Mutex
	states []gateway.MailboxCheckState
}

func (e *mailboxEvents) emit(eventType, _ string, data json.RawMessage) {
	if eventType != "mailbox" {
		return
	}
	var st gateway.MailboxCheckState
	if json.Unmarshal(data, &st) == nil {
		e.mu.Lock()
		e.states = append(e.states, st)
		e.mu.Unlock()
	}
}

func (e *mailboxEvents) all() []gateway.MailboxCheckState {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]gateway.MailboxCheckState(nil), e.states...)
}

// mailboxServer runs a real gateway manager with an SBD gateway on sbd
// and an IMT gateway on imt (either may be nil), as main.go wires them.
func mailboxServer(t *testing.T, sbd, imt *apiSat) (*Server, *mailboxEvents) {
	t.Helper()
	s := newTestServerWithDB(t)
	var sat transport.SatTransport
	if sbd != nil {
		sat = sbd
	}
	mgr := gateway.NewManager(s.db, sat)
	if imt != nil {
		mgr.SetIMTTransport(imt)
	}
	events := &mailboxEvents{}
	mgr.SetEventDataEmitFunc(events.emit)
	ctx, cancel := context.WithCancel(context.Background())
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		mgr.Stop()
		cancel()
	})
	if sbd != nil {
		if err := mgr.ConfigureInstance(ctx, "iridium", "iridium_0", true, `{"mailbox_mode":"off","auto_receive":false}`); err != nil {
			t.Fatalf("configure iridium: %v", err)
		}
	}
	if imt != nil {
		if err := mgr.ConfigureInstance(ctx, "iridium_imt", "iridium_imt_0", true, `{}`); err != nil {
			t.Fatalf("configure iridium_imt: %v", err)
		}
	}
	s.gwManager = mgr
	return s, events
}

func mailboxGET(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func mailboxState(t *testing.T, s *Server) gateway.MailboxCheckState {
	t.Helper()
	w := mailboxGET(t, s, "/api/iridium/mailbox")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/iridium/mailbox: %d %s", w.Code, w.Body.String())
	}
	var st gateway.MailboxCheckState
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("GET /api/iridium/mailbox: %v (%s)", err, w.Body.String())
	}
	return st
}

func mailboxFinished(t *testing.T, s *Server) gateway.MailboxCheckState {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st := mailboxState(t, s); !st.Running && st.Result != nil {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the mailbox check did not finish")
	return gateway.MailboxCheckState{}
}

func gssCount(t *testing.T, s *Server, source string) int {
	t.Helper()
	pts, err := s.db.GetSignalHistoryRaw(source, 0, time.Now().Unix()+60, 100)
	if err != nil {
		t.Fatal(err)
	}
	return len(pts)
}

// POST starts the check and answers at once; a second POST while it runs
// is refused with 409; GET follows it from running to its outcome, which a
// "mailbox" event carries too.
func TestMailboxAPI_StartConflictAndOutcome(t *testing.T) {
	sbd := &apiSat{kind: "sbd", connected: true, release: make(chan struct{}), ran: make(chan context.Context, 1),
		outcome: transport.MailboxCheckOutcome{
			Result:          transport.MailboxResult{Kind: transport.MailboxChecked, MOStatus: 0, Received: 1, StillQueued: 2},
			Messages:        [][]byte{[]byte("hello")},
			SessionAnswered: true,
		}}
	s, events := mailboxServer(t, sbd, nil)

	if w := mailboxGET(t, s, "/api/iridium/mailbox"); w.Code != http.StatusOK ||
		strings.TrimSpace(w.Body.String()) != `{"running":false,"result":null,"finished_at":null}` {
		t.Fatalf("before the first check: %d %s", w.Code, w.Body.String())
	}

	w := post(t, s, "/api/iridium/mailbox/check", "")
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"status":"mailbox check started"}` {
		t.Fatalf("POST: %d %s", w.Code, w.Body.String())
	}
	select {
	case <-sbd.ran:
	case <-time.After(3 * time.Second):
		t.Fatal("the check never reached the modem")
	}
	if st := mailboxState(t, s); !st.Running || st.Result != nil {
		t.Fatalf("while running: %+v", st)
	}
	w = post(t, s, "/api/iridium/mailbox/check", "")
	if w.Code != http.StatusConflict || strings.TrimSpace(w.Body.String()) != `{"error":"a mailbox check is already running"}` {
		t.Fatalf("second POST: %d %s", w.Code, w.Body.String())
	}

	close(sbd.release)
	st := mailboxFinished(t, s)
	if want := (transport.MailboxResult{Kind: "checked", MOStatus: 0, Received: 1, StillQueued: 2}); *st.Result != want {
		t.Fatalf("result %+v, want %+v", *st.Result, want)
	}
	if st.FinishedAt == nil {
		t.Fatal("finished_at missing")
	}
	if at, err := time.Parse(time.RFC3339, *st.FinishedAt); err != nil || time.Since(at) > time.Minute {
		t.Fatalf("finished_at %q: %v", *st.FinishedAt, err)
	}
	raw := mailboxGET(t, s, "/api/iridium/mailbox").Body.String()
	for _, key := range []string{`"kind":"checked"`, `"seconds":0`, `"mo_status":0`, `"received":1`, `"still_queued":2`} {
		if !strings.Contains(raw, key) {
			t.Fatalf("GET body %s lacks %s", raw, key)
		}
	}
	if n := gssCount(t, s, "sbd_gss"); n != 1 {
		t.Fatalf("%d GSS rows for one answered session", n)
	}
	got := events.all()
	if len(got) != 2 || !got[0].Running || got[1].Running || got[1].Result == nil || got[1].Result.Kind != "checked" {
		t.Fatalf("mailbox events %+v", got)
	}
	if checks, _ := sbd.counts(); checks != 1 {
		t.Fatalf("%d checks on the modem, want 1", checks)
	}

	// The next check: while it runs there is no result (as on Android), then its own.
	sbd.mu.Lock()
	sbd.release = make(chan struct{})
	sbd.outcome = transport.MailboxCheckOutcome{
		Result:          transport.MailboxResult{Kind: transport.MailboxSessionFailed, MOStatus: 32},
		SessionAnswered: true,
	}
	release := sbd.release
	sbd.mu.Unlock()
	if w := post(t, s, "/api/iridium/mailbox/check", ""); w.Code != http.StatusOK {
		t.Fatalf("POST for the next check: %d %s", w.Code, w.Body.String())
	}
	<-sbd.ran
	if w := mailboxGET(t, s, "/api/iridium/mailbox"); strings.TrimSpace(w.Body.String()) != `{"running":true,"result":null,"finished_at":null}` {
		t.Fatalf("while the next check runs: %s", w.Body.String())
	}
	close(release)
	if st := mailboxFinished(t, s); *st.Result != (transport.MailboxResult{Kind: "session_failed", MOStatus: 32}) {
		t.Fatalf("next result %+v", *st.Result)
	}
}

// The check runs on the Bridge's own context: it completes after the
// request that started it is over and its context cancelled (trap 2).
func TestMailboxAPI_OutlivesTheRequest(t *testing.T) {
	sbd := &apiSat{kind: "sbd", connected: true, release: make(chan struct{}), ran: make(chan context.Context, 1),
		outcome: transport.MailboxCheckOutcome{
			Result:          transport.MailboxResult{Kind: transport.MailboxChecked},
			SessionAnswered: true,
		}}
	s, _ := mailboxServer(t, sbd, nil)

	reqCtx, cancelReq := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/api/iridium/mailbox/check", nil).WithContext(reqCtx)
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, req)
	cancelReq() // what net/http does once the handler returned
	if w.Code != http.StatusOK {
		t.Fatalf("POST: %d %s", w.Code, w.Body.String())
	}

	var checkCtx context.Context
	select {
	case checkCtx = <-sbd.ran:
	case <-time.After(3 * time.Second):
		t.Fatal("the check never reached the modem")
	}
	if checkCtx.Err() != nil {
		t.Fatalf("the check runs on a context that ended with the request: %v", checkCtx.Err())
	}
	close(sbd.release)
	if st := mailboxFinished(t, s); st.Result.Kind != transport.MailboxChecked {
		t.Fatalf("result %+v", *st.Result)
	}
	sbd.mu.Lock()
	endErr := sbd.ctxErrEnd
	sbd.mu.Unlock()
	if endErr != nil {
		t.Fatalf("the check's context ended before the check: %v", endErr)
	}
}

// Without an Iridium gateway: 503 as before, and the kept outcome is
// not_connected, with no GSS row written.
func TestMailboxAPI_NoGateway(t *testing.T) {
	s, events := mailboxServer(t, nil, nil)
	w := post(t, s, "/api/iridium/mailbox/check", "")
	if w.Code != http.StatusServiceUnavailable || strings.TrimSpace(w.Body.String()) != `{"error":"iridium gateway not running"}` {
		t.Fatalf("POST: %d %s", w.Code, w.Body.String())
	}
	st := mailboxState(t, s)
	if st.Running || st.Result == nil || st.Result.Kind != transport.MailboxNotConnected || st.Result.MOStatus != -1 || st.FinishedAt == nil {
		t.Fatalf("state %+v", st)
	}
	for _, source := range []string{"sbd_gss", "imt_gss", "gss"} {
		if n := gssCount(t, s, source); n != 0 {
			t.Fatalf("%d %s rows without a session", n, source)
		}
	}
	if got := events.all(); len(got) != 1 || got[0].Result == nil || got[0].Result.Kind != transport.MailboxNotConnected {
		t.Fatalf("events %+v", got)
	}
}

// A check within the hold after a failed session is an answer, not an
// error: 200, then the result held with its seconds, and no GSS row.
func TestMailboxAPI_HeldIsAResult(t *testing.T) {
	sbd := &apiSat{kind: "sbd", connected: true, outcome: transport.MailboxCheckOutcome{
		Result: transport.MailboxResult{Kind: transport.MailboxHeld, Seconds: 97, MOStatus: -1},
	}}
	s, _ := mailboxServer(t, sbd, nil)
	if w := post(t, s, "/api/iridium/mailbox/check", ""); w.Code != http.StatusOK {
		t.Fatalf("POST: %d %s", w.Code, w.Body.String())
	}
	st := mailboxFinished(t, s)
	if want := (transport.MailboxResult{Kind: "held", Seconds: 97, MOStatus: -1}); *st.Result != want {
		t.Fatalf("result %+v, want %+v", *st.Result, want)
	}
	if n := gssCount(t, s, "sbd_gss"); n != 0 {
		t.Fatalf("%d GSS rows for a held check", n)
	}
}

// With an SBD and an IMT gateway both running, the check runs on the SBD one.
func TestMailboxAPI_SBDFirst(t *testing.T) {
	sbd := &apiSat{kind: "sbd", connected: true, outcome: transport.MailboxCheckOutcome{
		Result: transport.MailboxResult{Kind: transport.MailboxChecked}, SessionAnswered: true,
	}}
	imt := &apiSat{kind: "imt", connected: true}
	s, _ := mailboxServer(t, sbd, imt)
	for i := 0; i < 5; i++ {
		if w := post(t, s, "/api/iridium/mailbox/check", ""); w.Code != http.StatusOK {
			t.Fatalf("POST: %d %s", w.Code, w.Body.String())
		}
		mailboxFinished(t, s)
	}
	if checks, _ := sbd.counts(); checks != 5 {
		t.Fatalf("SBD checked %d times, want 5", checks)
	}
	if _, polls := imt.counts(); polls != 0 {
		t.Fatalf("the IMT gateway was polled %d times", polls)
	}
}

// Without a gateway manager both endpoints answer 503.
func TestMailboxAPI_NoManager(t *testing.T) {
	s := newTestServerWithDB(t)
	if w := post(t, s, "/api/iridium/mailbox/check", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST: %d", w.Code)
	}
	if w := mailboxGET(t, s, "/api/iridium/mailbox"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET: %d", w.Code)
	}
}

func signalBars(t *testing.T, s *Server, path string) (int, string) {
	t.Helper()
	w := mailboxGET(t, s, path)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
	}
	var sig struct {
		Bars   int    `json:"bars"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &sig); err != nil {
		t.Fatal(err)
	}
	return sig.Bars, sig.Source
}

// ?type= reads one modem; without it the endpoints pick as before (the
// connected one, the 9704 first); anything else is a 400.
func TestSignalAPI_PerModem(t *testing.T) {
	sbd := &apiSat{kind: "sbd", connected: true, bars: 2, fastBars: 1}
	imt := &apiSat{kind: "imt", connected: true, bars: 4, fastBars: 3}
	s := newTestServerWithDB(t)
	mgr := gateway.NewManager(s.db, sbd)
	mgr.SetIMTTransport(imt)
	s.gwManager = mgr

	for _, tc := range []struct {
		path   string
		bars   int
		source string
	}{
		{"/api/iridium/signal", 4, "imt"},
		{"/api/iridium/signal?type=sbd", 2, "sbd"},
		{"/api/iridium/signal?type=imt", 4, "imt"},
		{"/api/iridium/signal/fast", 3, "imt"},
		{"/api/iridium/signal/fast?type=sbd", 1, "sbd"},
		{"/api/iridium/signal/fast?type=imt", 3, "imt"},
	} {
		if bars, source := signalBars(t, s, tc.path); bars != tc.bars || source != tc.source {
			t.Errorf("%s: %d bars from %q, want %d from %q", tc.path, bars, source, tc.bars, tc.source)
		}
	}
	for _, path := range []string{"/api/iridium/signal?type=9603", "/api/iridium/signal/fast?type=SBD"} {
		if w := mailboxGET(t, s, path); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "type must be sbd or imt") {
			t.Errorf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
}

// A modem that is not there is a 503 for its type only.
func TestSignalAPI_MissingModem(t *testing.T) {
	sbd := &apiSat{kind: "sbd", connected: true, bars: 2, fastBars: 2}
	s := newTestServerWithDB(t)
	s.gwManager = gateway.NewManager(s.db, sbd)
	for _, path := range []string{"/api/iridium/signal?type=imt", "/api/iridium/signal/fast?type=imt"} {
		if w := mailboxGET(t, s, path); w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if bars, _ := signalBars(t, s, "/api/iridium/signal/fast"); bars != 2 {
		t.Errorf("default: %d bars", bars)
	}
}

// The fast reading's fallback to the last recorded bars uses that modem's
// readings only when a type is asked for; without one it takes any, as before.
func TestSignalFastAPI_FallbackStaysWithTheModem(t *testing.T) {
	sbd := &apiSat{kind: "sbd", connected: true, fastBars: 0}
	s := newTestServerWithDB(t)
	s.gwManager = gateway.NewManager(s.db, sbd)
	now := time.Now().Unix()
	if err := s.db.InsertSignalHistory("sbd", now-120, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.db.InsertSignalHistory("imt", now-60, 5); err != nil {
		t.Fatal(err)
	}
	if bars, _ := signalBars(t, s, "/api/iridium/signal/fast?type=sbd"); bars != 3 {
		t.Errorf("type=sbd: %d bars, want the 9603's last 3", bars)
	}
	if bars, _ := signalBars(t, s, "/api/iridium/signal/fast"); bars != 5 {
		t.Errorf("no type: %d bars, want the newest of any modem (5), as before", bars)
	}
}
