package api

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func newTestServer() *Server {
	return &Server{}
}

func TestRockBLOCKWebhook_ValidPayload(t *testing.T) {
	s := newTestServer()
	router := s.Router()

	data := hex.EncodeToString([]byte("Hello from Iridium"))
	form := url.Values{
		"imei":              {"300234063904190"},
		"momsn":             {"42"},
		"transmit_time":     {"26-03-17 12:30:00"},
		"iridium_latitude":  {"52.1234"},
		"iridium_longitude": {"4.5678"},
		"iridium_cep":       {"10"},
		"data":              {data},
	}

	req := httptest.NewRequest("POST", "/api/webhook/rockblock", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "accepted") {
		t.Errorf("expected 'accepted' in body, got: %s", w.Body.String())
	}
}

func TestRockBLOCKWebhook_InvalidSecret(t *testing.T) {
	t.Setenv("MESHSAT_ROCKBLOCK_SECRET", "mysecret123")

	s := newTestServer()
	router := s.Router()

	form := url.Values{
		"imei":          {"300234063904190"},
		"momsn":         {"42"},
		"transmit_time": {"26-03-17 12:30:00"},
		"data":          {"48656c6c6f"},
		"secret":        {"wrongsecret"},
	}

	req := httptest.NewRequest("POST", "/api/webhook/rockblock", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRockBLOCKWebhook_ValidSecret(t *testing.T) {
	t.Setenv("MESHSAT_ROCKBLOCK_SECRET", "mysecret123")

	s := newTestServer()
	router := s.Router()

	form := url.Values{
		"imei":          {"300234063904190"},
		"momsn":         {"42"},
		"transmit_time": {"26-03-17 12:30:00"},
		"data":          {"48656c6c6f"},
		"secret":        {"mysecret123"},
	}

	req := httptest.NewRequest("POST", "/api/webhook/rockblock", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRockBLOCKWebhook_MissingFields(t *testing.T) {
	s := newTestServer()
	router := s.Router()

	form := url.Values{
		"imei": {"300234063904190"},
		// missing momsn and transmit_time
	}

	req := httptest.NewRequest("POST", "/api/webhook/rockblock", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRockBLOCKWebhook_InvalidHexData(t *testing.T) {
	s := newTestServer()
	router := s.Router()

	form := url.Values{
		"imei":          {"300234063904190"},
		"momsn":         {"42"},
		"transmit_time": {"26-03-17 12:30:00"},
		"data":          {"ZZZZ"},
	}

	req := httptest.NewRequest("POST", "/api/webhook/rockblock", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid hex data") {
		t.Errorf("expected hex error message, got: %s", w.Body.String())
	}
}

func TestRockBLOCKWebhook_EmptyData(t *testing.T) {
	s := newTestServer()
	router := s.Router()

	form := url.Values{
		"imei":          {"300234063904190"},
		"momsn":         {"42"},
		"transmit_time": {"26-03-17 12:30:00"},
		"data":          {""},
	}

	req := httptest.NewRequest("POST", "/api/webhook/rockblock", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRockBLOCKWebhook_SecretViaQueryParam(t *testing.T) {
	t.Setenv("MESHSAT_ROCKBLOCK_SECRET", "querysecret")

	s := newTestServer()
	router := s.Router()

	form := url.Values{
		"imei":          {"300234063904190"},
		"momsn":         {"42"},
		"transmit_time": {"26-03-17 12:30:00"},
		"data":          {"48656c6c6f"},
	}

	req := httptest.NewRequest("POST", "/api/webhook/rockblock?secret=querysecret", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestIsPrintable(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"Hello World", true},
		{"with\nnewline", true},
		{"with\ttab", true},
		{"\x00binary", false},
		{"\x01control", false},
		{"", false},
	}
	for _, tt := range tests {
		got := isPrintable(tt.input)
		if got != tt.want {
			t.Errorf("isPrintable(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
