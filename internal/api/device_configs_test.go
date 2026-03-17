package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func createTestDevice(t *testing.T, router http.Handler) {
	t.Helper()
	body := `{"imei":"300234063904190","label":"Test Unit","type":"rockblock"}`
	req := httptest.NewRequest("POST", "/api/device-registry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create device: expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceConfigPut_Valid(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()
	createTestDevice(t, router)

	body := `{"yaml":"key: value\nother: 42","comment":"initial config"}`
	req := httptest.NewRequest("PUT", "/api/device-registry/1/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"version":1`) {
		t.Errorf("expected version 1: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"comment":"initial config"`) {
		t.Errorf("expected comment in response: %s", w.Body.String())
	}
}

func TestDeviceConfigPut_InvalidYAML(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()
	createTestDevice(t, router)

	body := `{"yaml":"key: [unclosed","comment":"bad"}`
	req := httptest.NewRequest("PUT", "/api/device-registry/1/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid YAML") {
		t.Errorf("expected YAML error: %s", w.Body.String())
	}
}

func TestDeviceConfigPut_DeviceNotFound(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	body := `{"yaml":"key: value"}`
	req := httptest.NewRequest("PUT", "/api/device-registry/999/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceConfigGet_Latest(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()
	createTestDevice(t, router)

	// Create two versions
	for _, yml := range []string{"key: v1", "key: v2"} {
		body := `{"yaml":"` + yml + `"}`
		req := httptest.NewRequest("PUT", "/api/device-registry/1/config", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("put config: expected 201, got %d", w.Code)
		}
	}

	// Get latest
	req := httptest.NewRequest("GET", "/api/device-registry/1/config", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"version":2`) {
		t.Errorf("expected version 2: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "key: v2") {
		t.Errorf("expected v2 yaml: %s", w.Body.String())
	}
}

func TestDeviceConfigGet_NoConfig(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()
	createTestDevice(t, router)

	req := httptest.NewRequest("GET", "/api/device-registry/1/config", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceConfigVersions_List(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()
	createTestDevice(t, router)

	// Create 2 versions
	for _, yml := range []string{"key: v1", "key: v2"} {
		body := `{"yaml":"` + yml + `"}`
		req := httptest.NewRequest("PUT", "/api/device-registry/1/config", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}

	req := httptest.NewRequest("GET", "/api/device-registry/1/config/versions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"version":1`) {
		t.Errorf("expected version 1: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"version":2`) {
		t.Errorf("expected version 2: %s", w.Body.String())
	}
}

func TestDeviceConfigVersion_Specific(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()
	createTestDevice(t, router)

	// Create 2 versions
	for _, yml := range []string{"key: v1", "key: v2"} {
		body := `{"yaml":"` + yml + `"}`
		req := httptest.NewRequest("PUT", "/api/device-registry/1/config", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}

	// Get version 1 specifically
	req := httptest.NewRequest("GET", "/api/device-registry/1/config/versions/1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "key: v1") {
		t.Errorf("expected v1 yaml: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"version":1`) {
		t.Errorf("expected version 1: %s", w.Body.String())
	}
}

func TestDeviceConfigRollback(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()
	createTestDevice(t, router)

	// Create v1 and v2
	for _, yml := range []string{"key: original", "key: changed"} {
		body := `{"yaml":"` + yml + `"}`
		req := httptest.NewRequest("PUT", "/api/device-registry/1/config", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}

	// Rollback to v1
	req := httptest.NewRequest("POST", "/api/device-registry/1/config/rollback/1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	// Should be version 3
	if !strings.Contains(w.Body.String(), `"version":3`) {
		t.Errorf("expected version 3: %s", w.Body.String())
	}
	// Should have v1's YAML
	if !strings.Contains(w.Body.String(), "key: original") {
		t.Errorf("expected original yaml: %s", w.Body.String())
	}
	// Should have rollback comment
	if !strings.Contains(w.Body.String(), "rollback to v1") {
		t.Errorf("expected rollback comment: %s", w.Body.String())
	}
}
