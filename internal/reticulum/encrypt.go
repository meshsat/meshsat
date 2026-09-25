package reticulum

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
)

// DerivedKeyLen is the HKDF output length for single-packet and link keys
// (AES-256-CBC Token: 32-byte signing key + 32-byte encryption key).
const DerivedKeyLen = 64

// EncryptedMDU is the largest plaintext that fits one encrypted packet at
// MTU 500: floor((MDU - TokenOverhead - 32) / 16) * 16 - 1 = 383.
// Reference: RNS/Packet.py ENCRYPTED_MDU.
const EncryptedMDU = ((MDU-TokenOverhead-EncryptionPubLen)/AES128BlockSize)*AES128BlockSize - 1

// ErrDecrypt is returned when no key decrypts a token.
var ErrDecrypt = errors.New("reticulum: decryption failed")

// PrivateBytes returns the identity's private key in RNS.Identity.from_bytes
// order: x25519 private (32) || ed25519 seed (32).
func (id *Identity) PrivateBytes() []byte {
	out := make([]byte, 0, 64)
	out = append(out, id.encryptionKey.Bytes()...)
	out = append(out, id.signingKey.Seed()...)
	return out
}

// IdentityFromPrivateBytes is RNS.Identity.from_bytes: x25519 prv (32) ||
// ed25519 seed (32).
func IdentityFromPrivateBytes(prv []byte) (*Identity, error) {
	if len(prv) != 64 {
		return nil, fmt.Errorf("reticulum: private key must be 64 bytes, got %d", len(prv))
	}
	sig := ed25519.NewKeyFromSeed(prv[32:])
	return LoadIdentity(prv[:32], sig)
}

// IdentityFromPublicBytes builds a public-only identity from the 64-byte
// announce public key. It can encrypt to and verify, but not sign or decrypt.
func IdentityFromPublicBytes(pub []byte) (*Identity, error) {
	encPub, sigPub, err := ParsePublicKeys(pub)
	if err != nil {
		return nil, err
	}
	return &Identity{encryptionPub: encPub, signingPub: sigPub}, nil
}

// HasPrivateKey reports whether the identity can sign and decrypt.
func (id *Identity) HasPrivateKey() bool { return id.encryptionKey != nil && id.signingKey != nil }

// Encrypt produces the data field of an encrypted DATA packet to this
// identity: ephemeral x25519 pub (32) || Token(HKDF(shared, salt=identity
// hash)). When ratchet is not nil the ECDH runs against the destination's
// latest announced ratchet key instead of its identity key.
// Reference: RNS/Identity.py encrypt.
func (id *Identity) Encrypt(plaintext []byte, ratchet *ecdh.PublicKey) ([]byte, error) {
	if id.encryptionPub == nil {
		return nil, errors.New("reticulum: identity holds no public key")
	}
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	target := id.encryptionPub
	if ratchet != nil {
		target = ratchet
	}
	shared, err := eph.ECDH(target)
	if err != nil {
		return nil, err
	}
	salt := id.IdentityHash()
	tok, err := NewToken(HKDF(DerivedKeyLen, shared, salt[:], nil))
	if err != nil {
		return nil, err
	}
	ct, err := tok.Encrypt(plaintext)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, EncryptionPubLen+len(ct))
	out = append(out, eph.PublicKey().Bytes()...)
	return append(out, ct...), nil
}

// Decrypt reverses Encrypt. ratchetPrivs are this identity's own ratchet
// private keys, newest first; they are tried before the identity key.
// Returns the plaintext and the index of the ratchet that worked (-1 for
// the identity key).
// Reference: RNS/Identity.py decrypt.
func (id *Identity) Decrypt(data []byte, ratchetPrivs [][]byte) ([]byte, int, error) {
	if id.encryptionKey == nil {
		return nil, -1, errors.New("reticulum: identity holds no private key")
	}
	if len(data) <= EncryptionPubLen {
		return nil, -1, ErrTooShort
	}
	peerPub, err := ecdh.X25519().NewPublicKey(data[:EncryptionPubLen])
	if err != nil {
		return nil, -1, fmt.Errorf("reticulum: bad ephemeral key: %w", err)
	}
	ct := data[EncryptionPubLen:]
	salt := id.IdentityHash()
	try := func(prv *ecdh.PrivateKey) ([]byte, bool) {
		shared, err := prv.ECDH(peerPub)
		if err != nil {
			return nil, false
		}
		tok, err := NewToken(HKDF(DerivedKeyLen, shared, salt[:], nil))
		if err != nil {
			return nil, false
		}
		pt, err := tok.Decrypt(ct)
		return pt, err == nil
	}
	for i, r := range ratchetPrivs {
		prv, err := ecdh.X25519().NewPrivateKey(r)
		if err != nil {
			continue
		}
		if pt, ok := try(prv); ok {
			return pt, i, nil
		}
	}
	if pt, ok := try(id.encryptionKey); ok {
		return pt, -1, nil
	}
	return nil, -1, ErrDecrypt
}

// GenerateRatchet returns a fresh x25519 ratchet private key (32 bytes).
func GenerateRatchet() ([]byte, error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return k.Bytes(), nil
}

// RatchetPublic returns the public key of a ratchet private key.
func RatchetPublic(prv []byte) (*ecdh.PublicKey, error) {
	k, err := ecdh.X25519().NewPrivateKey(prv)
	if err != nil {
		return nil, err
	}
	return k.PublicKey(), nil
}
