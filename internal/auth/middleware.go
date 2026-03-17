package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"
)

type contextKey string

const userContextKey contextKey = "auth_user"

// RequireAuth returns a chi middleware that validates the session cookie or
// Authorization: Bearer <id_token> header. On failure it returns 401.
// When auth is disabled (provider == nil), it passes through all requests.
func RequireAuth(provider *Provider, sessionStore *SessionStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// If auth is not configured, pass through
			if provider == nil {
				next.ServeHTTP(w, r)
				return
			}

			// Try session cookie first
			if sessionStore != nil {
				if cookie, err := r.Cookie(SessionCookieName); err == nil {
					if session, ok := sessionStore.Get(cookie.Value); ok {
						ctx := context.WithValue(r.Context(), userContextKey, &session.User)
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
				}
			}

			// Try Authorization: Bearer <id_token>
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				rawToken := strings.TrimPrefix(authHeader, "Bearer ")
				user, err := provider.VerifyIDToken(r.Context(), rawToken)
				if err != nil {
					log.Debug().Err(err).Msg("auth: invalid bearer token")
					http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
					return
				}
				ctx := context.WithValue(r.Context(), userContextKey, user)
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
