package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"
)

type contextKey string

const userContextKey contextKey = "auth_user"

// APIKeyResult is returned by the API key validator when a key is authenticated.
type APIKeyResult struct {
	ID       int64
	TenantID string
	DeviceID *int64
	Role     Role
	Label    string
}

// APIKeyValidator looks up an API key by its hash and returns the result.
// Implemented by the database layer to avoid circular imports.
type APIKeyValidator func(keyHash string) (*APIKeyResult, error)

// RequireAuth returns a chi middleware that validates authentication via:
//  1. Session cookie (meshsat_session)
//  2. API key (Authorization: Bearer meshsat_*)
//  3. OIDC ID token (Authorization: Bearer <jwt>)
//
// When auth is disabled (provider == nil), it passes through all requests with RoleOwner.
// The apiKeyValidator may be nil if API keys are not configured.
func RequireAuth(provider *Provider, sessionStore *SessionStore, apiKeyValidator APIKeyValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// If auth is not configured, pass through with owner role
			if provider == nil {
				ctx := WithRole(r.Context(), RoleOwner)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// 1. Try session cookie first
			if sessionStore != nil {
				if cookie, err := r.Cookie(SessionCookieName); err == nil {
					if session, ok := sessionStore.Get(cookie.Value); ok {
						ctx := context.WithValue(r.Context(), userContextKey, &session.User)
						ctx = WithRole(ctx, RoleOwner) // OIDC users get owner role
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
				}
			}

			// 2. Try Authorization: Bearer <token>
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				rawToken := strings.TrimPrefix(authHeader, "Bearer ")

				// 2a. API key (meshsat_* prefix)
				if IsAPIKey(rawToken) && apiKeyValidator != nil {
					keyHash := HashAPIKey(rawToken)
					result, err := apiKeyValidator(keyHash)
					if err != nil {
						log.Debug().Err(err).Msg("auth: invalid API key")
						http.Error(w, `{"error":"invalid or expired API key"}`, http.StatusUnauthorized)
						return
					}
					user := &UserInfo{
						Subject:  "apikey:" + result.Label,
						TenantID: result.TenantID,
						Name:     "API Key: " + result.Label,
					}
					ctx := context.WithValue(r.Context(), userContextKey, user)
					ctx = WithRole(ctx, result.Role)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}

				// 2b. OIDC ID token
				user, err := provider.VerifyIDToken(r.Context(), rawToken)
				if err != nil {
					log.Debug().Err(err).Msg("auth: invalid bearer token")
					http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
					return
				}
				ctx := context.WithValue(r.Context(), userContextKey, user)
				ctx = WithRole(ctx, RoleOwner) // OIDC users get owner role
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// No valid auth found
			http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
		})
	}
}

// UserFromContext extracts the authenticated user from the request context.
// Returns nil if no user is present (auth disabled or unauthenticated).
func UserFromContext(ctx context.Context) *UserInfo {
	user, _ := ctx.Value(userContextKey).(*UserInfo)
	return user
}
