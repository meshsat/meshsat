package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	// APIKeyPrefix is prepended to all generated keys for identification.
	APIKeyPrefix = "meshsat_"
	// APIKeyBytes is the number of random bytes in a key (32 bytes → 64 hex chars).
	APIKeyBytes = 32
)

// Role represents the RBAC role hierarchy.
type Role string

const (
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
	RoleOwner    Role = "owner"
)

// roleRank maps roles to their hierarchy level for comparison.
var roleRank = map[Role]int{
	RoleViewer:   1,
	RoleOperator: 2,
	RoleOwner:    3,
}

// RoleAtLeast returns true if the role is at or above the minimum required level.
func RoleAtLeast(role, minRole Role) bool {
	return roleRank[role] >= roleRank[minRole]
}

// ValidRole returns true if the role string is a recognized role.
func ValidRole(s string) bool {
	_, ok := roleRank[Role(s)]
	return ok
}

// APIKey represents a stored API key (hash only, never the plaintext).
type APIKey struct {
	ID        int64   `json:"id"`
	KeyPrefix string  `json:"key_prefix"`
	TenantID  string  `json:"tenant_id"`
	DeviceID  *int64  `json:"device_id,omitempty"`
	Role      Role    `json:"role"`
	Label     string  `json:"label"`
	LastUsed  *string `json:"last_used,omitempty"`
	ExpiresAt *string `json:"expires_at,omitempty"`
	CreatedAt string  `json:"created_at"`
}

// GenerateAPIKey creates a new API key and returns the plaintext key, its SHA-256 hash, and the display prefix.
func GenerateAPIKey() (plaintext, hash, prefix string, err error) {
	b := make([]byte, APIKeyBytes)
	if _, err := rand.Read(b); err != nil {
		return "", "", "", fmt.Errorf("generate key: %w", err)
	}
	plaintext = APIKeyPrefix + hex.EncodeToString(b)
	hash = HashAPIKey(plaintext)
	prefix = plaintext[:len(APIKeyPrefix)+8]
	return plaintext, hash, prefix, nil
}

// HashAPIKey returns the SHA-256 hex digest of a plaintext API key.
func HashAPIKey(plaintext string) string {
	h := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(h[:])
}

// IsAPIKey returns true if the token looks like a MeshSat API key.
func IsAPIKey(token string) bool {
	return strings.HasPrefix(token, APIKeyPrefix)
}
