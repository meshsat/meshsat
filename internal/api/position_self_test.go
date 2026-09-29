package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"meshsat/internal/selfpos"
)

func selfPosRequest(t *testing.T, s *Server, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/position/self", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, req)
	return w
}

// PUT /api/position/self validates and keeps the app's fix; GET answers the
// resolved position: the app's fix while under 10 minutes old, then the
// fallback (the local node, then the GPS reader in main.go), else 404.
// [MESHSAT-1421]
func TestSelfPositionAPI(t *testing.T) {
	// Not wired: PUT has nowhere to go, GET knows nothing.
	bare := &Server{}
	if w := selfPosRequest(t, bare, "PUT", `{"latitude":52.3676,"longitude":4.9041}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT without a store: %d", w.Code)
	}
	if w := selfPosRequest(t, bare, "GET", ""); w.Code != http.StatusNotFound {
		t.Fatalf("GET without a store: %d", w.Code)
	}

	store := &selfpos.Store{}
	var node *selfpos.Fix
	fallback := func() (selfpos.Fix, bool) {
		if node == nil {
			return selfpos.Fix{}, false
		}
		return *node, true
	}
	s := &Server{}
	s.SetSelfPosition(store, func() (selfpos.Fix, bool) { return selfpos.Resolve(store, fallback, selfpos.MaxAge) })

	// Nothing known yet.
	if w := selfPosRequest(t, s, "GET", ""); w.Code != http.StatusNotFound {
		t.Fatalf("GET with no position: %d %s", w.Code, w.Body)
	}

	// Validation: 400 with the reason, nothing kept.
	for body, want := range map[string]string{
		`{"latitude":91,"longitude":4.9}`:                     "latitude",
		`{"latitude":52.3,"longitude":181}`:                   "longitude",
		`{"longitude":4.9}`:                                   "latitude and longitude are required",
		`{"latitude":0,"longitude":0}`:                        "0,0",
		`{"latitude":52.3,"longitude":4.9,"speed_mps":-1}`:    "speed_mps",
		`{"latitude":52.3,"longitude":4.9,"course_deg":-1}`:   "course_deg",
		`{"latitude":52.3,"longitude":4.9,"accuracy_m":-5}`:   "accuracy_m",
		`{"latitude":52.3,"longitude":4.9,"altitude_m":1e10}`: "altitude_m",
		`not json`: "invalid request body",
	} {
		w := selfPosRequest(t, s, "PUT", body)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), want) {
			t.Errorf("PUT %s: %d %s, want 400 naming %q", body, w.Code, w.Body, want)
		}
	}
	if _, ok := store.Get(); ok {
		t.Fatal("a refused position was kept")
	}

	// The fallback stands in while the app has no fix.
	node = &selfpos.Fix{Latitude: 51.5, Longitude: -0.1, AltitudeM: 20, At: time.Now()}
	var got selfpos.Fix
	w := selfPosRequest(t, s, "GET", "")
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Latitude != 51.5 {
		t.Fatalf("GET with the node only: %d %s", w.Code, w.Body)
	}

	// A good PUT: 200 with the stored fix and its time; it wins over the node.
	w = selfPosRequest(t, s, "PUT", `{"latitude":52.3676,"longitude":4.9041,"altitude_m":15.4,"speed_mps":6.944,"course_deg":90,"accuracy_m":8}`)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	if got.Latitude != 52.3676 || got.Longitude != 4.9041 || got.AltitudeM != 15.4 || got.SpeedMPS != 6.944 ||
		got.CourseDeg != 90 || got.AccuracyM != 8 || time.Since(got.At) > time.Minute {
		t.Fatalf("PUT answered %+v", got)
	}
	w = selfPosRequest(t, s, "GET", "")
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Latitude != 52.3676 {
		t.Fatalf("GET after the PUT: %d %s", w.Code, w.Body)
	}
	for _, key := range []string{`"latitude"`, `"longitude"`, `"altitude_m"`, `"speed_mps"`, `"course_deg"`, `"accuracy_m"`, `"at"`} {
		if !strings.Contains(w.Body.String(), key) {
			t.Errorf("GET lacks %s: %s", key, w.Body)
		}
	}

	// Stale: an app fix 10 minutes old gives way to the node, and with no
	// node there is no position.
	if err := store.Set(selfpos.Fix{Latitude: 52.3676, Longitude: 4.9041, At: time.Now().Add(-selfpos.MaxAge - time.Second)}); err != nil {
		t.Fatal(err)
	}
	w = selfPosRequest(t, s, "GET", "")
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Latitude != 51.5 {
		t.Fatalf("GET with a stale app fix: %d %s", w.Code, w.Body)
	}
	node = nil
	if w := selfPosRequest(t, s, "GET", ""); w.Code != http.StatusNotFound {
		t.Fatalf("GET with a stale app fix and no node: %d %s", w.Code, w.Body)
	}

	// Wired with the store only (no resolver), GET still answers a fresh fix.
	only := &Server{}
	only.SetSelfPosition(&selfpos.Store{}, nil)
	if w := selfPosRequest(t, only, "PUT", `{"latitude":-33.9,"longitude":151.2}`); w.Code != http.StatusOK {
		t.Fatalf("PUT to a store-only server: %d", w.Code)
	}
	if w := selfPosRequest(t, only, "GET", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "-33.9") {
		t.Fatalf("GET from a store-only server: %d %s", w.Code, w.Body)
	}
}

// GET /api/aprs/status answers without a gateway manager (spec section 7).
func TestAPRSStatusWithoutGatewayManager(t *testing.T) {
	s := &Server{}
	for _, path := range []string{"/api/aprs/status", "/api/aprs/heard", "/api/aprs/activity"} {
		w := httptest.NewRecorder()
		s.Router().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusOK {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
}
