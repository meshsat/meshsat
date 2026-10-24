package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// The enabled interval: what the hop exchange and the probes gather in one
// interval is never used in the next, and the interval's own goroutines are
// done before the switch to off returns (R55-B1, R55-B2). [MESHSAT-1650]

// off_runs_the_resets_and_ends_the_interval: each switch to off moves the
// interval and runs every reset once; watch and on keep the interval.
func TestHopIntervalOffRunsTheResets(t *testing.T) {
	r := newHopRig(t, threeFree...)
	var resets atomic.Int32
	r.m.OnOff(func() { resets.Add(1) })
	r.m.OnOff(func() { resets.Add(10) })
	g0, on := r.m.Session()
	if !on {
		t.Fatal("the rig starts on")
	}
	for _, mode := range []HopMode{HopWatch, HopOn, HopWatch} {
		if _, err := r.m.SetMode(mode); err != nil {
			t.Fatal(err)
		}
		if g, on := r.m.Session(); g != g0 || !on || resets.Load() != 0 {
			t.Fatalf("%s: interval %d on %v resets %d, want %d true 0", mode, g, on, resets.Load(), g0)
		}
	}
	if _, err := r.m.SetMode(HopOff); err != nil {
		t.Fatal(err)
	}
	g1, on := r.m.Session()
	if g1 == g0 || on || resets.Load() != 11 {
		t.Fatalf("off: interval %d (was %d) on %v resets %d, want a new interval, off, 11", g1, g0, on, resets.Load())
	}
	if _, err := r.m.SetMode(HopOff); err != nil {
		t.Fatal(err)
	}
	if resets.Load() != 22 {
		t.Fatalf("a second off ran the resets %d times in all, want 22", resets.Load())
	}
}

// probe_and_peer_report_of_an_ended_interval_not_taken: a probe or a peer's
// report gathered before off is not taken after the switch comes back.
func TestHopIntervalEndedEvidenceNotTaken(t *testing.T) {
	r := newHopRig(t, threeFree...)
	if _, err := r.m.SetMode(HopWatch); err != nil {
		t.Fatal(err)
	}
	old, _ := r.m.Session()
	for _, mode := range []HopMode{HopOff, HopWatch} {
		if _, err := r.m.SetMode(mode); err != nil {
			t.Fatal(err)
		}
	}
	r.m.Probe("mesh_0", old)
	r.m.NotePeer(old, 7, "parallax", "mesh_0", "reply", allExcluded("mesh_0"))
	if l := r.state(t, "mesh_0"); l.LastProbe != nil {
		t.Errorf("a probe of the ended interval was taken at %v", l.LastProbe)
	}
	if p := r.m.Status().Peers; len(p) != 0 {
		t.Errorf("a peer report of the ended interval is shown: %+v", p)
	}
	now, _ := r.m.Session()
	r.m.Probe("mesh_0", now)
	r.m.NotePeer(now, 7, "parallax", "mesh_0", "reply", allExcluded("mesh_0"))
	if l := r.state(t, "mesh_0"); l.LastProbe == nil {
		t.Error("a probe of this interval was not taken")
	}
	if p := r.m.Status().Peers; len(p) != 1 {
		t.Errorf("a peer report of this interval is not shown: %+v", p)
	}
}

// interval_goroutine_settled_before_off_returns: off cancels the interval's
// goroutine and returns only once it has; one that ignores the cancel holds
// off until it is done.
func TestHopIntervalGoSettledBeforeOffReturns(t *testing.T) {
	r := newHopRig(t, threeFree...)
	gen, _ := r.m.Session()
	started := make(chan struct{})
	var cancelled, done atomic.Bool
	r.m.Go(gen, func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		cancelled.Store(true)
		// What a share does on its way out: our form, which evaluates and
		// flushes. It must not wait on the switch that is waiting on it.
		if f := r.m.Compact(200); f != "" {
			t.Errorf("our form after off: %q", f)
		}
		done.Store(true)
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("the interval's goroutine never started")
	}
	offed := make(chan struct{})
	go func() {
		_, _ = r.m.SetMode(HopOff)
		close(offed)
	}()
	select {
	case <-offed:
	case <-time.After(3 * time.Second):
		t.Fatal("off never returned")
	}
	if !cancelled.Load() || !done.Load() {
		t.Fatalf("off returned with the interval's goroutine cancelled %v, done %v", cancelled.Load(), done.Load())
	}

	if _, err := r.m.SetMode(HopOn); err != nil {
		t.Fatal(err)
	}
	gen, _ = r.m.Session()
	release := make(chan struct{})
	var finished atomic.Bool
	started2 := make(chan struct{})
	r.m.Go(gen, func(context.Context) {
		close(started2)
		<-release
		finished.Store(true)
	})
	select {
	case <-started2:
	case <-time.After(3 * time.Second):
		t.Fatal("the next interval's goroutine never started")
	}
	offed = make(chan struct{})
	go func() {
		_, _ = r.m.SetMode(HopOff)
		close(offed)
	}()
	select {
	case <-offed:
		t.Fatal("off returned while the interval's goroutine was still running")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	select {
	case <-offed:
	case <-time.After(3 * time.Second):
		t.Fatal("off never returned")
	}
	if !finished.Load() {
		t.Fatal("off returned before the goroutine finished")
	}
}

// work_prepared_in_an_ended_interval_never_runs: output prepared before off
// (a change hook given the old interval) starts nothing after it, nor once
// the switch is back; nothing starts while off.
func TestHopIntervalPreparedWorkNeverRunsAfterOff(t *testing.T) {
	r := newHopRig(t, threeFree...)
	old, _ := r.m.Session()
	if _, err := r.m.SetMode(HopOff); err != nil {
		t.Fatal(err)
	}
	var ran atomic.Int32
	r.m.Go(old, func(context.Context) { ran.Add(1) })
	off, _ := r.m.Session()
	r.m.Go(off, func(context.Context) { ran.Add(1) })
	if _, err := r.m.SetMode(HopWatch); err != nil {
		t.Fatal(err)
	}
	r.m.Go(old, func(context.Context) { ran.Add(1) })
	time.Sleep(50 * time.Millisecond)
	if n := ran.Load(); n != 0 {
		t.Fatalf("%d goroutines of an ended interval or of off ran", n)
	}
}

// change_hook_carries_its_interval: the change hook is told the interval the
// change happened in, so the share it starts belongs to that interval.
func TestHopIntervalChangeHookCarriesItsInterval(t *testing.T) {
	r := newHopRig(t, threeFree...)
	got := make(chan uint64, 4)
	r.m.SetOnChange(func(gen uint64) { got <- gen })
	want, _ := r.m.Session()
	excludeMesh(t, r)
	select {
	case g := <-got:
		if g != want {
			t.Fatalf("change hook told interval %d, want %d", g, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no change hook for a link going out of use")
	}
}

// hold_settled_before_off_returns: work holding the interval (a later one,
// after an off) is told which, and holds off until it is released; nothing
// is held with the switch off.
func TestHopIntervalHoldSettledBeforeOffReturns(t *testing.T) {
	r := newHopRig(t, threeFree...)
	_, _ = r.m.SetMode(HopOff)
	_, _ = r.m.SetMode(HopOn)
	gen, _ := r.m.Session()
	release, held, ok := r.m.Hold()
	if !ok || held != gen {
		t.Fatalf("hold with the switch on: ok %v, interval %d, want %d", ok, held, gen)
	}
	offed := make(chan struct{})
	go func() {
		_, _ = r.m.SetMode(HopOff)
		close(offed)
	}()
	select {
	case <-offed:
		t.Fatal("off returned while the interval was held")
	case <-time.After(150 * time.Millisecond):
	}
	release()
	release() // twice is once
	select {
	case <-offed:
	case <-time.After(3 * time.Second):
		t.Fatal("off never returned after the release")
	}
	if _, _, ok := r.m.Hold(); ok {
		t.Fatal("a hold with the switch off")
	}
}
