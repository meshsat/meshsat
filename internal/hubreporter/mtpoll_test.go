package hubreporter

import (
	"errors"
	"testing"
	"time"
)

func TestMTPoller(t *testing.T) {
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	var sent [][]byte
	connected, pending := true, 0
	last := now.Add(-time.Hour)
	p := NewMTPoller(MTPollConfig{
		Interval:     10 * time.Minute,
		Connected:    func() bool { return connected },
		LastActivity: func() time.Time { return last },
		Pending:      func() int { return pending },
		Frame:        func() []byte { return HealthFrame("nllei01tesseract01", BridgeHealth{}, now) },
		Send:         func(f []byte) error { sent = append(sent, f); return nil },
		Now:          func() time.Time { return now },
	})

	// Quiet for an hour: one poll, a frame the Hub takes off before its relay.
	if ok, why := p.Tick(); !ok || len(sent) != 1 {
		t.Fatalf("no poll after an hour of silence: %s", why)
	}
	if !IsSatUplink(sent[0]) {
		t.Fatal("the poll is not a satellite uplink frame")
	}
	if hdr, _, err := DecodeSatUplink(sent[0]); err != nil || hdr.MsgType != SatMsgHealthSummary {
		t.Fatalf("the poll is not a health frame: %v %v", hdr, err)
	}
	// Not again inside the interval.
	now = now.Add(5 * time.Minute)
	if ok, _ := p.Tick(); ok {
		t.Fatal("polled twice inside one interval")
	}
	// A pending send opens the session itself.
	now = now.Add(time.Hour)
	pending = 1
	if ok, why := p.Tick(); ok || why == "" {
		t.Fatal("polled while a send was already waiting")
	}
	pending = 0
	// A recent session (an MO or an MT) makes a poll pointless.
	last = now.Add(-2 * time.Minute)
	if ok, _ := p.Tick(); ok {
		t.Fatal("polled right after a session")
	}
	// No modem, no poll.
	last = now.Add(-time.Hour)
	connected = false
	if ok, _ := p.Tick(); ok {
		t.Fatal("polled a modem that is not connected")
	}
	connected = true
	if ok, _ := p.Tick(); !ok || len(sent) != 2 {
		t.Fatal("no poll once every condition holds again")
	}
}

func TestMTPoller_DisabledAndSendFailure(t *testing.T) {
	p := NewMTPoller(MTPollConfig{Interval: 0, Frame: func() []byte { return nil }, Send: func([]byte) error { return nil }})
	if ok, why := p.Tick(); ok || why != "disabled" {
		t.Fatalf("interval 0 polled: %s", why)
	}
	now := time.Now()
	calls := 0
	p = NewMTPoller(MTPollConfig{Interval: time.Minute, Frame: func() []byte { return []byte{1} },
		Send: func([]byte) error { calls++; return errors.New("ledger busy") }, Now: func() time.Time { return now }})
	p.Tick()
	p.Tick()
	if calls != 2 {
		t.Fatalf("a failed queue was not retried at the next tick: %d calls", calls)
	}
}
