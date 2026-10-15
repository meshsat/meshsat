package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// With HeMB experimental and off, POST /api/hemb/send and the fault
// injection refuse with 409 before anything is sent. Switched on, the same
// request is no longer refused for that reason. [MESHSAT-1581]
func TestHeMBRoutes_OffRefuse(t *testing.T) {
	s, _ := testServerWithDispatcher(t)
	router := s.Router()
	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}

	t.Setenv("MESHSAT_HEMB_EXPERIMENTAL", "")
	for _, path := range []string{"/api/hemb/send", "/api/hemb/fault-inject"} {
		if rr := post(path, `{"bond_group":"bond1","text":"x","interface_id":"tcp_0"}`); rr.Code != http.StatusConflict {
			t.Errorf("%s with HeMB off: %d %s, want 409", path, rr.Code, rr.Body.String())
		}
	}
	t.Setenv("MESHSAT_HEMB_EXPERIMENTAL", "true")
	if rr := post("/api/hemb/send", `{"bond_group":"no-such-bond","text":"x"}`); rr.Code == http.StatusConflict {
		t.Errorf("/api/hemb/send with HeMB on still refused: %s", rr.Body.String())
	}
}
