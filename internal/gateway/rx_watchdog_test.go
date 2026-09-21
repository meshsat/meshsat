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
	// Next window with the audio stats still alive: peer silence, no
	// hardware rung. [MESHSAT-857]
	h.advance(4 * time.Minute)
	h.wd.tick(ctx)
	if _, c, _ := h.counts(); c != 0 {
		t.Fatalf("power cycle on peer silence: cycles=%d", c)
	}
	// Direwolf's own stats go stale (hung): step 2, the power cycle.
	h.wd.act.Probe = func() (ReceiveHealth, bool) {
		h.mu.Lock()
		defer h.mu.Unlock()
		hh := h.health
		hh.LevelAt = h.now.Add(-3 * time.Minute)
		return hh, h.ok
	}
	h.advance(6 * time.Minute)
	h.wd.tick(ctx)
	waitFor(t, func() bool { _, c, _ := h.counts(); return c == 1 })
	// Next window, still hung: step 3, the bridge restart.
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
	// Direwolf's stats go stale (hung): step 2, then step 3 is held by the
	// seeded cooldown (20 min < 1 h). [MESHSAT-857: hardware rungs need hung]
	h.wd.act.Probe = func() (ReceiveHealth, bool) {
		h.mu.Lock()
		defer h.mu.Unlock()
		hh := h.health
		hh.LevelAt = h.now.Add(-3 * time.Minute)
		return hh, h.ok
	}
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

// A hardware-TNC kit has no AIOC to power-cycle: step 2 reopens the TNC's
// serial port instead, and the hung-Direwolf branch stays off because no
// audio level ever arrives. [MESHSAT-821]
func TestRxWatchdog_SerialTNCReopenStep(t *testing.T) {
	h := newWDHarness()
	var reopens int
	h.wd.act.Reopen = func(ctx context.Context) error { h.mu.Lock(); reopens++; h.mu.Unlock(); return nil }
	// No audio level for a serial TNC.
	h.mu.Lock()
	h.health.Level = -1
	h.mu.Unlock()
	h.wd.act.Probe = func() (ReceiveHealth, bool) {
		h.mu.Lock()
		defer h.mu.Unlock()
		hh := h.health
		hh.LevelAt = time.Time{}
		return hh, h.ok
	}
	ctx := context.Background()

	h.frame()
	h.wd.tick(ctx)
	if h.wd.State() != ReceiveStateOK {
		t.Fatalf("state %s, want ok", h.wd.State())
	}
	// Silence past the window: step 1 (gateway restart).
	h.advance(6 * time.Minute)
	h.wd.tick(ctx)
	waitFor(t, func() bool { r, _, _ := h.counts(); return r == 1 })
	// Another window: step 2 must be the reopen, never the power cycle.
	h.advance(6 * time.Minute)
	h.wd.tick(ctx)
	waitFor(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return reopens == 1 })
	_, cycles, _ := h.counts()
	if cycles != 0 {
		t.Fatalf("power cycle ran %d times on a serial-TNC kit", cycles)
	}
	if h.wd.State() != ReceiveStateDeaf {
		t.Fatalf("state %s, want deaf", h.wd.State())
	}
	// A frame ends the episode.
	h.frame()
	h.wd.tick(ctx)
	if h.wd.State() != ReceiveStateOK {
		t.Fatalf("state %s after recovery, want ok", h.wd.State())
	}
}

// A TNC that survives the gateway restart and the port reopen is wedged below
// the serial layer: rung 3 cuts its hub port, and the gateway restart follows
// the cut. [MESHSAT-821]
func TestRxWatchdog_SerialTNCPortCutAfterReopen(t *testing.T) {
	h := newWDHarness()
	var reopens int
	h.wd.act.Reopen = func(ctx context.Context) error { h.mu.Lock(); reopens++; h.mu.Unlock(); return nil }
	h.mu.Lock()
	h.health.Level = -1
	h.health.Serial = true
	h.health.BytesIn = 4096 // the TNC talks; only decodes stopped
	h.mu.Unlock()
	h.wd.act.Probe = func() (ReceiveHealth, bool) {
		h.mu.Lock()
		defer h.mu.Unlock()
		hh := h.health
		hh.LevelAt = time.Time{}
		return hh, h.ok
	}
	ctx := context.Background()

	h.frame()
	h.wd.tick(ctx)
	h.advance(6 * time.Minute)
	h.wd.tick(ctx) // step 1: gateway restart
	waitFor(t, func() bool { r, _, _ := h.counts(); return r == 1 })
	h.advance(6 * time.Minute)
	h.wd.tick(ctx) // step 2: reopen
	waitFor(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return reopens == 1 })
	if _, cycles, _ := h.counts(); cycles != 0 {
		t.Fatalf("port cut ran at step 2 (%d times), want the reopen first", cycles)
	}

	h.advance(6 * time.Minute)
	h.wd.tick(ctx) // step 3: the TNC's own hub port
	waitFor(t, func() bool { _, cycles, _ := h.counts(); return cycles == 1 })
	if _, _, bridge := h.counts(); bridge != 0 {
		t.Fatalf("bridge restarted %d times before the port cut", bridge)
	}
	if h.wd.step != 3 {
		t.Fatalf("step %d after the port cut, want 3", h.wd.step)
	}
	// A frame ends the episode and resets the ladder.
	h.frame()
	h.wd.tick(ctx)
	if h.wd.State() != ReceiveStateOK {
		t.Fatalf("state %s after recovery, want ok", h.wd.State())
	}
	if h.wd.step != 0 {
		t.Fatalf("step %d after recovery, want 0", h.wd.step)
	}
}

// A sound-card kit keeps the old ladder: the AIOC cut is rung 2 and the bridge
// restart is the last rung, which must not run twice. [MESHSAT-814]
func TestRxWatchdog_SoundCardLadderUnchanged(t *testing.T) {
	h := newWDHarness()
	ctx := context.Background()
	h.frame()
	h.wd.tick(ctx)
	h.advance(6 * time.Minute)
	h.mu.Lock()
	h.health.LevelAt = h.now.Add(-10 * time.Minute) // hung Direwolf
	h.mu.Unlock()
	h.wd.act.Probe = func() (ReceiveHealth, bool) {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.health, h.ok
	}
	h.wd.tick(ctx) // step 1
	waitFor(t, func() bool { r, _, _ := h.counts(); return r == 1 })
	h.advance(6 * time.Minute)
	h.wd.tick(ctx) // step 2: AIOC cut
	waitFor(t, func() bool { _, c, _ := h.counts(); return c == 1 })
	h.advance(6 * time.Minute)
	h.wd.tick(ctx) // step 3: bridge restart
	waitFor(t, func() bool { _, _, b := h.counts(); return b == 1 })
	// Further windows must not restart the bridge again: the ladder is done.
	for i := 0; i < 3; i++ {
		h.advance(6 * time.Minute)
		h.wd.tick(ctx)
	}
	if _, _, b := h.counts(); b != 1 {
		t.Fatalf("bridge restarted %d times, want 1", b)
	}
}

// Peer silence with Direwolf's own stats alive stops at rung 1; the
// hardware and bridge rungs need a hung Direwolf. [MESHSAT-857]
func TestRxWatchdog_PeerSilenceStopsAtGatewayRestart(t *testing.T) {
	h := newWDHarness()
	ctx := context.Background()
	h.frame()
	h.wd.tick(ctx)
	// 6, 12, 18 and 24 minutes of peer silence while the audio stats keep coming.
	for i := 0; i < 4; i++ {
		h.advance(6 * time.Minute)
		h.wd.tick(ctx)
	}
	waitFor(t, func() bool { r, _, _ := h.counts(); return r == 1 })
	if r, c, b := h.counts(); r != 1 || c != 0 || b != 0 {
		t.Fatalf("peer silence escalated past the gateway restart: restarts=%d cycles=%d bridge=%d", r, c, b)
	}
	if h.wd.State() != ReceiveStateDeaf {
		t.Fatalf("state %s", h.wd.State())
	}
	// A frame recovers.
	h.frame()
	h.wd.tick(ctx)
	if h.wd.State() != ReceiveStateOK {
		t.Fatalf("not recovered: %s", h.wd.State())
	}
}

// A serial TNC that has delivered no byte at all since its link opened is
// reported silent after the cold-start window: one event, no rung (a
// restart or reopen cannot switch a radio on), and the state clears with
// the first frame. [MESHSAT-1028]
func TestRxWatchdog_SerialTNCSilentSinceOpen(t *testing.T) {
	h := newWDHarness()
	ctx := context.Background()
	h.mu.Lock()
	h.health = ReceiveHealth{Running: true, Level: -1, Serial: true, LinkOpenedAt: h.now}
	h.mu.Unlock()

	h.advance(time.Minute)
	h.wd.tick(ctx)
	if h.wd.State() != ReceiveStateQuiet {
		t.Fatalf("inside the cold-start window: state %q, want quiet", h.wd.State())
	}
	for range 10 {
		h.advance(time.Minute)
		h.wd.tick(ctx)
	}
	if h.wd.State() != ReceiveStateSilent {
		t.Fatalf("zero bytes for 11 min: state %q, want silent", h.wd.State())
	}
	r, c, b := h.counts()
	if r+c+b != 0 || h.wd.ReceiveDeaf("aprs_0") {
		t.Fatalf("silent TNC must not run a rung: restarts=%d cycles=%d bridge=%d deaf=%v", r, c, b, h.wd.ReceiveDeaf("aprs_0"))
	}
	h.mu.Lock()
	events := append([]string(nil), h.events...)
	h.mu.Unlock()
	silentEvents := 0
	for _, e := range events {
		if e == "aprs_rx_silent" {
			silentEvents++
		}
	}
	if silentEvents != 1 {
		t.Fatalf("want exactly one aprs_rx_silent event, got %d (%v)", silentEvents, events)
	}

	// The operator presses PTT: bytes and a frame arrive.
	h.mu.Lock()
	h.health.BytesIn = 90
	h.mu.Unlock()
	h.frame()
	h.wd.tick(ctx)
	if h.wd.State() != ReceiveStateOK {
		t.Fatalf("after the first frame: state %q, want ok", h.wd.State())
	}
}

// Bytes without frames (noise, a stray byte) mean the radio is on: not
// silent, the ordinary quiet/deaf logic applies.
func TestRxWatchdog_SerialTNCWithBytesIsNotSilent(t *testing.T) {
	h := newWDHarness()
	ctx := context.Background()
	h.mu.Lock()
	h.health = ReceiveHealth{Running: true, Level: -1, Serial: true, LinkOpenedAt: h.now, BytesIn: 3}
	h.mu.Unlock()
	for range 12 {
		h.advance(time.Minute)
		h.wd.tick(ctx)
	}
	if h.wd.State() != ReceiveStateQuiet {
		t.Fatalf("state %q, want quiet", h.wd.State())
	}
}

// A seeded expectation (the peer was heard before a restart) does not turn a
// TNC with zero bytes into a deaf receiver with a running ladder: silent wins,
// because the rungs cannot help.
func TestRxWatchdog_SilentWinsOverSeededExpectation(t *testing.T) {
	h := newWDHarness()
	h.wd.lastHeardAt = h.now.Add(-2 * time.Minute)
	ctx := context.Background()
	h.mu.Lock()
	h.health = ReceiveHealth{Running: true, Level: -1, Serial: true, LinkOpenedAt: h.now}
	h.mu.Unlock()
	for range 12 {
		h.advance(time.Minute)
		h.wd.tick(ctx)
	}
	// Rungs run in goroutines: give one that fired the time to count, or
	// this test only fails when the scheduler happens to be quick (CI
	// pipeline 55533, 21 Sep 2026).
	time.Sleep(50 * time.Millisecond)
	r, c, b := h.counts()
	if h.wd.State() != ReceiveStateSilent || r+c+b != 0 {
		t.Fatalf("state %q restarts=%d cycles=%d bridge=%d, want silent and no rung", h.wd.State(), r, c, b)
	}
}
