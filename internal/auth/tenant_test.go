package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTenantMiddleware_FromUserClaim(t *testing.T) {
	user := &UserInfo{Subject: "sub-1", TenantID: "tenant-from-claim"}
	provider := &Provider{states: make(map[string]*authState)}
	store := NewSessionStore()
	sessionID, _ := store.Create(user, nil, "")

	var gotTenantID string
	handler := RequireAuth(provider, store)(
		TenantMiddleware("", false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotTenantID = TenantIDFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		})),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionID})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if gotTenantID != "tenant-from-claim" {
		t.Errorf("expected tenant-from-claim, got %q", gotTenantID)
	}
}

func TestTenantMiddleware_FromHeader(t *testing.T) {
	// No user in context → falls back to header
	mw := TenantMiddleware("", false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tc := TenantFromContext(r.Context())
		if tc == nil {
			t.Fatal("expected tenant context")
		}
		if tc.ID != "header-tenant" {
			t.Errorf("expected header-tenant, got %q", tc.ID)
		}
		if tc.Source != "header" {
			t.Errorf("expected source header, got %q", tc.Source)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set(TenantHeaderName, "header-tenant")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestTenantMiddleware_DefaultFallback(t *testing.T) {
	mw := TenantMiddleware("", false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tid := TenantIDFromContext(r.Context())
		if tid != DefaultTenantID {
			t.Errorf("expected %q, got %q", DefaultTenantID, tid)
		}
		tc := TenantFromContext(r.Context())
		if tc.Source != "default" {
			t.Errorf("expected source default, got %q", tc.Source)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestTenantMiddleware_EnforceMode_Rejects(t *testing.T) {
	mw := TenantMiddleware("", true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be reached when enforcement rejects")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
}

func TestTenantMiddleware_EnforceMode_AllowsWithHeader(t *testing.T) {
	mw := TenantMiddleware("", true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set(TenantHeaderName, "valid-tenant")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestTenantIDFromContext_NoMiddleware(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	tid := TenantIDFromContext(req.Context())
	if tid != DefaultTenantID {
		t.Errorf("expected %q without middleware, got %q", DefaultTenantID, tid)
	}
}

func TestSanitizeTenantID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"with-dash_and.dot", "with-dash_and.dot"},
		{"has spaces!", "hasspaces"},
		{"<script>alert(1)</script>", "scriptalert1script"},
		{"", ""},
		{" padded ", "padded"},
	}
	for _, tt := range tests {
		got := sanitizeTenantID(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeTenantID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSanitizeTenantID_MaxLength(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "a"
	}
	got := sanitizeTenantID(long)
	if len(got) > 128 {
		t.Errorf("expected max 128 chars, got %d", len(got))
	}
}
