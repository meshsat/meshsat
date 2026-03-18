package database

import (
	"fmt"
	"time"
)

// APIKeyRecord represents a stored API key in the database.
type APIKeyRecord struct {
	ID        int64   `json:"id"`
	KeyHash   string  `json:"-"` // never expose
	KeyPrefix string  `json:"key_prefix"`
	TenantID  string  `json:"tenant_id"`
	DeviceID  *int64  `json:"device_id,omitempty"`
	Role      string  `json:"role"`
	Label     string  `json:"label"`
	LastUsed  *string `json:"last_used,omitempty"`
	ExpiresAt *string `json:"expires_at,omitempty"`
	CreatedAt string  `json:"created_at"`
}

// CreateAPIKey inserts a new API key record.
func (db *DB) CreateAPIKey(keyHash, keyPrefix, tenantID string, deviceID *int64, role, label string, expiresAt *string) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO api_keys (key_hash, key_prefix, tenant_id, device_id, role, label, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		keyHash, keyPrefix, tenantID, deviceID, role, label, expiresAt)
	if err != nil {
		return 0, fmt.Errorf("create api key: %w", err)
	}
	return res.LastInsertId()
}

// GetAPIKeyByHash looks up an API key by its SHA-256 hash.
// Returns nil if not found or expired.
func (db *DB) GetAPIKeyByHash(keyHash string) (*APIKeyRecord, error) {
	var k APIKeyRecord
	err := db.QueryRow(
		`SELECT id, key_hash, key_prefix, tenant_id, device_id, role, label, last_used, expires_at, created_at
		 FROM api_keys WHERE key_hash = ?`, keyHash).
		Scan(&k.ID, &k.KeyHash, &k.KeyPrefix, &k.TenantID, &k.DeviceID, &k.Role, &k.Label, &k.LastUsed, &k.ExpiresAt, &k.CreatedAt)
	if err != nil {
		return nil, err
	}
	// Check expiry
	if k.ExpiresAt != nil && *k.ExpiresAt != "" {
		exp, err := time.Parse("2006-01-02 15:04:05", *k.ExpiresAt)
		if err == nil && time.Now().After(exp) {
			return nil, fmt.Errorf("api key expired")
		}
	}
	return &k, nil
}

// GetAPIKeys returns all API keys for a tenant (hashes excluded from JSON).
func (db *DB) GetAPIKeys(tenantID string) ([]APIKeyRecord, error) {
	rows, err := db.Query(
		`SELECT id, key_hash, key_prefix, tenant_id, device_id, role, label, last_used, expires_at, created_at
		 FROM api_keys WHERE tenant_id = ? ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()
	var keys []APIKeyRecord
	for rows.Next() {
		var k APIKeyRecord
		if err := rows.Scan(&k.ID, &k.KeyHash, &k.KeyPrefix, &k.TenantID, &k.DeviceID, &k.Role, &k.Label, &k.LastUsed, &k.ExpiresAt, &k.CreatedAt); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// DeleteAPIKey removes an API key by ID, scoped to tenant.
func (db *DB) DeleteAPIKey(id int64, tenantID string) error {
	res, err := db.Exec("DELETE FROM api_keys WHERE id = ? AND tenant_id = ?", id, tenantID)
	if err != nil {
		return fmt.Errorf("delete api key: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("api key %d not found", id)
	}
	return nil
}

// TouchAPIKeyLastUsed updates the last_used timestamp for an API key.
func (db *DB) TouchAPIKeyLastUsed(id int64) {
	db.Exec("UPDATE api_keys SET last_used = datetime('now') WHERE id = ?", id)
}
