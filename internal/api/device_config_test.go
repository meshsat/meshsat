package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeviceConfig_PutAndGet(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	// Create a device first
	body := `{"imei":"300234063904190","label":"Unit 1","type":"rockblock"}`
	req := httptest.NewRequest("POST", "/api/device-registry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create device: got %d: %s", w.Code, w.Body.String())
	}

	// PUT config
	cfg := `{"yaml":"radio:\n  frequency: 915.0\n","comment":"initial"}`
	req = httptest.NewRequest("PUT", "/api/device-registry/1/config", strings.NewReader(cfg))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("put config: got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"version":1`) {
		t.Errorf("expected version 1: %s", w.Body.String())
	}

	// GET current config
	req = httptest.NewRequest("GET", "/api/device-registry/1/config", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("get config: got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "915.0") {
		t.Errorf("expected yaml content: %s", w.Body.String())
	}
}

func TestDeviceConfig_NoConfig204(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	// Create device
	body := `{"imei":"300234063904190","label":"Unit 1"}`
	req := httptest.NewRequest("POST", "/api/device-registry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// GET config before any is saved
	req = httptest.NewRequest("GET", "/api/device-registry/1/config", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceConfig_InvalidYAML(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	body := `{"imei":"300234063904190","label":"Unit 1"}`
	req := httptest.NewRequest("POST", "/api/device-registry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// PUT invalid YAML
	cfg := `{"yaml":":\n  bad: [unclosed","comment":"bad"}`
	req = httptest.NewRequest("PUT", "/api/device-registry/1/config", strings.NewReader(cfg))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid YAML, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceConfig_VersionHistory(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	// Create device
	body := `{"imei":"300234063904190","label":"Unit 1"}`
	req := httptest.NewRequest("POST", "/api/device-registry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Save two versions
	for _, yml := range []string{"key: v1", "key: v2"} {
		cfg := `{"yaml":"` + yml + `","comment":"ver"}`
		req = httptest.NewRequest("PUT", "/api/device-registry/1/config", strings.NewReader(cfg))
		req.Header.Set("Content-Type", "application/json")
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}

	// List versions
	req = httptest.NewRequest("GET", "/api/device-registry/1/config/versions", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list versions: got %d: %s", w.Code, w.Body.String())
	}
	// Should contain both versions
	if !strings.Contains(w.Body.String(), `"version":1`) || !strings.Contains(w.Body.String(), `"version":2`) {
		t.Errorf("expected both versions: %s", w.Body.String())
	}

	// Get specific version
	req = httptest.NewRequest("GET", "/api/device-registry/1/config/versions/1", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("get v1: got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "key: v1") {
		t.Errorf("expected v1 content: %s", w.Body.String())
	}
}

func TestDeviceConfig_Rollback(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	// Create device
	body := `{"imei":"300234063904190","label":"Unit 1"}`
	req := httptest.NewRequest("POST", "/api/device-registry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Save two versions
	for _, yml := range []string{"key: original", "key: changed"} {
		cfg := `{"yaml":"` + yml + `","comment":"save"}`
		req = httptest.NewRequest("PUT", "/api/device-registry/1/config", strings.NewReader(cfg))
		req.Header.Set("Content-Type", "application/json")
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}

	// Rollback to v1 — creates v3
	req = httptest.NewRequest("POST", "/api/device-registry/1/config/rollback/1", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("rollback: got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"version":3`) {
		t.Errorf("expected v3 after rollback: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "key: original") {
		t.Errorf("expected original content after rollback: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "rollback to v1") {
		t.Errorf("expected rollback comment: %s", w.Body.String())
	}
}

func TestDeviceConfig_DeviceNotFound(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	req := httptest.NewRequest("GET", "/api/device-registry/999/config", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestDeviceConfig_EmptyYAML(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	body := `{"imei":"300234063904190","label":"Unit 1"}`
	req := httptest.NewRequest("POST", "/api/device-registry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	cfg := `{"yaml":"","comment":"empty"}`
	req = httptest.NewRequest("PUT", "/api/device-registry/1/config", strings.NewReader(cfg))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty yaml, got %d: %s", w.Code, w.Body.String())
	}
}
