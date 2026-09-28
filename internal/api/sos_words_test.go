package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An SOS goes out with the caller's words (the apps' "SOS: <name> needs help. At
// <position> at <time>.") on every route, and the status reports them; without
// words the fixed sentence goes, as before. [MESHSAT-1397]
func TestSOS_CarriesTheCallersWords(t *testing.T) {
	s := &Server{}
	words := "SOS: Kyriakos needs help. At 52.37310, 4.89320 at 18:30 UTC."
	if !s.TriggerSOSWithText("hold", words) {
		t.Fatal("SOS was refused")
	}
	t.Cleanup(func() {
		s.sos.mu.Lock()
		if s.sos.cancelFn != nil {
			s.sos.cancelFn()
		}
		s.sos.mu.Unlock()
	})
	rec := httptest.NewRecorder()
	s.handleSOSStatus(rec, httptest.NewRequest(http.MethodGet, "/api/sos/status", nil))
	var status map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status["message"] != words {
		t.Fatalf("status message = %v, want the caller's words", status["message"])
	}
	if status["trigger"] != "hold" {
		t.Fatalf("status trigger = %v", status["trigger"])
	}
}

func TestSOSTextOf_TrimsCapsAndDefaults(t *testing.T) {
	if got := sosTextOf("  "); got != sosDefaultText {
		t.Fatalf("empty words gave %q", got)
	}
	if got := sosTextOf(" SOS: A needs help. "); got != "SOS: A needs help." {
		t.Fatalf("trim gave %q", got)
	}
	long := strings.Repeat("x", 300)
	if got := sosTextOf(long); len(got) != 200 {
		t.Fatalf("cap gave %d characters", len(got))
	}
}

// The activate handler passes the message through; the old callers (the dead
// man's switch, TriggerSOS) still get the fixed sentence.
func TestSOSActivate_TakesAMessage(t *testing.T) {
	s := &Server{}
	body := strings.NewReader(`{"trigger":"hold","message":"SOS: A MeshSat user needs help. Position unknown."}`)
	rec := httptest.NewRecorder()
	s.handleSOSActivate(rec, httptest.NewRequest(http.MethodPost, "/api/sos/activate", body))
	t.Cleanup(func() {
		s.sos.mu.Lock()
		if s.sos.cancelFn != nil {
			s.sos.cancelFn()
		}
		s.sos.mu.Unlock()
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("activate answered %d: %s", rec.Code, rec.Body.String())
	}
	s.sos.mu.Lock()
	text := s.sos.text
	s.sos.mu.Unlock()
	if text != "SOS: A MeshSat user needs help. Position unknown." {
		t.Fatalf("the worker's text is %q", text)
	}
	other := &Server{}
	if !other.TriggerSOS("deadman") {
		t.Fatal("refused")
	}
	t.Cleanup(func() {
		other.sos.mu.Lock()
		if other.sos.cancelFn != nil {
			other.sos.cancelFn()
		}
		other.sos.mu.Unlock()
	})
	other.sos.mu.Lock()
	defer other.sos.mu.Unlock()
	if other.sos.text != sosDefaultText {
		t.Fatalf("the dead man's switch got %q", other.sos.text)
	}
}

// A test of the alarm routes reaches the Hub only through a connected reporter;
// without one the caller is told, and nothing else happens.
func TestSOSTest_NeedsTheHub(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.handleSOSTest(rec, httptest.NewRequest(http.MethodPost, "/api/sos/test", strings.NewReader(`{"message":"Test from A: checking the MeshSat alarm routes. No help needed."}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("without a Hub the test answered %d: %s", rec.Code, rec.Body.String())
	}
	if s.sos != nil && s.sos.active {
		t.Fatal("a test started an SOS")
	}
}

// The Hub lane reads the link's state, never the stored settings.
func TestHubLinkState_NoReporterIsUnknown(t *testing.T) {
	s := &Server{}
	if got := s.hubLinkState(); got != "" {
		t.Fatalf("without a reporter the link state is %q", got)
	}
}
