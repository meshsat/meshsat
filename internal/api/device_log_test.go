package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/gateway"
)

// The device reliability log's three endpoints [MESHSAT-1842].
func TestDeviceLogEndpoints(t *testing.T) {
	s := newTestServerWithDB(t)
	var mu sync.Mutex
	var rows []string
	dh := gateway.NewDeviceHealth(gateway.DeviceHealthConfig{}, gateway.DeviceHealthActions{
		Record: func(ev, target, iface, detail string) {
			mu.Lock()
			rows = append(rows, ev+"|"+target+": "+detail)
			mu.Unlock()
		},
	})
	dh.Register(gateway.HealthTarget{Name: "rtl_sdr", Probe: func(context.Context) gateway.ProbeResult {
		return gateway.ProbeResult{Unknown: true}
	}})
	s.SetDeviceHealth(dh)

	post := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		s.Router().ServeHTTP(w, req)
		return w
	}

	if w := post("/api/devices/health/rtl_sdr/fitted", `{"fitted":false,"note":"out for the bench"}`); w.Code != http.StatusNoContent {
		t.Fatalf("fitted=false: %d %s", w.Code, w.Body.String())
	}
	if w := post("/api/devices/health/rtl_sdr/resume", `{}`); w.Code != http.StatusConflict {
		t.Fatalf("resume of a removed device: %d, want 409", w.Code)
	}
	if w := post("/api/devices/health/nope/fitted", `{"fitted":true}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown target: %d, want 404", w.Code)
	}
	if w := post("/api/devices/health/display/note", `{"action":"power-cycle","case_opened":true,"note":"panel black"}`); w.Code != http.StatusNoContent {
		t.Fatalf("note: %d %s", w.Code, w.Body.String())
	}
	if w := post("/api/devices/health/display/note", `{"action":"power-cycle"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("note without case_opened: %d, want 400", w.Code)
	}
	if w := post("/api/devices/health/display/note", `{"action":"hammer","case_opened":false}`); w.Code != http.StatusBadRequest {
		t.Fatalf("note with an unknown action: %d, want 400", w.Code)
	}
	mu.Lock()
	got := strings.Join(rows, "\n")
	mu.Unlock()
	for _, want := range []string{
		"device_inventory|rtl_sdr: rtl_sdr removed from the kit: out for the bench",
		"device_note|display: display hands-on fix: power-cycle; case opened: yes; panel black",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing row %q in\n%s", want, got)
		}
	}

	var st DeviceHealthResponse
	if code := getJSON(t, s, "/api/devices/health", &st); code != http.StatusOK || len(st.Targets) != 1 || !st.Targets[0].Removed {
		t.Fatalf("health %d %+v, want rtl_sdr removed", code, st.Targets)
	}

	t0 := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	if err := s.db.TouchKitPower("aaaaaaaa-1111", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.db.TouchKitPower("aaaaaaaa-1111", t0.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.db.TouchKitPower("bbbbbbbb-2222", t0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.db.TouchKitPower("bbbbbbbb-2222", t0.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var pw DevicePowerResponse
	if code := getJSON(t, s, "/api/devices/power", &pw); code != http.StatusOK {
		t.Fatalf("power: %d", code)
	}
	if pw.TotalHours != 2.5 || len(pw.Boots) != 2 || pw.Boots[0].Boot != "aaaaaaaa" || pw.Since != "2026-11-13T08:00:00Z" {
		t.Fatalf("power %+v", pw)
	}
}
