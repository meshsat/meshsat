package auth

import (
	"context"
	"net/http"
)

const roleContextKey contextKey = "auth_role"

// WithRole stores the user's role in the request context.
func WithRole(ctx context.Context, role Role) context.Context {
	return context.WithValue(ctx, roleContextKey, role)
}

// RoleFromContext extracts the role from the request context.
// Returns RoleOwner if no role is set (auth disabled = full access).
func RoleFromContext(ctx context.Context) Role {
	if r, ok := ctx.Value(roleContextKey).(Role); ok {
		return r
	}
	return RoleOwner // default when auth is disabled
}

// RequireRole returns a chi middleware that enforces a minimum role level.
// Must run after RequireAuth (which sets the role in context).
func RequireRole(minRole Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := RoleFromContext(r.Context())
			if !RoleAtLeast(role, minRole) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(`{"error":"insufficient permissions, requires ` + string(minRole) + ` role"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
