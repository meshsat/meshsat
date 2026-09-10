package spectrum

import (
	"context"
	"testing"
	"time"
)

// A monitor built without a dongle stays disabled, and Attach turns it
// into a running monitor with every band calibrating, as if the dongle
// had been present at boot. [MESHSAT-1002]
func TestAttachEnablesADisabledMonitor(t *testing.T) {
	m := NewSpectrumMonitor(nil, DefaultBands)
	if m.Enabled() {
		t.Fatal("monitor without a scanner must start disabled")
	}
	for _, bs := range m.Status() {
		if bs.State != StateDisabled {
			t.Fatalf("band %s: want disabled before attach, got %s", bs.Band, bs.State)
		}
	}
	if m.Hardware().Available {
		t.Fatal("hardware must report unavailable before attach")
	}
	if err := m.RestartScan(context.Background()); err == nil {
		t.Fatal("RestartScan on a disabled monitor must fail")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if m.Attach(ctx, &mockScanner{enabled: false}) {
		t.Fatal("an unavailable scanner must not attach")
	}
	if m.Enabled() {
		t.Fatal("monitor must stay disabled after a refused attach")
	}

	sc := newMockScanner([]float64{-60, -60, -60})
	if !m.Attach(ctx, sc) {
		t.Fatal("attach with an available scanner must succeed")
	}
	if !m.Enabled() {
		t.Fatal("monitor must be enabled after attach")
	}
	hw := m.Hardware()
	if !hw.Available || hw.Scanner.BinaryPath != "mock" {
		t.Fatalf("hardware after attach: %+v", hw)
	}
	for _, bs := range m.Status() {
		if bs.State != StateCalibrating {
			t.Fatalf("band %s: want calibrating after attach, got %s", bs.Band, bs.State)
		}
	}
	if m.Uptime() <= 0 {
		t.Fatal("uptime must run from the attach")
	}
	if m.Attach(ctx, newMockScanner(nil)) {
		t.Fatal("a second attach must be a no-op")
	}
	if err := m.RestartScan(ctx); err != nil {
		t.Fatalf("RestartScan after attach: %v", err)
	}
	// The scan loop is running: the mock gets called.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sc.mu.Lock()
		calls := sc.calls
		sc.mu.Unlock()
		if calls > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("scan loop did not start after attach")
}

// AttachWhenPresent needs a scanner binary on PATH to have anything to
// attach; without one it returns immediately instead of polling.
func TestAttachWhenPresentReturnsWithoutBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	m := NewSpectrumMonitor(nil, DefaultBands)
	done := make(chan struct{})
	go func() {
		AttachWhenPresent(context.Background(), m, time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("AttachWhenPresent must return at once when no scanner binary exists")
	}
	if m.Enabled() {
		t.Fatal("nothing to attach without a binary")
	}
}
