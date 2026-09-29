package transport

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.bug.st/serial"
)

// sbdEmu is a scripted RockBLOCK 9603 behind a serial port, a Go port of
// MeshSat Android's FakeModem (IridiumSppOverPipeTest.kt at v2.19.4): echo
// off (the Bridge sends ATE0 at connect), +SBDSX and +SBDIX answers,
// binary SBDRB frames, SBDD0/1/2, SBDWT and SBDWB into an MO buffer, and
// OK for the rest; optionally a ring-alert flag and a gateway MT queue whose
// sessions empty the MT buffer as they start. Every SBDIX records what the
// MO buffer held when it went out. A dropped link fails every read and
// write at once, as an unplugged adapter does.
type sbdEmu struct {
	serial.Port

	mu          sync.Mutex
	commands    []string
	out         []byte
	line        []byte
	readTimeout time.Duration

	sbdsxReply   string   // the +SBDSX line
	sbdsxLive    bool     // answer SBDSX from the MO and MT buffers instead
	sbdsxBroken  bool     // answer SBDSX with ERROR
	sbdixReply   string   // the +SBDIX line
	sbdixSilent  bool     // the session never answers
	sbdd0Fails   bool     // SBDD0 answers 1 (not cleared)
	sbdd1Fails   bool     // SBDD1 answers 1: the MT buffer keeps its message
	sbdrbCorrupt bool     // SBDRB frames arrive with a wrong checksum
	mo           []byte   // the MO buffer
	mt           []byte   // the MT buffer
	carried      []string // what the MO buffer held at each SBDIX, in order
	binLeft      int      // SBDWB payload and checksum bytes still to come
	bin          []byte
	// loadLost: the modem takes an SBDWT or SBDWB load but its answer is
	// lost on the line (SBDWT answered ERROR, SBDWB's result code garbled).
	loadLost bool
	// ra is the ring-alert flag live SBDSX reports; a gssLive session
	// answers it.
	ra bool
	// gssLive: a session empties the MT buffer as it starts, as the ISU
	// does, brings in the first message of gss, and its +SBDIX answer is
	// built from them (MO status 0) instead of sbdixReply.
	gssLive bool
	gss     [][]byte
	momsn   int
	// onCommand is called with every command once it is answered, in the
	// goroutine that wrote it (which holds the transport's serial lock),
	// with e.mu not held.
	onCommand func(cmd string)
	dropped   bool
	closed    bool
}

var errEmuGone = errors.New("read /dev/ttyEMU0: input/output error")

func newSBDEmu() *sbdEmu {
	return &sbdEmu{
		sbdsxReply: "+SBDSX: 0, 218, 0, -1, 0, 0",
		sbdixReply: "+SBDIX: 0, 219, 0, 0, 0, 0",
	}
}

func (e *sbdEmu) Read(b []byte) (int, error) {
	e.mu.Lock()
	if e.dropped || e.closed {
		e.mu.Unlock()
		return 0, errEmuGone
	}
	if len(e.out) > 0 {
		n := copy(b, e.out)
		e.out = e.out[n:]
		e.mu.Unlock()
		return n, nil
	}
	wait := e.readTimeout
	e.mu.Unlock()
	if wait <= 0 || wait > 5*time.Millisecond {
		wait = 5 * time.Millisecond
	}
	time.Sleep(wait) // the read timeout, shortened
	return 0, nil
}

func (e *sbdEmu) Write(b []byte) (int, error) {
	e.mu.Lock()
	if e.dropped || e.closed {
		e.mu.Unlock()
		return 0, errEmuGone
	}
	var cmds []string
	for _, c := range b {
		if e.binLeft > 0 {
			e.bin = append(e.bin, c)
			e.binLeft--
			if e.binLeft == 0 {
				e.loadBinary()
			}
			continue
		}
		if c != '\r' {
			e.line = append(e.line, c)
			continue
		}
		cmd := string(e.line)
		e.line = e.line[:0]
		e.commands = append(e.commands, cmd)
		e.answer(cmd)
		cmds = append(cmds, cmd)
	}
	hook := e.onCommand
	e.mu.Unlock()
	if hook != nil {
		for _, cmd := range cmds {
			hook(cmd)
		}
	}
	return len(b), nil
}

// loadBinary ends an SBDWB: the payload and its two-byte sum, answered 0
// (loaded into the MO buffer) or 2 (checksum mismatch). Runs under e.mu.
func (e *sbdEmu) loadBinary() {
	n := len(e.bin) - 2
	payload := e.bin[:n]
	sum := 0
	for _, c := range payload {
		sum += int(c)
	}
	got := int(e.bin[n])<<8 | int(e.bin[n+1])
	e.bin = nil
	if sum&0xFFFF != got {
		e.out = append(e.out, "\r\n2\r\n\r\nOK\r\n"...)
		return
	}
	e.mo = append([]byte(nil), payload...)
	if e.loadLost {
		e.out = append(e.out, "\r\n\x7f\r\n\r\nOK\r\n"...) // the 0 garbled on the line
		return
	}
	e.out = append(e.out, "\r\n0\r\n\r\nOK\r\n"...)
}

func (e *sbdEmu) SetReadTimeout(d time.Duration) error {
	e.mu.Lock()
	e.readTimeout = d
	e.mu.Unlock()
	return nil
}

func (e *sbdEmu) Close() error {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	return nil
}

// answer runs under e.mu.
func (e *sbdEmu) answer(cmd string) {
	reply := func(s string) { e.out = append(e.out, s...) }
	switch {
	case cmd == "AT+SBDSX":
		switch {
		case e.sbdsxBroken:
			reply("\r\nERROR\r\n")
		case e.sbdsxLive:
			mo, mt, mtmsn, ra := 0, 0, -1, 0
			if len(e.mo) > 0 {
				mo = 1
			}
			if len(e.mt) > 0 {
				mt, mtmsn = 1, 6
			}
			if e.ra {
				ra = 1
			}
			reply(fmt.Sprintf("\r\n+SBDSX: %d, 218, %d, %d, %d, 0\r\n\r\nOK\r\n", mo, mt, mtmsn, ra))
		default:
			reply("\r\n" + e.sbdsxReply + "\r\n\r\nOK\r\n")
		}
	case cmd == "AT+SBDIX" || cmd == "AT+SBDIXA":
		e.carried = append(e.carried, string(e.mo))
		if e.gssLive {
			e.mt, e.ra = nil, false
			e.momsn++
			mtStatus := 0
			if len(e.gss) > 0 {
				e.mt, e.gss = e.gss[0], e.gss[1:]
				mtStatus = 1
			}
			if !e.sbdixSilent {
				reply(fmt.Sprintf("\r\n+SBDIX: 0, %d, %d, 7, %d, %d\r\n\r\nOK\r\n", e.momsn, mtStatus, len(e.mt), len(e.gss)))
			}
			return
		}
		if !e.sbdixSilent {
			reply("\r\n" + e.sbdixReply + "\r\n\r\nOK\r\n")
		}
	case strings.HasPrefix(cmd, "AT+SBDWT="):
		e.mo = []byte(strings.TrimPrefix(cmd, "AT+SBDWT="))
		if e.loadLost {
			reply("\r\nERROR\r\n")
			return
		}
		reply("\r\nOK\r\n")
	case strings.HasPrefix(cmd, "AT+SBDWB="):
		n, err := strconv.Atoi(strings.TrimPrefix(cmd, "AT+SBDWB="))
		if err != nil || n < 1 || n > 340 {
			reply("\r\n3\r\n\r\nOK\r\n")
			return
		}
		e.binLeft = n + 2
		reply("\r\nREADY\r\n")
	case cmd == "AT+SBDD0":
		if e.sbdd0Fails {
			reply("\r\n1\r\n\r\nOK\r\n")
		} else {
			e.mo = nil
			reply("\r\n0\r\n\r\nOK\r\n")
		}
	case cmd == "AT+SBDD1":
		if e.sbdd1Fails {
			reply("\r\n1\r\n\r\nOK\r\n")
			return
		}
		e.mt = nil
		reply("\r\n0\r\n\r\nOK\r\n")
	case cmd == "AT+SBDD2":
		e.mo, e.mt = nil, nil
		reply("\r\n0\r\n\r\nOK\r\n")
	case cmd == "AT+SBDRB":
		sum := 0
		for _, c := range e.mt {
			sum += int(c)
		}
		if e.sbdrbCorrupt {
			sum++ // a byte flipped on the line
		}
		frame := []byte{byte(len(e.mt) >> 8), byte(len(e.mt))}
		frame = append(frame, e.mt...)
		frame = append(frame, byte(sum>>8), byte(sum))
		e.out = append(e.out, frame...)
		reply("\r\nOK\r\n")
	case cmd == "AT+CSQF":
		reply("\r\n+CSQF:3\r\n\r\nOK\r\n")
	default:
		reply("\r\nOK\r\n")
	}
}

func (e *sbdEmu) set(fn func(e *sbdEmu)) {
	e.mu.Lock()
	fn(e)
	e.mu.Unlock()
}

func (e *sbdEmu) dropLink() { e.set(func(e *sbdEmu) { e.dropped = true }) }

func (e *sbdEmu) sent() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.commands...)
}

func (e *sbdEmu) count(cmd string) int {
	n := 0
	for _, c := range e.sent() {
		if c == cmd {
			n++
		}
	}
	return n
}

// sessions is what the MO buffer held at each SBDIX, in order.
func (e *sbdEmu) sessions() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.carried...)
}

func (e *sbdEmu) mtBuffer() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return string(e.mt)
}

func (e *sbdEmu) moBuffer() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return string(e.mo)
}

func indexOf(cmds []string, cmd string) int {
	return indexFrom(cmds, 0, cmd)
}

// indexFrom is the index of the first cmd at or after from, or -1.
func indexFrom(cmds []string, from int, cmd string) int {
	for i := from; i >= 0 && i < len(cmds); i++ {
		if cmds[i] == cmd {
			return i
		}
	}
	return -1
}

// waitForCommand waits until cmd has been sent n times.
func waitForCommand(t *testing.T, e *sbdEmu, cmd string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for e.count(cmd) < n {
		if time.Now().After(deadline) {
			t.Fatalf("%q was not sent %d times: %q", cmd, n, e.sent())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// attachEmu connects tr to the emulator the way connectLocked leaves a
// transport (a new link, port set, monitor running), with no SBDIX in the
// last ten seconds so no rate-limit wait slows the tests.
func attachEmu(tr *DirectSatTransport, e *sbdEmu) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.file = e
	tr.port = "/dev/ttyEMU0"
	tr.connGen++
	tr.connected = true
	tr.imei = "300434067943980"
	tr.awake = true
	tr.lastSBDIX = time.Now().Add(-time.Hour)
	tr.lastGSSSync = time.Now()
	tr.startMonitor()
}

func emuTransport(t *testing.T, e *sbdEmu) *DirectSatTransport {
	t.Helper()
	tr := NewDirectSatTransport("/dev/ttyEMU0")
	attachEmu(tr, e)
	t.Cleanup(func() { tr.Close() })
	return tr
}

// An empty mailbox check is one session and reports no messages.
func TestCheckMailboxNow_EmptyIsOneSession(t *testing.T) {
	e := newSBDEmu()
	tr := emuTransport(t, e)

	out := tr.CheckMailboxNow(context.Background())

	want := MailboxResult{Kind: MailboxChecked, MOStatus: 0, Received: 0, StillQueued: 0}
	if out.Result != want {
		t.Fatalf("result %+v, want %+v", out.Result, want)
	}
	if !out.SessionAnswered || len(out.Messages) != 0 {
		t.Fatalf("answered %v, messages %q", out.SessionAnswered, out.Messages)
	}
	if n := e.count("AT+SBDIX"); n != 1 {
		t.Fatalf("%d SBDIX, want exactly 1 (%q)", n, e.sent())
	}
}

// A session that brings a message in hands it over, and what still waits
// at the gateway is reported.
func TestCheckMailboxNow_MessageAndStillQueued(t *testing.T) {
	e := newSBDEmu()
	e.sbdixReply = "+SBDIX: 0, 219, 1, 7, 5, 2"
	e.mt = []byte("hello")
	tr := emuTransport(t, e)

	out := tr.CheckMailboxNow(context.Background())

	want := MailboxResult{Kind: MailboxChecked, MOStatus: 0, Received: 1, StillQueued: 2}
	if out.Result != want {
		t.Fatalf("result %+v, want %+v", out.Result, want)
	}
	if len(out.Messages) != 1 || string(out.Messages[0]) != "hello" {
		t.Fatalf("messages %q", out.Messages)
	}
	cmds := e.sent()
	if i, j := indexOf(cmds, "AT+SBDIX"), indexOf(cmds, "AT+SBDRB"); j < i {
		t.Fatalf("the session's message was not read after the session: %q", cmds)
	}
	// Read, then dropped from the modem, or every later poll delivers it again (MESHSAT-1266).
	if indexOf(cmds, "AT+SBDD1") < indexOf(cmds, "AT+SBDRB") {
		t.Fatalf("the message was not cleared from the modem after the read: %q", cmds)
	}
}

// A message already in the MT buffer is read for free before any session,
// and a left-over MO is cleared (SBDD0) before the session, never sent
// (Android's check, open question 16 of the 0.12.0 spec).
func TestCheckMailboxNow_FreeReadFirstAndMOClearedBeforeTheSession(t *testing.T) {
	e := newSBDEmu()
	e.sbdsxReply = "+SBDSX: 1, 218, 1, 6, 0, 0"
	e.mt = []byte("earlier")
	tr := emuTransport(t, e)

	out := tr.CheckMailboxNow(context.Background())

	want := MailboxResult{Kind: MailboxChecked, MOStatus: 0, Received: 1, StillQueued: 0}
	if out.Result != want {
		t.Fatalf("result %+v, want %+v", out.Result, want)
	}
	if len(out.Messages) != 1 || string(out.Messages[0]) != "earlier" {
		t.Fatalf("messages %q", out.Messages)
	}
	cmds := e.sent()
	ix := indexOf(cmds, "AT+SBDIX")
	if rb := indexOf(cmds, "AT+SBDRB"); rb < 0 || rb > ix {
		t.Fatalf("the waiting MT was not read before the session: %q", cmds)
	}
	if d0 := indexOf(cmds, "AT+SBDD0"); d0 < 0 || d0 > ix {
		t.Fatalf("the MO buffer was not cleared before the session: %q", cmds)
	}
	if n := e.count("AT+SBDIX"); n != 1 {
		t.Fatalf("%d SBDIX, want 1", n)
	}
}

// An MO buffer the modem will not clear is never sent with a session.
func TestCheckMailboxNow_MOThatWillNotClearIsNotSent(t *testing.T) {
	e := newSBDEmu()
	e.sbdsxReply = "+SBDSX: 1, 218, 0, -1, 0, 0"
	e.sbdd0Fails = true
	tr := emuTransport(t, e)

	out := tr.CheckMailboxNow(context.Background())

	if out.Result.Kind != MailboxNoAnswer || out.SessionAnswered {
		t.Fatalf("result %+v answered %v, want no_answer", out.Result, out.SessionAnswered)
	}
	if n := e.count("AT+SBDIX"); n != 0 {
		t.Fatalf("a session went out with an MO buffer that would not clear: %q", e.sent())
	}
}

// No network is reported with its status, and the next check within the
// hold sends nothing at all.
func TestCheckMailboxNow_NoNetworkThenHeld(t *testing.T) {
	e := newSBDEmu()
	e.sbdixReply = "+SBDIX: 32, 218, 0, 0, 0, 0"
	tr := emuTransport(t, e)

	out := tr.CheckMailboxNow(context.Background())
	if out.Result.Kind != MailboxSessionFailed || out.Result.MOStatus != 32 || !out.SessionAnswered {
		t.Fatalf("first check %+v answered %v, want session_failed 32", out.Result, out.SessionAnswered)
	}
	before := len(e.sent())

	held := tr.CheckMailboxNow(context.Background())
	if held.Result.Kind != MailboxHeld || held.Result.Seconds < 1 || held.Result.Seconds > 180 {
		t.Fatalf("second check %+v, want held 1..180 s", held.Result)
	}
	if held.SessionAnswered || held.Result.MOStatus != MOStatusUnknown {
		t.Fatalf("a held check reported a session: %+v", held)
	}
	if after := len(e.sent()); after != before {
		t.Fatalf("a held check sent %q", e.sent()[before:])
	}
}

// Status 36 holds as 32 does; any other failure does not.
func TestCheckMailboxNow_OnlyNoServiceAndWaitHold(t *testing.T) {
	for _, tc := range []struct {
		mo   int
		held bool
	}{{36, true}, {32, true}, {18, false}, {10, false}} {
		t.Run(strconv.Itoa(tc.mo), func(t *testing.T) {
			e := newSBDEmu()
			e.sbdixReply = "+SBDIX: " + strconv.Itoa(tc.mo) + ", 218, 0, 0, 0, 0"
			tr := emuTransport(t, e)
			if out := tr.CheckMailboxNow(context.Background()); out.Result.Kind != MailboxSessionFailed || out.Result.MOStatus != tc.mo {
				t.Fatalf("first check %+v", out.Result)
			}
			tr.mu.Lock()
			tr.lastSBDIX = time.Now().Add(-time.Hour) // past the 10 s rate limit
			tr.mu.Unlock()
			next := tr.CheckMailboxNow(context.Background())
			if got := next.Result.Kind == MailboxHeld; got != tc.held {
				t.Fatalf("after MO %d: %+v, held want %v", tc.mo, next.Result, tc.held)
			}
		})
	}
}

// A session any caller ran (here a queued send) starts the hold too: the
// modem cannot register again for 3 minutes whoever asked.
func TestCheckMailboxNow_HeldAfterASendWithNoNetwork(t *testing.T) {
	e := newSBDEmu()
	e.sbdixReply = "+SBDIX: 32, 218, 0, 0, 0, 0"
	tr := emuTransport(t, e)
	res, err := tr.SendText(context.Background(), "hi")
	if err != nil || res.MOStatus != 32 {
		t.Fatalf("send: %+v %v", res, err)
	}
	if out := tr.CheckMailboxNow(context.Background()); out.Result.Kind != MailboxHeld {
		t.Fatalf("check after a failed send: %+v, want held", out.Result)
	}
	if n := e.count("AT+SBDIX"); n != 1 {
		t.Fatalf("%d SBDIX, want only the send's", n)
	}
}

// A link drop mid-session ends the check at once as link_lost, with no
// status; the hold runs across the reconnect and the next link is sent
// nothing.
func TestCheckMailboxNow_LinkDropMidSession(t *testing.T) {
	e := newSBDEmu()
	e.sbdixSilent = true
	tr := emuTransport(t, e)

	done := make(chan MailboxCheckOutcome, 1)
	go func() { done <- tr.CheckMailboxNow(context.Background()) }()
	deadline := time.Now().Add(3 * time.Second)
	for e.count("AT+SBDIX") == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no SBDIX went out: %q", e.sent())
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	dropped := time.Now()
	e.dropLink()

	var out MailboxCheckOutcome
	select {
	case out = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the check did not end when the link dropped")
	}
	if took := time.Since(dropped); took > 2*time.Second {
		t.Fatalf("the check took %v after the drop", took)
	}
	if out.Result.Kind != MailboxLinkLost || out.Result.MOStatus != MOStatusUnknown || out.SessionAnswered {
		t.Fatalf("result %+v answered %v, want link_lost with no status", out.Result, out.SessionAnswered)
	}
	if tr.IsConnected() {
		t.Fatal("still connected after the link dropped")
	}

	next := newSBDEmu()
	attachEmu(tr, next)
	held := tr.CheckMailboxNow(context.Background())
	if held.Result.Kind != MailboxHeld || held.Result.Seconds < 1 || held.Result.Seconds > 180 {
		t.Fatalf("check on the next link: %+v, want held", held.Result)
	}
	if n := next.count("AT+SBDIX"); n != 0 {
		t.Fatalf("the next link was sent a session within the hold: %q", next.sent())
	}
}

// A session that never answers is no_answer, not a lost link: no hold, the
// next link gets its session.
func TestCheckMailboxNow_SilentSessionIsNoAnswerWithoutHold(t *testing.T) {
	t.Setenv("IRIDIUM_SBDIX_TIMEOUT", "1")
	e := newSBDEmu()
	e.sbdixSilent = true
	tr := emuTransport(t, e)

	out := tr.CheckMailboxNow(context.Background())
	if out.Result.Kind != MailboxNoAnswer || out.SessionAnswered {
		t.Fatalf("result %+v answered %v, want no_answer", out.Result, out.SessionAnswered)
	}

	next := newSBDEmu()
	attachEmu(tr, next)
	if again := tr.CheckMailboxNow(context.Background()); again.Result.Kind != MailboxChecked {
		t.Fatalf("check after a silent session: %+v, want checked", again.Result)
	}
	if n := next.count("AT+SBDIX"); n != 1 {
		t.Fatalf("%d SBDIX on the next link, want 1", n)
	}
}

// Without a modem nothing is sent.
func TestCheckMailboxNow_NotConnected(t *testing.T) {
	tr := NewDirectSatTransport("/dev/ttyEMU0")
	if out := tr.CheckMailboxNow(context.Background()); out.Result.Kind != MailboxNotConnected || out.SessionAnswered {
		t.Fatalf("result %+v", out.Result)
	}

	e := newSBDEmu()
	attachEmu(tr, e)
	tr.Close()
	if out := tr.CheckMailboxNow(context.Background()); out.Result.Kind != MailboxNotConnected {
		t.Fatalf("after close: %+v", out.Result)
	}
	if n := len(e.sent()); n != 0 {
		t.Fatalf("sent %q without a modem", e.sent())
	}
}

// A status answer that cannot be read opens no session.
func TestCheckMailboxNow_UnreadableStatusOpensNoSession(t *testing.T) {
	e := newSBDEmu()
	e.sbdsxBroken = true
	tr := emuTransport(t, e)

	if out := tr.CheckMailboxNow(context.Background()); out.Result.Kind != MailboxNoAnswer || out.SessionAnswered {
		t.Fatalf("result %+v", out.Result)
	}
	if n := e.count("AT+SBDIX"); n != 0 {
		t.Fatalf("a session went out after an unreadable status: %q", e.sent())
	}
}

// The check waits out the 10 s between sessions on the context it is given,
// so that context must live as long as the check: the HTTP request's, which
// ended with the handler, lost the check here before B22 (trap 2). A live
// context gets its session; one cancelled during the wait gets none.
func TestCheckMailboxNow_RateLimitWaitHonoursItsContext(t *testing.T) {
	live := newSBDEmu()
	tr := emuTransport(t, live)
	tr.mu.Lock()
	tr.lastSBDIX = time.Now().Add(-minSBDIXInterval + 300*time.Millisecond)
	tr.mu.Unlock()
	start := time.Now()
	if out := tr.CheckMailboxNow(context.Background()); out.Result.Kind != MailboxChecked {
		t.Fatalf("live context: %+v", out.Result)
	}
	if waited := time.Since(start); waited < 200*time.Millisecond {
		t.Fatalf("no rate-limit wait (%v)", waited)
	}
	if n := live.count("AT+SBDIX"); n != 1 {
		t.Fatalf("live context: %d SBDIX", n)
	}

	cut := newSBDEmu()
	tr2 := emuTransport(t, cut)
	tr2.mu.Lock()
	tr2.lastSBDIX = time.Now().Add(-minSBDIXInterval + 2*time.Second)
	tr2.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	if out := tr2.CheckMailboxNow(ctx); out.Result.Kind != MailboxNotConnected {
		t.Fatalf("cancelled context: %+v", out.Result)
	}
	if n := cut.count("AT+SBDIX"); n != 0 {
		t.Fatalf("a session went out on a cancelled context: %q", cut.sent())
	}
}

// The ring-alert path's MailboxCheck says when it opened no session, so the
// gateway records none (it used to record an empty result as a successful
// one).
func TestMailboxCheck_SaysWhenNoSessionRan(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sbdsx   string
		session bool
	}{
		{"nothing calls for a session", "+SBDSX: 0, 218, 0, -1, 0, 0", false},
		{"the message is already in the modem", "+SBDSX: 0, 218, 1, 6, 0, 0", false},
		{"a ring alert", "+SBDSX: 0, 218, 0, -1, 1, 0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSBDEmu()
			e.sbdsxReply = tc.sbdsx
			tr := emuTransport(t, e)
			res, err := tr.MailboxCheck(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if res.NoSession == tc.session {
				t.Fatalf("NoSession %v, session wanted %v", res.NoSession, tc.session)
			}
			if got := e.count("AT+SBDIX") == 1; got != tc.session {
				t.Fatalf("SBDIX sent %v, want %v (%q)", got, tc.session, e.sent())
			}
		})
	}
}

// Receive, now on the shared locked read, still reads the frame and clears
// the MT buffer after the read.
func TestReceive_ReadsAndClearsTheMTBuffer(t *testing.T) {
	e := newSBDEmu()
	e.mt = []byte{0x21, 0x0D, 0x0A, 0x00, 0x4F, 0x4B}
	tr := emuTransport(t, e)
	data, err := tr.Receive(context.Background())
	if err != nil || string(data) != string([]byte{0x21, 0x0D, 0x0A, 0x00, 0x4F, 0x4B}) {
		t.Fatalf("receive %q %v", data, err)
	}
	if cmds := e.sent(); !strings.HasPrefix(strings.Join(cmds, ","), "AT+SBDRB,AT+SBDD1") {
		t.Fatalf("commands %q", cmds)
	}
}

// queuedSend is a send the delivery queue makes, binary (SBDWB) or text
// (SBDWT).
type queuedSend struct {
	payload string
	binary  bool
}

// load is the command that puts the message into the MO buffer.
func (s queuedSend) load() string {
	if s.binary {
		return "AT+SBDWB=" + strconv.Itoa(len(s.payload))
	}
	return "AT+SBDWT=" + s.payload
}

type sendOutcome struct {
	res *SBDResult
	err error
}

func (s queuedSend) start(tr *DirectSatTransport) <-chan sendOutcome {
	ch := make(chan sendOutcome, 1)
	go func() {
		var o sendOutcome
		if s.binary {
			o.res, o.err = tr.Send(context.Background(), []byte(s.payload))
		} else {
			o.res, o.err = tr.SendText(context.Background(), s.payload)
		}
		ch <- o
	}()
	return ch
}

// checkOutcome is a mailbox check's outcome: the one a person asks for
// (CheckMailboxNow) or the ring alert's (MailboxCheck).
type checkOutcome struct {
	kind    string // CheckMailboxNow's kind; empty for MailboxCheck
	session bool   // a session ran (and, for CheckMailboxNow, answered)
	err     error  // MailboxCheck's error
}

func startCheck(tr *DirectSatTransport, ring bool) <-chan checkOutcome {
	ch := make(chan checkOutcome, 1)
	go func() {
		if ring {
			res, err := tr.MailboxCheck(context.Background())
			o := checkOutcome{err: err}
			if res != nil {
				o.session = !res.NoSession
			}
			ch <- o
			return
		}
		out := tr.CheckMailboxNow(context.Background())
		ch <- checkOutcome{kind: out.Result.Kind, session: out.SessionAnswered}
	}()
	return ch
}

func await[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(15 * time.Second):
		t.Fatalf("%s did not end", what)
	}
	var zero T
	return zero
}

// A send lets go of the serial lock between loading its message into the MO
// buffer and its SBDIX: while it waits out the 10 s between sessions and
// while it stops the monitor. A mailbox check that took the serial lock
// there emptied the MO buffer (the button's check) or sent the message in
// its own session (the ring alert's); the send's SBDIX then went out empty,
// the modem answered MO status 0 and the queue marked sent a message that
// had not left, with one more billed session. A check requested then now
// waits for the send's session: nothing touches the MO buffer between the
// send's load and its SBDIX, that SBDIX carries the message, and every
// session goes out once.
func TestMailboxCheck_WaitsForASendsSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		send queuedSend
		ring bool
	}{
		{"binary send, the button's check", queuedSend{"queued data", true}, false},
		{"text send, the button's check", queuedSend{"queued text", false}, false},
		{"text send, the ring alert's check", queuedSend{"queued text", false}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSBDEmu()
			e.sbdsxLive = true
			tr := emuTransport(t, e)
			// The send waits out the rate limit after its load. SBDWB is
			// followed by 0.7 s of drains, so the binary send gets more lead.
			lead := 600 * time.Millisecond
			if tc.send.binary {
				lead = 1500 * time.Millisecond
			}
			tr.mu.Lock()
			tr.lastSBDIX = time.Now().Add(-minSBDIXInterval + lead)
			tr.mu.Unlock()
			// The check's own session would wait 10 s after the send's: the
			// check's first command (sent from its goroutine, under the
			// serial lock) moves the rate limit out of the way.
			e.set(func(e *sbdEmu) {
				e.onCommand = func(cmd string) {
					if cmd == "AT+SBDSX" {
						tr.lastSBDIX = time.Now().Add(-time.Hour)
					}
				}
			})

			sent := tc.send.start(tr)
			waitForCommand(t, e, tc.send.load(), 1)
			if tc.send.binary {
				waitForCommand(t, e, "AT", 1) // the probe after the load's drains
			}
			time.Sleep(100 * time.Millisecond) // into the rate-limit wait
			checked := startCheck(tr, tc.ring)

			so := await(t, sent, "the send")
			co := await(t, checked, "the check")
			if so.err != nil || so.res == nil || so.res.MOStatus != 0 {
				t.Fatalf("send: %+v %v", so.res, so.err)
			}
			if tc.ring {
				// After the send's session nothing calls for another.
				if co.err != nil || co.session {
					t.Fatalf("ring alert's check: session %v, err %v", co.session, co.err)
				}
			} else if co.kind != MailboxChecked || !co.session {
				t.Fatalf("check: %+v", co)
			}

			cmds := e.sent()
			load := indexOf(cmds, tc.send.load())
			ix := indexFrom(cmds, load, "AT+SBDIX")
			post := indexFrom(cmds, ix, "AT+SBDD0")
			if load < 0 || ix < 0 || post < 0 {
				t.Fatalf("no load, session or clear after it: %q", cmds)
			}
			for _, c := range cmds[load+1 : ix] {
				if c == "AT+SBDD0" || c == "AT+SBDD2" || c == "AT+SBDSX" {
					t.Fatalf("%s between the send's load and its session: %q", c, cmds)
				}
			}
			if sx := indexOf(cmds, "AT+SBDSX"); sx < post {
				t.Fatalf("the check ran before the send's session had ended: %q", cmds)
			}
			want := []string{tc.send.payload, ""}
			if tc.ring {
				want = want[:1]
			}
			if got := e.sessions(); !slices.Equal(got, want) {
				t.Fatalf("the sessions carried %q, want %q (%q)", got, want, cmds)
			}
		})
	}
}

// The reverse order: a mailbox check holds the session lock from its status
// check until its session has ended, across its own rate-limit wait. A send
// requested meanwhile used to load its message during that wait, so one of
// the two sessions carried it and the other went out empty; had the check's
// session carried it and failed, the send's empty session still answered
// MO status 0 and the message was marked sent. The send now loads its
// message only after the check's session: the check's session carries
// nothing, the send's carries the message, one session each.
func TestSend_WaitsForAMailboxChecksSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		send queuedSend
		ring bool
	}{
		{"the button's check, binary send", queuedSend{"queued data", true}, false},
		{"the button's check, text send", queuedSend{"queued text", false}, false},
		{"the ring alert's check, text send", queuedSend{"queued text", false}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSBDEmu()
			if tc.ring {
				e.sbdsxReply = "+SBDSX: 0, 218, 0, -1, 1, 0" // a ring alert calls for a session
			} else {
				e.sbdsxLive = true
			}
			tr := emuTransport(t, e)
			tr.mu.Lock()
			tr.lastSBDIX = time.Now().Add(-minSBDIXInterval + 600*time.Millisecond)
			tr.mu.Unlock()
			// The send's own session would wait 10 s after the check's: its
			// load (sent from its goroutine, under the serial lock) moves
			// the rate limit out of the way.
			load := tc.send.load()
			e.set(func(e *sbdEmu) {
				e.onCommand = func(cmd string) {
					if cmd == load {
						tr.lastSBDIX = time.Now().Add(-time.Hour)
					}
				}
			})

			checked := startCheck(tr, tc.ring)
			waitForCommand(t, e, "AT+SBDD0", 1) // the check emptied the MO buffer
			time.Sleep(100 * time.Millisecond)  // into its rate-limit wait
			sent := tc.send.start(tr)

			co := await(t, checked, "the check")
			so := await(t, sent, "the send")
			if tc.ring {
				if co.err != nil || !co.session {
					t.Fatalf("ring alert's check: session %v, err %v", co.session, co.err)
				}
			} else if co.kind != MailboxChecked || !co.session {
				t.Fatalf("check: %+v", co)
			}
			if so.err != nil || so.res == nil || so.res.MOStatus != 0 {
				t.Fatalf("send: %+v %v", so.res, so.err)
			}

			cmds := e.sent()
			ix := indexOf(cmds, "AT+SBDIX")
			post := indexFrom(cmds, ix, "AT+SBDD0")
			if l := indexOf(cmds, load); l < 0 || post < 0 || l < post {
				t.Fatalf("the send loaded its message before the check's session had ended: %q", cmds)
			}
			if got, want := e.sessions(), []string{"", tc.send.payload}; !slices.Equal(got, want) {
				t.Fatalf("the sessions carried %q, want %q (%q)", got, want, cmds)
			}
		})
	}
}

// A message waiting in the MT buffer whose free read fails is not lost to
// the check's session: a session empties the MT buffer, so the check ends
// no_answer and opens none (nothing billed, no GSS row), and the message
// stays in the modem for the next read.
func TestCheckMailboxNow_FailedFreeReadOpensNoSession(t *testing.T) {
	e := newSBDEmu()
	e.sbdsxLive = true
	e.mt = []byte("waiting")
	e.sbdrbCorrupt = true
	tr := emuTransport(t, e)

	out := tr.CheckMailboxNow(context.Background())

	want := MailboxResult{Kind: MailboxNoAnswer, MOStatus: MOStatusUnknown}
	if out.Result != want || out.SessionAnswered || len(out.Messages) != 0 {
		t.Fatalf("outcome %+v, want %+v with no session and no messages", out, want)
	}
	if n := e.count("AT+SBDRB"); n != 1 {
		t.Fatalf("%d SBDRB, want the one free read: %q", n, e.sent())
	}
	if n := e.count("AT+SBDIX"); n != 0 {
		t.Fatalf("a session went out over a message that could not be read: %q", e.sent())
	}
	if got := e.mtBuffer(); got != "waiting" {
		t.Fatalf("MT buffer %q, want the message still in it", got)
	}

	e.set(func(e *sbdEmu) { e.sbdrbCorrupt = false })
	if data, err := tr.Receive(context.Background()); err != nil || string(data) != "waiting" {
		t.Fatalf("the next read: %q %v", data, err)
	}
}

// A link closed and reopened while a send waits out the rate limit has lost
// the send's MO buffer, since every connect empties it. The send opens no
// session on the new link, where it would have gone out empty and been
// counted sent, and says the modem was not connected, so the queue keeps
// the message without spending a retry on it.
func TestSend_LinkReopenedDuringTheRateLimitWaitSendsNothing(t *testing.T) {
	e := newSBDEmu()
	tr := emuTransport(t, e)
	tr.mu.Lock()
	tr.lastSBDIX = time.Now().Add(-minSBDIXInterval + 600*time.Millisecond)
	tr.mu.Unlock()

	send := queuedSend{"queued text", false}
	sent := send.start(tr)
	waitForCommand(t, e, send.load(), 1)
	time.Sleep(100 * time.Millisecond) // into the rate-limit wait
	tr.Close()
	next := newSBDEmu()
	attachEmu(tr, next) // reconnected: a new port, both buffers empty

	so := await(t, sent, "the send")
	if so.res != nil || !errors.Is(so.err, ErrNotConnected) {
		t.Fatalf("send: %+v %v, want not connected", so.res, so.err)
	}
	if n := e.count("AT+SBDIX") + next.count("AT+SBDIX"); n != 0 {
		t.Fatalf("%d sessions went out (new link: %q)", n, next.sent())
	}
}

// The ring-alert path (the gateway's handleRingAlertWithRetry, the Reticulum
// interface's handleInbound) reads with Receive after MailboxCheck returns.
// A send queued behind the check took the session lock in between, and its
// session emptied the MT buffer (the ISU clears it as a session starts), so
// the message the check had reported was gone when Receive came. The check
// now reads it under the same lock hold that found it, and Receive hands it
// over after the send's session, once: a message already waiting, and one
// the check's own session brought in.
func TestMailboxCheck_RingAlertKeepsItsMTAcrossAQueuedSend(t *testing.T) {
	for _, tc := range []struct {
		name    string
		waiting bool // the MT is in the modem already; else the check's session brings it
	}{
		{"the MT waiting in the modem", true},
		{"the MT brought by the check's session", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSBDEmu()
			e.sbdsxLive = true
			e.gssLive = true
			if tc.waiting {
				e.mt = []byte("for you")
			} else {
				e.ra = true
				e.gss = [][]byte{[]byte("for you")}
			}
			tr := emuTransport(t, e)

			// The send is requested while the check holds the session lock
			// (the check's first command runs under it), so it queues behind
			// the check; its own session need not wait 10 s after the check's.
			send := queuedSend{"queued text", false}
			var sent <-chan sendOutcome
			var once sync.Once
			e.set(func(e *sbdEmu) {
				e.onCommand = func(cmd string) {
					switch cmd {
					case "AT+SBDSX":
						once.Do(func() {
							sent = send.start(tr)
							time.Sleep(50 * time.Millisecond) // blocked on the session lock
						})
					case send.load():
						tr.lastSBDIX = time.Now().Add(-time.Hour)
					}
				}
			})

			res, err := tr.MailboxCheck(context.Background())
			if err != nil || res.MTStatus != 1 || res.MTLength == 0 {
				t.Fatalf("the ring alert's check: %+v %v, want a message reported", res, err)
			}
			if res.NoSession != tc.waiting {
				t.Fatalf("NoSession %v, want %v", res.NoSession, tc.waiting)
			}
			so := await(t, sent, "the queued send")
			if so.err != nil || so.res.MOStatus != 0 {
				t.Fatalf("send: %+v %v", so.res, so.err)
			}

			// The caller's Receive, after the send's session.
			data, err := tr.Receive(context.Background())
			if err != nil || string(data) != "for you" {
				t.Fatalf("receive after the send's session: %q %v, want the message", data, err)
			}
			if again, err := tr.Receive(context.Background()); err != nil || len(again) != 0 {
				t.Fatalf("a second receive: %q %v, want nothing", again, err)
			}

			cmds := e.sent()
			sendIX := indexFrom(cmds, indexOf(cmds, send.load()), "AT+SBDIX")
			if rb := indexOf(cmds, "AT+SBDRB"); rb < 0 || sendIX < 0 || rb > sendIX {
				t.Fatalf("the MT was not read before the send's session: %q", cmds)
			}
			want := []string{send.payload}
			if !tc.waiting {
				want = []string{"", send.payload}
			}
			if got := e.sessions(); !slices.Equal(got, want) {
				t.Fatalf("the sessions carried %q, want %q", got, want)
			}
		})
	}
}

// A send whose load the modem took but whose answer was lost fails before
// its session. It left the message in the MO buffer, where the next session
// (a ring alert's, which sent whatever it found there) carried it, and the
// queue's retry carried it again. A send now empties the buffer on every
// failure after its load: the next session carries none of it, and the
// retry sends it once.
func TestSend_FailureAfterTheLoadLeavesTheMOBufferEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		send queuedSend
	}{
		{"binary", queuedSend{"queued data", true}},
		{"text", queuedSend{"queued text", false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSBDEmu()
			e.sbdsxLive = true
			e.gssLive = true
			e.loadLost = true
			tr := emuTransport(t, e)

			if o := await(t, tc.send.start(tr), "the send"); o.err == nil {
				t.Fatalf("the send did not fail: %+v", o.res)
			}
			cmds := e.sent()
			if load := indexOf(cmds, tc.send.load()); load < 0 || indexFrom(cmds, load, "AT+SBDD0") < 0 {
				t.Fatalf("the MO buffer was not cleared after the failed load: %q", cmds)
			}
			if mo := e.moBuffer(); mo != "" {
				t.Fatalf("MO buffer %q after the failed send, want it empty", mo)
			}
			if n := e.count("AT+SBDIX"); n != 0 {
				t.Fatalf("the failed send opened a session: %q", cmds)
			}

			// The next session, a ring alert's, carries none of it.
			e.set(func(e *sbdEmu) { e.ra, e.loadLost = true, false })
			if res, err := tr.MailboxCheck(context.Background()); err != nil || res.NoSession {
				t.Fatalf("the ring alert's check: %+v %v, want a session", res, err)
			}
			// The queue's retry sends it, once.
			tr.mu.Lock()
			tr.lastSBDIX = time.Now().Add(-time.Hour)
			tr.mu.Unlock()
			if o := await(t, tc.send.start(tr), "the retry"); o.err != nil || o.res.MOStatus != 0 {
				t.Fatalf("retry: %+v %v", o.res, o.err)
			}
			if got, want := e.sessions(), []string{"", tc.send.payload}; !slices.Equal(got, want) {
				t.Fatalf("the sessions carried %q, want %q", got, want)
			}
		})
	}
}

// Whatever is left in the MO buffer when the ring alert's check runs (a
// send's clear that did not get through) belongs to a send that failed and
// that its queue retries, so it is cleared before any session and never
// sent: sent from here it went out twice. It no longer calls for a session
// by itself, and a buffer the modem will not clear keeps any session from
// going out, as in the button's check.
func TestMailboxCheck_ClearsALeftoverMOInsteadOfSendingIt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ra       bool
		stuck    bool // the modem will not clear the MO buffer
		wantErr  bool
		sessions []string
	}{
		{"with a ring alert", true, false, false, []string{""}},
		{"alone", false, false, false, nil},
		{"that will not clear", true, true, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSBDEmu()
			e.sbdsxLive = true
			e.gssLive = true
			e.mo = []byte("leftover")
			e.ra = tc.ra
			e.sbdd0Fails = tc.stuck
			tr := emuTransport(t, e)

			res, err := tr.MailboxCheck(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("err %v, want an error: %v", err, tc.wantErr)
			}
			if err == nil && res.NoSession != (tc.sessions == nil) {
				t.Fatalf("NoSession %v, want %v", res.NoSession, tc.sessions == nil)
			}
			if got := e.sessions(); !slices.Equal(got, tc.sessions) {
				t.Fatalf("the sessions carried %q, want %q (%q)", got, tc.sessions, e.sent())
			}
			if !tc.stuck && e.moBuffer() != "" {
				t.Fatalf("MO buffer %q, want it cleared", e.moBuffer())
			}
		})
	}
}

// A send's own session can bring an MT in. The send returned with the
// message still in the modem, and the gateway's follow-up check
// (handleRingAlert: MailboxCheck, then Receive) came only after the send let
// go of the session lock; a send already queued for it ran its session
// first and emptied the MT buffer. The send now holds the message itself:
// the queued send's session cannot touch it, the follow-up check reports it
// without opening a session, and Receive hands it over once.
func TestSend_HoldsTheMTItsSessionBroughtIn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first queuedSend
	}{
		{"binary send", queuedSend{"first data", true}},
		{"text send", queuedSend{"first text", false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSBDEmu()
			e.sbdsxLive = true
			e.gssLive = true
			e.gss = [][]byte{[]byte("for you")}
			tr := emuTransport(t, e)

			// The second send is requested while the first holds the session
			// lock (its load runs under it), so it queues behind the first;
			// its own session need not wait 10 s after the first one's.
			second := queuedSend{"second text", false}
			var queued <-chan sendOutcome
			var once sync.Once
			e.set(func(e *sbdEmu) {
				e.onCommand = func(cmd string) {
					switch cmd {
					case tc.first.load():
						once.Do(func() {
							queued = second.start(tr)
							time.Sleep(50 * time.Millisecond) // blocked on the session lock
						})
					case second.load():
						tr.lastSBDIX = time.Now().Add(-time.Hour)
					}
				}
			})

			fo := await(t, tc.first.start(tr), "the first send")
			if fo.err != nil || fo.res.MOStatus != 0 || fo.res.MTStatus != 1 {
				t.Fatalf("first send: %+v %v, want sent with a message in", fo.res, fo.err)
			}
			so := await(t, queued, "the queued send")
			if so.err != nil || so.res.MOStatus != 0 || so.res.MTStatus != 0 {
				t.Fatalf("queued send: %+v %v", so.res, so.err)
			}

			// The gateway's follow-up check, after both sends.
			res, err := tr.MailboxCheck(context.Background())
			if err != nil || res.MTStatus != 1 || res.MTLength == 0 || !res.NoSession {
				t.Fatalf("follow-up check: %+v %v, want the message reported and no session", res, err)
			}
			if data, err := tr.Receive(context.Background()); err != nil || string(data) != "for you" {
				t.Fatalf("receive: %q %v, want the message", data, err)
			}
			if again, err := tr.Receive(context.Background()); err != nil || len(again) != 0 {
				t.Fatalf("a second receive: %q %v, want nothing", again, err)
			}
			if res, err := tr.MailboxCheck(context.Background()); err != nil || res.MTStatus == 1 || !res.NoSession {
				t.Fatalf("the next check: %+v %v, want nothing reported and no session", res, err)
			}

			cmds := e.sent()
			queuedIX := indexFrom(cmds, indexOf(cmds, second.load()), "AT+SBDIX")
			if rb := indexOf(cmds, "AT+SBDRB"); rb < 0 || queuedIX < 0 || rb > queuedIX {
				t.Fatalf("the MT was not read before the queued send's session: %q", cmds)
			}
			if got, want := e.sessions(), []string{tc.first.payload, second.payload}; !slices.Equal(got, want) {
				t.Fatalf("the sessions carried %q, want %q (no other session)", got, want)
			}
		})
	}
}

// When no follow-up check runs for a send whose session brought a message
// in (the gateway's ringAlertActive skips the spawn while another check
// runs), the held message survives later sessions and the next Receive
// hands it over, once. A send whose session brings nothing in reads
// nothing.
func TestSend_HeldMTWaitsForTheNextReceive(t *testing.T) {
	e := newSBDEmu()
	e.sbdsxLive = true
	e.gssLive = true
	e.gss = [][]byte{[]byte("for you")}
	tr := emuTransport(t, e)

	first, next := queuedSend{"first text", false}, queuedSend{"next text", false}
	if o := await(t, first.start(tr), "the send"); o.err != nil || o.res.MTStatus != 1 {
		t.Fatalf("send: %+v %v, want a message in", o.res, o.err)
	}
	tr.mu.Lock()
	tr.lastSBDIX = time.Now().Add(-time.Hour)
	tr.mu.Unlock()
	if o := await(t, next.start(tr), "the next send"); o.err != nil || o.res.MTStatus != 0 {
		t.Fatalf("next send: %+v %v", o.res, o.err)
	}
	if n := e.count("AT+SBDRB"); n != 1 {
		t.Fatalf("%d SBDRB, want only the first send's read: %q", n, e.sent())
	}

	if data, err := tr.Receive(context.Background()); err != nil || string(data) != "for you" {
		t.Fatalf("receive: %q %v, want the message", data, err)
	}
	if again, err := tr.Receive(context.Background()); err != nil || len(again) != 0 {
		t.Fatalf("a second receive: %q %v, want nothing", again, err)
	}
	if got, want := e.sessions(), []string{first.payload, next.payload}; !slices.Equal(got, want) {
		t.Fatalf("the sessions carried %q, want %q", got, want)
	}
}

// A person's check hands over a message a send's session brought in and no
// follow-up check has taken yet: the modem no longer has it for the check's
// free read.
func TestCheckMailboxNow_HandsOverAMessageASendHeld(t *testing.T) {
	e := newSBDEmu()
	e.sbdsxLive = true
	e.gssLive = true
	e.gss = [][]byte{[]byte("for you")}
	tr := emuTransport(t, e)

	send := queuedSend{"first text", false}
	if o := await(t, send.start(tr), "the send"); o.err != nil || o.res.MTStatus != 1 {
		t.Fatalf("send: %+v %v, want a message in", o.res, o.err)
	}
	tr.mu.Lock()
	tr.lastSBDIX = time.Now().Add(-time.Hour)
	tr.mu.Unlock()

	out := tr.CheckMailboxNow(context.Background())
	want := MailboxResult{Kind: MailboxChecked, MOStatus: 0, Received: 1, StillQueued: 0}
	if out.Result != want || len(out.Messages) != 1 || string(out.Messages[0]) != "for you" {
		t.Fatalf("check: %+v, want %+v with the held message", out, want)
	}
	if again, err := tr.Receive(context.Background()); err != nil || len(again) != 0 {
		t.Fatalf("a receive after the check: %q %v, want nothing", again, err)
	}
	if got, want := e.sessions(), []string{send.payload, ""}; !slices.Equal(got, want) {
		t.Fatalf("the sessions carried %q, want %q", got, want)
	}
}

// When the clear after a read does not go through, the modem keeps
// reporting the message in its MT buffer (the MT flag stays set after
// SBDRB). It is not read, held or handed over again: the follow-up check
// reports the held copy without a session, Receive hands it over once, and
// later reads and checks leave the modem's copy alone. The next session
// empties the buffer, and a message it brings in is handed over.
func TestMailboxCheck_AMessageStillInTheModemAfterItsReadIsNotHandedOverTwice(t *testing.T) {
	e := newSBDEmu()
	e.sbdsxLive = true
	e.gssLive = true
	e.gss = [][]byte{[]byte("for you"), []byte("then this")}
	e.sbdd1Fails = true
	tr := emuTransport(t, e)

	first := queuedSend{"first text", false}
	if o := await(t, first.start(tr), "the send"); o.err != nil || o.res.MTStatus != 1 {
		t.Fatalf("send: %+v %v, want a message in", o.res, o.err)
	}
	if got := e.mtBuffer(); got != "for you" {
		t.Fatalf("MT buffer %q, want the message still in the modem", got)
	}

	res, err := tr.MailboxCheck(context.Background())
	if err != nil || res.MTStatus != 1 || !res.NoSession {
		t.Fatalf("follow-up check: %+v %v, want the held message reported and no session", res, err)
	}
	if data, err := tr.Receive(context.Background()); err != nil || string(data) != "for you" {
		t.Fatalf("receive: %q %v, want the message", data, err)
	}
	if again, err := tr.Receive(context.Background()); err != nil || len(again) != 0 {
		t.Fatalf("a second receive: %q %v, want nothing", again, err)
	}
	if res, err := tr.MailboxCheck(context.Background()); err != nil || res.MTStatus == 1 || !res.NoSession {
		t.Fatalf("the next check: %+v %v, want nothing reported and no session", res, err)
	}
	if n := e.count("AT+SBDRB"); n != 1 {
		t.Fatalf("%d SBDRB, want the message read once: %q", n, e.sent())
	}
	if n := e.count("AT+SBDIX"); n != 1 {
		t.Fatalf("%d SBDIX, want only the send's: %q", n, e.sent())
	}

	// The next session empties the buffer and brings the next message in.
	tr.mu.Lock()
	tr.lastSBDIX = time.Now().Add(-time.Hour)
	tr.mu.Unlock()
	next := queuedSend{"next text", false}
	if o := await(t, next.start(tr), "the next send"); o.err != nil || o.res.MTStatus != 1 {
		t.Fatalf("next send: %+v %v, want the next message in", o.res, o.err)
	}
	if data, err := tr.Receive(context.Background()); err != nil || string(data) != "then this" {
		t.Fatalf("receive after the next session: %q %v, want the new message", data, err)
	}
	if got, want := e.sessions(), []string{first.payload, next.payload}; !slices.Equal(got, want) {
		t.Fatalf("the sessions carried %q, want %q", got, want)
	}
}

// An SBDIX the modem refuses with ERROR runs no session and leaves the MT
// buffer as it was, so a message already read that is still there (its
// clear did not go through) is not handed over again.
func TestReceive_AMessageLeftInTheModemSurvivesARefusedSBDIX(t *testing.T) {
	e := newSBDEmu()
	e.sbdsxLive = true
	e.mt = []byte("for you")
	e.sbdd1Fails = true
	tr := emuTransport(t, e)

	if data, err := tr.Receive(context.Background()); err != nil || string(data) != "for you" {
		t.Fatalf("receive: %q %v, want the message", data, err)
	}
	e.set(func(e *sbdEmu) { e.sbdixReply = "ERROR" })
	if res, err := tr.SendText(context.Background(), "refused"); err == nil {
		t.Fatalf("send: %+v, want the refused SBDIX to fail it", res)
	}
	if again, err := tr.Receive(context.Background()); err != nil || len(again) != 0 {
		t.Fatalf("receive after the refused SBDIX: %q %v, want nothing", again, err)
	}
	if n := e.count("AT+SBDRB"); n != 1 {
		t.Fatalf("%d SBDRB, want the message read once: %q", n, e.sent())
	}
}
