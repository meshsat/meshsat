package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIRateLimiter_AllowsWithinLimit(t *testing.T) {
	handler := apiRateLimiter(5)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 5; i++ {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/status", nil)
		req.RemoteAddr = "192.168.1.10:12345"
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: got %d, want 200", i+1, rr.Code)
		}
	}
}

func TestAPIRateLimiter_BlocksOverLimit(t *testing.T) {
	handler := apiRateLimiter(3)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Exhaust the limit.
	for i := 0; i < 3; i++ {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/messages", nil)
		req.RemoteAddr = "10.0.0.1:9999"
		handler.ServeHTTP(rr, req)
	}

	// 4th request should be rejected.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/messages", nil)
	req.RemoteAddr = "10.0.0.1:9999"
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("got %d, want 429", rr.Code)
	}
	if rr.Header().Get("Retry-After") != "60" {
		t.Fatalf("Retry-After = %q, want 60", rr.Header().Get("Retry-After"))
	}
	var body map[string]string
	json.NewDecoder(rr.Body).Decode(&body)
	if body["error"] != "rate limit exceeded" {
		t.Fatalf("error = %q, want 'rate limit exceeded'", body["error"])
	}
}

func TestAPIRateLimiter_ExemptsHealth(t *testing.T) {
	handler := apiRateLimiter(1)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Exhaust the limit on a regular endpoint.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/status", nil)
	req.RemoteAddr = "10.0.0.2:8080"
	handler.ServeHTTP(rr, req)

	// /health must still pass even though the IP is exhausted.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/health", nil)
	req.RemoteAddr = "10.0.0.2:8080"
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("/health got %d, want 200", rr.Code)
	}
}

func TestAPIRateLimiter_ExemptsMetrics(t *testing.T) {
	handler := apiRateLimiter(1)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Exhaust the limit.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/nodes", nil)
	req.RemoteAddr = "10.0.0.3:8080"
	handler.ServeHTTP(rr, req)

	// /metrics must still pass.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/metrics", nil)
	req.RemoteAddr = "10.0.0.3:8080"
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("/metrics got %d, want 200", rr.Code)
	}
}

func TestAPIRateLimiter_IsolatesIPs(t *testing.T) {
	handler := apiRateLimiter(2)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Exhaust limit for IP A.
	for i := 0; i < 2; i++ {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/status", nil)
		req.RemoteAddr = "10.0.0.10:1234"
		handler.ServeHTTP(rr, req)
	}

	// IP A is now blocked.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/status", nil)
	req.RemoteAddr = "10.0.0.10:1234"
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("IP A: got %d, want 429", rr.Code)
	}

	// IP B should still be allowed.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/status", nil)
	req.RemoteAddr = "10.0.0.20:5678"
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("IP B: got %d, want 200", rr.Code)
	}
}
