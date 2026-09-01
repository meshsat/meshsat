package oob

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

const (
	peerIDLabel = "meshsat-oob-peer-id"
	hkdfSalt    = "meshsat-oob-v1"
	hkdfInfo    = "meshsat-oob-mgmt-key"
)

// RandomKey returns a fresh 32-byte management key.
func RandomKey() ([]byte, error) {
	key := make([]byte, KeyLen)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("oob: random key: %w", err)
	}
	return key, nil
}

// PeerIDFromKey derives the 16-bit wire peer id from the key so both sides
// agree on it with nothing extra in the bundle or on the wire. Zero is
// reserved for the Hub and local origins and maps to 1.
func PeerIDFromKey(key []byte) uint16 {
	h := sha256.New()
	h.Write([]byte(peerIDLabel))
	h.Write(key)
	sum := h.Sum(nil)
	id := binary.BigEndian.Uint16(sum[:2])
	if id == 0 {
		id = 1
	}
	return id
}

// RoleForECDH assigns roles on the ECDH path: the lower destination hash is
// the issuer. Both kits compute the same answer from the same two hashes.
func RoleForECDH(myHash, peerHash []byte) Role {
	if bytes.Compare(myHash, peerHash) < 0 {
		return RoleIssuer
	}
	return RoleImporter
}

// DeriveECDHKey derives the management key from an X25519 agreement between
// two routing identities. Both sides sort the two destination hashes into
// the HKDF info so the result is symmetric. See spec section 4.
func DeriveECDHKey(myPriv *ecdh.PrivateKey, peerPub *ecdh.PublicKey, myHash, peerHash []byte) ([]byte, error) {
	if myPriv == nil || peerPub == nil {
		return nil, errors.New("oob: ecdh keys required")
	}
	if len(myHash) == 0 || len(peerHash) == 0 {
		return nil, errors.New("oob: destination hashes required")
	}
	ikm, err := myPriv.ECDH(peerPub)
	if err != nil {
		return nil, fmt.Errorf("oob: ecdh: %w", err)
	}
	lo, hi := myHash, peerHash
	if bytes.Compare(lo, hi) > 0 {
		lo, hi = hi, lo
	}
	info := make([]byte, 0, len(hkdfInfo)+len(lo)+len(hi))
	info = append(info, hkdfInfo...)
	info = append(info, lo...)
	info = append(info, hi...)
	r := hkdf.New(sha256.New, ikm, []byte(hkdfSalt), info)
	key := make([]byte, KeyLen)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("oob: hkdf: %w", err)
	}
	return key, nil
}
