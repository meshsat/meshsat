package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"
)

// DefaultTenantID is used when tenant isolation is disabled or no tenant is resolved.
const DefaultTenantID = "default"

// TenantHeaderName is the HTTP header clients can use to specify a tenant.
const TenantHeaderName = "X-Tenant-ID"

const tenantContextKey contextKey = "auth_tenant"

// TenantContext holds the resolved tenant for the current request.
type TenantContext struct {
	// ID is the unique tenant identifier (from OIDC claim or header).
	ID string `json:"id"`
	// Source indicates where the tenant was resolved from: "claim", "header", or "default".
	Source string `json:"source"`
}

// TenantMiddleware extracts the tenant ID from the authenticated user's OIDC claims
// (via the configured claim name) or falls back to the X-Tenant-ID header.
// When no tenant can be resolved and enforcement is off, it falls back to DefaultTenantID.
//
// This middleware MUST run after RequireAuth so that UserInfo is available in context.
func TenantMiddleware(claimName string, enforce bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var tc TenantContext

			// 1. Try tenant from authenticated user's TenantID (populated during token exchange)
			if user := UserFromContext(r.Context()); user != nil && user.TenantID != "" {
				tc = TenantContext{ID: user.TenantID, Source: "claim"}
			}

			// 2. Fall back to X-Tenant-ID header (for service-to-service or API key auth)
			if tc.ID == "" {
				if headerVal := r.Header.Get(TenantHeaderName); headerVal != "" {
					tc = TenantContext{ID: sanitizeTenantID(headerVal), Source: "header"}
				}
			}

			// 3. No tenant resolved
			if tc.ID == "" {
				if enforce {
					log.Warn().Msg("tenant: no tenant ID resolved and enforcement is enabled")
					http.Error(w, `{"error":"tenant identification required"}`, http.StatusForbidden)
					return
				}
				tc = TenantContext{ID: DefaultTenantID, Source: "default"}
			}

			ctx := context.WithValue(r.Context(), tenantContextKey, &tc)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// TenantFromContext extracts the tenant context from the request context.
// Returns nil if no tenant is present (middleware not applied).
func TenantFromContext(ctx context.Context) *TenantContext {
	tc, _ := ctx.Value(tenantContextKey).(*TenantContext)
	return tc
}

// TenantIDFromContext returns the tenant ID from context, or DefaultTenantID if not present.
func TenantIDFromContext(ctx context.Context) string {
	if tc := TenantFromContext(ctx); tc != nil {
		return tc.ID
	}
	return DefaultTenantID
}

// sanitizeTenantID cleans a tenant ID from untrusted input (header).
// Allows alphanumeric, hyphens, underscores, and dots. Max 128 chars.
func sanitizeTenantID(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) > 128 {
		raw = raw[:128]
	}
	var b strings.Builder
	for _, c := range raw {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' {
			b.WriteRune(c)
		}
	}
	return b.String()
}
