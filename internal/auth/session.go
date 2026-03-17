package auth

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const (
	// SessionCookieName is the HTTP cookie name for session tokens.
	SessionCookieName = "meshsat_session"
	// SessionMaxAge is the default session lifetime.
	SessionMaxAge = 24 * time.Hour
)

// Session holds an authenticated user session.
type Session struct {
	ID        string
	User      UserInfo
	TenantID  string // resolved tenant for this session
	CreatedAt time.Time
	ExpiresAt time.Time

	// OAuth2 tokens — stored for refresh and RP-Initiated Logout.
	AccessToken  string
	RefreshToken string
	IDToken      string    // raw id_token for id_token_hint on logout
	TokenExpiry  time.Time // access token expiry
}

// SessionStore is a thread-safe in-memory session store.
// For single-node MeshSat Hub deployments this is sufficient.
// Cluster mode would need Redis-backed sessions (future).
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewSessionStore creates a new in-memory session store.
func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions: make(map[string]*Session),
	}
}

// Create generates a new session for the given user and returns the session ID.
// The oauth2Token and rawIDToken are stored for refresh and RP-Initiated Logout.
func (s *SessionStore) Create(user *UserInfo, oauth2Token *oauth2.Token, rawIDToken string) (string, error) {
	id, err := generateSessionID()
	if err != nil {
		return "", err
	}

	now := time.Now()
	session := &Session{
		ID:        id,
		User:      *user,
		TenantID:  user.TenantID,
		CreatedAt: now,
		ExpiresAt: now.Add(SessionMaxAge),
		IDToken:   rawIDToken,
	}

	if oauth2Token != nil {
		session.AccessToken = oauth2Token.AccessToken
		session.RefreshToken = oauth2Token.RefreshToken
		session.TokenExpiry = oauth2Token.Expiry
	}

	s.mu.Lock()
	s.sessions[id] = session
	s.mu.Unlock()

	return id, nil
}

// Get retrieves a session by ID. Returns the session and true if found and not expired.
func (s *SessionStore) Get(id string) (*Session, bool) {
	s.mu.RLock()
	session, ok := s.sessions[id]
	s.mu.RUnlock()

	if !ok {
		return nil, false
	}
	if time.Now().After(session.ExpiresAt) {
		s.Delete(id)
		return nil, false
	}
	return session, true
}

// Delete removes a session by ID.
func (s *SessionStore) Delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// Cleanup removes all expired sessions. Called periodically.
func (s *SessionStore) Cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, session := range s.sessions {
		if now.After(session.ExpiresAt) {
			delete(s.sessions, id)
		}
	}
}

// StartCleanup runs periodic cleanup of expired sessions.
func (s *SessionStore) StartCleanup(done <-chan struct{}) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			s.Cleanup()
		}
	}
}

func generateSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
