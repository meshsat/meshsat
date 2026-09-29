package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/engine"
	"meshsat/internal/gateway"
	"meshsat/internal/hubreporter"
	"meshsat/internal/transport"
)

// sosLiveServer is the Bridge with its delivery queue running: an SBD
// gateway on iridium_0 over modem, and the delivery workers started, so a
// queued test really goes to the (fake) modem. No satellite session opens.
func sosLiveServer(t *testing.T, modem transport.SatTransport) *Server {
	t.Helper()
	s := newTestServerWithDB(t)
	mgr := gateway.NewManager(s.db, modem)
	ctx, cancel := context.WithCancel(context.Background())
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := mgr.ConfigureInstance(ctx, "iridium", "iridium_0", true, `{"mailbox_mode":"off","auto_receive":false}`); err != nil {
		t.Fatal(err)
	}
	reg := channel.NewRegistry()
	channel.RegisterDefaults(reg)
	d := engine.NewDispatcher(s.db, reg, mgr, nil)
	d.Start(ctx)
	t.Cleanup(func() {
		cancel()
		d.Wait()
		mgr.Stop()
	})
	s.gwManager = mgr
	s.SetDispatcher(d)
	s.SetHubBridgeID("msa-flaneur")
	return s
}

// stallSat is a 9603 whose first frame stays on the modem until the test
// ends that session, as a failure (mo_status 32, no network); texts and
// later frames go at once. An SOS's own frame is counted apart and always
// goes. [MESHSAT-1446]
type stallSat struct {
	*apiSat
	onModem chan struct{} // closed when the first frame is on the modem
	finish  chan struct{} // closing it ends that session, failed
	mu      sync.Mutex
	frames  int
	sos     int
}

func (m *stallSat) Send(_ context.Context, data []byte) (*transport.SatResult, error) {
	m.mu.Lock()
	if hubreporter.IsSatSOS(data) {
		m.sos++
		m.mu.Unlock()
		return &transport.SatResult{MOStatus: 0}, nil
	}
	m.frames++
	first := m.frames == 1
	m.mu.Unlock()
	if first {
		close(m.onModem)
		<-m.finish
		return &transport.SatResult{MOStatus: 32}, nil
	}
	return &transport.SatResult{MOStatus: 0}, nil
}
func (m *stallSat) SendText(context.Context, string) (*transport.SatResult, error) {
	return &transport.SatResult{MOStatus: 0}, nil
}
func (m *stallSat) frameSessions() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.frames
}
func (m *stallSat) sosSessions() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sos
}

// A test already on the modem when an SOS starts cannot be cancelled, but
// if that session fails it is not tried again: the SOS gives it a deadline
// of now, so the retry it comes back as is never handed to the delivery
// worker and the reaper expires it. It came back minutes later, in the
// middle of the emergency, and spent a credit on a test position at the
// Hub. [MESHSAT-1430] The SOS's own frame goes on the same modem once the
// test's session is over. [MESHSAT-1446]
func TestSOSTest_ATestOnTheModemWhenAnSOSStartsIsNotTriedAgain(t *testing.T) {
	modem := &stallSat{apiSat: &apiSat{kind: "sbd", connected: true}, onModem: make(chan struct{}), finish: make(chan struct{})}
	s := sosLiveServer(t, modem)
	_, del := postSatTest(t, s, sosTestSatBody)
	select {
	case <-modem.onModem:
	case <-time.After(10 * time.Second):
		close(modem.finish)
		t.Fatal("the delivery worker never put the test on the modem")
	}
	if st, _ := rowStatus(t, s, del.ID); st != "sending" {
		close(modem.finish)
		t.Fatalf("the test on the modem is %s, want sending", st)
	}

	if !s.TriggerSOS("hold") {
		close(modem.finish)
		t.Fatal("the SOS was refused")
	}
	t.Cleanup(stopSOS(s))
	if st, _ := rowStatus(t, s, del.ID); st != "sending" {
		close(modem.finish)
		t.Fatalf("the SOS changed the test on the modem to %s; a send in progress is left to finish", st)
	}

	close(modem.finish) // the session fails
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, lastErr := rowStatus(t, s, del.ID)
		if st == "retry" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the failed session the test is %s %q, want a retry", st, lastErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The retry's time comes: the worker does not get it, over more than
	// one of its rounds, and the reaper expires it.
	setRow(t, s, del.ID, `next_retry = ?`, time.Now().UTC().Add(-time.Minute).Format(sqliteTime))
	if pendingOn(t, s, "iridium_0", del.ID) {
		t.Fatal("the failed test is handed to the delivery worker again")
	}
	time.Sleep(2500 * time.Millisecond)
	if n := modem.frameSessions(); n != 1 {
		t.Fatalf("the modem got %d sessions for the test, want 1", n)
	}
	if n := modem.sosSessions(); n != 1 {
		t.Fatalf("the modem got %d sessions for the SOS's own frame, want 1", n)
	}
	if n, err := s.db.ExpireDeliveries(); err != nil || n != 1 {
		t.Fatalf("the reaper expired %d (%v)", n, err)
	}
	if st, _ := rowStatus(t, s, del.ID); st != "expired" {
		t.Fatalf("the test is %s, want expired", st)
	}
}

// triggerSOSWithin starts an SOS and fails the test if that takes longer
// than limit: the Linux app gives POST /api/sos/activate 8 seconds, and
// marks the routes "Not sent" when it runs out.
func triggerSOSWithin(t *testing.T, s *Server, limit time.Duration) {
	t.Helper()
	started := make(chan bool, 1)
	go func() { started <- s.TriggerSOS("hold") }()
	select {
	case ok := <-started:
		if !ok {
			t.Fatal("the SOS was refused")
		}
	case <-time.After(limit):
		t.Fatalf("starting the SOS took over %v", limit)
	}
}

// Starting an SOS does not wait for a test of the alarm stuck on its
// gateway lookup: the lookup (and the position and the frame) are worked
// out before sosTestMu, which the SOS start takes to cancel waiting tests.
// The test, once its lookup returns, finds the SOS and answers 409 without
// queueing anything. [MESHSAT-1430]
func TestSOSTest_AnSOSDoesNotWaitForATestOnItsGatewayLookup(t *testing.T) {
	s := sosSatServer(t, &apiSat{kind: "sbd", connected: true}, nil)
	inLookup, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	letGo := func() { once.Do(func() { close(release) }) }
	s.satTestIfaceFn = func() string {
		close(inLookup)
		<-release
		return "iridium_0"
	}
	answer := make(chan *httptest.ResponseRecorder, 1)
	go func() { answer <- post(t, s, "/api/sos/test", sosTestSatBody) }()
	<-inLookup

	t.Cleanup(stopSOS(s))
	t.Cleanup(letGo)
	triggerSOSWithin(t, s, 2*time.Second)

	letGo()
	w := <-answer
	if w.Code != http.StatusConflict {
		t.Fatalf("the test after the SOS began: %d %s, want 409", w.Code, w.Body.String())
	}
	if n := testDeliveryCount(t, s); n != 0 {
		t.Fatalf("%d tests queued", n)
	}
}

// holdSat is a 9603 whose first text stays on the modem, whatever its
// context says, until the test lets it go; the gateway's Stop then waits for
// it, and the gateway manager holds its lock across that Stop.
type holdSat struct {
	*apiSat
	onModem  chan struct{} // closed when the first text is on the modem
	stopping chan struct{} // closed when the gateway's context ends meanwhile
	release  chan struct{}
	once     sync.Once
}

func (m *holdSat) SendText(ctx context.Context, _ string) (*transport.SatResult, error) {
	m.once.Do(func() {
		close(m.onModem)
		select {
		case <-ctx.Done():
			close(m.stopping)
		case <-m.release:
		}
		<-m.release
	})
	return &transport.SatResult{MOStatus: 0}, nil
}

// The same with the gateway manager's own lock: a gateway being stopped
// while its modem is in a session holds the manager's lock for that whole
// session (ConfigureInstance stops the old gateway under it). The SOS start
// waits for nothing under that lock: not for the TAK gateway lookup (it
// runs in the background now), not for a test stuck on its lookup.
// [MESHSAT-1430]
func TestSOS_StartsWhileTheGatewayManagerIsBusy(t *testing.T) {
	modem := &holdSat{apiSat: &apiSat{kind: "sbd", connected: true},
		onModem: make(chan struct{}), stopping: make(chan struct{}), release: make(chan struct{})}
	s := newTestServerWithDB(t)
	mgr := gateway.NewManager(s.db, modem)
	ctx, cancel := context.WithCancel(context.Background())
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		mgr.Stop()
		cancel()
	})
	const cfg = `{"mailbox_mode":"off","auto_receive":false}`
	if err := mgr.ConfigureInstance(ctx, "iridium", "iridium_0", true, cfg); err != nil {
		t.Fatal(err)
	}
	s.gwManager = mgr
	reg := channel.NewRegistry()
	channel.RegisterDefaults(reg)
	s.SetDispatcher(engine.NewDispatcher(s.db, reg, mgr, nil))
	s.SetHubBridgeID("msa-flaneur")

	// A text on the modem, then the gateway reconfigured: the manager takes
	// its lock and waits in the old gateway's Stop for that session.
	if err := mgr.GatewayByInterfaceID("iridium_0").Enqueue(&transport.MeshMessage{PortNum: 1, DecodedText: "hello from the kit"}); err != nil {
		t.Fatal(err)
	}
	<-modem.onModem
	reconfigured := make(chan error, 1)
	go func() { reconfigured <- mgr.ConfigureInstance(ctx, "iridium", "iridium_0", true, cfg) }()
	select {
	case <-modem.stopping:
	case <-time.After(5 * time.Second):
		close(modem.release)
		t.Fatal("the gateway was never stopped")
	}
	answer := make(chan *httptest.ResponseRecorder, 1)
	go func() { answer <- post(t, s, "/api/sos/test", sosTestSatBody) }()
	time.Sleep(100 * time.Millisecond) // the test is now waiting on the manager's lock

	t.Cleanup(stopSOS(s))
	started := make(chan bool, 1)
	go func() { started <- s.TriggerSOS("hold") }()
	select {
	case ok := <-started:
		if !ok {
			close(modem.release)
			t.Fatal("the SOS was refused")
		}
	case <-time.After(2 * time.Second):
		close(modem.release)
		t.Fatal("starting the SOS waited on the gateway manager for over 2s")
	}

	close(modem.release)
	if err := <-reconfigured; err != nil {
		t.Fatal(err)
	}
	// The test, let go: the SOS is on (409), or the gateway was not back
	// yet when it looked (503). Either way nothing is queued.
	if w := <-answer; w.Code != http.StatusConflict && w.Code != http.StatusServiceUnavailable {
		t.Fatalf("the test after the SOS began: %d %s, want 409 or 503", w.Code, w.Body.String())
	}
	if n := testDeliveryCount(t, s); n != 0 {
		t.Fatalf("%d tests queued", n)
	}
}
