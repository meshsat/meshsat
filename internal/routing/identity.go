package routing

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/reticulum"
)

// DestHashLen is the length of a destination hash (truncated SHA-256).
const DestHashLen = reticulum.TruncatedHashLen

// DefaultAppName is the Reticulum application name for MeshSat Bridge.
// The dest hash is computed as SHA256(nameHash || identityHash)[:16]
// where nameHash = SHA256("meshsat.bridge")[:10].
const DefaultAppName = "meshsat.bridge"

// Identity holds the Reticulum routing keypair: an Ed25519 signing key and
// an X25519 encryption key. It wraps reticulum.Identity for wire-compatible
// cryptographic operations and adds database persistence.
//
// Destination hash is computed using the Reticulum 3-stage method:
//
//	name_hash     = SHA256(app_name)[:10]
//	identity_hash = SHA256(enc_pub || sig_pub)[:16]
//	dest_hash     = SHA256(name_hash || identity_hash)[:16]
type Identity struct {
	mu       sync.RWMutex
	retID    *reticulum.Identity // underlying Reticulum identity
	appName  string
	destHash [DestHashLen]byte
	db       *database.DB
}

// NewIdentity loads or generates a routing identity. Keys are persisted to the
// system_config table under routing_signing_key and routing_encryption_key.
func NewIdentity(db *database.DB) (*Identity, error) {
	return NewIdentityWithAppName(db, DefaultAppName)
}

// NewIdentityWithAppName loads or generates a routing identity with a custom
// Reticulum app name. Different app names produce different destination hashes
// for the same keypair.
func NewIdentityWithAppName(db *database.DB, appName string) (*Identity, error) {
	id := &Identity{
		db:      db,
		appName: appName,
	}

	sigHex, _ := db.GetSystemConfig("routing_signing_key")
	encHex, _ := db.GetSystemConfig("routing_encryption_key")

	if sigHex != "" && encHex != "" {
		if err := id.loadKeys(sigHex, encHex); err != nil {
			return nil, fmt.Errorf("load routing identity: %w", err)
		}
	} else {
		if err := id.generateKeys(); err != nil {
			return nil, fmt.Errorf("generate routing identity: %w", err)
		}
		if err := id.persistKeys(); err != nil {
			return nil, fmt.Errorf("persist routing identity: %w", err)
		}
	}

	id.computeDestHash()
	log.Info().
		Str("dest_hash", id.DestHashHex()).
		Str("app_name", appName).
		Msg("routing identity initialized")
	return id, nil
}

// DestHash returns the 16-byte destination hash.
func (id *Identity) DestHash() [DestHashLen]byte {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return id.destHash
}

// DestHashHex returns the hex-encoded destination hash.
func (id *Identity) DestHashHex() string {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return hex.EncodeToString(id.destHash[:])
}

// AppName returns the Reticulum app name used for dest hash computation.
func (id *Identity) AppName() string {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return id.appName
}

// NameHash returns the 10-byte name hash for this identity's app name.
func (id *Identity) NameHash() [reticulum.NameHashLen]byte {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return reticulum.ComputeNameHash(id.appName)
}

// SigningPublicKey returns the Ed25519 public signing key (32 bytes).
func (id *Identity) SigningPublicKey() ed25519.PublicKey {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return id.retID.SigningPublicKey()
}

// EncryptionPublicKey returns the X25519 public encryption key (32 bytes).
func (id *Identity) EncryptionPublicKey() *ecdh.PublicKey {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return id.retID.EncryptionPublicKey()
}

// PublicBytes returns the 64-byte public key: [32B X25519][32B Ed25519].
// This matches the Reticulum wire format order.
func (id *Identity) PublicBytes() []byte {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return id.retID.PublicBytes()
}

// ReticulumIdentity returns the underlying reticulum.Identity for direct use
// with the reticulum wire format library.
func (id *Identity) ReticulumIdentity() *reticulum.Identity {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return id.retID
}

// Sign creates an Ed25519 signature of the given data.
func (id *Identity) Sign(data []byte) []byte {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return id.retID.Sign(data)
}

// Verify checks an Ed25519 signature against this identity's signing key.
func (id *Identity) Verify(data, signature []byte) bool {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return reticulum.VerifySignature(id.retID.SigningPublicKey(), data, signature)
}

// VerifyWith checks an Ed25519 signature against an arbitrary public key.
func VerifyWith(pubKey ed25519.PublicKey, data, signature []byte) bool {
	return reticulum.VerifySignature(pubKey, data, signature)
}

// ComputeDestHash computes the Reticulum 3-stage destination hash for
// arbitrary public keys using the default app name.
//
// Deprecated: For new code, use reticulum.ComputeDestHash with explicit
// name hash and identity hash. This wrapper exists for backward compatibility.
func ComputeDestHash(signingPub ed25519.PublicKey, encryptionPub *ecdh.PublicKey) [DestHashLen]byte {
	nameHash := reticulum.ComputeNameHash(DefaultAppName)
	identityHash := reticulum.ComputeIdentityHash(encryptionPub, signingPub)
	return reticulum.ComputeDestHash(nameHash, identityHash)
}

func (id *Identity) computeDestHash() {
	id.destHash = id.retID.DestHash(id.appName)
}

func (id *Identity) loadKeys(sigHex, encHex string) error {
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil || len(sigBytes) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid signing key")
	}

	encBytes, err := hex.DecodeString(encHex)
	if err != nil {
		return fmt.Errorf("invalid encryption key hex")
	}

	retID, err := reticulum.LoadIdentity(encBytes, sigBytes)
	if err != nil {
		return fmt.Errorf("load reticulum identity: %w", err)
	}
	id.retID = retID
	return nil
}

func (id *Identity) generateKeys() error {
	retID, err := reticulum.GenerateIdentity()
	if err != nil {
		return fmt.Errorf("generate reticulum identity: %w", err)
	}
	id.retID = retID
	return nil
}

func (id *Identity) persistKeys() error {
	if err := id.db.SetSystemConfig("routing_signing_key", hex.EncodeToString(id.retID.SigningPrivateBytes())); err != nil {
		return err
	}
	return id.db.SetSystemConfig("routing_encryption_key", hex.EncodeToString(id.retID.EncryptionPrivateBytes()))
}
