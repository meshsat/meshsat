package auth

import (
	"testing"
	"time"
)

func TestSessionStore_CreateAndGet(t *testing.T) {
	store := NewSessionStore()
	user := &UserInfo{Subject: "sub-1", Email: "test@example.com", Name: "Test User"}

	id, err := store.Create(user, nil, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty session ID")
	}

	session, ok := store.Get(id)
	if !ok {
		t.Fatal("expected session to exist")
	}
	if session.User.Subject != "sub-1" {
		t.Errorf("expected subject sub-1, got %s", session.User.Subject)
	}
	if session.User.Email != "test@example.com" {
		t.Errorf("expected email test@example.com, got %s", session.User.Email)
	}
}

func TestSessionStore_GetNonExistent(t *testing.T) {
	store := NewSessionStore()
	_, ok := store.Get("does-not-exist")
	if ok {
		t.Fatal("expected Get to return false for non-existent session")
	}
}

func TestSessionStore_Delete(t *testing.T) {
	store := NewSessionStore()
	user := &UserInfo{Subject: "sub-del"}
	id, _ := store.Create(user, nil, "")

	store.Delete(id)

	_, ok := store.Get(id)
	if ok {
		t.Fatal("expected session to be deleted")
	}
}

func TestSessionStore_Expiry(t *testing.T) {
	store := NewSessionStore()
	user := &UserInfo{Subject: "sub-exp"}
	id, _ := store.Create(user, nil, "")

	// Force expire the session
	store.mu.Lock()
	store.sessions[id].ExpiresAt = time.Now().Add(-1 * time.Second)
	store.mu.Unlock()

	_, ok := store.Get(id)
	if ok {
		t.Fatal("expected expired session to return false")
	}

	// Should also be cleaned up from store
	store.mu.RLock()
	_, exists := store.sessions[id]
	store.mu.RUnlock()
	if exists {
		t.Fatal("expected expired session to be removed from store on Get")
	}
}

func TestSessionStore_Cleanup(t *testing.T) {
	store := NewSessionStore()
	user := &UserInfo{Subject: "sub-cleanup"}

	id1, _ := store.Create(user, nil, "")
	id2, _ := store.Create(user, nil, "")

	// Expire only id1
	store.mu.Lock()
	store.sessions[id1].ExpiresAt = time.Now().Add(-1 * time.Second)
	store.mu.Unlock()

	store.Cleanup()

	store.mu.RLock()
	_, has1 := store.sessions[id1]
	_, has2 := store.sessions[id2]
	store.mu.RUnlock()

	if has1 {
		t.Error("expected expired session id1 to be cleaned up")
	}
	if !has2 {
		t.Error("expected valid session id2 to survive cleanup")
	}
}

func TestSessionStore_TokenStorage(t *testing.T) {
	store := NewSessionStore()
	user := &UserInfo{Subject: "sub-tok"}

	// golang.org/x/oauth2 Token is imported via session.go
	// We pass nil token and raw ID token
	id, _ := store.Create(user, nil, "raw-id-token-value")

	session, ok := store.Get(id)
	if !ok {
		t.Fatal("expected session to exist")
	}
	if session.IDToken != "raw-id-token-value" {
		t.Errorf("expected IDToken raw-id-token-value, got %s", session.IDToken)
	}
	if session.AccessToken != "" {
		t.Errorf("expected empty AccessToken when oauth2.Token is nil, got %s", session.AccessToken)
	}
}

func TestSessionStore_UniqueIDs(t *testing.T) {
	store := NewSessionStore()
	user := &UserInfo{Subject: "sub-uniq"}
	ids := make(map[string]bool)

	for i := 0; i < 100; i++ {
		id, err := store.Create(user, nil, "")
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		if ids[id] {
			t.Fatalf("duplicate session ID generated: %s", id)
		}
		ids[id] = true
	}
}
