package reticulum

import (
	"crypto/sha256"
	"io"

	"golang.org/x/crypto/hkdf"
)

// HKDF derives length bytes with HKDF-SHA256 (RFC 5869) exactly as
// RNS.Cryptography.hkdf does: an empty or nil salt becomes 32 zero bytes and
// a nil info (RNS "context") is the empty string.
func HKDF(length int, ikm, salt, info []byte) []byte {
	if len(salt) == 0 {
		salt = make([]byte, sha256.Size)
	}
	out := make([]byte, length)
	if _, err := io.ReadFull(hkdf.New(sha256.New, ikm, salt, info), out); err != nil {
		panic("reticulum: hkdf: " + err.Error()) // only on length > 255*32, never for our sizes
	}
	return out
}
