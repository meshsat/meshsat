package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"meshsat/internal/engine"
	"meshsat/internal/hopwire"
)

// GET /api/hopset shows each covered link with its state and whether the
// hop set gates or only observes; 503 when there is none. [MESHSAT-1650]
func TestGetHopSet(t *testing.T) {
	s := newTestServerWithDB(t)
	req := httptest.NewRequest(http.MethodGet, "/api/hopset", nil)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no hop set: %d, want 503", rec.Code)
	}

	s.SetHopSet(engine.NewHopSetManager(engine.DefaultHopSetConfig(), s.db.GetAllInterfaces, engine.HopWatch))
	rec = httptest.NewRecorder()
	s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/hopset", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("hop set: %d %s", rec.Code, rec.Body.String())
	}
	var st engine.HopSetStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Gating {
		t.Error("gating true, want observing")
	}
	found := false
	for _, l := range st.Links {
		if l.InterfaceID == "mesh_0" {
			found = l.State == engine.HopAdmitted && !l.Paid
		}
	}
	if !found {
		t.Errorf("links %+v, want mesh_0 admitted and free", st.Links)
	}
}

// The media hopping switch on the API: off by default, a PUT stores the mode
// in system_config, applies it at once and audits it; anything but off,
// watch or on is refused. [MESHSAT-1650]
func TestHopModeAPI(t *testing.T) {
	s := newTestServerWithDB(t)
	do := func(method, body string) *httptest.ResponseRecorder {
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, "/api/hopset/mode", nil)
		} else {
			req = httptest.NewRequest(method, "/api/hopset/mode", strings.NewReader(body))
		}
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, req)
		return rec
	}
	if rec := do(http.MethodGet, ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no hop set: %d, want 503", rec.Code)
	}
	hs := engine.NewHopSetManager(engine.DefaultHopSetConfig(), s.db.GetAllInterfaces, engine.HopOff)
	s.SetHopSet(hs)
	signing, err := engine.NewSigningService(s.db)
	if err != nil {
		t.Fatal(err)
	}
	s.SetSigningService(signing)

	if rec := do(http.MethodGet, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"mode":"off"`) {
		t.Fatalf("GET: %d %s, want off", rec.Code, rec.Body.String())
	}
	for _, bad := range []string{`{"mode":"gate"}`, `{"mode":""}`, `not json`} {
		if rec := do(http.MethodPut, bad); rec.Code != http.StatusBadRequest {
			t.Errorf("PUT %s: %d, want 400", bad, rec.Code)
		}
	}
	if v, err := s.db.GetSystemConfig(engine.HopModeKey); err == nil {
		t.Fatalf("a refused PUT stored %q", v)
	}
	if rec := do(http.MethodPut, `{"mode":"watch"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT watch: %d %s", rec.Code, rec.Body.String())
	}
	if hs.Mode() != engine.HopWatch {
		t.Errorf("after PUT watch: mode %q", hs.Mode())
	}
	if v, err := s.db.GetSystemConfig(engine.HopModeKey); err != nil || v != "watch" {
		t.Errorf("stored %q (%v), want watch", v, err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE event_type = 'media_hop_mode' AND detail = 'media hopping off -> watch'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("audit entries for the change: %d (%v), want 1", n, err)
	}
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/hopset", nil))
	if !strings.Contains(rec.Body.String(), `"mode":"watch"`) {
		t.Errorf("GET /api/hopset after the change: %s, want mode watch", rec.Body.String())
	}
}

// api_peers_shape (part two): GET /api/hopset carries the peers' reported hop
// state as a list (empty, never null) and each link's last probe. [MESHSAT-1650]
func TestGetHopSetPeers(t *testing.T) {
	s := newTestServerWithDB(t)
	hs := engine.NewHopSetManager(engine.DefaultHopSetConfig(), s.db.GetAllInterfaces, engine.HopWatch)
	s.SetHopSet(hs)
	get := func() map[string]json.RawMessage {
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/hopset", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("hop set: %d", rec.Code)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if p := string(get()["peers"]); p != "[]" {
		t.Fatalf("peers %s, want []", p)
	}
	hs.Probe("mesh_0")
	hs.NotePeer(9, "tesseract", "mesh_0", "reply", hopwire.Report{Links: []hopwire.Link{{Iface: "aprs_0", State: hopwire.Excluded}}})
	raw := get()
	if !strings.Contains(string(raw["peers"]), `"alias":"tesseract"`) || !strings.Contains(string(raw["peers"]), `"state":"excluded"`) {
		t.Fatalf("peers %s", raw["peers"])
	}
	if !strings.Contains(string(raw["links"]), `"last_probe"`) {
		t.Fatalf("links carry no last_probe: %s", raw["links"])
	}
}
