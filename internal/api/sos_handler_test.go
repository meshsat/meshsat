package api

import (
	"net/http"
	"net/http/httptest"
	"sync"
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

// The SOS state is part of the server, however it is built: on a server
// nobody has used yet, a status read, the alarm test's SOS check and an SOS
// start do not race over making it (go test -race). It was made on first
// use, without a lock. [MESHSAT-1430]
func TestSOSState_ReadyWithTheServer(t *testing.T) {
	s := &Server{}
	t.Cleanup(stopSOS(s))
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = s.sosActive()
		}()
		go func() {
			defer wg.Done()
			s.handleSOSStatus(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/sos/status", nil))
		}()
	}
	started := s.TriggerSOS("hold")
	wg.Wait()
	if !started || !s.sosActive() {
		t.Fatalf("started %v, active %v", started, s.sosActive())
	}
}
