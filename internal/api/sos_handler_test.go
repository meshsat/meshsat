package api

import (
	"testing"
	"time"
)

// TriggerSOS is the single door an SOS comes through: the dashboard button and
// the dead man's switch both arrive here. Before MESHSAT-996 the already-active
// guard lived in the HTTP handler, so anything reaching the worker another way
// ran without it and could start a second burst on top of the first, corrupting
// the send counter.
func TestTriggerSOS_RefusesASecondBurst(t *testing.T) {
	s := &Server{}

	if !s.TriggerSOS("test") {
		t.Fatal("first SOS was refused")
	}
	t.Cleanup(func() {
		s.sos.mu.Lock()
		if s.sos.cancelFn != nil {
			s.sos.cancelFn()
		}
		s.sos.mu.Unlock()
	})

	if s.TriggerSOS("deadman") {
		t.Fatal("a second SOS started while one was already active")
	}
}

// The burst runs in a goroutine, so a nil transport is not an error return, it
// is an unrecovered panic that takes the bridge down mid-emergency. A kit whose
// mesh radio failed to start must still reach the satellite legs.
func TestTriggerSOS_SurvivesMissingTransports(t *testing.T) {
	s := &Server{} // no mesh, no gateways, no GPS, no signing, no processor

	if !s.TriggerSOS("deadman") {
		t.Fatal("SOS was refused")
	}
	t.Cleanup(func() {
		s.sos.mu.Lock()
		if s.sos.cancelFn != nil {
			s.sos.cancelFn()
		}
		s.sos.mu.Unlock()
	})

	// Give the worker long enough to reach every leg of the first iteration.
	time.Sleep(300 * time.Millisecond)

	s.sos.mu.Lock()
	defer s.sos.mu.Unlock()
	if !s.sos.active {
		t.Fatal("worker died before completing an iteration")
	}
}

// touchOperatorActivity is called from handlers that may run before a switch is
// wired, and on a Hub-less build there is never one.
func TestTouchOperatorActivity_NilSafe(t *testing.T) {
	(&Server{}).touchOperatorActivity()
}
