package keystore

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/routing"
)

// KeyMeta is the metadata for a stored key (no raw key material).
type KeyMeta struct {
	ID          int64   `json:"id"`
	ChannelType string  `json:"channel_type"`
	Address     string  `json:"address"`
	KeyVersion  int     `json:"key_version"`
	Status      string  `json:"status"` // active, retired, revoked
	KeyPreview  string  `json:"key_preview"`
	ExpiresAt   *string `json:"expires_at,omitempty"`
	CreatedAt   string  `json:"created_at"`
}

// KeyStore manages envelope-encrypted channel keys with master key wrapping.
type KeyStore struct {
	db        *database.DB
	identity  *routing.Identity
	masterKey []byte // unwrapped master key (in-memory only)
	mu        sync.RWMutex
}

// NewKeyStore creates a key store, bootstrapping the master key if needed.
func NewKeyStore(db *database.DB, identity *routing.Identity, passphrase string) (*KeyStore, error) {
	ks := &KeyStore{db: db, identity: identity}

	// Try to load existing master key
	wrappedHex, _ := db.GetSystemConfig("keystore_master_key")
	saltHex, _ := db.GetSystemConfig("keystore_master_salt")

	if wrappedHex != "" && saltHex != "" {
		mk, err := loadMasterKey(passphrase, wrappedHex, saltHex)
		if err != nil {
			return nil, fmt.Errorf("unlock master key: %w (wrong passphrase?)", err)
		}
		ks.masterKey = mk
		log.Info().Msg("keystore: master key loaded")
	} else {
		// Bootstrap new master key
		mk, salt, wrapped, err := bootstrapMasterKey(passphrase)
		if err != nil {
			return nil, fmt.Errorf("bootstrap master key: %w", err)
		}
		if err := db.SetSystemConfig("keystore_master_key", wrapped); err != nil {
			return nil, fmt.Errorf("persist master key: %w", err)
		}
		if err := db.SetSystemConfig("keystore_master_salt", hex.EncodeToString(salt)); err != nil {
			return nil, fmt.Errorf("persist salt: %w", err)
		}
		ks.masterKey = mk
		log.Info().Msg("keystore: master key bootstrapped")
	}

	return ks, nil
}

// GenerateKey creates a new AES-256 key for a channel+address, wraps it, and stores it.
func (ks *KeyStore) GenerateKey(channelType, address string) (rawKey []byte, version int, err error) {
	rawKey = make([]byte, aesKeyLen)
	if _, err = io.ReadFull(rand.Reader, rawKey); err != nil {
		return nil, 0, fmt.Errorf("generate key: %w", err)
	}

	ks.mu.Lock()
	defer ks.mu.Unlock()

	wrapped, err := wrapKey(ks.masterKey, rawKey)
	if err != nil {
		return nil, 0, fmt.Errorf("wrap key: %w", err)
	}

	// Get next version
	version = 1
	if current, err := ks.db.GetActiveKeyBundle(channelType, address); err == nil && current != nil {
		version = current.KeyVersion + 1
	}

	if err := ks.db.InsertKeyBundle(channelType, address, wrapped, version); err != nil {
		return nil, 0, fmt.Errorf("store key: %w", err)
	}

	log.Info().Str("channel", channelType).Str("address", address).Int("version", version).
		Msg("keystore: key generated")
	return rawKey, version, nil
}

// GetKey returns the active raw AES-256 key for a channel+address.
func (ks *KeyStore) GetKey(channelType, address string) ([]byte, int, error) {
	ks.mu.RLock()
	defer ks.mu.RUnlock()

	kb, err := ks.db.GetActiveKeyBundle(channelType, address)
	if err != nil {
		return nil, 0, fmt.Errorf("key not found: %w", err)
	}

	raw, err := unwrapKey(ks.masterKey, kb.EncryptedKey)
	if err != nil {
		return nil, 0, fmt.Errorf("unwrap key: %w", err)
	}

	return raw, kb.KeyVersion, nil
}

// RotateKey generates a new key version and retires the old one.
func (ks *KeyStore) RotateKey(channelType, address string, graceHours int) (rawKey []byte, newVersion int, err error) {
	ks.mu.Lock()
	defer ks.mu.Unlock()

	// Retire current key
	if graceHours <= 0 {
		graceHours = 24
	}
	expiresAt := time.Now().Add(time.Duration(graceHours) * time.Hour)
	if err := ks.db.RetireKeyBundle(channelType, address, expiresAt); err != nil {
		log.Debug().Err(err).Msg("keystore: no active key to retire")
	}

	// Generate new key
	rawKey = make([]byte, aesKeyLen)
	if _, err = io.ReadFull(rand.Reader, rawKey); err != nil {
		return nil, 0, fmt.Errorf("generate key: %w", err)
	}

	wrapped, err := wrapKey(ks.masterKey, rawKey)
	if err != nil {
		return nil, 0, fmt.Errorf("wrap key: %w", err)
	}

	// Get next version
	newVersion = 1
	if latest, err := ks.db.GetLatestKeyVersion(channelType, address); err == nil {
		newVersion = latest + 1
	}

	if err := ks.db.InsertKeyBundle(channelType, address, wrapped, newVersion); err != nil {
		return nil, 0, fmt.Errorf("store rotated key: %w", err)
	}

	log.Info().Str("channel", channelType).Str("address", address).
		Int("version", newVersion).Int("grace_hours", graceHours).
		Msg("keystore: key rotated")
	return rawKey, newVersion, nil
}

// RevokeKey marks a key as revoked (immediately invalid, no grace period).
func (ks *KeyStore) RevokeKey(channelType, address string) error {
	return ks.db.RevokeKeyBundle(channelType, address)
}

// StoreKey imports an externally-provided raw key (e.g. from Hub key_rotate command).
// If a key already exists for this channel+address, a new version is created. [MESHSAT-447]
func (ks *KeyStore) StoreKey(channelType, address string, rawKey []byte) (int, error) {
	if len(rawKey) != aesKeyLen {
		return 0, fmt.Errorf("key must be %d bytes, got %d", aesKeyLen, len(rawKey))
	}

	ks.mu.Lock()
	defer ks.mu.Unlock()

	wrapped, err := wrapKey(ks.masterKey, rawKey)
	if err != nil {
		return 0, fmt.Errorf("wrap key: %w", err)
	}

	// Retire any active key with grace period, then find max version to avoid conflicts.
	if current, err := ks.db.GetActiveKeyBundle(channelType, address); err == nil && current != nil {
		_ = ks.db.RetireKeyBundle(channelType, address, time.Now().Add(7*24*time.Hour))
	}
	version := ks.db.MaxKeyVersion(channelType, address) + 1

	if err := ks.db.InsertKeyBundle(channelType, address, wrapped, version); err != nil {
		return 0, fmt.Errorf("store key: %w", err)
	}

	log.Info().Str("channel", channelType).Str("address", address).Int("version", version).
		Msg("keystore: key imported from Hub")
	return version, nil
}

// ResolveKeyHex resolves a key_ref string ("channel_type:address") to a hex-encoded
// AES-256 key. Implements the engine.KeyResolver interface. [MESHSAT-447]
// During grace period, returns the active key (not retired).
func (ks *KeyStore) ResolveKeyHex(keyRef string) (string, error) {
	parts := strings.SplitN(keyRef, ":", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid key_ref format %q (expected channel_type:address)", keyRef)
	}
	raw, _, err := ks.GetKey(parts[0], parts[1])
	if err != nil {
		return "", fmt.Errorf("resolve key_ref %q: %w", keyRef, err)
	}
	return hex.EncodeToString(raw), nil
}

// ListKeys returns metadata for all keys (no raw key material).
func (ks *KeyStore) ListKeys() ([]KeyMeta, error) {
	ks.mu.RLock()
	defer ks.mu.RUnlock()

	bundles, err := ks.db.ListKeyBundles()
	if err != nil {
		return nil, err
	}

	metas := make([]KeyMeta, len(bundles))
	for i, kb := range bundles {
		// Unwrap to get preview (first 4 bytes hex)
		preview := "****"
		if raw, err := unwrapKey(ks.masterKey, kb.EncryptedKey); err == nil && len(raw) >= 4 {
			preview = hex.EncodeToString(raw[:4]) + "..."
		}
		metas[i] = KeyMeta{
			ID:          kb.ID,
			ChannelType: kb.ChannelType,
			Address:     kb.Address,
			KeyVersion:  kb.KeyVersion,
			Status:      kb.Status,
			KeyPreview:  preview,
			ExpiresAt:   kb.ExpiresAt,
			CreatedAt:   kb.CreatedAt,
		}
	}
	return metas, nil
}

// CreateBundle generates a signed QR-ready key bundle for the specified channels.
// If a key doesn't exist for a channel+address, one is generated.
func (ks *KeyStore) CreateBundle(requests []BundleRequest) ([]byte, string, error) {
	if ks.identity == nil {
		return nil, "", fmt.Errorf("routing identity not available")
	}

	var entries []BundleEntry
	for _, req := range requests {
		rawKey, _, err := ks.GetKey(req.ChannelType, req.Address)
		if err != nil {
			// Generate a new key if none exists
			rawKey, _, err = ks.GenerateKey(req.ChannelType, req.Address)
			if err != nil {
				return nil, "", fmt.Errorf("generate key for %s:%s: %w", req.ChannelType, req.Address, err)
			}
		}

		var key [aesKeyLen]byte
		copy(key[:], rawKey)
		entries = append(entries, BundleEntry{
			ChannelType: ChannelTypeToByte(req.ChannelType),
			Address:     req.Address,
			Key:         key,
		})
	}

	bridgeHash := ks.identity.DestHash()

	data, err := MarshalBundle(bridgeHash, entries, ks.identity)
	if err != nil {
		return nil, "", err
	}

	url := BundleToURL(data)
	return data, url, nil
}

// BundleRequest specifies a channel+address to include in a key bundle.
type BundleRequest struct {
	ChannelType string `json:"channel_type"`
	Address     string `json:"address"`
}

// WrapData encrypts arbitrary data with the master key (for credential storage).
func (ks *KeyStore) WrapData(data []byte) ([]byte, error) {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return wrapKey(ks.masterKey, data)
}

// UnwrapData decrypts data that was encrypted with WrapData.
func (ks *KeyStore) UnwrapData(wrapped []byte) ([]byte, error) {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return unwrapKey(ks.masterKey, wrapped)
}

// Stats returns key inventory counts.
func (ks *KeyStore) Stats() (active, retired, revoked int, err error) {
	return ks.db.KeyBundleStats()
}

// SigningPublicKey returns the bridge's Ed25519 public key for bundle verification.
func (ks *KeyStore) SigningPublicKey() ed25519.PublicKey {
	if ks.identity == nil {
		return nil
	}
	return ks.identity.SigningPublicKey()
}
