package transport

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// Review findings of B9 (MESHSAT-1391): the node's modem given back, the
// node forgotten or replaced, the link lost, all while a satellite session
// of this Bridge runs on the node's modem. Every SBDIX is billed, and a
// message that goes out twice is the cost this guards against.

// Switching "Use the node's modem" off while this Bridge's SBDIX is in flight
// waits for the session: the send gets its real answer (sent, once), and only
// then is the modem given back. A release that cut the session on this side
// while the node finished it made the send fail, and the queue sent the
// message a second time. Nothing starts on the modem after the switch.
func TestPipeRelease_SwitchOffDuringASessionWaitsForItsAnswer(t *testing.T) {
	m := newNodeModem()
	m.sbdixDeferred = true
	b := newSimBridge(t, m)
	b.connect()
	b.ready()

	sent := queuedSend{"hello", false}.start(b.sat)
	pipeEventually(t, 10*time.Second, "the session under way", func() bool { return m.count("AT+SBDIX") == 1 })
	if err := b.mesh.BLESetSatellite(false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	select {
	case o := <-sent:
		t.Fatalf("the send ended before its session answered: %+v, %v", o.res, o.err)
	default:
	}
	if owner := b.node.ownerNow(); owner != 1 {
		t.Fatalf("the modem was given back during the session: owner %d, %v", owner, b.node.eventLog())
	}

	m.answerSBDIX()
	o := await(t, sent, "the send")
	if o.err != nil || o.res == nil || o.res.MOStatus != 0 || o.res.MOMSN != 219 {
		t.Fatalf("send: %+v, %v; want the session's own answer, MO status 0", o.res, o.err)
	}
	pipeEventually(t, 5*time.Second, "the modem given back", func() bool { return b.node.ownerNow() == 0 })
	log := b.node.eventLog()
	if a, u := pipeCmdPos(log, "answer"), pipeCmdPos(log, "unsubscribe"); a < 0 || u < a {
		t.Fatalf("the modem was given back before the session's answer: %v", log)
	}
	if n := m.count("AT+SBDIX"); n != 1 {
		t.Fatalf("%d sessions for one message", n)
	}
	if _, err := b.sat.SendText(context.Background(), "after"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("a send after the switch went off: %v", err)
	}
	if n := m.count("AT+SBDIX"); n != 1 {
		t.Fatalf("a session after the switch went off: %d", n)
	}
}

// Forgetting the node, or choosing another, while this Bridge's SBDIX is in
// flight waits for the session's answer before the pipe and the link end.
func TestPipeRelease_ForgettingOrReplacingTheNodeWaitsForTheSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(b *simBridge) error
	}{
		{"forget", func(b *simBridge) error { return b.mesh.BLEForget(context.Background(), false) }},
		{"choose another", func(b *simBridge) error {
			b.mesh.ble.mu.Lock()
			b.mesh.ble.connecting = true // no BlueZ behind this test: the new link is not brought up
			b.mesh.ble.mu.Unlock()
			return b.mesh.BLEConnect(context.Background(), "AA:BB:CC:DD:EE:01")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newNodeModem()
			m.sbdixDeferred = true
			b := newSimBridge(t, m)
			b.connect()
			b.ready()

			sent := queuedSend{"hello", true}.start(b.sat)
			pipeEventually(t, 10*time.Second, "the session under way", func() bool { return m.count("AT+SBDIX") == 1 })
			ended := make(chan error, 1)
			go func() { ended <- tc.end(b) }()
			time.Sleep(300 * time.Millisecond)
			select {
			case err := <-ended:
				t.Fatalf("the node was let go during the session: %v", err)
			default:
			}
			m.answerSBDIX()
			o := await(t, sent, "the send")
			if o.err != nil || o.res == nil || !o.res.MOSuccess() {
				t.Fatalf("send: %+v, %v", o.res, o.err)
			}
			if err := await(t, ended, tc.name); err != nil {
				t.Fatal(err)
			}
			log := b.node.eventLog()
			if a, u := pipeCmdPos(log, "answer"), pipeCmdPos(log, "unsubscribe"); a < 0 || u < a {
				t.Fatalf("the modem was given back before the session's answer: %v", log)
			}
			if b.mesh.BLEStatus().SatelliteOwner != "" {
				t.Fatalf("a pipe is left: %+v", b.mesh.BLEStatus())
			}
			if n := m.count("AT+SBDIX"); n != 1 {
				t.Fatalf("%d sessions for one message", n)
			}
		})
	}
}

// The write of AT+SBDIX lands on the node (the modem runs the session and
// answers MO status 0) but its acknowledgement is lost. It is a cut session,
// settled from the node's account: sent, exactly once. Before, it was a
// plain failure, and the retry sent the message in a second billed session.
func TestPipeSettle_AnSBDIXWriteThatLandsWithItsAckLostIsSentOnce(t *testing.T) {
	m := newNodeModem()
	b := newSimBridge(t, m)
	b.connect()
	b.ready()
	b.node.set(func(n *simNode) { n.ackLost = "AT+SBDIX\r" })

	res, err := b.sat.SendText(context.Background(), "hello")
	if err != nil || res == nil || res.MOStatus != 0 || res.MOMSN != 219 || !res.MOSuccess() {
		t.Fatalf("send: %+v, %v; want MO status 0, MOMSN 219 from the node's account", res, err)
	}
	if n := m.count("AT+SBDIX"); n != 1 {
		t.Fatalf("%d sessions for one message", n)
	}
	pipeEventually(t, 10*time.Second, "the line back", b.sat.IsConnected)
}

// The link drops in the middle of AT+SBDIX. The node holds its modem for the
// session's answer; the link comes back while it does, and STATUS says 01
// with the session in flight: nothing is written into the session (a
// set-up there swallowed its answer). The send waits for the node's account
// and answers the session's own result: sent, once. With STATUS v1 (the
// firmware's default build) the flag comes from STATS.
func TestPipeSettle_ALinkDropMidSessionIsSettledFromTheNodesAccount(t *testing.T) {
	for _, v1 := range []bool{false, true} {
		name := "STATUS v2"
		if v1 {
			name = "STATUS v1, flags from STATS"
		}
		t.Run(name, func(t *testing.T) {
			m := newNodeModem()
			m.sbdixDeferred = true
			b := newSimBridge(t, m)
			b.node.set(func(n *simNode) { n.statusV1 = v1 })
			link := b.connect()
			b.ready()

			sent := queuedSend{"hello", false}.start(b.sat)
			pipeEventually(t, 10*time.Second, "the session under way", func() bool { return m.count("AT+SBDIX") == 1 })
			link.drop()
			time.Sleep(200 * time.Millisecond)
			select {
			case o := <-sent:
				t.Fatalf("the send ended while the node ran its session: %+v, %v", o.res, o.err)
			default:
			}

			b.connect()
			pipeEventually(t, 5*time.Second, "the node's modem claimed again", func() bool {
				return b.mesh.BLEStatus().SatelliteOwner == PipeOwnerPhone
			})
			time.Sleep(300 * time.Millisecond)
			if cmds := between(m.sent(), "AT+SBDIX", "<answer>"); len(cmds) != 0 {
				t.Fatalf("written into the running session: %v", cmds)
			}
			if b.sat.IsConnected() {
				t.Fatal("connected while the node's session runs")
			}

			m.answerSBDIX()
			o := await(t, sent, "the send")
			if o.err != nil || o.res == nil || o.res.MOStatus != 0 || o.res.MOMSN != 219 {
				t.Fatalf("send: %+v, %v; want MO status 0 from the node's account", o.res, o.err)
			}
			pipeEventually(t, 10*time.Second, "the line back once the session ended", b.sat.IsConnected)
			if cmds := between(m.sent(), "AT+SBDIX", "<answer>"); len(cmds) != 0 {
				t.Fatalf("written into the running session: %v", cmds)
			}
			if n := m.count("AT+SBDIX"); n != 1 {
				t.Fatalf("%d sessions for one message", n)
			}
		})
	}
}

// When the node's account cannot settle a cut session, its outcome is
// unknown: ErrOutcomeUnknown beside MO status -1, which the queue never
// retries by itself. The node ran a session of its own meanwhile, it
// restarted, the write never showed on the node at all (its bytes may land
// late), or the node gave the session up at its own cap with no answer.
func TestPipeSettle_AnAccountThatDoesNotMatchLeavesTheOutcomeUnknown(t *testing.T) {
	shortOutcome(t, 5*time.Second)
	for _, tc := range []struct {
		name string
		// cut cuts the session under way (or, with noSession, the write).
		cut       func(b *simBridge, link *simLink)
		noSession bool
	}{
		{name: "the node ran a session of its own", cut: func(b *simBridge, link *simLink) {
			link.drop()
			b.modem.answerSBDIX()
			pipeEventually(b.t, 5*time.Second, "the node took its modem back", func() bool { return b.node.ownerNow() == 0 })
			b.node.nodeSession(0, 300)
			b.connect()
		}},
		{name: "the node restarted", cut: func(b *simBridge, link *simLink) {
			link.drop()
			b.node.reboot()
			b.connect()
		}},
		{name: "the node gave the session up at its cap", cut: func(b *simBridge, link *simLink) {
			link.drop()
			// No answer ever: past the node's cap it lets the modem go and
			// drops the flag, with no result and no ERROR.
			b.modem.set(func(m *nodeModem) { m.pending = nil })
			b.node.advance(10 * time.Second)
			pipeEventually(b.t, 5*time.Second, "the node let go", func() bool { return b.node.ownerNow() == 0 })
			b.connect()
		}},
		{name: "the write never showed on the node", noSession: true, cut: func(b *simBridge, link *simLink) {
			pipeEventually(b.t, 10*time.Second, "the write failed", func() bool {
				b.node.mu.Lock()
				defer b.node.mu.Unlock()
				return b.node.notLanded == ""
			})
			// Only an account built surely after the write says no session
			// started; the node's clock moves on.
			b.node.advance(10 * time.Second)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newNodeModem()
			m.sbdixDeferred = true
			b := newSimBridge(t, m)
			link := b.connect()
			b.ready()
			if tc.noSession {
				b.node.set(func(n *simNode) { n.notLanded = "AT+SBDIX\r" })
			}
			sent := queuedSend{"hello", false}.start(b.sat)
			if !tc.noSession {
				pipeEventually(t, 10*time.Second, "the session under way", func() bool { return m.count("AT+SBDIX") == 1 })
			}
			tc.cut(b, link)
			o := await(t, sent, "the send")
			if !errors.Is(o.err, ErrOutcomeUnknown) || errors.Is(o.err, ErrNotConnected) || o.res == nil ||
				o.res.MOStatus != MOStatusUnknown || o.res.MOSuccess() {
				t.Fatalf("send: %+v, %v; want MO status %d and ErrOutcomeUnknown", o.res, o.err, MOStatusUnknown)
			}
			want := 1
			if tc.noSession {
				want = 0
			}
			if n := m.count("AT+SBDIX"); n != want {
				t.Fatalf("%d sessions reached the modem, want %d", n, want)
			}
		})
	}
}

// A cut session that sent nothing, by the node's account, is a failure the
// queue may try again, never unknown: the modem answered ERROR (a new
// session event, no new answer: no session ran), or it answered the very
// failure it answered before (MO 32 keeps the MOMSN). The node's clock moves
// on between the two, as a real one does.
func TestPipeSettle_ASessionThatSentNothingIsAFailureToRetry(t *testing.T) {
	shortOutcome(t, 10*time.Second)
	t.Run("the modem answered ERROR", func(t *testing.T) {
		m := newNodeModem()
		m.sbdixDeferred = true
		b := newSimBridge(t, m)
		link := b.connect()
		b.ready()
		sent := queuedSend{"hello", false}.start(b.sat)
		pipeEventually(t, 10*time.Second, "the session under way", func() bool { return m.count("AT+SBDIX") == 1 })
		link.drop()
		b.node.advance(3 * time.Second)
		b.modem.set(func(m *nodeModem) { m.pending = []byte("\r\nERROR\r\n") })
		b.modem.answerSBDIX()
		b.connect()
		o := await(t, sent, "the send")
		if !errors.Is(o.err, errSBDIXRefused) || errors.Is(o.err, ErrOutcomeUnknown) || errors.Is(o.err, ErrNotConnected) {
			t.Fatalf("send: %+v, %v; want errSBDIXRefused, a failure to retry", o.res, o.err)
		}
	})
	t.Run("MO 32 answered again", func(t *testing.T) {
		m := newNodeModem()
		m.sbdixReply = "+SBDIX: 32, 218, 2, 0, 0, 0"
		b := newSimBridge(t, m)
		link := b.connect()
		b.ready()
		// A first session finds no network, and its answer reaches this
		// Bridge.
		if res, err := b.sat.SendText(context.Background(), "first"); err != nil || res.MOStatus != 32 {
			t.Fatalf("first send: %+v, %v", res, err)
		}
		b.node.advance(20 * time.Second)
		b.sat.mu.Lock()
		b.sat.lastSBDIX = time.Time{}
		b.sat.mu.Unlock()
		m.set(func(m *nodeModem) { m.sbdixDeferred = true })
		sent := queuedSend{"second", false}.start(b.sat)
		pipeEventually(t, 10*time.Second, "the second session under way", func() bool { return m.count("AT+SBDIX") == 2 })
		link.drop()
		b.node.advance(3 * time.Second)
		b.modem.answerSBDIX() // MO 32 again, the same MOMSN
		b.connect()
		o := await(t, sent, "the second send")
		if o.err != nil || o.res == nil || o.res.MOStatus != 32 || o.res.MOSuccess() {
			t.Fatalf("second send: %+v, %v; want MO status 32, a failure to retry", o.res, o.err)
		}
	})
}

// The link dropped mid-session and the send waits for the node's account,
// when the node is forgotten: the send gives up at once, its outcome
// unknown. The account of a node that is going, or of the next one chosen,
// must never settle it.
func TestPipeSettle_ForgettingTheNodeGivesUpAWaitingSettle(t *testing.T) {
	shortOutcome(t, time.Minute)
	m := newNodeModem()
	m.sbdixDeferred = true
	b := newSimBridge(t, m)
	link := b.connect()
	b.ready()
	sent := queuedSend{"hello", false}.start(b.sat)
	pipeEventually(t, 10*time.Second, "the session under way", func() bool { return m.count("AT+SBDIX") == 1 })
	link.drop()
	pipeEventually(t, 5*time.Second, "the pipe gone", func() bool { return b.mesh.BLEStatus().SatelliteOwner == "" })
	time.Sleep(100 * time.Millisecond)
	select {
	case o := <-sent:
		t.Fatalf("the send ended before the node's account: %+v, %v", o.res, o.err)
	default:
	}
	forgot := time.Now()
	if err := b.mesh.BLEForget(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	o := await(t, sent, "the send")
	if took := time.Since(forgot); took > 3*time.Second {
		t.Fatalf("the send waited %s after the node was forgotten", took)
	}
	if !errors.Is(o.err, ErrOutcomeUnknown) || o.res == nil || o.res.MOStatus != MOStatusUnknown {
		t.Fatalf("send: %+v, %v; want an unknown outcome", o.res, o.err)
	}
}

// The rules that settle a cut session from the node's accounts before and
// after it. A false "sent" loses a message, so anything short of one new
// session with a new answer and nothing else on the node is unknown, not
// yet known, or (a session event after the one before and no new answer:
// an ERROR, or the failure answered before answered again) sent nothing.
func TestJudgeCutSession_TheRules(t *testing.T) {
	ip := func(v int) *int { return &v }
	up := func(v uint32) *uint32 { return &v }
	// The last session event at node time 990 (age 10 at uptime 1000).
	before := PipeStats{Sessions: 7, NodeSessions: 2, UptimeS: 1000, LastMOStatus: ip(0), LastMOMSN: 218, LastMTStatus: ip(0),
		LastSessionAgeS: up(10)}
	// Read and written in the same instant: an account is surely later than
	// the write from 4 s of the node's uptime on (its 2 s rebuild, both
	// uptimes truncated, and a second for the node to count the command).
	at := time.Now()
	mark := &pipeSessionMark{before: before, readAt: at, writtenAt: at}
	after := func(fn func(a *PipeStats)) *PipeStats {
		a := before
		a.UptimeS = 1030
		a.LastSessionAgeS = up(40) // the same event, 30 s on
		fn(&a)
		return &a
	}
	// A session event at node time 1025: after the one before.
	newer := func(a *PipeStats) { a.LastSessionAgeS = up(5) }
	const none, refused = -1, -2
	for _, tc := range []struct {
		name  string
		after *PipeStats
		mo    int  // the settled MO status; none; refused (errSBDIXRefused)
		ran   bool // settled with a new answer
		final bool
	}{
		{"sent: one session, a new answer", after(func(a *PipeStats) { a.Sessions = 8; a.LastMOMSN = 219; newer(a) }), 0, true, true},
		{"failed: one session, MO 32", after(func(a *PipeStats) { a.Sessions = 8; a.LastMOStatus = ip(32); newer(a) }), 32, true, true},
		{"sent with a message in: MT status 1", after(func(a *PipeStats) { a.Sessions = 8; a.LastMOMSN = 219; a.LastMTStatus = ip(1); newer(a) }), 0, true, true},
		{"still in flight", after(func(a *PipeStats) { a.Sessions = 8; a.Flags.SessionInFlight = true }), none, false, false},
		{"no new answer, a newer event: the modem answered ERROR", after(func(a *PipeStats) { a.Sessions = 8; newer(a) }), refused, false, true},
		{"no answer ever, a newer event: ERROR", after(func(a *PipeStats) { a.Sessions = 8; a.LastMOStatus = nil; newer(a) }), refused, false, true},
		{"no new answer, no newer event: the node's cap", after(func(a *PipeStats) { a.Sessions = 8 }), none, false, true},
		{"no new answer, the flag stuck: never answered", after(func(a *PipeStats) { a.Sessions = 8; newer(a); a.flightStuck = true }), none, false, true},
		{"two sessions", after(func(a *PipeStats) { a.Sessions = 9; a.LastMOMSN = 220; newer(a) }), none, false, true},
		{"the node's own session", after(func(a *PipeStats) { a.Sessions = 8; a.NodeSessions = 3; a.LastMOMSN = 219; newer(a) }), none, false, true},
		{"restarted: uptime", after(func(a *PipeStats) { a.Sessions = 8; a.LastMOMSN = 219; a.UptimeS = 20 }), none, false, true},
		{"restarted: fewer sessions", after(func(a *PipeStats) { a.Sessions = 1; a.LastMOMSN = 219 }), none, false, true},
		{"no session yet, the account may predate the write", after(func(a *PipeStats) { a.UptimeS = 1003; a.LastSessionAgeS = up(13) }), none, false, false},
		{"no session, an account surely after the write", after(func(a *PipeStats) { a.UptimeS = 1004; a.LastSessionAgeS = up(14) }), none, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := judgeCutSession(mark, tc.after)
			switch {
			case tc.mo >= 0 && (v.res == nil || v.res.MOStatus != tc.mo || v.res.MOMSN != tc.after.LastMOMSN || v.ran != tc.ran):
				t.Fatalf("want MO status %d settled (new answer %v), got %+v (ran %v, %s)", tc.mo, tc.ran, v.res, v.ran, v.why)
			case tc.mo == refused && (v.res != nil || !errors.Is(v.notSent, errSBDIXRefused)):
				t.Fatalf("want errSBDIXRefused, got %+v, %v", v.res, v.notSent)
			case tc.mo == none && (v.res != nil || v.notSent != nil):
				t.Fatalf("settled %+v, %v; want none", v.res, v.notSent)
			case v.final != tc.final:
				t.Fatalf("final %v (%s), want %v", v.final, v.why, tc.final)
			}
			if v.res != nil && v.ran && v.res.MTReceived != (tc.after.LastMTStatus != nil && *tc.after.LastMTStatus == 1) {
				t.Fatalf("MT received %v for MT status %v", v.res.MTReceived, tc.after.LastMTStatus)
			}
		})
	}

	// A failure answered again (MO 32 keeps its MOMSN): the session sent
	// nothing, a failure, not unknown.
	failedBefore := before
	failedBefore.LastMOStatus, failedBefore.LastMTStatus = ip(32), ip(2)
	fmark := &pipeSessionMark{before: failedBefore, readAt: at, writtenAt: at}
	again := failedBefore
	again.Sessions, again.UptimeS, again.LastSessionAgeS = 8, 1030, up(5)
	v := judgeCutSession(fmark, &again)
	if v.res == nil || v.res.MOStatus != 32 || v.res.MOSuccess() || v.ran || !v.final {
		t.Fatalf("MO 32 again: %+v (ran %v, final %v, %s); want the failure, settled", v.res, v.ran, v.final, v.why)
	}
}

// STATUS says 01 with a session in flight before this Bridge subscribes: the
// node holds its modem for the answer of a session a dropped link left
// behind (up to 95 s). The claim takes it, but no port is handed out until
// the node says the session ended; then the owner is told again, so the SBD
// transport connects.
func TestPipe_NoPortWhileTheNodeHoldsASessionInFlight(t *testing.T) {
	n := newPipeNode()
	n.owner, n.flags, n.keep = 1, 0x05, true
	var mu sync.Mutex
	var told []string
	p := newBLEPipe(n, nil, func(o string) { mu.Lock(); told = append(told, o); mu.Unlock() }, nil)
	go p.run()
	t.Cleanup(n.drop)
	owners := func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), told...) }

	if !p.claim(context.Background(), 2*time.Second) {
		t.Fatal("no claim")
	}
	if _, err := p.port(); !errors.Is(err, errPipeSessionInFlight) {
		t.Fatalf("a port while the node's session runs: %v", err)
	}
	pipeEventually(t, time.Second, "the owner told", func() bool { return len(owners()) == 1 })
	if len(n.written()) != 0 {
		t.Fatal("written to the node during its session")
	}

	n.set(func(n *pipeNode) { n.flags = 0x04 })
	n.notifyStatus()
	pipeEventually(t, time.Second, "the owner told again", func() bool { return len(owners()) == 2 })
	if got := owners(); got[0] != PipeOwnerPhone || got[1] != PipeOwnerPhone {
		t.Fatalf("owners told: %v", got)
	}
	port, err := p.port()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := port.Write([]byte("AT&K0\r")); err != nil {
		t.Fatal(err)
	}

	// A flag the node never clears (its client stayed, the modem silent)
	// holds the port back only up to pipeFlightCap.
	flightCap := pipeFlightCap
	pipeFlightCap = 50 * time.Millisecond
	t.Cleanup(func() { pipeFlightCap = flightCap })
	n.set(func(n *pipeNode) { n.flags = 0x05 })
	n.notifyStatus()
	pipeEventually(t, time.Second, "in flight again", func() bool { _, err := p.port(); return errors.Is(err, errPipeSessionInFlight) })
	time.Sleep(60 * time.Millisecond)
	if _, err := p.port(); err != nil {
		t.Fatalf("a flag past its cap still holds the port: %v", err)
	}
}

// The already-read mark (a message handed over whose AT+SBDD1 did not go
// through) clears the MT buffer unread only for that very message in that
// very modem: its IMEI, its MTMSN and its bytes. A new message (the node's
// own session brought it in while the Bridge was away, and gave the modem
// back before reading it), another node's modem, or a second message alike
// in every byte but its MTMSN, is read at the set-up and handed over.
func TestPipeModem_TheReadMarkClearsOnlyThatMessageInThatModem(t *testing.T) {
	const ours, old = "300434067943980", "already handed over"
	for _, tc := range []struct {
		name, imei string
		mtmsn      int
		msg        string
		cleared    bool
	}{
		{"a new message in the same modem", ours, 8, "fresh from the node's own session", false},
		{"the same message in another node's modem", "300434067999999", 7, old, false},
		{"the same bytes, another MTMSN", ours, 8, old, false},
		{"the same message in the same modem", ours, 7, old, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPipeRig(t, true, fastWake)
			n := r.link(newNodeModem())
			r.connected()
			r.tr.mu.Lock()
			r.tr.mtStale = newMTReadMark(ours, 7, []byte(old))
			r.tr.mu.Unlock()
			n.drop()
			pipeEventually(t, 5*time.Second, "not connected", func() bool { return !r.tr.IsConnected() })

			next := newNodeModem()
			next.imei = tc.imei
			next.sbdsxReply = fmt.Sprintf("+SBDSX: 0, 218, 1, %d, 0, 0", tc.mtmsn)
			next.mt = []byte(tc.msg)
			r.link(next)
			r.connected()
			cmds := next.sent()
			rb, d1 := pipeCmdPos(cmds, "AT+SBDRB"), pipeCmdPos(cmds, "AT+SBDD1")
			if rb < 0 || d1 < rb {
				t.Fatalf("the buffer was not read before it was cleared: %v", cmds)
			}
			r.tr.mu.Lock()
			held, stale := len(r.tr.mtHeld), r.tr.mtStale
			r.tr.mu.Unlock()
			if stale != nil {
				t.Fatalf("the mark outlived the set-up: %+v", stale)
			}
			if tc.cleared {
				if held != 0 {
					t.Fatalf("a message already handed over was held again")
				}
				return
			}
			if held != 1 {
				t.Fatalf("%d messages held, want the new one", held)
			}
			if data, err := r.tr.Receive(context.Background()); err != nil || string(data) != tc.msg {
				t.Fatalf("receive %q, %v", data, err)
			}
		})
	}
}

// Over the pipe a read with a mark (one the set-up kept when its own read
// failed, or one from an earlier link) reads the buffer and compares before
// it clears anything: a new message is handed over, never cleared blind.
func TestPipeModem_AReadWithAMarkComparesBeforeItClears(t *testing.T) {
	const ours, old = "300434067943980", "already handed over"
	for _, tc := range []struct {
		name    string
		mtmsn   int
		msg     string
		cleared bool
	}{
		{"a new message", 8, "fresh", false},
		{"the same bytes, another MTMSN", 8, old, false},
		{"the very message marked", 7, old, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newNodeModem()
			r := newPipeRig(t, true, fastWake)
			r.link(m)
			r.connected()
			r.tr.mu.Lock()
			r.tr.mtStale = newMTReadMark(ours, 7, []byte(old))
			r.tr.lastGSSSync = time.Now() // no session for the gateway's sake in this check
			r.tr.mu.Unlock()
			m.set(func(m *nodeModem) {
				m.sbdsxReply = fmt.Sprintf("+SBDSX: 0, 218, 1, %d, 0, 0", tc.mtmsn)
				m.mt = []byte(tc.msg)
			})
			before := len(m.sent())
			res, err := r.tr.MailboxCheck(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			cmds := m.sent()[before:]
			rb, d1 := pipeCmdPos(cmds, "AT+SBDRB"), pipeCmdPos(cmds, "AT+SBDD1")
			if rb < 0 || d1 < rb {
				t.Fatalf("the buffer was not read before it was cleared: %v", cmds)
			}
			if tc.cleared {
				if res.MTReceived && res.NoSession {
					t.Fatalf("a message already handed over was handed over again: %+v", res)
				}
				r.tr.mu.Lock()
				held := len(r.tr.mtHeld)
				r.tr.mu.Unlock()
				if held != 0 {
					t.Fatalf("%d held", held)
				}
				return
			}
			if !res.MTReceived || !res.NoSession {
				t.Fatalf("mailbox check %+v; want the new message, no session", res)
			}
			if data, err := r.tr.Receive(context.Background()); err != nil || string(data) != tc.msg {
				t.Fatalf("receive %q, %v", data, err)
			}
		})
	}
}

// A message held while the link is down is handed over by the ring alert's
// check as the check a person asks for hands it over, not refused as "not
// connected".
func TestPipeModem_HeldMessagesAreHandedOverWithTheLinkDown(t *testing.T) {
	m := newNodeModem()
	m.sbdsxReply = "+SBDSX: 1, 218, 1, 6, 0, 0"
	m.mt = []byte("earlier")
	r := newPipeRig(t, true, fastWake)
	n := r.link(m)
	r.connected()
	n.drop()
	pipeEventually(t, 5*time.Second, "not connected", func() bool { return !r.tr.IsConnected() })

	res, err := r.tr.MailboxCheck(context.Background())
	if err != nil || !res.MTReceived || res.MTStatus != 1 || res.MTLength != len("earlier") || !res.NoSession {
		t.Fatalf("mailbox check with the link down: %+v, %v", res, err)
	}
	if data, err := r.tr.Receive(context.Background()); err != nil || string(data) != "earlier" {
		t.Fatalf("receive %q, %v", data, err)
	}
	if _, err := r.tr.MailboxCheck(context.Background()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("nothing held and no link: %v", err)
	}
}

// A send whose first command meets a port that has just died (the modem
// given back, the link gone) before the transport heard of it sent nothing:
// ErrNotConnected, which the queue defers without spending a try.
func TestPipeModem_ASendOnAPortThatJustDiedIsNotConnected(t *testing.T) {
	m := newNodeModem()
	r := newPipeRig(t, false, fastWake)
	r.link(m)
	r.connected()
	r.pipe.Load().release() // the transport is not told (no wiring in this rig)
	before := len(m.sent())
	if _, err := r.tr.Send(context.Background(), []byte("hello")); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("send: %v", err)
	}
	if _, err := r.tr.SendText(context.Background(), "hello"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("send text: %v", err)
	}
	if after := m.sent(); len(after) != before {
		t.Fatalf("sent %v", after[before:])
	}
}

// The reader of the node's notifications never waits for onOwner: a slow
// owner callback (the node link's lock, held across D-Bus calls) left the
// modem's bytes backing up until BlueZ dropped them. The owners are told in
// order all the same.
func TestPipe_ASlowOwnerCallbackNeverStallsTheReader(t *testing.T) {
	n := newPipeNode()
	block := make(chan struct{})
	var mu sync.Mutex
	var told []string
	p := newBLEPipe(n, nil, func(o string) {
		<-block
		mu.Lock()
		told = append(told, o)
		mu.Unlock()
	}, nil)
	go p.run()
	t.Cleanup(n.drop)
	if !p.claim(context.Background(), 2*time.Second) {
		t.Fatal("no claim")
	}
	n.setOwner(2)
	n.setOwner(1)
	pipeEventually(t, time.Second, "owner phone again", func() bool { return p.Owner() == PipeOwnerPhone })
	port, err := p.port()
	if err != nil {
		t.Fatal(err)
	}
	n.notify(pipeTXUUID, []byte("\r\nOK\r\n"))
	buf := make([]byte, 16)
	port.SetReadTimeout(time.Second)
	if k, err := port.Read(buf); err != nil || string(buf[:k]) != "\r\nOK\r\n" {
		t.Fatalf("the reader stalled behind the callback: %q, %v", buf[:k], err)
	}
	close(block)
	pipeEventually(t, time.Second, "every owner told", func() bool { mu.Lock(); defer mu.Unlock(); return len(told) == 3 })
	mu.Lock()
	defer mu.Unlock()
	if told[0] != PipeOwnerPhone || told[1] != PipeOwnerNode || told[2] != PipeOwnerPhone {
		t.Fatalf("owners told out of order: %v", told)
	}
}

// A watcher that falls behind BlueZ's signals has what it missed counted.
func TestBluez_AWatcherThatFallsBehindCountsWhatItMissed(t *testing.T) {
	b := &bluezBus{watchers: map[dbus.ObjectPath][]chan propsChange{}}
	path := dbus.ObjectPath("/org/bluez/hci0/dev_E0_72_A1_B3_C2_ED/service0028/char0031")
	ch, stop := b.watch(path)
	defer stop()
	for i := 0; i < cap(ch)+3; i++ {
		b.deliver(path, propsChange{Iface: bluezGattChrIf})
	}
	if got := b.dropped.Load(); got != 3 {
		t.Fatalf("%d drops counted, want 3", got)
	}
	if len(ch) != cap(ch) {
		t.Fatalf("the watcher holds %d", len(ch))
	}
}

// handoverAfterFirst is a node that takes its modem back once the first
// chunk of a write has landed, the notification applied at once.
type handoverAfterFirst struct {
	*pipeNode
	p      *blePipe
	writes int
}

func (h *handoverAfterFirst) write(ctx context.Context, uuid string, data []byte) error {
	err := h.pipeNode.write(ctx, uuid, data)
	h.writes++
	if h.writes == 1 {
		h.pipeNode.set(func(n *pipeNode) { n.owner = 2 })
		h.p.applyStatus([]byte{2, 2, 0x04, 0xFF}, nil)
	}
	return err
}

// A write in chunks stops at the chunk after the modem changed hands: the
// rest would be discarded by the node, or reach whoever holds it now.
func TestPipePort_AWriteStopsWhenTheModemChangesHandsMidway(t *testing.T) {
	n := newPipeNode()
	h := &handoverAfterFirst{pipeNode: n}
	p := newBLEPipe(h, nil, nil, nil)
	h.p = p
	go p.run()
	t.Cleanup(n.drop)
	if !p.claim(context.Background(), 2*time.Second) {
		t.Fatal("no claim")
	}
	port, err := p.port()
	if err != nil {
		t.Fatal(err)
	}
	k, err := port.Write(make([]byte, 60))
	if !errors.Is(err, errPipeNotOwned) || k != 20 {
		t.Fatalf("wrote %d, %v; want the first chunk and errPipeNotOwned", k, err)
	}
	if len(n.written()) != 1 {
		t.Fatalf("%d chunks went out", len(n.written()))
	}
}

// slowStatusRead is a node whose STATUS read answers the value it had when
// the read began, once let go: a notification can overtake it.
type slowStatusRead struct {
	*pipeNode
	gate  chan struct{}
	value []byte
}

func (s *slowStatusRead) read(ctx context.Context, uuid string) ([]byte, error) {
	if uuid == pipeStatusUUID && s.gate != nil {
		v := s.value
		<-s.gate
		return v, nil
	}
	return s.pipeNode.read(ctx, uuid)
}

// A STATUS read that a notification overtook on its way is not applied over
// it: the notification is at least as new.
func TestPipe_AStatusReadNeverUndoesANewerNotification(t *testing.T) {
	n := newPipeNode()
	s := &slowStatusRead{pipeNode: n}
	p := newBLEPipe(s, nil, nil, nil)
	go p.run()
	t.Cleanup(n.drop)
	s.gate, s.value = make(chan struct{}), []byte{2, 0, 0x04, 0xFF}
	done := make(chan struct{})
	go func() { p.refreshStatus(context.Background()); close(done) }()
	time.Sleep(50 * time.Millisecond)
	n.setOwner(1) // notified while the read is on its way
	pipeEventually(t, time.Second, "the notification applied", func() bool { return p.Owner() == PipeOwnerPhone })
	close(s.gate)
	<-done
	if p.Owner() != PipeOwnerPhone {
		t.Fatalf("an older read undid the notification: owner %q", p.Owner())
	}
}

// slowTake is a node whose TX subscription completes in bluetoothd after
// the Bridge's call ended (the D-Bus call errors, the CCCD write lands), and
// whose STATUS notification follows about 30 ms later.
type slowTake struct {
	*pipeNode
	gate chan struct{}
}

func (s *slowTake) startNotify(ctx context.Context, uuid string) error {
	if uuid != pipeTXUUID {
		return nil
	}
	<-s.gate
	go func() {
		time.Sleep(30 * time.Millisecond)
		s.pipeNode.set(func(n *pipeNode) { n.owner = 1 })
		s.pipeNode.notifyStatus()
	}()
	return ctx.Err()
}

// The switch goes off while a claim's TX subscription is on its way, and the
// subscription lands all the same: the node's modem is given back, never left
// with this Bridge (the node's own routing blocked by a client nobody runs).
func TestBLELink_ASubscriptionThatLandsAfterSwitchOffIsGivenBack(t *testing.T) {
	d := &pipeDriver{}
	tr, _ := pipeLink(t, d)
	n := newPipeNode()
	g := &slowTake{pipeNode: n, gate: make(chan struct{})}
	t.Cleanup(n.drop)
	p := tr.ble.runPipeOn(g)
	time.Sleep(200 * time.Millisecond) // the claim waits on the TX subscription
	tr.BLESetSatellite(false)
	close(g.gate)
	pipeEventually(t, 3*time.Second, "the modem given back", func() bool {
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.owner == 0 && !p.holds()
	})
	time.Sleep(200 * time.Millisecond)
	n.mu.Lock()
	owner := n.owner
	n.mu.Unlock()
	if owner != 0 {
		t.Fatalf("the node gives this Bridge its modem with the switch off: owner %d", owner)
	}
}

// The switch goes off and on again within the node's release debounce: the
// node never gives the modem back (and says nothing), and the SBD transport
// is on the line again.
func TestPipeRelease_OffAndOnWithinTheDebounceUsesTheModemAgain(t *testing.T) {
	m := newNodeModem()
	b := newSimBridge(t, m)
	b.node.set(func(n *simNode) { n.debounce = 2 * time.Second })
	b.connect()
	b.ready()
	b.mesh.BLESetSatellite(false)
	pipeEventually(t, 5*time.Second, "the line dropped", func() bool { return !b.sat.IsConnected() })
	b.mesh.BLESetSatellite(true)
	pipeEventually(t, 10*time.Second, "the line back", b.sat.IsConnected)
	if log := b.node.eventLog(); pipeCmdPos(log, "release") >= 0 {
		t.Fatalf("the node let the modem go: %v", log)
	}
	if st := b.mesh.BLEStatus(); !st.SatelliteEnabled || st.SatelliteOwner != PipeOwnerPhone {
		t.Fatalf("status %+v", st)
	}
}

// A set-up whose AT+SBDMTA=1 or AT+SBDD0 the modem did not take fails and is
// asked again: ring alerts on and an empty MO buffer are what the link is
// taken to be from then on.
func TestPipeModem_ASetUpTheModemDidNotTakeIsAskedAgain(t *testing.T) {
	for _, cmd := range []string{"AT+SBDMTA=1", "AT+SBDD0"} {
		t.Run(cmd, func(t *testing.T) {
			m := newNodeModem()
			m.refuse = map[string]bool{cmd: true}
			r := newPipeRig(t, true, fastWake)
			r.link(m)
			pipeEventually(t, 5*time.Second, "the refused command asked", func() bool { return m.count(cmd) >= 1 })
			time.Sleep(200 * time.Millisecond)
			if r.tr.IsConnected() {
				t.Fatalf("connected with %s refused", cmd)
			}
			m.set(func(m *nodeModem) { m.refuse = nil })
			r.tr.kickPipe() // the pipe loop's next round, without its 10 s
			r.connected()
		})
	}
}
