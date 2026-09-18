package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeDongle records the order of events around an rtl_sdr level-3 heal.
type fakeDongle struct {
	mu      sync.Mutex
	events  []string
	present bool
	// after the cut: how many presence polls it stays gone, -1 = forever
	goneFor int
	polls   int
	cutOK   bool
	cut     bool
	leaves  bool
}

func (f *fakeDongle) log(e string) {
	f.mu.Lock()
	f.events = append(f.events, e)
	f.mu.Unlock()
}

func (f *fakeDongle) reset() rtlSDRReset {
	r := newRTLSDRReset(
		func(context.Context) { f.log("suspend") },
		func() { f.log("resume") },
		func(context.Context) bool {
			f.log("cut")
			f.mu.Lock()
			f.cut = f.cutOK
			f.mu.Unlock()
			return f.cutOK
		},
		func() bool {
			f.mu.Lock()
			defer f.mu.Unlock()
			if !f.cut || !f.leaves {
				return f.present
			}
			f.polls++
			if f.goneFor < 0 || f.polls <= f.goneFor {
				return false
			}
			return true
		},
	)
	r.gone, r.back, r.settle, r.poll = 200*time.Millisecond, 300*time.Millisecond, 10*time.Millisecond, 5*time.Millisecond
	return r
}

func (f *fakeDongle) order() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func TestRTLSDRResetSuspendsCutsWaitsResumes(t *testing.T) {
	f := &fakeDongle{present: true, cutOK: true, leaves: true, goneFor: 5}
	if err := f.reset().run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := f.order()
	want := []string{"suspend", "cut", "resume"}
	if len(got) != len(want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events %v, want %v", got, want)
		}
	}
}

func TestRTLSDRResetNoSwitchablePortIsAnErrorNotAUSBReset(t *testing.T) {
	f := &fakeDongle{present: true, cutOK: false}
	err := f.reset().run(context.Background())
	if !errors.Is(err, errRTLSDRNoSwitchablePort) {
		t.Fatalf("want errRTLSDRNoSwitchablePort, got %v", err)
	}
	if got := f.order(); got[len(got)-1] != "resume" {
		t.Errorf("scanning not resumed after a refused cut: %v", got)
	}
}

func TestRTLSDRResetCutThatNeverTookEffect(t *testing.T) {
	f := &fakeDongle{present: true, cutOK: true, leaves: false}
	if err := f.reset().run(context.Background()); !errors.Is(err, errRTLSDRNeverLeft) {
		t.Fatalf("want errRTLSDRNeverLeft, got %v", err)
	}
}

func TestRTLSDRResetDongleDoesNotComeBack(t *testing.T) {
	f := &fakeDongle{present: true, cutOK: true, leaves: true, goneFor: -1}
	if err := f.reset().run(context.Background()); !errors.Is(err, errRTLSDRNotBack) {
		t.Fatalf("want errRTLSDRNotBack, got %v", err)
	}
	if got := f.order(); got[len(got)-1] != "resume" {
		t.Errorf("scanning not resumed after a failed heal: %v", got)
	}
}

// A dongle already off the bus: cut its last port and wait for it to appear.
func TestRTLSDRResetAbsentDongleComesBack(t *testing.T) {
	f := &fakeDongle{present: false, cutOK: true, leaves: true, goneFor: 3}
	if err := f.reset().run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// Two heals at once (watchdog and an OOB RESET) run one after the other:
// the second never resumes scanning under the first one's cut.
func TestRTLSDRResetSerialisesConcurrentHeals(t *testing.T) {
	f := &fakeDongle{present: true, cutOK: true, leaves: true, goneFor: 2}
	r := f.reset()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.mu.Lock()
			f.polls = 0
			f.mu.Unlock()
			_ = r.run(context.Background())
		}()
	}
	wg.Wait()
	got := f.order()
	for i := 0; i+2 < len(got); i += 3 {
		if got[i] != "suspend" || got[i+1] != "cut" || got[i+2] != "resume" {
			t.Fatalf("heals interleaved: %v", got)
		}
	}
	if len(got) != 6 {
		t.Fatalf("events %v, want two suspend/cut/resume runs", got)
	}
}
