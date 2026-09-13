package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"meshsat/internal/transport"
)

func batteryJSON(t *testing.T, ac bool, soc float64, draining bool, updated time.Time) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]interface{}{
		"voltage": 3.9, "soc_percent": soc, "ac_present": ac,
		"input_insufficient": draining, "last_update": float64(updated.Unix()),
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBatteryState(t *testing.T) {
	now := time.Now()
	parse := func(b []byte) *batteryStatus {
		bs, err := parseBatteryStatus(b, now)
		if err != nil {
			t.Fatal(err)
		}
		return bs
	}
	cases := []struct {
		name string
		bs   *batteryStatus
		want string
	}{
		{"no reading", nil, "missing"},
		{"mains", parse(batteryJSON(t, true, 98, false, now)), "mains"},
		{"draining on mains", parse(batteryJSON(t, true, 60, true, now)), "draining"},
		{"on battery", parse(batteryJSON(t, false, 70, false, now)), "battery"},
		{"low on battery", parse(batteryJSON(t, false, 12, false, now)), "low"},
		{"stale beats everything", parse(batteryJSON(t, false, 12, true, now.Add(-2*time.Minute))), "stale"},
	}
	for _, c := range cases {
		if got, _ := batteryState(c.bs); got != c.want {
			t.Errorf("%s: state %q, want %q", c.name, got, c.want)
		}
	}
}

// The monitor rewrites /run/x1202.json in place, so one poll can catch it
// half written; that must not read as the UPS going away. [MESHSAT-794]
func TestBatteryWatch_ObserveChangesAndMisses(t *testing.T) {
	now := time.Now()
	reading := func(ac bool, soc float64) *batteryStatus {
		bs, err := parseBatteryStatus(batteryJSON(t, ac, soc, false, now), now)
		if err != nil {
			t.Fatal(err)
		}
		return bs
	}
	var w batteryWatch
	steps := []struct {
		name    string
		bs      *batteryStatus
		changed bool
		key     string
	}{
		{"baseline", reading(true, 97), false, "mains"},
		{"level moves, state does not", reading(true, 95), false, "mains"},
		{"mains lost", reading(false, 95), true, "battery"},
		{"one bad read", nil, false, "battery"},
		{"second bad read", nil, false, "battery"},
		{"good read again", reading(false, 94), false, "battery"},
		{"bad read 1", nil, false, "battery"},
		{"bad read 2", nil, false, "battery"},
		{"bad read 3: gone", nil, true, "missing"},
		{"still gone", nil, false, "missing"},
		{"back on mains", reading(true, 94), true, "mains"},
	}
	for _, s := range steps {
		changed, _, key, _ := w.observe(s.bs)
		if changed != s.changed || key != s.key {
			t.Fatalf("%s: changed %v key %q, want %v %q", s.name, changed, key, s.changed, s.key)
		}
	}
}

func TestWatchBatteryEvents_EmitsOnStateChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x1202.json")
	write := func(b []byte) {
		t.Helper()
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(batteryJSON(t, true, 97, false, time.Now()))

	events := make(chan transport.MeshEvent, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go WatchBatteryEvents(ctx, path, 10*time.Millisecond, func(ev transport.MeshEvent) { events <- ev })

	select {
	case ev := <-events:
		t.Fatalf("event for the baseline reading: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
	write(batteryJSON(t, false, 96, false, time.Now()))
	select {
	case ev := <-events:
		var p struct {
			State    string `json:"state"`
			Previous string `json:"previous"`
		}
		if err := json.Unmarshal(ev.Data, &p); err != nil {
			t.Fatal(err)
		}
		if ev.Type != "battery" || p.State != "battery" || p.Previous != "mains" {
			t.Fatalf("event %q %s -> %s, want battery mains -> battery", ev.Type, p.Previous, p.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event after mains was lost")
	}
}

func TestWatchBatteryEvents_ReturnsWithoutFile(t *testing.T) {
	done := make(chan struct{})
	go func() {
		WatchBatteryEvents(context.Background(), filepath.Join(t.TempDir(), "absent.json"), time.Millisecond, func(transport.MeshEvent) {})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watcher kept running on a host without the status file")
	}
}
