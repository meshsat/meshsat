package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// POST /api/aprs/tnc/reset: the TNC reset on request. [MESHSAT-1896]
func TestAPRSTNCReset(t *testing.T) {
	post := func(s *Server) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/aprs/tnc/reset", nil))
		return rec
	}

	if rec := post(newTestServer()); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("not wired: %d, want 503", rec.Code)
	}

	s := newTestServer()
	var by string
	s.SetAPRSTNCReset(func(ctx context.Context, who string) error { by = who; return nil })
	rec := post(s)
	if rec.Code != http.StatusOK || by != "API" {
		t.Fatalf("reset: %d by %q, want 200 by API", rec.Code, by)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["ok"] != true || body["held_ms"] != float64(5000) {
		t.Fatalf("body %s", rec.Body.String())
	}

	s.SetAPRSTNCReset(func(ctx context.Context, who string) error {
		return errors.New("aprs: the TNC is not on a serial port")
	})
	if rec := post(s); rec.Code != http.StatusConflict {
		t.Fatalf("no serial TNC: %d, want 409", rec.Code)
	}
}
