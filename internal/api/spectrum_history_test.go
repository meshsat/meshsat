package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/spectrum"
)

// TestSpectrumHistory_LimitIgnoresAge is the end-to-end regression for
// MESHSAT-654. Persist a single scan an hour ago, then ask the handler
// for ?limit=100 and confirm the row is returned. The previous seed
// path used ?minutes=5 and would return zero rows here, leaving the
// waterfall blank after any container restart longer than five minutes.
func TestSpectrumHistory_LimitIgnoresAge(t *testing.T) {
	db, err := database.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	staleTS := time.Now().Add(-time.Hour)
	if err := db.SaveScan(t.Context(), spectrum.ScanRow{
		TS:     staleTS,
		Band:   "lora_868",
		State:  "clear",
		AvgDB:  -57.3,
		MaxDB:  -35.1,
		Powers: []float64{-58, -57, -35, -57},
	}); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	srv := &Server{db: db}
	r := srv.Router()

	// Precondition: minutes=5 path returns nothing — the bug we're fixing.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/spectrum/history?band=lora_868&minutes=5", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("minutes path: got %d: %s", w.Code, w.Body.String())
	}
	var minutesResp struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &minutesResp); err != nil {
		t.Fatalf("decode minutes: %v", err)
	}
	if len(minutesResp.Rows) != 0 {
		t.Fatalf("precondition broken: minutes=5 returned %d rows for stale data", len(minutesResp.Rows))
	}

	// Fix: limit=100 returns the stale row regardless of age.
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/spectrum/history?band=lora_868&limit=100", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("limit path: got %d: %s", w.Code, w.Body.String())
	}
	var limitResp struct {
		Band string           `json:"band"`
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &limitResp); err != nil {
		t.Fatalf("decode limit: %v", err)
	}
	if limitResp.Band != "lora_868" {
		t.Fatalf("band echo: got %q", limitResp.Band)
	}
	if len(limitResp.Rows) != 1 {
		t.Fatalf("want 1 row from ?limit=100, got %d (rehydration broken)", len(limitResp.Rows))
	}
}

// TestSpectrumHistory_LimitValidation covers the input-validation arms
// of the handler — bad limit values must 400 rather than fall through
// to a no-op or, worse, an unbounded scan.
func TestSpectrumHistory_LimitValidation(t *testing.T) {
	db, err := database.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	srv := &Server{db: db}
	r := srv.Router()

	cases := []struct {
		name string
		url  string
		want int
	}{
		{"non-numeric", "/api/spectrum/history?band=x&limit=abc", http.StatusBadRequest},
		{"zero", "/api/spectrum/history?band=x&limit=0", http.StatusBadRequest},
		{"negative", "/api/spectrum/history?band=x&limit=-1", http.StatusBadRequest},
		{"missing band", "/api/spectrum/history?limit=10", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", tc.url, nil))
			if w.Code != tc.want {
				t.Errorf("got %d, want %d (body=%s)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}
