package auth

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/rs/zerolog/log"
)

// Handlers provides HTTP handlers for OAuth2/OIDC authentication flows.
type Handlers struct {
	provider     *Provider
	sessions     *SessionStore
	cookieDomain string
	cookieSecure bool
}

// NewHandlers creates auth HTTP handlers.
func NewHandlers(provider *Provider, sessions *SessionStore, cookieDomain string, cookieSecure bool) *Handlers {
	return &Handlers{
		provider:     provider,
		sessions:     sessions,
		cookieDomain: cookieDomain,
		cookieSecure: cookieSecure,
	}
}

// HandleLogin initiates the OAuth2/OIDC login flow by redirecting to the IdP.
// @Summary Initiate OAuth2/OIDC login
// @Description Redirects the user to the configured identity provider for authentication
// @Tags auth
// @Produce json
// @Success 302 {string} string "Redirect to IdP"
// @Router /auth/login [get]
func (h *Handlers) HandleLogin(w http.ResponseWriter, r *http.Request) {
	authURL, state, err := h.provider.AuthCodeURL()
	if err != nil {
		log.Error().Err(err).Msg("auth: failed to generate auth URL")
		writeAuthError(w, http.StatusInternalServerError, "failed to initiate login")
		return
	}

	// Store state in a short-lived cookie for CSRF validation
	http.SetCookie(w, &http.Cookie{
		Name:     "meshsat_oauth_state",
		Value:    state,
		Path:     "/auth",
		MaxAge:   600, // 10 minutes
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, authURL, http.StatusFound)
}

// HandleCallback processes the OAuth2/OIDC callback from the IdP.
// @Summary OAuth2/OIDC callback
// @Description Processes the authorization code callback, creates a session, and redirects to the dashboard
// @Tags auth
// @Param code query string true "Authorization code"
// @Param state query string true "CSRF state"
// @Success 302 {string} string "Redirect to dashboard"
// @Failure 400 {object} map[string]string "Invalid request"
// @Failure 401 {object} map[string]string "Authentication failed"
// @Router /auth/callback [get]
func (h *Handlers) HandleCallback(w http.ResponseWriter, r *http.Request) {
	// Validate CSRF state
	stateCookie, err := r.Cookie("meshsat_oauth_state")
	if err != nil {
		writeAuthError(w, http.StatusBadRequest, "missing state cookie")
		return
	}
	queryState := r.URL.Query().Get("state")
	if queryState == "" || stateCookie.Value != queryState {
		writeAuthError(w, http.StatusBadRequest, "state mismatch")
		return
	}
	codeVerifier, valid := h.provider.ValidateState(queryState)
	if !valid {
		writeAuthError(w, http.StatusBadRequest, "invalid or expired state")
		return
	}

	// Clear state cookie
	http.SetCookie(w, &http.Cookie{
		Name:   "meshsat_oauth_state",
		Path:   "/auth",
		MaxAge: -1,
	})

	// Check for error from IdP
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		desc := r.URL.Query().Get("error_description")
		log.Warn().Str("error", errParam).Str("description", desc).Msg("auth: IdP returned error")
		writeAuthError(w, http.StatusUnauthorized, "authentication failed: "+errParam)
		return
	}

	// Exchange code for tokens
	code := r.URL.Query().Get("code")
	if code == "" {
		writeAuthError(w, http.StatusBadRequest, "missing authorization code")
		return
	}

	user, token, err := h.provider.Exchange(r.Context(), code, codeVerifier)
	if err != nil {
		log.Error().Err(err).Msg("auth: token exchange failed")
		writeAuthError(w, http.StatusUnauthorized, "token exchange failed")
		return
	}

	// Extract raw id_token for RP-Initiated Logout (id_token_hint)
	rawIDToken, _ := token.Extra("id_token").(string)

	// Create session with OAuth2 tokens
	sessionID, err := h.sessions.Create(user, token, rawIDToken)
	if err != nil {
		log.Error().Err(err).Msg("auth: session creation failed")
		writeAuthError(w, http.StatusInternalServerError, "session creation failed")
		return
	}

	// Set session cookie
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    sessionID,
		Path:     "/",
		Domain:   h.cookieDomain,
		MaxAge:   int(SessionMaxAge.Seconds()),
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})

	log.Info().Str("sub", user.Subject).Str("email", user.Email).Msg("auth: user logged in")

	// Redirect to dashboard
	http.Redirect(w, r, "/", http.StatusFound)
}

// HandleLogout clears the session and redirects to the IdP's end_session_endpoint (RP-Initiated Logout).
// Falls back to local-only logout if the IdP doesn't support end_session_endpoint.
// @Summary Logout
// @Description Clears the user session and redirects to IdP logout, then back to login
// @Tags auth
// @Success 302 {string} string "Redirect to IdP logout or login"
// @Router /auth/logout [post]
func (h *Handlers) HandleLogout(w http.ResponseWriter, r *http.Request) {
	var idTokenHint string
	if cookie, err := r.Cookie(SessionCookieName); err == nil {
		if session, ok := h.sessions.Get(cookie.Value); ok {
			idTokenHint = session.IDToken
		}
		h.sessions.Delete(cookie.Value)
	}

	// Clear session cookie
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		Domain:   h.cookieDomain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})

	log.Info().Msg("auth: user logged out")

	// RP-Initiated Logout: redirect to IdP's end_session_endpoint if available
	if endSessionURL := h.provider.EndSessionURL(); endSessionURL != "" {
		u, err := url.Parse(endSessionURL)
		if err == nil {
			q := u.Query()
			if idTokenHint != "" {
				q.Set("id_token_hint", idTokenHint)
			}
			q.Set("client_id", h.provider.ClientID())
			q.Set("post_logout_redirect_uri", h.postLogoutRedirectURI(r))
			u.RawQuery = q.Encode()
			http.Redirect(w, r, u.String(), http.StatusFound)
			return
		}
		log.Warn().Err(err).Msg("auth: failed to parse end_session_endpoint, falling back to local logout")
	}

	// Fallback: local-only logout
	http.Redirect(w, r, "/auth/login", http.StatusFound)
}

// postLogoutRedirectURI builds the URI the IdP should redirect to after logout.
func (h *Handlers) postLogoutRedirectURI(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	return scheme + "://" + host + "/auth/login"
}

// HandleMe returns the current authenticated user's info.
// @Summary Get current user
// @Description Returns the authenticated user's profile information
// @Tags auth
// @Produce json
// @Success 200 {object} UserInfo
// @Failure 401 {object} map[string]string "Not authenticated"
// @Router /auth/me [get]
func (h *Handlers) HandleMe(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeAuthError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// HandleStatus returns the auth configuration status (public endpoint).
// @Summary Auth status
// @Description Returns whether authentication is enabled and the login URL
// @Tags auth
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /auth/status [get]
func (h *Handlers) HandleStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]interface{}{
		"enabled":   true,
		"provider":  "oidc",
		"login_url": "/auth/login",
	}

	// Check if user is already authenticated
	user := UserFromContext(r.Context())
	if user != nil {
		status["authenticated"] = true
		status["user"] = user
	} else {
		// Also try session cookie without middleware
		if cookie, err := r.Cookie(SessionCookieName); err == nil {
			if session, ok := h.sessions.Get(cookie.Value); ok {
				status["authenticated"] = true
				status["user"] = &session.User
			}
		}
		if _, ok := status["authenticated"]; !ok {
			status["authenticated"] = false
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	json.NewEncoder(w).Encode(status)
}

// HandleAuthDisabledStatus returns a static response when auth is disabled (public endpoint).
func HandleAuthDisabledStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"enabled":       false,
		"authenticated": true,
	})
}

// HandleTokenRefresh validates the session with the IdP using the stored refresh token.
// If the IdP returns new tokens, the session is updated. Falls back to extending the
// session TTL if no refresh token is stored.
// @Summary Refresh session
// @Description Uses OAuth2 refresh token to re-validate with IdP and extend the session
// @Tags auth
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 401 {object} map[string]string "Not authenticated or refresh rejected"
// @Router /auth/refresh [post]
func (h *Handlers) HandleTokenRefresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "no session")
		return
	}

	session, ok := h.sessions.Get(cookie.Value)
	if !ok {
		writeAuthError(w, http.StatusUnauthorized, "session expired")
		return
	}

	// If we have a refresh token, use it to re-validate with the IdP
	if session.RefreshToken != "" {
		user, newToken, err := h.provider.RefreshTokens(r.Context(), session.RefreshToken)
		if err != nil {
			log.Warn().Err(err).Str("sub", session.User.Subject).Msg("auth: refresh token rejected by IdP")
			// IdP rejected the refresh — session is no longer valid
			h.sessions.Delete(cookie.Value)
			http.SetCookie(w, &http.Cookie{
				Name:   SessionCookieName,
				Path:   "/",
				Domain: h.cookieDomain,
				MaxAge: -1,
			})
			writeAuthError(w, http.StatusUnauthorized, "session revoked by identity provider")
			return
		}

		// Update session with new tokens and user info
		session.User = *user
		session.AccessToken = newToken.AccessToken
		session.TokenExpiry = newToken.Expiry
		if newToken.RefreshToken != "" {
			session.RefreshToken = newToken.RefreshToken // rotated
		}
		if rawID, ok := newToken.Extra("id_token").(string); ok {
			session.IDToken = rawID
		}
		session.ExpiresAt = time.Now().Add(SessionMaxAge)

		log.Debug().Str("sub", user.Subject).Msg("auth: session refreshed via IdP")
	} else {
		// No refresh token — just extend session TTL (backwards compat)
		session.ExpiresAt = time.Now().Add(SessionMaxAge)
		log.Debug().Str("sub", session.User.Subject).Msg("auth: session extended (no refresh token)")
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"expires_at": session.ExpiresAt.Format(time.RFC3339),
		"user":       &session.User,
	})
}

func writeAuthError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
