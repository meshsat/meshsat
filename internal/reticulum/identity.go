package reticulum

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Identity holds a Reticulum identity: an X25519 encryption key and an
// Ed25519 signing key. The public key bytes are ordered as:
//
//	[32B X25519 public][32B Ed25519 public]
//
// This matches the Reticulum spec where encryption key comes first.
type Identity struct {
	encryptionKey *ecdh.PrivateKey
	encryptionPub *ecdh.PublicKey
	signingKey    ed25519.PrivateKey
	signingPub    ed25519.PublicKey
}

// GenerateIdentity creates a new random Reticulum identity.
func GenerateIdentity() (*Identity, error) {
	encKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate x25519: %w", err)
	}

	sigPub, sigPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519: %w", err)
	}

	return &Identity{
		encryptionKey: encKey,
		encryptionPub: encKey.PublicKey(),
		signingKey:    sigPriv,
		signingPub:    sigPub,
	}, nil
}

// LoadIdentity reconstructs an identity from raw private key bytes.
// encPriv is 32 bytes (X25519 seed), sigPriv is 64 bytes (Ed25519 private key).
func LoadIdentity(encPriv, sigPriv []byte) (*Identity, error) {
	encKey, err := ecdh.X25519().NewPrivateKey(encPriv)
	if err != nil {
		return nil, fmt.Errorf("invalid x25519 key: %w", err)
	}

	if len(sigPriv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid ed25519 key: expected %d bytes, got %d", ed25519.PrivateKeySize, len(sigPriv))
	}
	sigKey := ed25519.PrivateKey(make([]byte, ed25519.PrivateKeySize))
	copy(sigKey, sigPriv)
	sigPub := sigKey.Public().(ed25519.PublicKey)

	return &Identity{
		encryptionKey: encKey,
		encryptionPub: encKey.PublicKey(),
		signingKey:    sigKey,
		signingPub:    sigPub,
	}, nil
}

// PublicBytes returns the 64-byte public key: [32B X25519][32B Ed25519].
func (id *Identity) PublicBytes() []byte {
	pub := make([]byte, IdentityKeySize)
	copy(pub[:EncryptionPubLen], id.encryptionPub.Bytes())
	copy(pub[EncryptionPubLen:], id.signingPub)
	return pub
}

// EncryptionPublicKey returns the X25519 public key.
func (id *Identity) EncryptionPublicKey() *ecdh.PublicKey {
	return id.encryptionPub
}

// SigningPublicKey returns the Ed25519 public key.
func (id *Identity) SigningPublicKey() ed25519.PublicKey {
	return id.signingPub
}

// EncryptionPrivateBytes returns the 32-byte X25519 private key for persistence.
func (id *Identity) EncryptionPrivateBytes() []byte {
	return id.encryptionKey.Bytes()
}

// SigningPrivateBytes returns the 64-byte Ed25519 private key for persistence.
func (id *Identity) SigningPrivateBytes() []byte {
	return []byte(id.signingKey)
}

// Sign creates an Ed25519 signature over data.
func (id *Identity) Sign(data []byte) []byte {
	return ed25519.Sign(id.signingKey, data)
}

// IdentityHash computes the identity hash: SHA-256(encPub || sigPub) truncated
// to TruncatedHashLen bytes. This is an intermediate value used in destination
// hash computation.
func (id *Identity) IdentityHash() [TruncatedHashLen]byte {
	return ComputeIdentityHash(id.encryptionPub, id.signingPub)
}

// ComputeIdentityHash computes the identity hash from public keys.
// Result: SHA-256(encPub || sigPub)[:16]
func ComputeIdentityHash(encPub *ecdh.PublicKey, sigPub ed25519.PublicKey) [TruncatedHashLen]byte {
	h := sha256.New()
	h.Write(encPub.Bytes())
	h.Write(sigPub)
	sum := h.Sum(nil)
	var out [TruncatedHashLen]byte
	copy(out[:], sum[:TruncatedHashLen])
	return out
}

// ComputeNameHash computes the name hash for a destination.
// Input: "app_name.aspect1.aspect2" (dot-separated).
// Result: SHA-256(name)[:NameHashLen]
func ComputeNameHash(name string) [NameHashLen]byte {
	sum := sha256.Sum256([]byte(name))
	var out [NameHashLen]byte
	copy(out[:], sum[:NameHashLen])
	return out
}

// ComputeDestHash computes a Reticulum destination hash.
// Result: SHA-256(nameHash || identityHash)[:TruncatedHashLen]
//
// This matches the Python RNS computation:
//
//	name_hash     = SHA256("app_name.aspects")[:10]
//	identity_hash = SHA256(enc_pub || sig_pub)[:16]
//	dest_hash     = SHA256(name_hash || identity_hash)[:16]
func ComputeDestHash(nameHash [NameHashLen]byte, identityHash [TruncatedHashLen]byte) [TruncatedHashLen]byte {
	h := sha256.New()
	h.Write(nameHash[:])
	h.Write(identityHash[:])
	sum := h.Sum(nil)
	var out [TruncatedHashLen]byte
	copy(out[:], sum[:TruncatedHashLen])
	return out
}

// DestHash computes this identity's destination hash for the given app name.
func (id *Identity) DestHash(appName string) [TruncatedHashLen]byte {
	nameHash := ComputeNameHash(appName)
	identityHash := id.IdentityHash()
	return ComputeDestHash(nameHash, identityHash)
}

// DestHashHex returns the hex-encoded destination hash.
func DestHashHex(dest [TruncatedHashLen]byte) string {
	return hex.EncodeToString(dest[:])
}

// VerifySignature checks an Ed25519 signature against a public key.
func VerifySignature(pubKey ed25519.PublicKey, data, signature []byte) bool {
	if len(pubKey) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pubKey, data, signature)
}

// ParsePublicKeys extracts X25519 and Ed25519 public keys from a 64-byte
// identity public key blob (Reticulum order: [encPub][sigPub]).
func ParsePublicKeys(pub []byte) (*ecdh.PublicKey, ed25519.PublicKey, error) {
	if len(pub) != IdentityKeySize {
		return nil, nil, fmt.Errorf("invalid public key size: expected %d, got %d", IdentityKeySize, len(pub))
	}
	encPub, err := ecdh.X25519().NewPublicKey(pub[:EncryptionPubLen])
	if err != nil {
		return nil, nil, fmt.Errorf("invalid x25519 public key: %w", err)
	}
	sigPub := make(ed25519.PublicKey, SigningPubLen)
	copy(sigPub, pub[EncryptionPubLen:])
	return encPub, sigPub, nil
}
