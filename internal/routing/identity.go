package routing

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// DestHashLen is the length of a destination hash (truncated SHA-256).
const DestHashLen = 16

// Identity holds the Reticulum-inspired routing keypair: an Ed25519 signing
// key and an X25519 encryption key. The destination hash is the first 16 bytes
// of SHA-256(signingPub || encryptionPub).
type Identity struct {
	mu            sync.RWMutex
	signingKey    ed25519.PrivateKey
	signingPub    ed25519.PublicKey
	encryptionKey *ecdh.PrivateKey
	encryptionPub *ecdh.PublicKey
	destHash      [DestHashLen]byte
	db            *database.DB
}

// NewIdentity loads or generates a routing identity. Keys are persisted to the
// system_config table under routing_signing_key and routing_encryption_key.
func NewIdentity(db *database.DB) (*Identity, error) {
	id := &Identity{db: db}

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
	log.Info().Str("dest_hash", id.DestHashHex()).Msg("routing identity initialized")
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

// SigningPublicKey returns the Ed25519 public signing key (32 bytes).
func (id *Identity) SigningPublicKey() ed25519.PublicKey {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return id.signingPub
}

// EncryptionPublicKey returns the X25519 public encryption key (32 bytes).
func (id *Identity) EncryptionPublicKey() *ecdh.PublicKey {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return id.encryptionPub
}

// Sign creates an Ed25519 signature of the given data.
func (id *Identity) Sign(data []byte) []byte {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return ed25519.Sign(id.signingKey, data)
}

// Verify checks an Ed25519 signature against this identity's signing key.
func (id *Identity) Verify(data, signature []byte) bool {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return ed25519.Verify(id.signingPub, data, signature)
}

// VerifyWith checks an Ed25519 signature against an arbitrary public key.
func VerifyWith(pubKey ed25519.PublicKey, data, signature []byte) bool {
	if len(pubKey) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(pubKey, data, signature)
}

// ComputeDestHash computes the 16-byte destination hash for arbitrary public keys.
func ComputeDestHash(signingPub ed25519.PublicKey, encryptionPub *ecdh.PublicKey) [DestHashLen]byte {
	h := sha256.New()
	h.Write(signingPub)
	h.Write(encryptionPub.Bytes())
	sum := h.Sum(nil)
	var dest [DestHashLen]byte
	copy(dest[:], sum[:DestHashLen])
	return dest
}

func (id *Identity) computeDestHash() {
	id.destHash = ComputeDestHash(id.signingPub, id.encryptionPub)
}

func (id *Identity) loadKeys(sigHex, encHex string) error {
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil || len(sigBytes) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid signing key")
	}
	id.signingKey = ed25519.PrivateKey(sigBytes)
	id.signingPub = id.signingKey.Public().(ed25519.PublicKey)

	encBytes, err := hex.DecodeString(encHex)
	if err != nil {
		return fmt.Errorf("invalid encryption key hex")
	}
	id.encryptionKey, err = ecdh.X25519().NewPrivateKey(encBytes)
	if err != nil {
		return fmt.Errorf("invalid encryption key: %w", err)
	}
	id.encryptionPub = id.encryptionKey.PublicKey()
	return nil
}

func (id *Identity) generateKeys() error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate ed25519: %w", err)
	}
	id.signingKey = priv
	id.signingPub = pub

	id.encryptionKey, err = ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate x25519: %w", err)
	}
	id.encryptionPub = id.encryptionKey.PublicKey()
	return nil
}

func (id *Identity) persistKeys() error {
	if err := id.db.SetSystemConfig("routing_signing_key", hex.EncodeToString(id.signingKey)); err != nil {
		return err
	}
	return id.db.SetSystemConfig("routing_encryption_key", hex.EncodeToString(id.encryptionKey.Bytes()))
}
