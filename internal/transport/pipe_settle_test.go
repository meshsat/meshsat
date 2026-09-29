package transport

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

// Re-review 2 of B9 (MESHSAT-1391): the settle of a session whose answer
// never reached this Bridge while the link stayed up, a session-in-flight
// flag the node never clears, the node let go while sends are queued, a
// mark a set-up made while a send settled, the Bridge stopping mid-settle,
// and the order the owners are told in.

// Over the node's pipe the SBDIX answer never reaches this Bridge while the
// link stays up: the notification carrying it is lost (the node read it off
// its modem: MO status 0), and the read times out. The send is settled from
// the node's account (sent, once), not a failure the queue retries into a
// second billed session.
func TestPipeSettle_AnAnswerLostWhileTheLinkStaysUpIsSettled(t *testing.T) {
	t.Setenv("IRIDIUM_SBDIX_TIMEOUT", "2")
	m := newNodeModem()
	m.sbdixDeferred = true
	b := newSimBridge(t, m)
	b.connect()
	b.ready()
	sent := queuedSend{"hello", false}.start(b.sat)
	pipeEventually(t, 10*time.Second, "the session under way", func() bool { return m.count("AT+SBDIX") == 1 })
	b.node.set(func(n *simNode) { n.dropTX = true }) // the answer's notification is lost
	m.answerSBDIX()
	o := await(t, sent, "the send")
	res, err := o.res, o.err
	if err != nil || res == nil || res.MOStatus != 0 || res.MOMSN != 219 {
		t.Fatalf("send: %+v, %v; want MO status 0, MOMSN 219 from the node's account", res, err)
	}
	if n := m.count("AT+SBDIX"); n != 1 {
		t.Fatalf("%d sessions for one message", n)
	}
	b.node.set(func(n *simNode) { n.dropTX = false })
	pipeEventually(t, 10*time.Second, "the line back", b.sat.IsConnected)
}

// The modem's answer reaches the node intact and this Bridge damaged: an
// +SBDIX line it cannot read, or bytes past the most an answer can be. The
// node read it: settled, sent once.
func TestPipeSettle_AnAnswerThisBridgeCannotReadIsSettled(t *testing.T) {
	for _, tc := range []struct {
		name string
		with []byte
	}{
		{"an unreadable +SBDIX line", []byte("+SBDIX: 0, 2")},
		{"an answer too large to be one", bytes.Repeat([]byte("x"), 5000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newNodeModem()
			b := newSimBridge(t, m)
			b.connect()
			b.ready()
			b.node.set(func(n *simNode) {
				n.mangleTX = func(v []byte) []byte {
					return bytes.ReplaceAll(v, []byte("+SBDIX: 0, 219, 0, 0, 0, 0"), tc.with)
				}
			})
			res, err := b.sat.SendText(context.Background(), "hello")
			if err != nil || res == nil || res.MOStatus != 0 || res.MOMSN != 219 {
				t.Fatalf("send: %+v, %v; want MO status 0, MOMSN 219 from the node's account", res, err)
			}
			if n := m.count("AT+SBDIX"); n != 1 {
				t.Fatalf("%d sessions for one message", n)
			}
		})
	}
}

// The modem never answers a session. While this Bridge holds the modem the
// node never clears its session-in-flight flag, and every later session
// waited for it: no send could start. Past pipeFlightCap the flag no
// longer counts (the session ends unknown, the next one starts), and the
// claim loop gives the modem back so the node, past its own cap, clears it,
// then takes it again.
func TestPipeSettle_AFlagTheNodeNeverClearsNeitherBlocksNorStays(t *testing.T) {
	t.Setenv("IRIDIUM_SBDIX_TIMEOUT", "1")
	flightCap, first, retry, unstick := pipeFlightCap, pipeClaimFirstRetry, pipeClaimRetry, pipeUnstickWait
	pipeFlightCap, pipeClaimFirstRetry, pipeClaimRetry, pipeUnstickWait = 300*time.Millisecond, 100*time.Millisecond, 100*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() {
		pipeFlightCap, pipeClaimFirstRetry, pipeClaimRetry, pipeUnstickWait = flightCap, first, retry, unstick
	})
	m := newNodeModem()
	m.sbdixSilent = true
	b := newSimBridge(t, m)
	b.node.set(func(n *simNode) { n.sessionCap = 500 * time.Millisecond })
	b.connect()
	b.ready()

	_, err := b.sat.SendText(context.Background(), "first")
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("a session the modem never answered: %v; want an unknown outcome", err)
	}
	// The claim loop gives the modem back, the node clears the flag past
	// its cap and lets go, and the claim takes it again.
	pipeEventually(t, 10*time.Second, "the node cleared its flag and gave the modem again", func() bool {
		log := b.node.eventLog()
		sbdix := pipeCmdPos(log, "sbdix")
		rest := log[sbdix+1:]
		u, r := pipeCmdPos(rest, "unsubscribe"), pipeCmdPos(rest, "release")
		return u >= 0 && r > u && pipeCmdPos(rest[r+1:], "take") >= 0
	})
	b.node.mu.Lock()
	inFlight := b.node.inFlight
	b.node.mu.Unlock()
	if inFlight {
		t.Fatal("the node's flag is still set")
	}
	pipeEventually(t, 10*time.Second, "the line back", b.sat.IsConnected)

	m.set(func(m *nodeModem) { m.sbdixSilent = false })
	b.sat.mu.Lock()
	b.sat.lastSBDIX = time.Time{}
	b.sat.mu.Unlock()
	if res, err := b.sat.SendText(context.Background(), "next"); err != nil || !res.MOSuccess() {
		t.Fatalf("the next send: %+v, %v", res, err)
	}
	if n := m.count("AT+SBDIX"); n != 2 {
		t.Fatalf("%d sessions, want 2", n)
	}
}

// With the flag stuck and the modem given back not yet, sessions start all
// the same once past pipeFlightCap: the session gate uses the cap port uses.
func TestPipeSettle_AStuckFlagDoesNotHoldTheNextSessionBack(t *testing.T) {
	t.Setenv("IRIDIUM_SBDIX_TIMEOUT", "1")
	flightCap := pipeFlightCap
	pipeFlightCap = 300 * time.Millisecond
	t.Cleanup(func() { pipeFlightCap = flightCap })
	m := newNodeModem()
	m.sbdixSilent = true
	b := newSimBridge(t, m) // the claim loop's rounds stay 15 s apart: no give-back here
	b.connect()
	b.ready()
	if _, err := b.sat.SendText(context.Background(), "first"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("first send: %v", err)
	}
	pipeEventually(t, 10*time.Second, "the line back past the cap", b.sat.IsConnected)
	m.set(func(m *nodeModem) { m.sbdixSilent = false })
	for i := 0; i < 2; i++ {
		b.sat.mu.Lock()
		b.sat.lastSBDIX = time.Time{}
		b.sat.mu.Unlock()
		if res, err := b.sat.SendText(context.Background(), "next"); err != nil || !res.MOSuccess() {
			t.Fatalf("send %d: %+v, %v (not connected=%v)", i+2, res, err, errors.Is(err, ErrNotConnected))
		}
	}
	if n := m.count("AT+SBDIX"); n != 3 {
		t.Fatalf("%d sessions, want 3", n)
	}
}

// Forgetting the node stops new sessions first: a send queued behind the
// one in flight finds the line gone (not connected, deferred) instead of
// opening another billed session on the node being forgotten, and the
// forget answers once the session in flight has.
func TestPipeRelease_ForgetStopsQueuedSendsFirst(t *testing.T) {
	m := newNodeModem()
	m.sbdixDeferred = true
	b := newSimBridge(t, m)
	b.connect()
	b.ready()

	first := queuedSend{"first", false}.start(b.sat)
	pipeEventually(t, 10*time.Second, "the first session under way", func() bool { return m.count("AT+SBDIX") == 1 })
	second := queuedSend{"second", false}.start(b.sat) // queued on the session lock
	time.Sleep(100 * time.Millisecond)
	forgot := make(chan error, 1)
	go func() { forgot <- b.mesh.BLEForget(context.Background(), false) }()
	time.Sleep(100 * time.Millisecond)
	m.answerSBDIX()
	if o := await(t, first, "the first send"); o.err != nil || !o.res.MOSuccess() {
		t.Fatalf("first: %+v, %v", o.res, o.err)
	}
	o2 := await(t, second, "the second send")
	if !errors.Is(o2.err, ErrNotConnected) {
		t.Fatalf("the queued send: %+v, %v; want not connected (deferred, no session)", o2.res, o2.err)
	}
	if err := await(t, forgot, "the forget"); err != nil {
		t.Fatal(err)
	}
	if n := m.count("AT+SBDIX"); n != 1 {
		t.Fatalf("%d sessions on the node being forgotten, want 1", n)
	}
	if n := m.count("AT+SBDWT=second"); n != 0 {
		t.Fatalf("the queued send loaded its message: %v", m.sent())
	}
}

// A settle keeps the mark a set-up left while it waited (a later link read
// the buffer and its clear did not go through); with no set-up meanwhile, a
// session that ran drops an older mark (it emptied the buffer as it
// started).
func TestPipeSettle_AMarkTheSetUpLeftMeanwhileIsKept(t *testing.T) {
	ip := func(v int) *int { return &v }
	up := func(v uint32) *uint32 { return &v }
	before := PipeStats{Sessions: 1, UptimeS: 100, LastMOStatus: ip(0), LastMOMSN: 5, LastSessionAgeS: up(50)}
	after := PipeStats{Sessions: 2, UptimeS: 130, LastMOStatus: ip(0), LastMOMSN: 6, LastSessionAgeS: up(2)}
	for _, setUp := range []bool{true, false} {
		tr := NewDirectSatTransport("ble")
		older := newMTReadMark("300434067943980", 3, []byte("read before the session"))
		newer := newMTReadMark("300434067943980", 7, []byte("read by the set-up meanwhile"))
		tr.mtStale = older
		tr.nodeStats = func(context.Context) (*PipeStats, error) {
			if setUp {
				// The pipe loop's set-up ran meanwhile, on a new link.
				tr.mu.Lock()
				tr.connGen++
				tr.mtStale = newer
				tr.mu.Unlock()
			}
			a := after
			return &a, nil
		}
		now := time.Now()
		mark := &pipeSessionMark{before: before, readAt: now, writtenAt: now}
		tr.sessionMu.Lock()
		tr.mu.Lock()
		res, err := tr.settleLocked(mark, errors.New("cut"))
		stale := tr.mtStale
		tr.mu.Unlock()
		tr.sessionMu.Unlock()
		if err != nil || res == nil || !res.MOSuccess() {
			t.Fatalf("set-up %v: %+v, %v", setUp, res, err)
		}
		if setUp && stale != newer {
			t.Fatal("the settle wiped the mark the set-up left meanwhile")
		}
		if !setUp && stale != nil {
			t.Fatal("the session ran, and the older mark stayed")
		}
	}
}

// The Bridge stops while a send waits for the node's account: it gives up
// at once, its outcome unknown, so its delivery ends "may have been sent"
// within the shutdown's drain rather than 'sending' for the next start to
// send again.
func TestPipeSettle_TheBridgeStoppingEndsAWaitingSettle(t *testing.T) {
	shortOutcome(t, time.Minute)
	m := newNodeModem()
	m.sbdixDeferred = true
	b := newSimBridge(t, m)
	life, stop := context.WithCancel(context.Background())
	defer stop()
	b.sat.SetLifetime(life)
	link := b.connect()
	b.ready()
	sent := queuedSend{"hello", false}.start(b.sat)
	pipeEventually(t, 10*time.Second, "the session under way", func() bool { return m.count("AT+SBDIX") == 1 })
	link.drop()
	time.Sleep(200 * time.Millisecond)
	select {
	case o := <-sent:
		t.Fatalf("the send ended before the stop: %+v, %v", o.res, o.err)
	default:
	}
	stopped := time.Now()
	stop()
	o := await(t, sent, "the send")
	if took := time.Since(stopped); took > 2*time.Second {
		t.Fatalf("the send waited %s after the stop", took)
	}
	if !errors.Is(o.err, ErrOutcomeUnknown) || o.res == nil || o.res.MOStatus != MOStatusUnknown {
		t.Fatalf("send: %+v, %v; want an unknown outcome", o.res, o.err)
	}
}

// Owner changes applied on two goroutines at once (the notification reader
// and a STATUS read of the claim loop) are told in the order they were
// applied: the last owner onOwner hears is the pipe's.
func TestPipe_OwnersAreToldInTheOrderTheyWereApplied(t *testing.T) {
	prev := runtime.GOMAXPROCS(4)
	defer runtime.GOMAXPROCS(prev)
	const rounds = 2000
	for i := 0; i < rounds; i++ {
		var mu sync.Mutex
		var last string
		n := newPipeNode()
		p := newBLEPipe(n, nil, func(o string) { mu.Lock(); last = o; mu.Unlock() }, nil)
		p.setOwner(PipeOwnerNone, nil)
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() { defer wg.Done(); <-start; p.applyStatus([]byte{2, 2, 0x04, 0xFF}, nil) }()
		go func() { defer wg.Done(); <-start; p.applyStatus([]byte{2, 1, 0x04, 0xFF}, nil) }()
		close(start)
		wg.Wait()
		deadline := time.Now().Add(time.Second)
		for {
			p.cbMu.Lock()
			idle := !p.cbRunning && len(p.cbQueue) == 0
			p.cbMu.Unlock()
			if idle {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the owner callbacks did not drain")
			}
			time.Sleep(100 * time.Microsecond)
		}
		mu.Lock()
		got := last
		mu.Unlock()
		if got != p.Owner() {
			t.Fatalf("round %d: the last owner told is %q, the pipe's %q", i, got, p.Owner())
		}
		n.drop()
	}
}

// With the node's flag masked (stuck past pipeFlightCap: the session before
// never answered), no session is written into a modem that does not answer
// the AT before it: it is still busy, or stopped working, and each session
// written into it ended with its outcome unknown ("may have been sent").
// The send is not connected (deferred, no try spent); once the modem
// answers again, sessions go on.
func TestPipeSettle_AMaskedFlagOpensNoSessionIntoAModemThatAnswersNothing(t *testing.T) {
	t.Setenv("IRIDIUM_SBDIX_TIMEOUT", "1")
	flightCap, first, retry := pipeFlightCap, pipeClaimFirstRetry, pipeClaimRetry
	// No give-back in this test: the flag stays stuck, and masked.
	pipeFlightCap, pipeClaimFirstRetry, pipeClaimRetry = 300*time.Millisecond, time.Hour, time.Hour
	t.Cleanup(func() { pipeFlightCap, pipeClaimFirstRetry, pipeClaimRetry = flightCap, first, retry })
	m := newNodeModem()
	m.sbdixSilent = true
	b := newSimBridge(t, m)
	b.connect()
	b.ready()
	if _, err := b.sat.SendText(context.Background(), "first"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("first send: %v; want an unknown outcome", err)
	}
	pipeEventually(t, 10*time.Second, "the line back past the cap", b.sat.IsConnected)

	// The modem takes the next message, then answers nothing more.
	m.set(func(m *nodeModem) { m.wedgeAfter = "AT+SBDWT=second" })
	b.sat.mu.Lock()
	b.sat.lastSBDIX = time.Time{}
	b.sat.mu.Unlock()
	_, err := b.sat.SendText(context.Background(), "second")
	if !errors.Is(err, ErrNotConnected) || errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("a send into a modem that answers nothing: %v; want not connected", err)
	}
	if n := m.count("AT+SBDIX"); n != 1 {
		t.Fatalf("%d sessions written, want the first only", n)
	}

	// It answers again.
	m.set(func(m *nodeModem) { m.silentFor, m.wedgeAfter, m.sbdixSilent = 0, "", false })
	pipeEventually(t, 10*time.Second, "the line back", b.sat.IsConnected)
	b.sat.mu.Lock()
	b.sat.lastSBDIX = time.Time{}
	b.sat.mu.Unlock()
	if res, err := b.sat.SendText(context.Background(), "third"); err != nil || !res.MOSuccess() {
		t.Fatalf("a send once the modem answers: %+v, %v", res, err)
	}
}

// The switch goes off while the claim loop has given the modem back to have
// the node clear a stuck flag: the loop does not take it again.
func TestBLELink_TheSwitchOffDuringAGiveBackIsHonoured(t *testing.T) {
	t.Setenv("IRIDIUM_SBDIX_TIMEOUT", "1")
	flightCap, first, retry, unstick := pipeFlightCap, pipeClaimFirstRetry, pipeClaimRetry, pipeUnstickWait
	pipeFlightCap, pipeClaimFirstRetry, pipeClaimRetry, pipeUnstickWait = 300*time.Millisecond, 100*time.Millisecond, 100*time.Millisecond, time.Second
	t.Cleanup(func() {
		pipeFlightCap, pipeClaimFirstRetry, pipeClaimRetry, pipeUnstickWait = flightCap, first, retry, unstick
	})
	m := newNodeModem()
	m.sbdixSilent = true
	b := newSimBridge(t, m)
	b.node.set(func(n *simNode) { n.sessionCap = 500 * time.Millisecond })
	b.connect()
	b.ready()
	if _, err := b.sat.SendText(context.Background(), "first"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("first send: %v", err)
	}
	after := func() []string {
		log := b.node.eventLog()
		return log[pipeCmdPos(log, "sbdix")+1:]
	}
	pipeEventually(t, 10*time.Second, "the modem given back to clear the flag", func() bool { return pipeCmdPos(after(), "unsubscribe") >= 0 })
	b.mesh.BLESetSatellite(false)
	time.Sleep(pipeUnstickWait + 500*time.Millisecond)
	if log := after(); pipeCmdPos(log, "take") >= 0 {
		t.Fatalf("the modem was taken again with the switch off: %v", log)
	}
	if owner := b.node.ownerNow(); owner != 0 {
		t.Fatalf("the node's owner byte is %d with the switch off", owner)
	}
}
