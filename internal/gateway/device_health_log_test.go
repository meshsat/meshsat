package gateway

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// The device reliability log [MESHSAT-1842] reads the watchdog's rows in the
// signed audit log. These tests hold the rows that tell a software heal from
// a hand, a removal and a restart.

type auditTape struct {
	mu   sync.Mutex
	rows []string
}

func (a *auditTape) add(s string) {
	a.mu.Lock()
	a.rows = append(a.rows, s)
	a.mu.Unlock()
}

func (a *auditTape) count(sub string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, r := range a.rows {
		if strings.Contains(r, sub) {
			n++
		}
	}
	return n
}

func (a *auditTape) wait(t *testing.T, sub string) {
	t.Helper()
	waitFor(t, func() bool { return a.count(sub) > 0 })
}

// settle gives the watchdog's audit goroutines time to land, for checks
// that something did NOT happen.
func (a *auditTape) settle() { time.Sleep(30 * time.Millisecond) }

func withTape(h *dhHarness) *auditTape {
	tape := &auditTape{}
	h.dh.act.Audit = func(target, iface, detail string) { tape.add("device_health|" + target + ": " + detail) }
	h.dh.act.Record = func(ev, target, iface, detail string) { tape.add(ev + "|" + target + ": " + detail) }
	return tape
}

func failingStep(level byte, name string, grace time.Duration) HealStep {
	return HealStep{Level: level, Name: name, Grace: grace, Run: func(ctx context.Context) error {
		return errors.New("no switchable hub port")
	}}
}

// wedge drives a target from ok to its first rung.
func wedge(t *testing.T, h *dhHarness, name string) {
	t.Helper()
	h.ok(name)
	h.tick(t)
	h.miss(name)
	for range 3 {
		h.advance(30 * time.Second)
		h.tick(t)
	}
}

func TestDeviceHealthLog_RecoveryWithinGraceIsCredited(t *testing.T) {
	h := newDHHarness(DeviceHealthConfig{Misses: 3, Tick: 30 * time.Second})
	tape := withTape(h)
	h.dh.Register(h.target("mesh", []string{"mesh_0"}, meshLadder(h)...))
	wedge(t, h, "mesh")
	tape.wait(t, "mesh: mesh unhealthy:")
	tape.wait(t, "step 1, level 1, reconnect (auto)")
	h.advance(30 * time.Second)
	h.ok("mesh")
	h.tick(t)
	tape.wait(t, "mesh: mesh recovered after step 1 (reconnect)")
	if n := tape.count("came back without"); n != 0 {
		t.Fatalf("a recovery inside the grace must be credited to the rung, got %d uncredited rows", n)
	}
}

func TestDeviceHealthLog_ReseatAfterLeavingTheBusIsNotCreditedToSoftware(t *testing.T) {
	h := newDHHarness(DeviceHealthConfig{Misses: 3, Tick: 30 * time.Second})
	tape := withTape(h)
	h.dh.Register(h.target("rtl_sdr", nil, h.step("rtl_sdr", HealLevelSoft, "restart reader", 60*time.Second, nil)))
	wedge(t, h, "rtl_sdr")
	tape.wait(t, "step 1, level 1, restart reader (auto)")
	// Past the grace the dongle drops off the bus.
	h.advance(2 * time.Minute)
	h.set("rtl_sdr", ProbeResult{Unknown: true, Detail: "dongle not on the USB bus"})
	h.tick(t)
	tape.wait(t, "rtl_sdr: rtl_sdr left the bus while unhealthy: dongle not on the USB bus")
	// Hours later a hand re-seats it.
	for range 10 {
		h.advance(30 * time.Minute)
		h.tick(t)
	}
	h.ok("rtl_sdr")
	h.tick(t)
	tape.wait(t, "rtl_sdr: rtl_sdr came back without a software step (by hand, by itself or a restart)")
	tape.settle()
	if n := tape.count("recovered after step"); n != 0 {
		t.Fatalf("a re-seat was credited to software: %d rows", n)
	}
	if n := tape.count("left the bus"); n != 1 {
		t.Fatalf("left-the-bus written %d times, want once", n)
	}
	// The episode is closed: another spell off the bus writes nothing.
	h.set("rtl_sdr", ProbeResult{Unknown: true, Detail: "gone"})
	h.tick(t)
	h.ok("rtl_sdr")
	h.tick(t)
	tape.settle()
	if n := tape.count("came back without"); n != 1 {
		t.Fatalf("a device absent outside an episode must write nothing, got %d came-back rows", n)
	}
}

func TestDeviceHealthLog_BackButStillUnhealthyIsTheSameWedge(t *testing.T) {
	h := newDHHarness(DeviceHealthConfig{Misses: 3, Tick: 30 * time.Second})
	tape := withTape(h)
	h.dh.Register(h.target("zigbee", []string{"zigbee_0"}, h.step("zigbee", HealLevelSoft, "reopen", 60*time.Second, nil)))
	wedge(t, h, "zigbee")
	h.advance(2 * time.Minute)
	h.set("zigbee", ProbeResult{Unknown: true, Detail: "no coordinator on the bus"})
	h.tick(t)
	tape.wait(t, "zigbee left the bus while unhealthy")
	h.miss("zigbee")
	for range 3 {
		h.advance(30 * time.Second)
		h.tick(t)
	}
	tape.wait(t, "zigbee: zigbee back but still unhealthy: silent (3 consecutive misses)")
	if n := tape.count("zigbee: zigbee unhealthy:"); n != 1 {
		t.Fatalf("the return must continue the wedge, not open a new one: %d opening rows", n)
	}
}

func TestDeviceHealthLog_AFailedRungIsNotCredited(t *testing.T) {
	h := newDHHarness(DeviceHealthConfig{Misses: 3, Tick: 30 * time.Second})
	tape := withTape(h)
	h.dh.Register(h.target("rtl_sdr", nil, failingStep(HealLevelHard, "hub-port power cycle", 90*time.Second)))
	wedge(t, h, "rtl_sdr")
	tape.wait(t, "step 1 hub-port power cycle: no switchable hub port")
	h.advance(30 * time.Second)
	h.ok("rtl_sdr")
	h.tick(t)
	tape.wait(t, "rtl_sdr came back without a software step that worked (by hand, by itself or a restart); the last, step 1 (hub-port power cycle), could not act")
}

func TestDeviceHealthLog_ARecoveryLongAfterTheLadderGaveUpIsNotCredited(t *testing.T) {
	h := newDHHarness(DeviceHealthConfig{Misses: 3, Tick: 30 * time.Second})
	tape := withTape(h)
	h.dh.Register(h.target("mesh", []string{"mesh_0"}, h.step("mesh", HealLevelSoft, "reconnect", 60*time.Second, nil)))
	wedge(t, h, "mesh")
	h.advance(90 * time.Second)
	h.tick(t)
	tape.wait(t, "mesh: ladder exhausted after 1 rungs")
	h.advance(20 * time.Minute)
	h.ok("mesh")
	h.tick(t)
	tape.wait(t, "mesh came back without a software step that worked (by hand, by itself or a restart); the last, step 1 (reconnect), ran 22m0s earlier")
}

func TestDeviceHealthLog_ARemovedDeviceWritesNothing(t *testing.T) {
	h := newDHHarness(DeviceHealthConfig{Misses: 3, Tick: 30 * time.Second})
	tape := withTape(h)
	h.dh.Register(h.target("rtl_sdr", nil, h.step("rtl_sdr", HealLevelSoft, "restart reader", 60*time.Second, nil)))
	wedge(t, h, "rtl_sdr")
	tape.wait(t, "rtl_sdr unhealthy:")
	if err := h.dh.SetFitted("rtl_sdr", false, "taken out for the\nHeMB-NG bench"); err != nil {
		t.Fatal(err)
	}
	tape.wait(t, "device_inventory|rtl_sdr: rtl_sdr removed from the kit: taken out for the HeMB-NG bench")
	before := tape.count("device_health|rtl_sdr")
	h.set("rtl_sdr", ProbeResult{Detail: "dongle not on the USB bus"})
	for range 20 {
		h.advance(30 * time.Minute)
		h.tick(t)
	}
	tape.settle()
	if after := tape.count("device_health|rtl_sdr"); after != before {
		t.Fatalf("a removed device wrote %d rows", after-before)
	}
	if st := h.dh.Status()[0]; !st.Removed || !st.Paused || st.State != HealthStatePaused {
		t.Fatalf("status %+v, want removed, paused", st)
	}
	if p := h.persist["rtl_sdr"]; !p.Removed || !p.Paused {
		t.Fatalf("persisted %+v, want removed", p)
	}
	if err := h.dh.Resume("rtl_sdr"); !errors.Is(err, ErrHealthRemoved) {
		t.Fatalf("resume of a removed device: %v, want ErrHealthRemoved", err)
	}
	if err := h.dh.SetFitted("rtl_sdr", true, ""); err != nil {
		t.Fatal(err)
	}
	tape.wait(t, "device_inventory|rtl_sdr: rtl_sdr fitted again")
	if st := h.dh.Status()[0]; st.Removed || st.Paused {
		t.Fatalf("status %+v after fitted again", st)
	}

	// A restart keeps the removal.
	h2 := newDHHarness(DeviceHealthConfig{Seed: map[string]PersistedTarget{"rtl_sdr": {Removed: true}}})
	h2.dh.Register(h2.target("rtl_sdr", nil))
	if st := h2.dh.Status()[0]; !st.Removed || !st.Paused {
		t.Fatalf("seeded status %+v, want removed and paused", st)
	}
}

func TestDeviceHealthLog_NotesAreCheckedAndRecorded(t *testing.T) {
	h := newDHHarness(DeviceHealthConfig{})
	tape := withTape(h)
	h.dh.Register(h.target("mesh", []string{"mesh_0"}))
	h.dh.RegisterExternal("aprs", []string{"aprs_0"}, func() (string, string) { return HealthStateOK, "" })
	if err := h.dh.Note("mesh", "kick", true, ""); !errors.Is(err, ErrHealthNoteAction) {
		t.Fatalf("unknown action: %v", err)
	}
	if err := h.dh.Note("toaster", "re-seat", true, ""); !errors.Is(err, ErrHealthTargetUnknown) {
		t.Fatalf("unknown target: %v", err)
	}
	if err := h.dh.Note("aprs", "button", true, "held PTT 3 s\r\n  on the PicoAPRS"); err != nil {
		t.Fatal(err)
	}
	if err := h.dh.Note("display", "power-cycle", false, ""); err != nil {
		t.Fatal(err)
	}
	tape.wait(t, "device_note|aprs: aprs hands-on fix: button; case opened: yes; held PTT 3 s on the PicoAPRS")
	tape.wait(t, "device_note|display: display hands-on fix: power-cycle; case opened: no")
	if got := CleanNote(strings.Repeat("x", 300)); len(got) != 200 {
		t.Fatalf("note cut at %d, want 200", len(got))
	}
}

func TestDeviceHealthLog_TheStartRowNamesTheBoot(t *testing.T) {
	h := newDHHarness(DeviceHealthConfig{BootID: "1a2b3c4d-0000-4000-8000-000000000000"})
	tape := withTape(h)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.dh.Run(ctx)
	tape.wait(t, "device_health|watchdog: watchdog started (boot 1a2b3c4d)")
}
