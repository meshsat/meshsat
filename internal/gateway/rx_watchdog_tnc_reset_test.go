package gateway

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// The TNC reset over the USB control lines: the watchdog's step for a serial
// TNC that has sent no byte since its link opened. [MESHSAT-1896]

type tncResetHarness struct {
	*wdHarness
	resets int
	audit  []string
}

func newTNCResetHarness() *tncResetHarness {
	h := &tncResetHarness{wdHarness: newWDHarness()}
	h.wd.act.ResetTNC = func(ctx context.Context) error { h.mu.Lock(); h.resets++; h.mu.Unlock(); return nil }
	h.wd.act.Audit = func(d string) { h.mu.Lock(); h.audit = append(h.audit, d); h.mu.Unlock() }
	h.mu.Lock()
	h.health = ReceiveHealth{Running: true, Level: -1, Serial: true, LinkOpenedAt: h.now}
	h.mu.Unlock()
	return h
}

func (h *tncResetHarness) resetCount() int { h.mu.Lock(); defer h.mu.Unlock(); return h.resets }

func (h *tncResetHarness) lastAudit() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.audit) == 0 {
		return ""
	}
	return h.audit[len(h.audit)-1]
}

func (h *tncResetHarness) talk() {
	h.mu.Lock()
	h.health.BytesIn += 60
	h.mu.Unlock()
	h.frame()
}

// Silent past the cold start: one reset with the silence, one more per
// silence window, three at most.
func TestRxWatchdog_SilentTNCIsResetOverItsUSBLines(t *testing.T) {
	h := newTNCResetHarness()
	ctx := context.Background()
	h.advance(4 * time.Minute)
	h.wd.tick(ctx)
	if h.resetCount() != 0 {
		t.Fatal("reset inside the cold start")
	}
	h.advance(2 * time.Minute)
	h.wd.tick(ctx)
	waitFor(t, func() bool { return h.resetCount() == 1 })
	if a := h.lastAudit(); a != "APRS TNC silent: step 1, reset the TNC over its USB lines" {
		t.Fatalf("step row %q", a)
	}
	for range 4 { // within the silence window: no second reset
		h.advance(time.Minute)
		h.wd.tick(ctx)
	}
	if h.resetCount() != 1 {
		t.Fatalf("%d resets within one silence window", h.resetCount())
	}
	for range 30 {
		h.advance(time.Minute)
		h.wd.tick(ctx)
	}
	waitFor(t, func() bool { return h.resetCount() == tncResetMax })
	time.Sleep(20 * time.Millisecond)
	if h.resetCount() != tncResetMax {
		t.Fatalf("%d resets, want at most %d per silence", h.resetCount(), tncResetMax)
	}
	r, c, b := h.counts()
	if r+c+b != 0 {
		t.Fatalf("other rungs ran on a silent TNC: restarts=%d cycles=%d bridge=%d", r, c, b)
	}
}

// A radio that talks again within one silence window of a reset is credited
// to it; the device log reads "recovered after step N".
func TestRxWatchdog_TNCTalkingSoonAfterTheResetIsCredited(t *testing.T) {
	h := newTNCResetHarness()
	ctx := context.Background()
	h.advance(6 * time.Minute)
	h.wd.tick(ctx)
	waitFor(t, func() bool { return h.resetCount() == 1 })
	h.advance(time.Minute)
	h.talk()
	h.wd.tick(ctx)
	if a := h.lastAudit(); a != "APRS TNC recovered after step 1 (reset the TNC over its USB lines)" {
		t.Fatalf("recovery row %q", a)
	}
	if h.wd.State() != ReceiveStateOK {
		t.Fatalf("state %q", h.wd.State())
	}
}

// A radio that comes back long after the last reset (someone pressed PTT)
// is not credited to the reset.
func TestRxWatchdog_TNCTalkingLongAfterTheResetIsNotCredited(t *testing.T) {
	h := newTNCResetHarness()
	h.wd.act.ResetTNC = func(ctx context.Context) error { h.mu.Lock(); h.resets++; h.mu.Unlock(); return errors.New("no lines") }
	ctx := context.Background()
	h.advance(6 * time.Minute)
	h.wd.tick(ctx)
	waitFor(t, func() bool { return h.resetCount() == 1 })
	waitFor(t, func() bool { return strings.HasPrefix(h.lastAudit(), "step 1 failed: no lines") })
	// Exhaust the resets, then wait more than a window past the last one.
	for range 40 {
		h.advance(time.Minute)
		h.wd.tick(ctx)
	}
	waitFor(t, func() bool { return h.resetCount() == tncResetMax })
	h.advance(10 * time.Minute)
	h.talk()
	h.wd.tick(ctx)
	if a := h.lastAudit(); a != "APRS TNC talking again after its silence (no step ran: the radio was switched on)" {
		t.Fatalf("late recovery row %q", a)
	}
}

// A reset on request during a silence is that silence's next step; outside
// one it is logged as a reset on request.
func TestRxWatchdog_ResetTNCNow(t *testing.T) {
	h := newTNCResetHarness()
	ctx := context.Background()
	if err := h.wd.ResetTNCNow(ctx, "API"); err != nil {
		t.Fatal(err)
	}
	if h.resetCount() != 1 || h.lastAudit() != "APRS TNC reset over its USB lines on request (API)" {
		t.Fatalf("resets %d, row %q", h.resetCount(), h.lastAudit())
	}
	h.advance(6 * time.Minute)
	h.wd.tick(ctx) // silent: automatic step 1
	waitFor(t, func() bool { return h.resetCount() == 2 })
	if err := h.wd.ResetTNCNow(ctx, "OOB"); err != nil {
		t.Fatal(err)
	}
	if a := h.lastAudit(); a != "APRS TNC silent: step 2, reset the TNC over its USB lines (manual, OOB)" {
		t.Fatalf("manual step row %q", a)
	}
	h.advance(30 * time.Second)
	h.talk()
	h.wd.tick(ctx)
	if a := h.lastAudit(); a != "APRS TNC recovered after step 2 (reset the TNC over its USB lines)" {
		t.Fatalf("recovery row %q", a)
	}

	h2 := newWDHarness()
	if err := h2.wd.ResetTNCNow(ctx, "API"); err == nil {
		t.Fatal("a kit without a TNC reset said it reset")
	}
}

// A new link opening starts the count over.
func TestRxWatchdog_TNCResetCountIsPerLink(t *testing.T) {
	h := newTNCResetHarness()
	ctx := context.Background()
	for range 40 {
		h.advance(time.Minute)
		h.wd.tick(ctx)
	}
	waitFor(t, func() bool { return h.resetCount() == tncResetMax })
	h.mu.Lock()
	h.health.LinkOpenedAt = h.now
	h.mu.Unlock()
	for range 6 {
		h.advance(time.Minute)
		h.wd.tick(ctx)
	}
	waitFor(t, func() bool { return h.resetCount() == tncResetMax+1 })
}

// The line sequence on the port: DTR off, RTS on for the hold, RTS off.
// DTR never goes on, so the radio cannot boot into its download mode.
type fakeTNCPort struct {
	mu    sync.Mutex
	calls []string
}

func (p *fakeTNCPort) Read(b []byte) (int, error)  { return 0, nil }
func (p *fakeTNCPort) Write(b []byte) (int, error) { return len(b), nil }
func (p *fakeTNCPort) Close() error                { return nil }
func (p *fakeTNCPort) SetDTR(on bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, map[bool]string{true: "DTR on", false: "DTR off"}[on])
	return nil
}
func (p *fakeTNCPort) SetRTS(on bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, map[bool]string{true: "RTS on", false: "RTS off"}[on])
	return nil
}

func TestKISSConn_ResetTNCDrivesTheLines(t *testing.T) {
	port := &fakeTNCPort{}
	k := NewKISSSerialConn("/dev/serial/by-id/usb-test", 115200)
	k.rw = port
	start := time.Now()
	if err := k.ResetTNC(context.Background(), 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Fatal("the reset was not held")
	}
	if got := strings.Join(port.calls, ", "); got != "DTR off, RTS on, RTS off" {
		t.Fatalf("lines: %s", got)
	}

	// Cancelled during the hold: RTS is still released.
	port2 := &fakeTNCPort{}
	k.rw = port2
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	if err := k.ResetTNC(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled reset: %v", err)
	}
	if got := strings.Join(port2.calls, ", "); got != "DTR off, RTS on, RTS off" {
		t.Fatalf("cancelled reset left the lines at: %s", got)
	}

	// No serial TNC: refused.
	if err := NewKISSConn("127.0.0.1:8001").ResetTNC(context.Background(), time.Millisecond); err == nil {
		t.Fatal("a TCP link said it reset a TNC")
	}
}
