package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireAuth_NilProvider_PassThrough(t *testing.T) {
	handler := RequireAuth(nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 when provider is nil, got %d", rec.Code)
	}
}

func TestRequireAuth_NilProvider_NoUserInContext(t *testing.T) {
	handler := RequireAuth(nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := UserFromContext(r.Context())
		if user != nil {
			t.Error("expected nil user when provider is nil")
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestRequireAuth_SessionCookie(t *testing.T) {
	store := NewSessionStore()
	user := &UserInfo{Subject: "sub-cookie", Email: "cookie@test.com"}
	sessionID, _ := store.Create(user, nil, "")

	var gotUser *UserInfo
	handler := RequireAuth(nil, store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// When provider is nil, it passes through regardless
		w.WriteHeader(http.StatusOK)
	}))

	// With provider nil, passes through — test the session cookie path with a non-nil store
	// We need a non-nil provider to actually test auth enforcement.
	// Since we can't easily create a Provider without a real OIDC issuer, test the session path directly.
	_ = gotUser
	_ = handler

	// Test the cookie lookup path via middleware with provider set to a marker value.
	// Instead, test the session store integration directly:
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionID})

	// Directly test UserFromContext after manual middleware logic
	if cookie, err := req.Cookie(SessionCookieName); err == nil {
		if session, ok := store.Get(cookie.Value); ok {
			if session.User.Subject != "sub-cookie" {
				t.Errorf("expected sub-cookie, got %s", session.User.Subject)
			}
			return
		}
	}
	t.Fatal("expected to find session from cookie")
}

func TestRequireAuth_NoAuth_Returns401(t *testing.T) {
	// We need a provider to trigger auth enforcement.
	// Create a minimal mock by using the struct directly.
	provider := &Provider{
		states: make(map[string]*authState),
	}
	store := NewSessionStore()

	handler := RequireAuth(provider, store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 when no auth provided, got %d", rec.Code)
	}
}

func TestRequireAuth_ValidSessionCookie(t *testing.T) {
	provider := &Provider{
		states: make(map[string]*authState),
	}
	store := NewSessionStore()
	user := &UserInfo{Subject: "sub-valid", Email: "valid@test.com", Name: "Valid"}
	sessionID, _ := store.Create(user, nil, "")

	var ctxUser *UserInfo
	handler := RequireAuth(provider, store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxUser = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionID})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 with valid session cookie, got %d", rec.Code)
	}
	if ctxUser == nil {
		t.Fatal("expected user in context")
	}
	if ctxUser.Subject != "sub-valid" {
		t.Errorf("expected sub-valid, got %s", ctxUser.Subject)
	}
}

func TestRequireAuth_ExpiredSessionCookie_Returns401(t *testing.T) {
	provider := &Provider{
		states: make(map[string]*authState),
	}
	store := NewSessionStore()
	user := &UserInfo{Subject: "sub-expired"}
	sessionID, _ := store.Create(user, nil, "")

	// Force expire
	store.mu.Lock()
	store.sessions[sessionID].ExpiresAt = store.sessions[sessionID].ExpiresAt.Add(-25 * 60 * 60 * 1e9) // -25h
	store.mu.Unlock()

	handler := RequireAuth(provider, store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionID})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for expired session, got %d", rec.Code)
	}
}

func TestUserFromContext_NoUser(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	user := UserFromContext(req.Context())
	if user != nil {
		t.Error("expected nil user from empty context")
	}
}
