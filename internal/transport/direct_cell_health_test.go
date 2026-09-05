package transport

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A held transport refuses to open its port and reports the hold from
// the probe; the hold expires by time. [MESHSAT-812, MESHSAT-817]
func TestCellularHoldRefusesConnect(t *testing.T) {
	tr := NewDirectCellTransport("/dev/null")
	tr.Hold(200 * time.Millisecond)
	if rem := tr.HeldFor(); rem <= 0 || rem > 200*time.Millisecond {
		t.Fatalf("HeldFor %v", rem)
	}
	if err := tr.connectLocked(context.Background()); !errors.Is(err, ErrCellHeld) {
		t.Fatalf("connect during hold: %v", err)
	}
	if err := tr.Probe(context.Background()); !errors.Is(err, ErrCellHeld) {
		t.Fatalf("probe during hold: %v", err)
	}
	time.Sleep(250 * time.Millisecond)
	if rem := tr.HeldFor(); rem != 0 {
		t.Fatalf("hold did not expire: %v", rem)
	}
	// After the hold the probe reports the real state (not connected).
	if err := tr.Probe(context.Background()); err == nil || errors.Is(err, ErrCellHeld) {
		t.Fatalf("probe after hold: %v", err)
	}
}

// Recent bytes from the modem satisfy the probe without an AT round trip.
func TestCellularProbeRecentRx(t *testing.T) {
	tr := NewDirectCellTransport("/dev/null")
	tr.stateMu.Lock()
	tr.connected = true
	tr.stateMu.Unlock()
	tr.lastRx.Store(time.Now().UnixNano())
	if err := tr.Probe(context.Background()); err != nil {
		t.Fatalf("probe with recent rx: %v", err)
	}
	if tr.LastRxAt().IsZero() {
		t.Fatal("LastRxAt zero")
	}
}
