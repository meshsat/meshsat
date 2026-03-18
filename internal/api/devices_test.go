package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"meshsat/internal/database"
)

func newTestServerWithDB(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	db, err := database.New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &Server{db: db}
}

func TestDeviceCreate_ValidIMEI(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	body := `{"imei":"300234063904190","label":"Field Unit 1","type":"rockblock"}`
	req := httptest.NewRequest("POST", "/api/device-registry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "300234063904190") {
		t.Errorf("expected IMEI in response: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"status":"never_seen"`) {
		t.Errorf("expected never_seen status: %s", w.Body.String())
	}
}

func TestDeviceCreate_InvalidIMEI(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	body := `{"imei":"12345"}`
	req := httptest.NewRequest("POST", "/api/device-registry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid IMEI") {
		t.Errorf("expected IMEI error: %s", w.Body.String())
	}
}

func TestDeviceCreate_DuplicateIMEI(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	body := `{"imei":"300234063904190","label":"Unit 1"}`
	req1 := httptest.NewRequest("POST", "/api/device-registry", strings.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first create: expected 201, got %d", w1.Code)
	}

	req2 := httptest.NewRequest("POST", "/api/device-registry", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	if w2.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", w2.Code, w2.Body.String())
	}
}

func TestDeviceCreate_AutoLabel(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	body := `{"imei":"300234063904190"}`
	req := httptest.NewRequest("POST", "/api/device-registry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Device 4190") {
		t.Errorf("expected auto-generated label 'Device 4190': %s", w.Body.String())
	}
}

func TestDeviceCreate_MissingIMEI(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	body := `{"label":"No IMEI"}`
	req := httptest.NewRequest("POST", "/api/device-registry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceGet_NotFound(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	req := httptest.NewRequest("GET", "/api/device-registry/999", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceUpdate(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	// Create
	body := `{"imei":"300234063904190","label":"Original"}`
	req := httptest.NewRequest("POST", "/api/device-registry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", w.Code)
	}

	// Update
	update := `{"label":"Updated","type":"iridium","notes":"test notes"}`
	req2 := httptest.NewRequest("PUT", "/api/device-registry/1", strings.NewReader(update))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Errorf("update: expected 200, got %d: %s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), "Updated") {
		t.Errorf("expected updated label: %s", w2.Body.String())
	}
}

func TestDeviceUpdate_NotFound(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	body := `{"label":"Ghost"}`
	req := httptest.NewRequest("PUT", "/api/device-registry/999", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceDelete(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	// Create
	body := `{"imei":"300234063904190"}`
	req := httptest.NewRequest("POST", "/api/device-registry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Delete
	req2 := httptest.NewRequest("DELETE", "/api/device-registry/1", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	if w2.Code != http.StatusNoContent {
		t.Errorf("delete: expected 204, got %d: %s", w2.Code, w2.Body.String())
	}

	// Verify gone
	req3 := httptest.NewRequest("GET", "/api/device-registry/1", nil)
	w3 := httptest.NewRecorder()
	router.ServeHTTP(w3, req3)

	if w3.Code != http.StatusNotFound {
		t.Errorf("after delete: expected 404, got %d", w3.Code)
	}
}

func TestDeviceDelete_NotFound(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	req := httptest.NewRequest("DELETE", "/api/device-registry/999", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceList_Empty(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	req := httptest.NewRequest("GET", "/api/device-registry", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "[]") {
		t.Errorf("expected empty array: %s", w.Body.String())
	}
}

func TestDeviceOnMOCallback(t *testing.T) {
	s := newTestServerWithDB(t)

	// Create a device
	imei := "300234063904190"
	s.db.CreateDevice(imei, "Test", "", "")

	var callbackIMEI string
	s.SetOnMOCallback(func(i string) {
		callbackIMEI = i
	})

	router := s.Router()

	// Send RockBLOCK webhook
	form := "imei=" + imei + "&momsn=1&transmit_time=26-03-17+12:00:00&data=48656c6c6f"
	req := httptest.NewRequest("POST", "/api/webhook/rockblock", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("webhook: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if callbackIMEI != imei {
		t.Errorf("callback IMEI: got %q, want %q", callbackIMEI, imei)
	}
}
