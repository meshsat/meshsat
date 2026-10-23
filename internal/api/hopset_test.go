package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"meshsat/internal/engine"
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

	s.SetHopSet(engine.NewHopSetManager(engine.DefaultHopSetConfig(), s.db.GetAllInterfaces, false))
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
