package spectrum

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// blockingScanner holds each Scan until released and counts overlaps.
type blockingScanner struct {
	mu       sync.Mutex
	active   int32
	overlaps atomic.Int32
	release  chan struct{}
	fail     bool
}

func (s *blockingScanner) Available() bool   { return true }
func (s *blockingScanner) Info() ScannerInfo { return ScannerInfo{BinaryPath: "block"} }
func (s *blockingScanner) Scan(ctx context.Context, _, _, _, _ int) ([]float64, error) {
	s.mu.Lock()
	s.active++
	if s.active > 1 {
		s.overlaps.Add(1)
	}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.active--; s.mu.Unlock() }()
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if s.fail {
		return nil, errors.New("boom")
	}
	return []float64{-50, -50, -50}, nil
}

// Two concurrent scanOnce callers never overlap on the dongle, and
// RestartScan cancels the one that is running. [MESHSAT-817]
func TestScanOnceSerialisesAndRestartCancels(t *testing.T) {
	sc := &blockingScanner{release: make(chan struct{})}
	m := NewSpectrumMonitor(sc, DefaultBands[:1])
	band := DefaultBands[0]

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = m.scanOnce(context.Background(), band, 5*time.Second)
		}(i)
	}
	// Wait until one scan is inside the scanner, then cancel it.
	deadline := time.Now().Add(2 * time.Second)
	for {
		sc.mu.Lock()
		n := sc.active
		sc.mu.Unlock()
		if n == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := m.RestartScan(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Release the second scan normally.
	time.Sleep(20 * time.Millisecond)
	close(sc.release)
	wg.Wait()

	if sc.overlaps.Load() != 0 {
		t.Fatalf("%d overlapping scans, want 0", sc.overlaps.Load())
	}
	cancelled := 0
	for _, err := range errs {
		if errors.Is(err, context.Canceled) {
			cancelled++
		}
	}
	if cancelled != 1 {
		t.Fatalf("%d scans cancelled, want exactly 1 (errs %v)", cancelled, errs)
	}
	last, fails := m.LastGoodScan()
	if last.IsZero() || fails != 0 {
		t.Fatalf("last good %v fails %d after a successful scan", last, fails)
	}

	// Failures count consecutively and reset on success.
	sc2 := &blockingScanner{release: make(chan struct{}), fail: true}
	close(sc2.release)
	m2 := NewSpectrumMonitor(sc2, DefaultBands[:1])
	for range 3 {
		_, _ = m2.scanOnce(context.Background(), band, time.Second)
	}
	if _, fails := m2.LastGoodScan(); fails != 3 {
		t.Fatalf("consecutive fails %d, want 3", fails)
	}
	if hs := m2.Hardware(); hs.ConsecutiveErrors != 3 {
		t.Fatalf("Hardware().ConsecutiveErrors %d", hs.ConsecutiveErrors)
	}
	sc2.fail = false
	_, _ = m2.scanOnce(context.Background(), band, time.Second)
	if _, fails := m2.LastGoodScan(); fails != 0 {
		t.Fatalf("fails not reset on success: %d", fails)
	}
}

// closingScanner is a blockingScanner that records Close, like the rtl_tcp
// reader releasing the dongle.
type closingScanner struct {
	blockingScanner
	closes atomic.Int32
}

func (s *closingScanner) Close() error { s.closes.Add(1); return nil }

// Suspend cancels the running scan, releases the dongle and parks every
// scan until Resume, so the RTL-SDR's port power cycle never runs under a
// live reader and nothing re-opens the dongle while it re-enumerates.
// [MESHSAT-1222]
func TestSuspendParksScansUntilResume(t *testing.T) {
	sc := &closingScanner{blockingScanner: blockingScanner{release: make(chan struct{})}}
	m := NewSpectrumMonitor(sc, DefaultBands[:1])
	band := DefaultBands[0]

	running := make(chan error, 1)
	go func() {
		_, err := m.scanOnce(context.Background(), band, 5*time.Second)
		running <- err
	}()
	for deadline := time.Now().Add(2 * time.Second); ; {
		sc.mu.Lock()
		a := sc.active
		sc.mu.Unlock()
		if a == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scan never started")
		}
		time.Sleep(5 * time.Millisecond)
	}

	m.Suspend(context.Background())
	if err := <-running; err == nil {
		t.Fatal("the running scan was not cancelled by Suspend")
	}
	if n := sc.closes.Load(); n != 1 {
		t.Fatalf("reader closed %d times by Suspend, want 1", n)
	}
	m.Suspend(context.Background()) // second Suspend is a no-op, not a deadlock

	close(sc.release)
	parked := make(chan error, 1)
	go func() {
		_, err := m.scanOnce(context.Background(), band, 5*time.Second)
		parked <- err
	}()
	select {
	case <-parked:
		t.Fatal("a scan ran while suspended")
	case <-time.After(200 * time.Millisecond):
	}
	m.Resume()
	select {
	case err := <-parked:
		if err != nil {
			t.Fatalf("scan after Resume: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scan still parked after Resume")
	}
	m.Resume() // extra Resume is a no-op, not an unlock of an unlocked mutex
}

// RestartScan restarts the reader, not only the running scan.
func TestRestartScanClosesTheReader(t *testing.T) {
	sc := &closingScanner{blockingScanner: blockingScanner{release: make(chan struct{})}}
	m := NewSpectrumMonitor(sc, DefaultBands[:1])
	if err := m.RestartScan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := sc.closes.Load(); n != 1 {
		t.Fatalf("reader closed %d times, want 1", n)
	}
}
