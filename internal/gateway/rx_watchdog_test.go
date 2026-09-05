package gateway

import (
	"context"
	"sync"
	"testing"
	"time"
)

type wdHarness struct {
	mu       sync.Mutex
	health   ReceiveHealth
	ok       bool
	restarts int
	cycles   int
	bridge   int
	states   []string
	events   []string
	now      time.Time
	wd       *RxWatchdog
}

func newWDHarness() *wdHarness {
	h := &wdHarness{ok: true, now: time.Date(2026, 9, 5, 19, 0, 0, 0, time.UTC)}
	h.health = ReceiveHealth{Running: true, Level: 40, LevelAt: h.now}
	h.wd = NewRxWatchdog(RxWatchdogConfig{Silence: 5 * time.Minute, HeardWithin: 2 * time.Hour, StatsStale: 90 * time.Second, BridgeCooldown: time.Hour}, RxWatchdogActions{
		Probe: func() (ReceiveHealth, bool) {
			h.mu.Lock()
			defer h.mu.Unlock()
			hh := h.health
			hh.LevelAt = h.now
			return hh, h.ok
		},
		RestartGateway: func(ctx context.Context) error { h.mu.Lock(); h.restarts++; h.mu.Unlock(); return nil },
		PowerCycle:     func(ctx context.Context) error { h.mu.Lock(); h.cycles++; h.mu.Unlock(); return nil },
		RestartBridge:  func() { h.mu.Lock(); h.bridge++; h.mu.Unlock() },
		SetState:       func(s string) { h.mu.Lock(); h.states = append(h.states, s); h.mu.Unlock() },
		Emit:           func(ev, msg string) { h.mu.Lock(); h.events = append(h.events, ev); h.mu.Unlock() },
	})
	h.wd.now = func() time.Time { return h.now }
	return h
}

func (h *wdHarness) frame() {
	h.mu.Lock()
	h.health.RxFrames++
	h.health.LastDecodeAt = h.now
	h.mu.Unlock()
}

func (h *wdHarness) advance(d time.Duration) { h.now = h.now.Add(d) }

func (h *wdHarness) counts() (int, int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.restarts, h.cycles, h.bridge
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A kit that never decodes anything is quiet, not deaf: no recovery runs.
func TestRxWatchdog_NeverHeardStaysQuiet(t *testing.T) {
	h := newWDHarness()
	ctx := context.Background()
	for range 20 {
		h.wd.tick(ctx)
		h.advance(time.Minute)
	}
	r, c, b := h.counts()
	if r+c+b != 0 || h.wd.State() != ReceiveStateQuiet || h.wd.ReceiveDeaf("aprs_0") {
		t.Fatalf("never-heard kit escalated: restarts=%d cycles=%d bridge=%d state=%s", r, c, b, h.wd.State())
	}
}

// The ladder: silence after activity restarts the gateway, then cycles the
// AIOC, then restarts the bridge, one step per silence window; a decoded
// frame at any point clears the deaf flag and resets the ladder.
func TestRxWatchdog_EscalatesAndRecovers(t *testing.T) {
	h := newWDHarness()
	ctx := context.Background()
	h.frame()
	h.wd.tick(ctx)
	if h.wd.State() != ReceiveStateOK {
		t.Fatalf("state after a frame: %s", h.wd.State())
	}
	// 6 minutes of silence: deaf, step 1.
	h.advance(6 * time.Minute)
	h.wd.tick(ctx)
	waitFor(t, func() bool { r, _, _ := h.counts(); return r == 1 })
	if !h.wd.ReceiveDeaf("aprs_0") || !h.wd.ReceiveDeaf("ax25_0") || h.wd.ReceiveDeaf("cellular_0") {
		t.Fatal("deaf flag wrong")
	}
	// Ticks inside the window must not add steps.
	h.advance(2 * time.Minute)
	h.wd.tick(ctx)
	if r, c, _ := h.counts(); r != 1 || c != 0 {
		t.Fatalf("stepped early: restarts=%d cycles=%d", r, c)
	}
	// Next window: step 2, the power cycle.
	h.advance(4 * time.Minute)
	h.wd.tick(ctx)
	waitFor(t, func() bool { _, c, _ := h.counts(); return c == 1 })
	// Next window: step 3, the bridge restart.
	h.advance(6 * time.Minute)
	h.wd.tick(ctx)
	if _, _, b := h.counts(); b != 1 {
		t.Fatalf("bridge restarts: %d", b)
	}
	// Further silence within the cooldown must not restart the bridge again.
	h.advance(6 * time.Minute)
	h.wd.tick(ctx)
	if _, _, b := h.counts(); b != 1 {
		t.Fatalf("bridge restarted inside the cooldown: %d", b)
	}
	// A frame recovers everything.
	h.frame()
	h.wd.tick(ctx)
	if h.wd.ReceiveDeaf("aprs_0") || h.wd.State() != ReceiveStateOK {
		t.Fatalf("not recovered: deaf=%v state=%s", h.wd.ReceiveDeaf("aprs_0"), h.wd.State())
	}
	found := false
	h.mu.Lock()
	for _, e := range h.events {
		if e == "aprs_rx_recovered" {
			found = true
		}
	}
	h.mu.Unlock()
	if !found {
		t.Fatal("no recovery event")
	}
}

// Silence older than HeardWithin is a quiet channel again, not a deaf one.
func TestRxWatchdog_ExpectationExpires(t *testing.T) {
	h := newWDHarness()
	ctx := context.Background()
	h.frame()
	h.wd.tick(ctx)
	h.advance(3 * time.Hour)
	h.wd.tick(ctx)
	r, c, b := h.counts()
	if r+c+b != 0 || h.wd.State() != ReceiveStateQuiet {
		t.Fatalf("expired expectation escalated: %d %d %d state=%s", r, c, b, h.wd.State())
	}
}

// A running Direwolf that stops reporting audio statistics is hung and is
// restarted without waiting for the silence window.
func TestRxWatchdog_HungDirewolf(t *testing.T) {
	h := newWDHarness()
	ctx := context.Background()
	h.wd.act.Probe = func() (ReceiveHealth, bool) {
		return ReceiveHealth{Running: true, Level: 30, LevelAt: h.now.Add(-3 * time.Minute)}, true
	}
	h.wd.tick(ctx)
	waitFor(t, func() bool { r, _, _ := h.counts(); return r == 1 })
	if h.wd.State() != ReceiveStateDeaf {
		t.Fatalf("state: %s", h.wd.State())
	}
}

// No supervisor (external Direwolf) means unknown and nothing else.
func TestRxWatchdog_NoProbe(t *testing.T) {
	h := newWDHarness()
	h.ok = false
	h.wd.tick(context.Background())
	if h.wd.State() != ReceiveStateUnknown {
		t.Fatalf("state: %s", h.wd.State())
	}
}

// A watchdog seeded with the previous process's last-heard time treats
// silence after a restart as deaf from the first tick: the failover and
// the ladder do not wait for a frame a deaf receiver never delivers. The
// seeded bridge-restart time keeps the cooldown across restarts.
func TestRxWatchdog_SeededExpectationSurvivesRestart(t *testing.T) {
	h := newWDHarness()
	ctx := context.Background()
	var persisted []time.Time
	var pmu sync.Mutex
	h.wd = NewRxWatchdog(RxWatchdogConfig{
		Silence: 5 * time.Minute, HeardWithin: 2 * time.Hour, StatsStale: 90 * time.Second, BridgeCooldown: time.Hour,
		LastHeard: h.now.Add(-6 * time.Minute), LastBridgeRestart: h.now.Add(-20 * time.Minute),
	}, RxWatchdogActions{
		Probe:          h.wd.act.Probe,
		RestartGateway: h.wd.act.RestartGateway,
		PowerCycle:     h.wd.act.PowerCycle,
		RestartBridge:  h.wd.act.RestartBridge,
		Persist: func(lastHeard, bridgeRestartAt time.Time) {
			pmu.Lock()
			persisted = append(persisted, lastHeard, bridgeRestartAt)
			pmu.Unlock()
		},
	})
	h.wd.now = func() time.Time { return h.now }
	// First tick after the restart: already deaf, step 1 runs.
	h.wd.tick(ctx)
	waitFor(t, func() bool { r, _, _ := h.counts(); return r == 1 })
	if !h.wd.ReceiveDeaf("aprs_0") || h.wd.State() != ReceiveStateDeaf {
		t.Fatalf("seeded silence not deaf: deaf=%v state=%s", h.wd.ReceiveDeaf("aprs_0"), h.wd.State())
	}
	// Step 2, then step 3 is held by the seeded cooldown (20 min < 1 h).
	h.advance(6 * time.Minute)
	h.wd.tick(ctx)
	waitFor(t, func() bool { _, c, _ := h.counts(); return c == 1 })
	h.advance(6 * time.Minute)
	h.wd.tick(ctx)
	if _, _, b := h.counts(); b != 0 {
		t.Fatalf("bridge restarted inside the seeded cooldown: %d", b)
	}
	// Once the cooldown has passed the bridge restart runs and persists first.
	h.advance(50 * time.Minute)
	h.wd.tick(ctx)
	if _, _, b := h.counts(); b != 1 {
		t.Fatalf("bridge restarts after the cooldown: %d", b)
	}
	pmu.Lock()
	n := len(persisted)
	pmu.Unlock()
	if n != 2 || !persisted[1].Equal(h.now) {
		t.Fatalf("persist before the bridge restart: %v", persisted)
	}
	// A frame after recovery persists the new last-heard time (rate limited).
	h.frame()
	h.wd.tick(ctx)
	waitFor(t, func() bool { pmu.Lock(); defer pmu.Unlock(); return len(persisted) == 4 })
	h.frame()
	h.wd.tick(ctx)
	pmu.Lock()
	n = len(persisted)
	pmu.Unlock()
	if n != 4 {
		t.Fatalf("persist not rate limited: %d entries", n)
	}
}

// An old seed (older than HeardWithin) is a quiet channel, not a deaf one:
// a kit switched on alone after a night off does not walk the ladder.
func TestRxWatchdog_StaleSeedIsQuiet(t *testing.T) {
	h := newWDHarness()
	h.wd = NewRxWatchdog(RxWatchdogConfig{Silence: 5 * time.Minute, HeardWithin: 2 * time.Hour, LastHeard: h.now.Add(-3 * time.Hour)}, h.wd.act)
	h.wd.now = func() time.Time { return h.now }
	h.wd.tick(context.Background())
	r, c, b := h.counts()
	if r+c+b != 0 || h.wd.State() != ReceiveStateQuiet {
		t.Fatalf("stale seed escalated: %d %d %d state=%s", r, c, b, h.wd.State())
	}
}
