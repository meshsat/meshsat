package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.bug.st/serial"
)

// nodeModem is the RockBLOCK 9603 behind the node's pipe, scripted like the
// FakeModem of MeshSat Android's IridiumSppOverPipeTest.kt: echo on until
// ATE0, READY then a binary phase for SBDWB, binary SBDRB frames, and
// commands it ignores while it powers up.
type nodeModem struct {
	mu          sync.Mutex
	out         func([]byte)
	commands    []string
	echo        bool
	sbdixReply  string
	sbdixSilent bool
	// sbdixDeferred: a session runs until answerSBDIX gives its answer.
	sbdixDeferred bool
	pending       []byte
	imei          string
	// refuse answers ERROR to the commands it names.
	refuse map[string]bool
	// wedgeAfter: once the command it names is answered, the modem answers
	// nothing more (a modem that stopped working).
	wedgeAfter string
	csqf       int
	sbdsxReply string
	mt         []byte
	silentFor  int
	moWritten  []byte
	line       []byte
	binaryLeft int
	binary     []byte
}

func newNodeModem() *nodeModem {
	return &nodeModem{
		echo:       true,
		sbdixReply: "+SBDIX: 0, 219, 0, 0, 0, 0",
		csqf:       2,
		sbdsxReply: "+SBDSX: 0, 218, 0, -1, 0, 0",
		imei:       "300434067943980",
	}
}

// answerSBDIX gives the answer of the session in flight (sbdixDeferred),
// and notes it among the commands as "<answer>".
func (m *nodeModem) answerSBDIX() {
	m.mu.Lock()
	reply, out := m.pending, m.out
	m.pending = nil
	m.commands = append(m.commands, "<answer>")
	m.mu.Unlock()
	if reply != nil && out != nil {
		out(reply)
	}
}

func (m *nodeModem) set(fn func(m *nodeModem)) {
	m.mu.Lock()
	fn(m)
	m.mu.Unlock()
}

func (m *nodeModem) sent() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.commands...)
}

func (m *nodeModem) count(cmd string) int {
	n := 0
	for _, c := range m.sent() {
		if c == cmd {
			n++
		}
	}
	return n
}

// pipeCmdPos is where cmd first went to the modem, -1 when it never did.
func pipeCmdPos(cmds []string, cmd string) int {
	for i, c := range cmds {
		if c == cmd {
			return i
		}
	}
	return -1
}

// feed takes what the node's RX carries to the modem.
func (m *nodeModem) feed(data []byte) {
	var replies [][]byte
	m.mu.Lock()
	for _, b := range data {
		if r := m.onByteLocked(b); r != nil {
			replies = append(replies, r)
		}
	}
	out := m.out
	m.mu.Unlock()
	for _, r := range replies {
		if out != nil {
			out(r)
		}
	}
}

func (m *nodeModem) onByteLocked(b byte) []byte {
	if m.binaryLeft > 0 {
		m.binary = append(m.binary, b)
		if m.binaryLeft--; m.binaryLeft > 0 {
			return nil
		}
		data := m.binary[:len(m.binary)-2]
		sum := uint16(m.binary[len(m.binary)-2])<<8 | uint16(m.binary[len(m.binary)-1])
		var got uint16
		for _, c := range data {
			got += uint16(c)
		}
		if got != sum {
			return []byte("\r\n2\r\n\r\nOK\r\n")
		}
		m.moWritten = append([]byte(nil), data...)
		return []byte("\r\n0\r\n\r\nOK\r\n")
	}
	if b != '\r' {
		m.line = append(m.line, b)
		return nil
	}
	cmd := string(m.line)
	m.line = m.line[:0]
	m.commands = append(m.commands, cmd)
	if m.silentFor > 0 {
		m.silentFor--
		return nil
	}
	if m.wedgeAfter != "" && cmd == m.wedgeAfter {
		// This command is answered; nothing after it is.
		m.silentFor = 1 << 30
	}
	return m.answerLocked(cmd)
}

// answerLocked is the modem's answer to one command line. Caller holds m.mu.
func (m *nodeModem) answerLocked(cmd string) []byte {
	reply := func(body string) []byte {
		if m.echo {
			return []byte(cmd + "\r" + body)
		}
		return []byte(body)
	}
	if m.refuse[cmd] {
		return reply("\r\nERROR\r\n")
	}
	switch {
	case cmd == "ATE0":
		r := reply("\r\nOK\r\n")
		m.echo = false
		return r
	case cmd == "AT+CGMI":
		return reply("\r\nIridium\r\n\r\nOK\r\n")
	case cmd == "AT+CGMM":
		return reply("\r\nIRIDIUM 9600 Family SBD Transceiver\r\n\r\nOK\r\n")
	case cmd == "AT+CGSN":
		return reply("\r\n" + m.imei + "\r\n\r\nOK\r\n")
	case cmd == "AT+CGMR":
		return reply("\r\nCall Processor Version: TA19002\r\n\r\nOK\r\n")
	case cmd == "AT+CSQ":
		return reply("\r\n+CSQ:4\r\n\r\nOK\r\n")
	case cmd == "AT+CSQF":
		return reply(fmt.Sprintf("\r\n+CSQF:%d\r\n\r\nOK\r\n", m.csqf))
	case cmd == "AT+SBDSX":
		return reply("\r\n" + m.sbdsxReply + "\r\n\r\nOK\r\n")
	case strings.HasPrefix(cmd, "AT+SBDWB="):
		n, _ := strconv.Atoi(strings.TrimPrefix(cmd, "AT+SBDWB="))
		m.binaryLeft, m.binary = n+2, nil
		return reply("READY\r\n")
	case strings.HasPrefix(cmd, "AT+SBDWT="):
		m.moWritten = []byte(strings.TrimPrefix(cmd, "AT+SBDWT="))
		return reply("\r\nOK\r\n")
	case cmd == "AT+SBDIX" || cmd == "AT+SBDIXA":
		if m.sbdixSilent {
			return nil
		}
		if m.sbdixDeferred {
			m.pending = reply("\r\n" + m.sbdixReply + "\r\n\r\nOK\r\n")
			return nil
		}
		return reply("\r\n" + m.sbdixReply + "\r\n\r\nOK\r\n")
	case cmd == "AT+SBDD0":
		m.moWritten = nil
		return reply("\r\n0\r\n\r\nOK\r\n")
	case cmd == "AT+SBDD1":
		m.mt = nil
		return reply("\r\n0\r\n\r\nOK\r\n")
	case cmd == "AT+SBDRB":
		var sum uint16
		for _, c := range m.mt {
			sum += uint16(c)
		}
		frame := []byte{byte(len(m.mt) >> 8), byte(len(m.mt))}
		frame = append(frame, m.mt...)
		frame = append(frame, byte(sum>>8), byte(sum))
		frame = append(frame, "\r\nOK\r\n"...)
		if m.echo {
			return append([]byte(cmd+"\r"), frame...)
		}
		return frame
	}
	return reply("\r\nOK\r\n")
}

// pipeRig is the SBD transport over a node's pipe, both in memory. With
// wire, every change of owner reaches the transport as the node link passes
// it on (PortReady, PortGone); without, only the first handover does, which
// leaves the transport's own reaction to a refused write on its own.
type pipeRig struct {
	t      *testing.T
	wire   bool
	health *pipeHealth
	tr     *DirectSatTransport
	pipe   atomic.Pointer[blePipe]
	events <-chan SatEvent
	nodes  []*pipeNode
}

func newPipeRig(t *testing.T, wire bool, wake pipeWakeTimings) *pipeRig {
	t.Helper()
	r := &pipeRig{t: t, wire: wire, health: &pipeHealth{}}
	r.tr = NewDirectSatTransport("ble")
	r.tr.pipeWake = wake
	r.tr.SetPortOpener(func(context.Context) (serial.Port, error) {
		p := r.pipe.Load()
		if p == nil {
			return nil, errors.New("no node")
		}
		return p.port()
	})
	ctx, cancel := context.WithCancel(context.Background())
	events, err := r.tr.Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe over the pipe: %v", err)
	}
	r.events = events
	t.Cleanup(func() {
		cancel()
		r.tr.Close()
		for _, n := range r.nodes {
			n.drop()
		}
	})
	return r
}

// fastWake paces the wake-up probe for tests.
var fastWake = pipeWakeTimings{at: 150 * time.Millisecond, retry: 10 * time.Millisecond, grace: time.Minute, silentRetry: 50 * time.Millisecond}

// link brings up a node link whose pipe carries modem, and claims the modem.
func (r *pipeRig) link(m *nodeModem) *pipeNode {
	r.t.Helper()
	n := newPipeNode()
	n.modem = m.feed
	m.set(func(m *nodeModem) { m.out = n.tx })
	p := newBLEPipe(n, r.health, func(owner string) {
		if !r.wire {
			return
		}
		if owner == PipeOwnerPhone {
			r.tr.PortReady()
		} else {
			r.tr.PortGone()
		}
	}, r.tr.PortGone)
	r.pipe.Store(p)
	r.nodes = append(r.nodes, n)
	go p.run()
	if !p.claim(context.Background(), 2*time.Second) {
		r.t.Fatal("the node did not hand its modem over")
	}
	if !r.wire {
		r.tr.PortReady()
	}
	return n
}

func (r *pipeRig) connected() {
	r.t.Helper()
	pipeEventually(r.t, 10*time.Second, "the modem connected over the pipe", r.tr.IsConnected)
}

func (r *pipeRig) status() *SatStatus {
	r.t.Helper()
	st, err := r.tr.GetStatus(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	return st
}

// event waits for an event of kind, passing over others.
func (r *pipeRig) event(kind string, d time.Duration) bool {
	deadline := time.After(d)
	for {
		select {
		case ev := <-r.events:
			if ev.Type == kind {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// The set-up turns flow control off first, reads the IMEI through the echo,
// asks the manufacturer, and never clears the MT buffer.
func TestPipeModem_SetUpReadsTheIMEIThroughTheEcho(t *testing.T) {
	m := newNodeModem()
	r := newPipeRig(t, true, fastWake)
	r.link(m)
	r.connected()
	if !r.event("connected", 2*time.Second) {
		t.Fatal("no connected event")
	}
	cmds := m.sent()
	if cmds[0] != "AT&K0" {
		t.Fatalf("first command %q", cmds[0])
	}
	if pipeCmdPos(cmds, "ATE0") > pipeCmdPos(cmds, "AT+CGSN") || pipeCmdPos(cmds, "AT+CGSN") < 0 {
		t.Fatalf("ATE0 after CGSN: %v", cmds)
	}
	for _, want := range []string{"AT+SBDMTA=1", "AT+CGMI", "AT+SBDSX", "AT+SBDD0"} {
		if pipeCmdPos(cmds, want) < 0 {
			t.Errorf("%s not sent: %v", want, cmds)
		}
	}
	if pipeCmdPos(cmds, "AT+SBDD1") >= 0 {
		t.Fatalf("the set-up deleted the MT buffer: %v", cmds)
	}
	st := r.status()
	if !st.Connected || st.Silent || st.IMEI != "300434067943980" || st.Manufacturer != "Iridium" ||
		st.Model != "IRIDIUM 9600 Family SBD Transceiver" || st.Port != "ble" || st.Type != "sbd" {
		t.Fatalf("status %+v", st)
	}
	if sig, err := r.tr.GetSignalFast(context.Background()); err != nil || sig.Bars != 2 {
		t.Fatalf("signal %+v, %v", sig, err)
	}
}

// Three writes in a row that do not get through take the transport out of
// connected and mark the link broken (MESHSAT-1270).
func TestPipeModem_ThreeFailedWritesBreakTheLink(t *testing.T) {
	m := newNodeModem()
	r := newPipeRig(t, true, fastWake)
	n := r.link(m)
	r.connected()
	n.set(func(n *pipeNode) { n.failWrites = true })
	for i := 0; i < pipeBrokenWrites; i++ {
		if _, err := r.tr.GetSignalFast(context.Background()); !errors.Is(err, errPipeWriteFailed) {
			t.Fatalf("poll %d: %v", i, err)
		}
	}
	pipeEventually(t, 3*time.Second, "not connected after a broken link", func() bool { return !r.tr.IsConnected() })
	if broken, _ := r.health.state(); !broken {
		t.Fatal("the link is not marked broken")
	}
	if r.status().Connected {
		t.Fatal("the status still says connected")
	}
}

// A node using its own modem refuses the writes: a handover, not a broken
// link. The transport stays connected until told the modem went, and the
// mailbox checks say so rather than drop the line or start the hold.
func TestPipeModem_NotOwnerIsAHandoverNotABrokenLink(t *testing.T) {
	m := newNodeModem()
	r := newPipeRig(t, false, fastWake)
	n := r.link(m)
	r.connected()
	n.setOwner(2)
	pipeEventually(t, time.Second, "owner node", func() bool { return r.pipe.Load().Owner() == PipeOwnerNode })
	for i := 0; i < pipeBrokenWrites+2; i++ {
		if _, err := r.tr.GetSignalFast(context.Background()); !errors.Is(err, errPipeNotOwned) {
			t.Fatalf("poll %d: %v", i, err)
		}
	}
	if _, err := r.tr.MailboxCheck(context.Background()); !errors.Is(err, errPipeNotOwned) {
		t.Fatalf("the ring alert's check: %v", err)
	}
	if out := r.tr.CheckMailboxNow(context.Background()); out.Result.Kind != MailboxNotConnected || out.SessionAnswered {
		t.Fatalf("the check a person asks for: %+v, want not_connected", out)
	}
	if broken, _ := r.health.state(); broken {
		t.Fatal("a handover counted as a broken link")
	}
	time.Sleep(200 * time.Millisecond)
	if !r.tr.IsConnected() {
		t.Fatal("a handover took the transport out of connected")
	}
	r.tr.mu.Lock()
	held := r.tr.sbdixHeldUntil
	r.tr.mu.Unlock()
	if !held.IsZero() {
		t.Fatalf("a handover started the hold (until %v)", held)
	}
}

// A modem still powering up (the node gives it about 10 s) is asked AT&K0
// again until it answers, then probed.
func TestPipeModem_PoweringUpIsAskedAgainThenProbed(t *testing.T) {
	m := newNodeModem()
	m.silentFor = 3
	r := newPipeRig(t, true, fastWake)
	r.link(m)
	r.connected()
	cmds := m.sent()
	if len(cmds) < 5 || strings.Join(cmds[:4], ",") != "AT&K0,AT&K0,AT&K0,AT&K0" || cmds[4] == "AT&K0" {
		t.Fatalf("commands %v", cmds)
	}
	if st := r.status(); st.IMEI != "300434067943980" || st.Silent {
		t.Fatalf("status %+v", st)
	}
}

// A node without a modem is reported silent after the grace, never
// connected; once the modem is not this Bridge's, nothing more is sent.
func TestPipeModem_ANodeWithoutAModemIsSilentAndNeverConnected(t *testing.T) {
	m := newNodeModem()
	m.silentFor = 1 << 30
	wake := fastWake
	wake.grace = 300 * time.Millisecond
	r := newPipeRig(t, true, wake)
	r.link(m)
	pipeEventually(t, 5*time.Second, "silent", func() bool { return r.status().Silent })
	if r.status().Connected {
		t.Fatal("connected to a modem that never answered")
	}
	for _, c := range m.sent() {
		if c != "AT&K0" {
			t.Fatalf("sent %q to a modem that never answered", c)
		}
	}
	r.tr.PortGone()
	pipeEventually(t, time.Second, "not silent once the modem went", func() bool { return !r.status().Silent })
	time.Sleep(700 * time.Millisecond) // a command already on its way (drain, then its answer timeout) lands now
	sent := len(m.sent())
	time.Sleep(400 * time.Millisecond)
	if len(m.sent()) != sent {
		t.Fatalf("still asking: %v", m.sent()[sent:])
	}
}

// A message already in the MT buffer when the link comes up is read at once,
// for free, and handed over by the next mailbox check without a session (no
// GSS row); it leaves the modem once read (AT+SBDD1 after the SBDRB, never a
// clear before it), and waits in the transport until handed over.
func TestPipeModem_AnMTInTheBufferIsReadFreeBeforeAnySession(t *testing.T) {
	m := newNodeModem()
	m.sbdsxReply = "+SBDSX: 1, 218, 1, 6, 0, 0"
	m.mt = []byte("earlier")
	r := newPipeRig(t, true, fastWake)
	r.link(m)
	r.connected()
	if !r.event("mt_received", 2*time.Second) {
		t.Fatal("no mt_received event for the waiting message")
	}
	cmds := m.sent()
	if sx, rb := pipeCmdPos(cmds, "AT+SBDSX"), pipeCmdPos(cmds, "AT+SBDRB"); sx < 0 || rb < sx {
		t.Fatalf("SBDSX then SBDRB: %v", cmds)
	}
	if d1 := pipeCmdPos(cmds, "AT+SBDD1"); d1 < pipeCmdPos(cmds, "AT+SBDRB") {
		t.Fatalf("the MT buffer was cleared before the read, or not after it: %v", cmds)
	}

	before := len(m.sent())
	res, err := r.tr.MailboxCheck(context.Background())
	if err != nil || !res.MTReceived || res.MTStatus != 1 || res.MTLength != len("earlier") || !res.NoSession {
		t.Fatalf("mailbox check %+v, %v", res, err)
	}
	if len(m.sent()) != before {
		t.Fatalf("the check sent %v", m.sent()[before:])
	}
	data, err := r.tr.Receive(context.Background())
	if err != nil || string(data) != "earlier" {
		t.Fatalf("receive %q, %v", data, err)
	}
	if len(m.sent()) != before {
		t.Fatalf("the hand-over sent %v", m.sent()[before:])
	}
	if m.count("AT+SBDIX") != 0 || m.count("AT+SBDIXA") != 0 {
		t.Fatalf("a session was opened: %v", m.sent())
	}
}

// The link dropping during AT+SBDIX ends the command at once with MO status
// -1 and an error that says so, and the next link is probed at once. The
// node here gives no account of its sessions (no STATS behind this rig), so
// the send's outcome is unknown at once: ErrOutcomeUnknown, which the queue
// never retries by itself, never a failure it retries (a retry sent the
// message a second time: the node had finished the session).
func TestPipeModem_ALinkDropMidSessionEndsItAtOnce(t *testing.T) {
	m := newNodeModem()
	m.sbdixSilent = true
	r := newPipeRig(t, true, fastWake)
	n := r.link(m)
	r.connected()
	r.tr.mu.Lock()
	r.tr.lastSBDIX = time.Time{} // no 10 s rate limit in a test
	r.tr.mu.Unlock()

	type sent struct {
		res *SBDResult
		err error
	}
	done := make(chan sent, 1)
	go func() {
		res, err := r.tr.Send(context.Background(), []byte("hello"))
		done <- sent{res, err}
	}()
	pipeEventually(t, 10*time.Second, "the session under way", func() bool { return m.count("AT+SBDIX") == 1 })
	time.Sleep(100 * time.Millisecond)
	dropped := time.Now()
	n.drop()
	var got sent
	select {
	case got = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the session did not end with the link")
	}
	if took := time.Since(dropped); took > time.Second {
		t.Fatalf("ended %s after the link went", took)
	}
	if got.res == nil || got.res.MOStatus != MOStatusUnknown || got.res.MOSuccess() || !errors.Is(got.err, errPipeLinkLost) ||
		!errors.Is(got.err, ErrOutcomeUnknown) || errors.Is(got.err, ErrNotConnected) {
		t.Fatalf("result %+v, %v; want MO status %d and ErrOutcomeUnknown", got.res, got.err, MOStatusUnknown)
	}
	pipeEventually(t, 2*time.Second, "not connected", func() bool { return !r.tr.IsConnected() })

	next := newNodeModem()
	r.link(next)
	pipeEventually(t, 5*time.Second, "the next link probed", r.tr.IsConnected)
	if time.Since(dropped) > 6*time.Second || pipeCmdPos(next.sent(), "AT+CGSN") < 0 {
		t.Fatalf("the next link: %v", next.sent())
	}
}

// A session over the pipe whose link goes mid-SBDIX gets MO status -1 beside
// the lost link and an unknown outcome (this rig's node gives no account of
// its sessions), never a failure the queue retries, and starts the 3-minute
// hold from the session's start, as a
// cable that fails under a session does: the node finishes the session, and
// the modem registers at most once every 3 minutes. The next mailbox check a
// person asks for, on the next link and within the hold, reports held and
// sends the modem nothing.
func TestPipeModem_ALinkLostMidSessionStartsTheHold(t *testing.T) {
	m := newNodeModem()
	m.sbdixSilent = true
	r := newPipeRig(t, true, fastWake)
	n := r.link(m)
	r.connected()
	r.tr.mu.Lock()
	r.tr.lastSBDIX = time.Time{} // no 10 s rate limit in a test
	r.tr.mu.Unlock()

	sent := queuedSend{"hello", false}.start(r.tr)
	pipeEventually(t, 10*time.Second, "the session under way", func() bool { return m.count("AT+SBDIX") == 1 })
	time.Sleep(100 * time.Millisecond)
	n.drop()
	so := await(t, sent, "the send")
	if so.res == nil || so.res.MOStatus != MOStatusUnknown || so.res.MOSuccess() ||
		!errors.Is(so.err, errPipeLinkLost) || !errors.Is(so.err, errSBDIXLinkLost) || !errors.Is(so.err, ErrOutcomeUnknown) {
		t.Fatalf("send: %+v, %v; want MO status %d, the lost link and an unknown outcome", so.res, so.err, MOStatusUnknown)
	}
	r.tr.mu.Lock()
	start, until := r.tr.lastSBDIX, r.tr.sbdixHeldUntil
	r.tr.mu.Unlock()
	if want := start.Add(SBDIXHold); !until.Equal(want) {
		t.Fatalf("held until %v, want %v (the session's start plus %v)", until, want, SBDIXHold)
	}

	next := newNodeModem()
	r.link(next)
	r.connected()
	before := len(next.sent())
	out := r.tr.CheckMailboxNow(context.Background())
	if out.Result.Kind != MailboxHeld || out.Result.Seconds < 1 || out.Result.Seconds > 180 ||
		out.Result.MOStatus != MOStatusUnknown || out.SessionAnswered || len(out.Messages) != 0 {
		t.Fatalf("the next check: %+v, want held with no session", out)
	}
	if after := next.sent(); len(after) != before {
		t.Fatalf("a held check sent %v", after[before:])
	}
	if n := next.count("AT+SBDIX") + next.count("AT+SBDIXA"); n != 0 {
		t.Fatalf("%d sessions went out within the hold: %v", n, next.sent())
	}
}

// Once the modem is not this Bridge's, nothing is sent.
func TestPipeModem_AfterDetachNothingIsSent(t *testing.T) {
	m := newNodeModem()
	r := newPipeRig(t, true, fastWake)
	r.link(m)
	r.connected()
	r.tr.PortGone()
	pipeEventually(t, 2*time.Second, "not connected", func() bool { return !r.tr.IsConnected() })
	time.Sleep(200 * time.Millisecond)
	before := len(m.sent())
	ctx := context.Background()
	if _, err := r.tr.Send(ctx, []byte{1}); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("send: %v", err)
	}
	if _, err := r.tr.SendText(ctx, "hi"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("send text: %v", err)
	}
	if _, err := r.tr.MailboxCheck(ctx); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("mailbox: %v", err)
	}
	if _, err := r.tr.GetSignalFast(ctx); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("signal: %v", err)
	}
	if _, err := r.tr.Receive(ctx); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("receive: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if len(m.sent()) != before {
		t.Fatalf("sent after detach: %v", m.sent()[before:])
	}
}

// SBDRB over the pipe reads a frame split across notifications whole,
// binary-safe, with and without echo, then drops it from the modem.
func TestPipeModem_ASplitSBDRBFrameIsReadWhole(t *testing.T) {
	m := newNodeModem()
	r := newPipeRig(t, true, fastWake)
	r.link(m)
	r.connected()
	payload := []byte("OK\r\nERROR\r\n\x00\xff")
	for i := 0; len(payload) < 150; i++ {
		payload = append(payload, byte(i*7))
	}
	for _, echo := range []bool{false, true} {
		m.set(func(m *nodeModem) { m.mt, m.echo = append([]byte(nil), payload...), echo })
		data, err := r.tr.Receive(context.Background())
		if err != nil || !bytes.Equal(data, payload) {
			t.Fatalf("echo %v: % x, %v", echo, data, err)
		}
	}
	if m.count("AT+SBDD1") != 2 {
		t.Fatalf("SBDD1 %d times", m.count("AT+SBDD1"))
	}
}

// An unsolicited SBDRING split across notifications is one ring alert, seen
// by the pipe whoever reads the bytes, and not counted again by the reader.
func TestPipeModem_ASplitSBDRINGIsOneRingAlert(t *testing.T) {
	m := newNodeModem()
	r := newPipeRig(t, true, fastWake)
	n := r.link(m)
	r.connected()
	n.notify(pipeTXUUID, []byte("\r\nSBD"))
	n.notify(pipeTXUUID, []byte("RI"))
	n.notify(pipeTXUUID, []byte("NG\r\n"))
	if !r.event("ring_alert", 2*time.Second) {
		t.Fatal("no ring alert")
	}
	if r.event("ring_alert", 500*time.Millisecond) {
		t.Fatal("the ring was counted twice")
	}
}
