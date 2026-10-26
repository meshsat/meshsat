package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// The demo status is a lock-free snapshot of the run, and its JSON must be
// the same bytes the run itself encodes to: same keys, same order, and a zero
// "finished" still present (omitempty never drops a time.Time). [MESHSAT-1673]
func TestDemoStatusEncodesTheRunWithoutItsLock(t *testing.T) {
	// Recent, or the registry evicts a finished run as it is put.
	started := time.Now().UTC().Truncate(time.Second)
	for _, finished := range []time.Time{{}, started.Add(3 * time.Second)} {
		run := &demoRun{
			ID: "demo-1673-" + finished.Format("150405"), Status: "running", Started: started,
			Finished: finished,
			Channels: []demoChannelResult{
				{Name: "mesh", Status: "success", LatencyMS: 42},
				{Name: "iridium", Status: "skipped", Detail: "no modem"},
			},
		}
		want, err := json.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(run.snapshot())
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("snapshot JSON differs from the run's\n got: %s\nwant: %s", got, want)
		}

		demoReg.put(run)
		r := chi.NewRouter()
		r.Get("/api/demo/{demo_id}", (&Server{}).handleDemoStatus)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/demo/"+run.ID, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d, body %s", rec.Code, rec.Body)
		}
		if body := rec.Body.String(); body != string(want)+"\n" {
			t.Fatalf("handler body differs from the run's JSON\n got: %s\nwant: %s", body, want)
		}
	}
}
