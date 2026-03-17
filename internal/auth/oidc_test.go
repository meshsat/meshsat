package auth

import (
	"testing"
	"time"
)

func TestPKCEVerifier_Length(t *testing.T) {
	verifier, err := generatePKCEVerifier()
	if err != nil {
		t.Fatalf("generatePKCEVerifier: %v", err)
	}
	// 32 bytes → 43 base64url chars (no padding)
	if len(verifier) != 43 {
		t.Errorf("expected verifier length 43, got %d", len(verifier))
	}
}

func TestPKCEVerifier_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		v, err := generatePKCEVerifier()
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if seen[v] {
			t.Fatalf("duplicate verifier: %s", v)
		}
		seen[v] = true
	}
}

func TestPKCEChallenge_Deterministic(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	c1 := pkceChallenge(verifier)
	c2 := pkceChallenge(verifier)
	if c1 != c2 {
		t.Errorf("expected deterministic challenge, got %s and %s", c1, c2)
	}
	if c1 == "" {
		t.Error("expected non-empty challenge")
	}
	// Challenge should be different from verifier
	if c1 == verifier {
		t.Error("challenge should differ from verifier")
	}
}

func TestPKCEChallenge_DifferentVerifiers(t *testing.T) {
	v1, _ := generatePKCEVerifier()
	v2, _ := generatePKCEVerifier()
	c1 := pkceChallenge(v1)
	c2 := pkceChallenge(v2)
	if c1 == c2 {
		t.Error("different verifiers should produce different challenges")
	}
}

func TestAuthState_ValidateState_Valid(t *testing.T) {
	p := &Provider{
		states: make(map[string]*authState),
	}

	// Insert a state manually
	p.states["test-state"] = &authState{
		CodeVerifier: "test-verifier",
		ExpiresAt:    time.Now().Add(10 * time.Minute),
	}

	verifier, ok := p.ValidateState("test-state")
	if !ok {
		t.Fatal("expected ValidateState to return true")
	}
	if verifier != "test-verifier" {
		t.Errorf("expected verifier test-verifier, got %s", verifier)
	}

	// Should be consumed (single-use)
	_, ok = p.ValidateState("test-state")
	if ok {
		t.Fatal("expected second ValidateState to return false (consumed)")
	}
}

func TestAuthState_ValidateState_Expired(t *testing.T) {
	p := &Provider{
		states: make(map[string]*authState),
	}

	p.states["expired-state"] = &authState{
		CodeVerifier: "verifier",
		ExpiresAt:    time.Now().Add(-1 * time.Second),
	}

	_, ok := p.ValidateState("expired-state")
	if ok {
		t.Fatal("expected expired state to return false")
	}
}

func TestAuthState_ValidateState_NonExistent(t *testing.T) {
	p := &Provider{
		states: make(map[string]*authState),
	}

	_, ok := p.ValidateState("non-existent")
	if ok {
		t.Fatal("expected non-existent state to return false")
	}
}

func TestProvider_EndSessionURL(t *testing.T) {
	p := &Provider{
		endSessionURL: "https://idp.example.com/logout",
	}
	if got := p.EndSessionURL(); got != "https://idp.example.com/logout" {
		t.Errorf("expected end session URL, got %s", got)
	}

	p2 := &Provider{}
	if got := p2.EndSessionURL(); got != "" {
		t.Errorf("expected empty end session URL, got %s", got)
	}
}

func TestProvider_ClientID(t *testing.T) {
	p := &Provider{clientID: "my-client"}
	if got := p.ClientID(); got != "my-client" {
		t.Errorf("expected my-client, got %s", got)
	}
}

func TestRandomState_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		s, err := randomState()
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if seen[s] {
			t.Fatalf("duplicate state: %s", s)
		}
		seen[s] = true
	}
}

func TestRandomState_Length(t *testing.T) {
	s, _ := randomState()
	// 16 bytes → 32 hex chars
	if len(s) != 32 {
		t.Errorf("expected state length 32, got %d", len(s))
	}
}
