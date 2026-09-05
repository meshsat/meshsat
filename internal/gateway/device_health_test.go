package gateway

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// dhHarness drives a DeviceHealth with a fake clock and counting rungs.
type dhHarness struct {
	mu      sync.Mutex
	now     time.Time
	results map[string]ProbeResult
	runs    map[string]int // "target/level/name" -> count
	events  []string
	persist map[string]PersistedTarget
	dh      *DeviceHealth
}

func newDHHarness(cfg DeviceHealthConfig) *dhHarness {
	h := &dhHarness{
		now:     time.Date(2026, 9, 6, 1, 0, 0, 0, time.UTC),
		results: map[string]ProbeResult{},
		runs:    map[string]int{},
		persist: map[string]PersistedTarget{},
	}
	cfg.Now = func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now }
	h.dh = NewDeviceHealth(cfg, DeviceHealthActions{
		Emit: func(ev, msg string, data map[string]any) {
			h.mu.Lock()
			h.events = append(h.events, ev)
			h.mu.Unlock()
		},
		Persist: func(name string, p PersistedTarget) {
			h.mu.Lock()
			h.persist[name] = p
			h.mu.Unlock()
		},
	})
	return h
}

func (h *dhHarness) set(target string, r ProbeResult) {
	h.mu.Lock()
	h.results[target] = r
	h.mu.Unlock()
}

func (h *dhHarness) miss(target string) { h.set(target, ProbeResult{Detail: "silent"}) }
func (h *dhHarness) ok(target string)   { h.set(target, ProbeResult{OK: true}) }

func (h *dhHarness) advance(d time.Duration) {
	h.mu.Lock()
	h.now = h.now.Add(d)
	h.mu.Unlock()
}

func (h *dhHarness) step(target string, level byte, name string, grace time.Duration, skip func() bool) HealStep {
	key := target + "/" + string(rune('0'+level)) + "/" + name
	return HealStep{Level: level, Name: name, Grace: grace, Skip: skip, Run: func(ctx context.Context) error {
		h.mu.Lock()
		h.runs[key]++
		h.mu.Unlock()
		return nil
	}}
}

func (h *dhHarness) target(name string, ifaces []string, steps ...HealStep) HealthTarget {
	return HealthTarget{
		Name: name, IfaceIDs: ifaces,
		Probe: func(ctx context.Context) ProbeResult {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.results[name]
		},
		Steps: steps,
	}
}

// tick runs one evaluation and waits for the probes and rungs to settle.
func (h *dhHarness) tick(t *testing.T) {
	t.Helper()
	h.dh.tick(context.Background())
	waitFor(t, func() bool {
		h.dh.mu.Lock()
		defer h.dh.mu.Unlock()
		for _, ts := range h.dh.targets {
			if ts.probing || ts.inflight {
				return false
			}
		}
		return true
	})
}

func (h *dhHarness) count(target string, level byte, name string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.runs[target+"/"+string(rune('0'+level))+"/"+name]
}

func (h *dhHarness) state(name string) string { return h.dh.TargetState(name) }

func (h *dhHarness) eventCount(ev string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, e := range h.events {
		if e == ev {
			n++
		}
	}
	return n
}

func meshLadder(h *dhHarness) []HealStep {
	return []HealStep{
		h.step("mesh", HealLevelSoft, "reconnect", 60*time.Second, nil),
		h.step("mesh", HealLevelDevice, "admin reboot", 60*time.Second, nil),
		h.step("mesh", HealLevelHard, "power cycle", 120*time.Second, nil),
	}
}

func TestDeviceHealth_OKAndUnknownNeverEscalate(t *testing.T) {
	h := newDHHarness(DeviceHealthConfig{})
	h.dh.Register(h.target("mesh", []string{"mesh_0"}, meshLadder(h)...))
	h.ok("mesh")
	for range 5 {
		h.tick(t)
		h.advance(30 * time.Second)
	}
	if got := h.state("mesh"); got != HealthStateOK {
		t.Fatalf("state %q, want ok", got)
	}
	h.set("mesh", ProbeResult{Unknown: true, Detail: "no port"})
	for range 5 {
		h.tick(t)
		h.advance(30 * time.Second)
	}
	if got := h.state("mesh"); got != HealthStateUnknown {
		t.Fatalf("state %q, want unknown", got)
	}
	if n := h.count("mesh", HealLevelSoft, "reconnect"); n != 0 {
		t.Fatalf("reconnect ran %d times, want 0", n)
	}
	if h.dh.ReceiveDeaf("mesh_0") {
		t.Fatal("unknown target must not read as deaf")
	}
}

func TestDeviceHealth_EscalatesThroughLadderAndRecovers(t *testing.T) {
	h := newDHHarness(DeviceHealthConfig{Misses: 3})
	h.dh.Register(h.target("mesh", []string{"mesh_0"}, meshLadder(h)...))
	h.miss("mesh")

	// Two misses: still ok, nothing runs.
	h.tick(t)
	h.advance(30 * time.Second)
	h.tick(t)
	if n := h.count("mesh", HealLevelSoft, "reconnect"); n != 0 {
		t.Fatalf("reconnect ran after 2 misses")
	}
	// Third miss: degraded, first rung runs on the same tick.
	h.advance(30 * time.Second)
	h.tick(t)
	if n := h.count("mesh", HealLevelSoft, "reconnect"); n != 1 {
		t.Fatalf("reconnect ran %d times, want 1", n)
	}
	if got := h.state("mesh"); got != HealthStateHealing {
		t.Fatalf("state %q, want healing", got)
	}
	if !h.dh.ReceiveDeaf("mesh_0") {
		t.Fatal("healing target must read as deaf for failover")
	}
	waitFor(t, func() bool { return h.eventCount("device_unhealthy") == 1 && h.eventCount("device_heal_step") == 1 })

	// Misses inside the 60 s grace do not advance the ladder.
	h.advance(30 * time.Second)
	h.tick(t)
	if n := h.count("mesh", HealLevelDevice, "admin reboot"); n != 0 {
		t.Fatal("second rung ran inside the grace window")
	}
	// After the grace the next miss runs rung 2, then rung 3 (hard).
	h.advance(31 * time.Second)
	h.tick(t)
	if n := h.count("mesh", HealLevelDevice, "admin reboot"); n != 1 {
		t.Fatalf("admin reboot ran %d times, want 1", n)
	}
	h.advance(61 * time.Second)
	h.tick(t)
	if n := h.count("mesh", HealLevelHard, "power cycle"); n != 1 {
		t.Fatalf("power cycle ran %d times, want 1", n)
	}
	st := h.dh.Status()
	if len(st) != 1 || st[0].HardResets != 1 || st[0].StepLevel != HealLevelHard {
		t.Fatalf("status %+v", st)
	}
	waitFor(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.persist["mesh"].HardResets) == 1 })

	// Recovery inside the hard grace resets everything.
	h.advance(20 * time.Second)
	h.ok("mesh")
	h.tick(t)
	if got := h.state("mesh"); got != HealthStateOK {
		t.Fatalf("state %q, want ok", got)
	}
	waitFor(t, func() bool { return h.eventCount("device_recovered") == 1 })
	if h.dh.ReceiveDeaf("mesh_0") {
		t.Fatal("recovered target still deaf")
	}
	if s := h.dh.Status()[0]; s.Step != 0 || s.Misses != 0 {
		t.Fatalf("ladder not reset: %+v", s)
	}
}

func TestDeviceHealth_LadderExhaustedThenRearms(t *testing.T) {
	h := newDHHarness(DeviceHealthConfig{Misses: 1, HardCooldown: 10 * time.Minute})
	h.dh.Register(h.target("zigbee", []string{"zigbee_0"},
		h.step("zigbee", HealLevelSoft, "reinit", 10*time.Second, nil)))
	h.miss("zigbee")
	h.tick(t) // degraded + rung 1
	h.advance(11 * time.Second)
	h.tick(t) // ladder exhausted
	if got := h.state("zigbee"); got != HealthStateFailed {
		t.Fatalf("state %q, want failed", got)
	}
	waitFor(t, func() bool { return h.eventCount("device_heal_failed") == 1 })
	// Inside the cooldown nothing runs.
	h.advance(5 * time.Minute)
	h.tick(t)
	if n := h.count("zigbee", HealLevelSoft, "reinit"); n != 1 {
		t.Fatalf("reinit ran %d times during cooldown, want 1", n)
	}
	// After the cooldown the ladder re-arms from the first rung.
	h.advance(6 * time.Minute)
	h.tick(t)
	if n := h.count("zigbee", HealLevelSoft, "reinit"); n != 2 {
		t.Fatalf("reinit ran %d times after cooldown, want 2", n)
	}
}

func TestDeviceHealth_HardBudgetAndGap(t *testing.T) {
	h := newDHHarness(DeviceHealthConfig{Misses: 1, HardBudget: 1, HardGap: 45 * time.Second})
	h.dh.Register(h.target("mesh", []string{"mesh_0"}, h.step("mesh", HealLevelHard, "cut", 10*time.Second, nil)))
	h.dh.Register(h.target("zigbee", []string{"zigbee_0"}, h.step("zigbee", HealLevelHard, "cut", 10*time.Second, nil)))
	h.miss("mesh")
	h.miss("zigbee")
	h.tick(t)
	// Only one of the two hard resets may run in the same tick.
	if n := h.count("mesh", HealLevelHard, "cut") + h.count("zigbee", HealLevelHard, "cut"); n != 1 {
		t.Fatalf("%d hard resets in one tick, want 1", n)
	}
	h.advance(30 * time.Second)
	h.tick(t)
	if n := h.count("mesh", HealLevelHard, "cut") + h.count("zigbee", HealLevelHard, "cut"); n != 1 {
		t.Fatalf("second hard reset ran inside the gap")
	}
	h.advance(20 * time.Second)
	h.tick(t)
	if n := h.count("mesh", HealLevelHard, "cut") + h.count("zigbee", HealLevelHard, "cut"); n != 2 {
		t.Fatalf("second hard reset did not run after the gap (%d)", n)
	}
	// Budget of 1 per hour: the next miss after the grace fails the target.
	h.advance(15 * time.Second)
	h.tick(t)
	h.advance(60 * time.Second)
	h.tick(t)
	if got := h.state("mesh"); got != HealthStateFailed {
		t.Fatalf("mesh state %q, want failed", got)
	}
	if n := h.count("mesh", HealLevelHard, "cut"); n != 1 {
		t.Fatalf("mesh cut ran %d times, want 1 (budget)", n)
	}
}

func TestDeviceHealth_SkipAndMaxLevel(t *testing.T) {
	h := newDHHarness(DeviceHealthConfig{Misses: 1})
	skipped := true
	tgt := h.target("cellular", []string{"cellular_0", "sms_0"},
		h.step("cellular", HealLevelSoft, "reconnect", 10*time.Second, func() bool { return skipped }),
		h.step("cellular", HealLevelDevice, "cfun", 10*time.Second, nil),
		h.step("cellular", HealLevelHard, "cut", 10*time.Second, nil))
	tgt.MaxLevel = HealLevelDevice
	h.dh.Register(tgt)
	h.miss("cellular")
	h.tick(t)
	if h.count("cellular", HealLevelSoft, "reconnect") != 0 || h.count("cellular", HealLevelDevice, "cfun") != 1 {
		t.Fatal("skip did not jump to the device rung")
	}
	if !h.dh.ReceiveDeaf("sms_0") || !h.dh.ReceiveDeaf("cellular_0") {
		t.Fatal("both cellular interfaces must read as deaf")
	}
	h.advance(11 * time.Second)
	h.tick(t)
	if got := h.state("cellular"); got != HealthStateFailed {
		t.Fatalf("state %q, want failed at max level", got)
	}
	if h.count("cellular", HealLevelHard, "cut") != 0 {
		t.Fatal("hard rung ran above MaxLevel")
	}
	// Manual heal above MaxLevel needs confirm.
	if err := h.dh.Heal(context.Background(), "cellular", HealLevelHard, false); !errors.Is(err, ErrHealthConfirm) {
		t.Fatalf("Heal without confirm: %v", err)
	}
	if err := h.dh.Heal(context.Background(), "cellular", HealLevelHard, true); err != nil {
		t.Fatalf("Heal with confirm: %v", err)
	}
	waitFor(t, func() bool { return h.count("cellular", HealLevelHard, "cut") == 1 })
	if err := h.dh.Heal(context.Background(), "nope", HealLevelSoft, false); !errors.Is(err, ErrHealthTargetUnknown) {
		t.Fatalf("unknown target: %v", err)
	}
}

func TestDeviceHealth_PauseAndExternalReset(t *testing.T) {
	h := newDHHarness(DeviceHealthConfig{Misses: 1, HardBudget: 2})
	h.dh.Register(h.target("mesh", []string{"mesh_0"}, meshLadder(h)...))
	if err := h.dh.Pause("mesh"); err != nil {
		t.Fatal(err)
	}
	h.miss("mesh")
	h.tick(t)
	if h.count("mesh", HealLevelSoft, "reconnect") != 0 {
		t.Fatal("rung ran while paused")
	}
	if got := h.state("mesh"); got != HealthStatePaused {
		t.Fatalf("state %q, want paused", got)
	}
	waitFor(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.persist["mesh"].Paused })
	if err := h.dh.Resume("mesh"); err != nil {
		t.Fatal(err)
	}

	// An operator RESET mesh L3 through OOB: grace starts, budget counts.
	h.dh.NoteExternalReset("mesh", HealLevelHard)
	h.tick(t)
	if h.count("mesh", HealLevelSoft, "reconnect") != 0 {
		t.Fatal("rung ran inside the external reset grace")
	}
	if s := h.dh.Status()[0]; s.HardResets != 1 {
		t.Fatalf("external hard reset not counted: %+v", s)
	}
	// After the grace (120 s hard grace) the ladder takes over.
	h.advance(121 * time.Second)
	h.tick(t)
	if h.count("mesh", HealLevelSoft, "reconnect") != 1 {
		t.Fatal("ladder did not resume after the external grace")
	}
}

func TestDeviceHealth_SeedAndExternalTarget(t *testing.T) {
	past := time.Date(2026, 9, 6, 0, 30, 0, 0, time.UTC)
	h := newDHHarness(DeviceHealthConfig{Misses: 1, HardBudget: 1, Seed: map[string]PersistedTarget{
		"mesh": {HardResets: []time.Time{past}},
	}})
	h.dh.Register(h.target("mesh", []string{"mesh_0"}, h.step("mesh", HealLevelHard, "cut", 10*time.Second, nil)))
	state := HealthStateOK
	h.dh.RegisterExternal("aprs", []string{"aprs_0", "ax25_0"}, func() (string, string) { return state, "rx watchdog" })
	h.miss("mesh")
	h.tick(t)
	// The seeded reset from 30 min ago spends the budget of 1: no cut, failed.
	if h.count("mesh", HealLevelHard, "cut") != 0 {
		t.Fatal("seeded budget ignored")
	}
	if got := h.state("mesh"); got != HealthStateFailed {
		t.Fatalf("state %q, want failed", got)
	}
	st := h.dh.Status()
	if len(st) != 2 || st[0].Name != "aprs" || !st[0].External || st[0].State != HealthStateOK {
		t.Fatalf("status %+v", st)
	}
	if err := h.dh.Heal(context.Background(), "aprs", HealLevelSoft, false); !errors.Is(err, ErrHealthExternal) {
		t.Fatalf("external heal: %v", err)
	}
	if h.dh.ReceiveDeaf("aprs_0") {
		t.Fatal("external targets never read as deaf here")
	}
}
