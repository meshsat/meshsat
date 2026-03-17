// Package auth provides OAuth2/OIDC authentication for MeshSat Hub.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/rs/zerolog/log"
	"golang.org/x/oauth2"
)

// Provider wraps an OIDC provider and OAuth2 config for token validation and login flows.
type Provider struct {
	oidcProvider *oidc.Provider
	oauth2Config oauth2.Config
	verifier     *oidc.IDTokenVerifier
	issuerURL    string
	clientID     string

	// End-session endpoint for RP-Initiated Logout (discovered from OIDC metadata).
	endSessionURL string

	// CSRF state + PKCE verifier store (in-memory, short-lived)
	stateMu sync.Mutex
	states  map[string]*authState
}

// authState holds CSRF state and PKCE code verifier for a pending login.
type authState struct {
	CodeVerifier string
	ExpiresAt    time.Time
}

// ProviderConfig holds the OIDC provider configuration.
type ProviderConfig struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       string // space-separated, e.g. "openid profile email"
}

// UserInfo represents the authenticated user extracted from an OIDC token.
type UserInfo struct {
	Subject  string `json:"sub"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Username string `json:"preferred_username"`
	Picture  string `json:"picture,omitempty"`
}

// NewProvider creates a new OIDC provider by performing discovery on the issuer URL.
func NewProvider(ctx context.Context, cfg ProviderConfig) (*Provider, error) {
	if cfg.IssuerURL == "" {
		return nil, fmt.Errorf("OIDC issuer URL is required")
	}
	if cfg.ClientID == "" {
		return nil, fmt.Errorf("OIDC client ID is required")
	}

	oidcProv, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery on %s: %w", cfg.IssuerURL, err)
	}

	scopes := []string{oidc.ScopeOpenID}
	if cfg.Scopes != "" {
		for _, s := range strings.Fields(cfg.Scopes) {
			if s != oidc.ScopeOpenID {
				scopes = append(scopes, s)
			}
		}
	} else {
		scopes = append(scopes, "profile", "email")
	}

	oauth2Cfg := oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint:     oidcProv.Endpoint(),
		RedirectURL:  cfg.RedirectURL,
		Scopes:       scopes,
	}

	verifier := oidcProv.Verifier(&oidc.Config{
		ClientID: cfg.ClientID,
	})

	// Discover end_session_endpoint from OIDC provider metadata.
	var providerClaims struct {
		EndSessionURL string `json:"end_session_endpoint"`
	}
	_ = oidcProv.Claims(&providerClaims)

	p := &Provider{
		oidcProvider:  oidcProv,
		oauth2Config:  oauth2Cfg,
		verifier:      verifier,
		issuerURL:     cfg.IssuerURL,
		clientID:      cfg.ClientID,
		endSessionURL: providerClaims.EndSessionURL,
		states:        make(map[string]*authState),
	}

	// Start state cleanup goroutine
	go p.cleanupStates(ctx)

	log.Info().
		Str("issuer", cfg.IssuerURL).
		Str("client_id", cfg.ClientID).
		Str("end_session_endpoint", p.endSessionURL).
		Msg("OIDC provider initialized")
	return p, nil
}

// AuthCodeURL generates an authorization URL with a random CSRF state and PKCE challenge.
func (p *Provider) AuthCodeURL() (string, string, error) {
	state, err := randomState()
	if err != nil {
		return "", "", fmt.Errorf("generate state: %w", err)
	}

	verifier, err := generatePKCEVerifier()
	if err != nil {
		return "", "", fmt.Errorf("generate PKCE verifier: %w", err)
	}
	challenge := pkceChallenge(verifier)

	p.stateMu.Lock()
	p.states[state] = &authState{
		CodeVerifier: verifier,
		ExpiresAt:    time.Now().Add(10 * time.Minute),
	}
	p.stateMu.Unlock()

	url := p.oauth2Config.AuthCodeURL(state,
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
	return url, state, nil
}

// ValidateState checks and consumes a CSRF state token.
// Returns the PKCE code verifier and true if valid, or empty string and false if invalid/expired.
func (p *Provider) ValidateState(state string) (string, bool) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()

	as, ok := p.states[state]
	if !ok {
		return "", false
	}
	delete(p.states, state)
	if time.Now().After(as.ExpiresAt) {
		return "", false
	}
	return as.CodeVerifier, true
}

// Exchange trades an authorization code for tokens and returns the verified user info.
// The codeVerifier is the PKCE code_verifier that was generated alongside the code_challenge.
func (p *Provider) Exchange(ctx context.Context, code, codeVerifier string) (*UserInfo, *oauth2.Token, error) {
	opts := []oauth2.AuthCodeOption{}
	if codeVerifier != "" {
		opts = append(opts, oauth2.SetAuthURLParam("code_verifier", codeVerifier))
	}
	token, err := p.oauth2Config.Exchange(ctx, code, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("token exchange: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return nil, nil, fmt.Errorf("no id_token in token response")
	}

	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, nil, fmt.Errorf("verify id_token: %w", err)
	}

	var claims struct {
		Email             string `json:"email"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Picture           string `json:"picture"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, nil, fmt.Errorf("parse claims: %w", err)
	}

	user := &UserInfo{
		Subject:  idToken.Subject,
		Email:    claims.Email,
		Name:     claims.Name,
		Username: claims.PreferredUsername,
		Picture:  claims.Picture,
	}

	return user, token, nil
}

// VerifyIDToken verifies a raw ID token string and returns the user info.
func (p *Provider) VerifyIDToken(ctx context.Context, rawToken string) (*UserInfo, error) {
	idToken, err := p.verifier.Verify(ctx, rawToken)
	if err != nil {
		return nil, fmt.Errorf("verify token: %w", err)
	}

	var claims struct {
		Email             string `json:"email"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Picture           string `json:"picture"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("parse claims: %w", err)
	}

	return &UserInfo{
		Subject:  idToken.Subject,
		Email:    claims.Email,
		Name:     claims.Name,
		Username: claims.PreferredUsername,
		Picture:  claims.Picture,
	}, nil
}

// RefreshTokens uses an OAuth2 refresh token to obtain new tokens from the IdP.
// Returns updated user info and new OAuth2 token (with potentially rotated refresh_token).
func (p *Provider) RefreshTokens(ctx context.Context, refreshToken string) (*UserInfo, *oauth2.Token, error) {
	ts := p.oauth2Config.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	newToken, err := ts.Token()
	if err != nil {
		return nil, nil, fmt.Errorf("refresh token: %w", err)
	}

	// Verify the new ID token if present
	rawIDToken, ok := newToken.Extra("id_token").(string)
	if !ok {
		// Some providers don't return id_token on refresh — use userinfo endpoint instead
		user, uErr := p.fetchUserInfo(ctx, newToken.AccessToken)
		if uErr != nil {
			return nil, nil, fmt.Errorf("refresh: no id_token and userinfo failed: %w", uErr)
		}
		return user, newToken, nil
	}

	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, nil, fmt.Errorf("refresh: verify id_token: %w", err)
	}

	var claims struct {
		Email             string `json:"email"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Picture           string `json:"picture"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, nil, fmt.Errorf("refresh: parse claims: %w", err)
	}

	return &UserInfo{
		Subject:  idToken.Subject,
		Email:    claims.Email,
		Name:     claims.Name,
		Username: claims.PreferredUsername,
		Picture:  claims.Picture,
	}, newToken, nil
}

// fetchUserInfo calls the OIDC userinfo endpoint as fallback when id_token is not returned on refresh.
func (p *Provider) fetchUserInfo(ctx context.Context, accessToken string) (*UserInfo, error) {
	userInfoURL := p.oidcProvider.Endpoint().AuthURL
	// Use the standard OIDC userinfo endpoint
	var providerClaims struct {
		UserinfoEndpoint string `json:"userinfo_endpoint"`
	}
	if err := p.oidcProvider.Claims(&providerClaims); err == nil && providerClaims.UserinfoEndpoint != "" {
		userInfoURL = providerClaims.UserinfoEndpoint
	} else {
		return nil, fmt.Errorf("no userinfo_endpoint discovered")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("userinfo returned %d: %s", resp.StatusCode, body)
	}

	var user UserInfo
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("decode userinfo: %w", err)
	}
	return &user, nil
}

// EndSessionURL returns the IdP's end_session_endpoint for RP-Initiated Logout.
// Returns empty string if the endpoint was not discovered.
func (p *Provider) EndSessionURL() string {
	return p.endSessionURL
}

// ClientID returns the configured OAuth2 client ID (needed for post_logout_redirect_uri).
func (p *Provider) ClientID() string {
	return p.clientID
}

func (p *Provider) cleanupStates(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.stateMu.Lock()
			now := time.Now()
			for state, as := range p.states {
				if now.After(as.ExpiresAt) {
					delete(p.states, state)
				}
			}
			p.stateMu.Unlock()
		}
	}
}

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// generatePKCEVerifier creates a cryptographically random PKCE code verifier (43-128 chars, RFC 7636).
func generatePKCEVerifier() (string, error) {
	b := make([]byte, 32) // 32 bytes → 43 base64url chars
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// pkceChallenge computes the S256 PKCE code challenge from a verifier.
func pkceChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
