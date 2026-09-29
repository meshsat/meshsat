package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func hubCall(t *testing.T, s *Server, method, body string) (int, map[string]interface{}) {
	t.Helper()
	rec := httptest.NewRecorder()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, "/api/routing/hub", nil)
	} else {
		req = httptest.NewRequest(method, "/api/routing/hub", strings.NewReader(body))
	}
	if method == http.MethodGet {
		s.handleGetHubConfig(rec, req)
	} else {
		s.handleSetHubConfig(rec, req)
	}
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// "Use the Hub" is on for settings saved before the switch existed, and a PUT
// that switches it off keeps everything else. [MESHSAT-1417]
func TestHubSettings_SwitchDefaultsOnAndRoundTrips(t *testing.T) {
	s := newTestServerWithDB(t)
	if code, got := hubCall(t, s, http.MethodPut, `{"url":"wss://mqtt-hub.example/mqtt","bridge_id":"kit-a","username":"kit-a","password":"p"}`); code != 200 || got["enabled"] != true {
		t.Fatalf("first save: %d %v", code, got)
	}
	if _, got := hubCall(t, s, http.MethodGet, ""); got["enabled"] != true || got["state"] != "" || got["url"] != "wss://mqtt-hub.example/mqtt" {
		t.Fatalf("GET after a save without the switch: %v", got)
	}
	if code, _ := hubCall(t, s, http.MethodPut, `{"enabled":false}`); code != 200 {
		t.Fatalf("switch off: %d", code)
	}
	_, got := hubCall(t, s, http.MethodGet, "")
	if got["enabled"] != false || got["url"] != "wss://mqtt-hub.example/mqtt" || got["has_password"] != true {
		t.Fatalf("GET after switching off: %v", got)
	}
}

// Callsign, health interval and the Hub's API URL are kept and shown; an empty
// callsign clears it, 0 is the default interval, and an interval out of range
// is refused. [MESHSAT-1417]
func TestHubSettings_CallsignIntervalAndAPIURL(t *testing.T) {
	s := newTestServerWithDB(t)
	if code, _ := hubCall(t, s, http.MethodPut, `{"url":"wss://mqtt-hub.example/mqtt","callsign":" Team Alpha ","health_interval":45,"api_url":"https://hub.example"}`); code != 200 {
		t.Fatalf("save: %d", code)
	}
	_, got := hubCall(t, s, http.MethodGet, "")
	if got["callsign"] != "Team Alpha" || got["health_interval"] != float64(45) || got["api_url"] != "https://hub.example" {
		t.Fatalf("GET: %v", got)
	}
	if code, _ := hubCall(t, s, http.MethodPut, `{"callsign":"","health_interval":0}`); code != 200 {
		t.Fatalf("clear: %d", code)
	}
	_, got = hubCall(t, s, http.MethodGet, "")
	if _, ok := got["callsign"]; ok {
		t.Fatalf("callsign still there: %v", got)
	}
	if _, ok := got["health_interval"]; ok {
		t.Fatalf("health interval still there: %v", got)
	}
	if got["api_url"] != "https://hub.example" {
		t.Fatalf("a PUT without api_url lost it: %v", got)
	}
	for _, bad := range []string{`{"health_interval":-1}`, `{"health_interval":10000}`} {
		if code, _ := hubCall(t, s, http.MethodPut, bad); code != http.StatusBadRequest {
			t.Fatalf("%s answered %d", bad, code)
		}
	}
}

// A PUT that leaves the CA and the insecure flag out keeps them; an empty CA
// sent clears it. Before MESHSAT-1417 every PUT without them cleared both, so
// a form that sent only what it showed lost the Hub's CA.
func TestHubSettings_CAKeptUnlessSent(t *testing.T) {
	s := newTestServerWithDB(t)
	hubCall(t, s, http.MethodPut, `{"url":"wss://mqtt-hub.example/mqtt","tls_ca_pem":"CA","tls_insecure":true}`)
	hubCall(t, s, http.MethodPut, `{"username":"kit-a"}`)
	if cfg := s.loadHubConfig(); cfg.TLSCAPEM != "CA" || !cfg.TLSInsecure || cfg.Username != "kit-a" {
		t.Fatalf("after a PUT without them: %+v", cfg)
	}
	hubCall(t, s, http.MethodPut, `{"tls_ca_pem":"","tls_insecure":false}`)
	if cfg := s.loadHubConfig(); cfg.TLSCAPEM != "" || cfg.TLSInsecure {
		t.Fatalf("after clearing: %+v", cfg)
	}
}

// The answer to a PUT carries no secret: no password, no key, no PEM.
func TestHubSettings_PutAnswerHasNoSecrets(t *testing.T) {
	s := newTestServerWithDB(t)
	code, got := hubCall(t, s, http.MethodPut, `{"url":"wss://h/mqtt","password":"secret","tls_cert_pem":"CERT","tls_key_pem":"KEY","tls_ca_pem":"CA"}`)
	if code != 200 {
		t.Fatalf("save: %d", code)
	}
	for _, field := range []string{"password", "tls_cert_pem", "tls_key_pem", "tls_ca_pem"} {
		if _, ok := got[field]; ok {
			t.Fatalf("the answer carries %s: %v", field, got)
		}
	}
	if got["has_cert"] != true || got["has_password"] != true {
		t.Fatalf("the answer lost the flags: %v", got)
	}
}

// Without a Hub session the test says so, and nothing is sent.
func TestHubPing_NoReporterIsNotConnected(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.handleHubPing(rec, httptest.NewRequest(http.MethodPost, "/api/routing/hub/ping", nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not connected") {
		t.Fatalf("ping without a reporter: %d %s", rec.Code, rec.Body.String())
	}
}
