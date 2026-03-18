package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGenerateAPIKey(t *testing.T) {
	plaintext, hash, prefix, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	if !strings.HasPrefix(plaintext, APIKeyPrefix) {
		t.Errorf("plaintext should start with %q, got %q", APIKeyPrefix, plaintext[:20])
	}
	if len(plaintext) != len(APIKeyPrefix)+APIKeyBytes*2 {
		t.Errorf("expected key length %d, got %d", len(APIKeyPrefix)+APIKeyBytes*2, len(plaintext))
	}
	if hash == "" {
		t.Error("expected non-empty hash")
	}
	if prefix != plaintext[:len(APIKeyPrefix)+8] {
		t.Errorf("prefix mismatch: got %q, want %q", prefix, plaintext[:len(APIKeyPrefix)+8])
	}
}

func TestHashAPIKey_Deterministic(t *testing.T) {
	h1 := HashAPIKey("meshsat_abc123")
	h2 := HashAPIKey("meshsat_abc123")
	if h1 != h2 {
		t.Error("same input should produce same hash")
	}
}

func TestHashAPIKey_Different(t *testing.T) {
	h1 := HashAPIKey("meshsat_key1")
	h2 := HashAPIKey("meshsat_key2")
	if h1 == h2 {
		t.Error("different inputs should produce different hashes")
	}
}

func TestIsAPIKey(t *testing.T) {
	if !IsAPIKey("meshsat_abc123def456") {
		t.Error("should recognize meshsat_ prefix")
	}
	if IsAPIKey("eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9") {
		t.Error("should not recognize JWT as API key")
	}
	if IsAPIKey("") {
		t.Error("should not recognize empty string")
	}
}

func TestGenerateAPIKey_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		pt, _, _, err := GenerateAPIKey()
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if seen[pt] {
			t.Fatalf("duplicate key generated")
		}
		seen[pt] = true
	}
}

func TestRoleAtLeast(t *testing.T) {
	tests := []struct {
		role, min Role
		want      bool
	}{
		{RoleOwner, RoleOwner, true},
		{RoleOwner, RoleOperator, true},
		{RoleOwner, RoleViewer, true},
		{RoleOperator, RoleOperator, true},
		{RoleOperator, RoleViewer, true},
		{RoleOperator, RoleOwner, false},
		{RoleViewer, RoleViewer, true},
		{RoleViewer, RoleOperator, false},
		{RoleViewer, RoleOwner, false},
	}
	for _, tt := range tests {
		got := RoleAtLeast(tt.role, tt.min)
		if got != tt.want {
			t.Errorf("RoleAtLeast(%s, %s) = %v, want %v", tt.role, tt.min, got, tt.want)
		}
	}
}

func TestValidRole(t *testing.T) {
	if !ValidRole("viewer") {
		t.Error("viewer should be valid")
	}
	if !ValidRole("operator") {
		t.Error("operator should be valid")
	}
	if !ValidRole("owner") {
		t.Error("owner should be valid")
	}
	if ValidRole("admin") {
		t.Error("admin should not be valid")
	}
	if ValidRole("") {
		t.Error("empty should not be valid")
	}
}

func TestRequireRole_Allows(t *testing.T) {
	handler := RequireRole(RoleOperator)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/test", nil)
	ctx := WithRole(req.Context(), RoleOwner)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for owner accessing operator endpoint, got %d", rec.Code)
	}
}

func TestRequireRole_Denies(t *testing.T) {
	handler := RequireRole(RoleOwner)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/test", nil)
	ctx := WithRole(req.Context(), RoleViewer)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for viewer accessing owner endpoint, got %d", rec.Code)
	}
}

func TestRequireRole_DefaultIsOwner(t *testing.T) {
	handler := RequireRole(RoleOwner)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// No role in context → defaults to owner (auth disabled)
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 when no role set (default owner), got %d", rec.Code)
	}
}

func TestRequireAuth_APIKey(t *testing.T) {
	provider := &Provider{states: make(map[string]*authState)}
	store := NewSessionStore()

	// Mock API key validator
	testKey := "meshsat_testkey1234567890abcdef1234567890abcdef1234567890abcdef12"
	testHash := HashAPIKey(testKey)
	validator := APIKeyValidator(func(keyHash string) (*APIKeyResult, error) {
		if keyHash == testHash {
			return &APIKeyResult{
				ID:       1,
				TenantID: "test-tenant",
				Role:     RoleOperator,
				Label:    "test-key",
			}, nil
		}
		return nil, fmt.Errorf("key not found")
	})

	var gotUser *UserInfo
	var gotRole Role
	handler := RequireAuth(provider, store, validator)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = UserFromContext(r.Context())
		gotRole = RoleFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid API key, got %d", rec.Code)
	}
	if gotUser == nil {
		t.Fatal("expected user in context")
	}
	if gotUser.TenantID != "test-tenant" {
		t.Errorf("expected tenant test-tenant, got %s", gotUser.TenantID)
	}
	if gotRole != RoleOperator {
		t.Errorf("expected role operator, got %s", gotRole)
	}
}
