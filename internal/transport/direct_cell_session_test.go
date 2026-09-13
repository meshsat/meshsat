package transport

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.bug.st/serial"
)

// fakeCellPort is a serial port that hands out data once, then idles until
// it is closed; reads on a closed port fail like a real one.
type fakeCellPort struct {
	serial.Port
	mu     sync.Mutex
	closed bool
	data   []byte
}

func (p *fakeCellPort) Read(b []byte) (int, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return 0, errors.New("Port has been closed")
	}
	if len(p.data) > 0 {
		n := copy(b, p.data)
		p.data = p.data[n:]
		p.mu.Unlock()
		return n, nil
	}
	p.mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	return 0, nil
}

func (p *fakeCellPort) Write(b []byte) (int, error)          { return len(b), nil }
func (p *fakeCellPort) SetReadTimeout(_ time.Duration) error { return nil }

func (p *fakeCellPort) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return nil
}

func (p *fakeCellPort) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func shortCellTimeouts(t *testing.T) {
	t.Helper()
	origQueue, origGrace := cmdEnqueueTimeout, cmdResponseGrace
	cmdEnqueueTimeout, cmdResponseGrace = 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { cmdEnqueueTimeout, cmdResponseGrace = origQueue, origGrace })
}

// installSession makes p the current port with a session whose loop is not
// serving its channel, as when a command waits in a channel nobody reads. It
// also sets the transport's own channel fields, the ones the code before
// MESHSAT-1114 queued to, so these tests exercise the same state a real
// reconnect leaves behind.
func installSession(tr *DirectCellTransport, p serial.Port) *cellSession {
	s := &cellSession{cmdCh: make(chan atCommand, 16), stop: make(chan struct{}), done: make(chan struct{}), file: p}
	tr.mu.Lock()
	tr.file = p
	tr.cmdCh, tr.stopCh, tr.ioDone = s.cmdCh, s.stop, s.done
	tr.mu.Unlock()
	tr.sessMu.Lock()
	tr.sess = s
	tr.sessMu.Unlock()
	return s
}

// parallax, 13 Sep 2026: a command queued to a session that a reconnect then
// replaced timed out and closed the new session's port, over and over.
// [MESHSAT-1114]
func TestCellSubmit_StaleSessionLeavesNewPortAlone(t *testing.T) {
	shortCellTimeouts(t)
	tr := NewDirectCellTransport("/dev/null")
	oldPort, newPort := &fakeCellPort{}, &fakeCellPort{}
	installSession(tr, oldPort)

	errCh := make(chan error, 1)
	go func() {
		_, err := tr.execRawFn(func(serial.Port) (string, error) { return "OK", nil }, 10*time.Millisecond)
		errCh <- err
	}()
	time.Sleep(20 * time.Millisecond) // queued to the old session
	installSession(tr, newPort)       // a reconnect replaced it

	if err := <-errCh; err == nil {
		t.Fatal("a command nobody ran reported success")
	}
	if newPort.isClosed() {
		t.Fatal("a timeout from the old session closed the new session's port")
	}
}

// Positive control for the test above: the same timeout on a session that
// still owns the port does close it (the MESHSAT-986 wedge cure).
func TestCellSubmit_TimeoutClosesItsOwnSession(t *testing.T) {
	shortCellTimeouts(t)
	tr := NewDirectCellTransport("/dev/null")
	p := &fakeCellPort{}
	installSession(tr, p)
	if _, err := tr.execAT("AT", 10*time.Millisecond); err == nil {
		t.Fatal("a command nobody ran reported success")
	}
	if !p.isClosed() {
		t.Fatal("a wedged session's own timeout did not close its port")
	}
}

func TestCellSubmit_EndedSessionFailsAtOnce(t *testing.T) {
	shortCellTimeouts(t)
	cmdEnqueueTimeout, cmdResponseGrace = time.Minute, time.Minute
	tr := NewDirectCellTransport("/dev/null")
	p := &fakeCellPort{}
	s := installSession(tr, p)
	close(s.done)

	start := time.Now()
	_, err := tr.execRawFn(func(serial.Port) (string, error) { return "OK", nil }, time.Minute)
	if !errors.Is(err, ErrCellSessionEnded) {
		t.Fatalf("err %v, want ErrCellSessionEnded", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v to notice the loop had exited", d)
	}
	if p.isClosed() {
		t.Fatal("an ended session closed the port")
	}
}

func TestCellSubmit_NoLoopFailsAtOnce(t *testing.T) {
	tr := NewDirectCellTransport("/dev/null")
	start := time.Now()
	if _, err := tr.execAT("AT", time.Second); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("err %v, want ErrNotConnected", err)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("took %v with no loop running", d)
	}
}

// The health probe's AT used to close the port under an SMS that was waiting
// for the network. A loop inside a command's deadline is busy, not wedged.
// [MESHSAT-1114]
func TestCellSubmit_BusyLoopIsNotCut(t *testing.T) {
	shortCellTimeouts(t)
	tr := NewDirectCellTransport("/dev/null")
	p := &fakeCellPort{}
	installSession(tr, p)

	tr.busyUntil.Store(time.Now().Add(time.Minute).UnixNano())
	if _, err := tr.execAT("AT", 10*time.Millisecond); !errors.Is(err, ErrCellProbeBusy) {
		t.Fatalf("err %v, want busy", err)
	}
	if p.isClosed() {
		t.Fatal("a queued command closed the port under a command inside its deadline")
	}

	// Past that deadline the loop is wedged after all, and the port is cut.
	tr.busyUntil.Store(time.Now().Add(-time.Second).UnixNano())
	if _, err := tr.execAT("AT", 10*time.Millisecond); err == nil || errors.Is(err, ErrCellProbeBusy) {
		t.Fatalf("err %v, want a timeout", err)
	}
	if !p.isClosed() {
		t.Fatal("a loop past its command's deadline was not cut")
	}
}

func TestCellProbe_BusyLoopIsNotAMiss(t *testing.T) {
	shortCellTimeouts(t)
	tr := NewDirectCellTransport("/dev/null")
	tr.stateMu.Lock()
	tr.connected = true
	tr.stateMu.Unlock()
	p := &fakeCellPort{}
	installSession(tr, p)
	tr.busyUntil.Store(time.Now().Add(time.Minute).UnixNano())

	if err := tr.Probe(context.Background()); !errors.Is(err, ErrCellProbeBusy) {
		t.Fatalf("probe during a long command: %v, want busy", err)
	}
	if p.isClosed() {
		t.Fatal("the probe closed the port under a long command")
	}
}

// When the I/O loop exits, its session is gone: new callers fail at once
// instead of queueing into a channel no loop will read. [MESHSAT-1114]
func TestCellIOLoopExit_DropsSession(t *testing.T) {
	tr := NewDirectCellTransport("/dev/null")
	p := &fakeCellPort{}
	p.Close() // the first read fails, as after a forced reconnect
	tr.mu.Lock()
	tr.file = p
	tr.running = true
	tr.cmdCh = make(chan atCommand, 16)
	tr.stopCh = make(chan struct{})
	tr.ioDone = make(chan struct{})
	tr.sess = &cellSession{cmdCh: tr.cmdCh, stop: tr.stopCh, done: tr.ioDone, file: p}
	tr.mu.Unlock()

	go tr.ioLoop()
	select {
	case <-tr.ioDone:
	case <-time.After(2 * time.Second):
		t.Fatal("I/O loop did not exit on a failed read")
	}
	if tr.currentSession() != nil {
		t.Fatal("the exited loop's session is still current")
	}
	if _, err := tr.execAT("AT", time.Second); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("err %v, want ErrNotConnected", err)
	}
}

// parallax, 13 Sep 2026: the reply to a sent SMS arrived as
// "\r\nCMGS:04\rK\r"; the message reference proves it went out. [MESHSAT-1114]
func TestReadCMGSResponse_DamagedReplyCountsAsSent(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		sent  bool
	}{
		{"damaged reply from parallax", "\r\nCMGS:04\rK\r", true},
		{"normal reply", "\r\n+CMGS: 12\r\n\r\nOK\r\n", true},
		{"network error", "\r\n+CMS ERROR: 500\r\n", false},
	}
	for _, c := range cases {
		start := time.Now()
		resp, err := readCMGSResponse(&fakeCellPort{data: []byte(c.reply)}, 2*time.Second)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if d := time.Since(start); d > time.Second {
			t.Fatalf("%s: took %v", c.name, d)
		}
		if got := cmgsReference.MatchString(resp); got != c.sent {
			t.Fatalf("%s: counted as sent %v, want %v (resp %q)", c.name, got, c.sent, resp)
		}
		if !c.sent && !strings.Contains(resp, "ERROR") {
			t.Fatalf("%s: error reply lost: %q", c.name, resp)
		}
	}
}
