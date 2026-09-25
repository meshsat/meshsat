package reticulum

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
)

// Token is RNS.Cryptography.Token: a Fernet-style token without the version
// and timestamp fields. Wire layout: iv(16) || AES-CBC(PKCS7(plaintext)) ||
// HMAC-SHA256(iv || ciphertext)(32). A 64-byte key selects AES-256-CBC with
// signing key = key[:32] and encryption key = key[32:]; a 32-byte key selects
// AES-128-CBC with 16-byte halves. The signing half comes FIRST.
type Token struct {
	signingKey    []byte
	encryptionKey []byte
}

// ErrTokenKeySize is returned for keys that are neither 32 nor 64 bytes.
var ErrTokenKeySize = errors.New("reticulum: token key must be 32 or 64 bytes")

// NewToken builds a Token from a 32- or 64-byte key.
func NewToken(key []byte) (*Token, error) {
	switch len(key) {
	case 32:
		return &Token{signingKey: key[:16], encryptionKey: key[16:]}, nil
	case 64:
		return &Token{signingKey: key[:32], encryptionKey: key[32:]}, nil
	default:
		return nil, ErrTokenKeySize
	}
}

// Encrypt returns iv || ciphertext || hmac for data.
func (t *Token) Encrypt(data []byte) ([]byte, error) {
	block, err := aes.NewCipher(t.encryptionKey)
	if err != nil {
		return nil, err
	}
	padLen := aes.BlockSize - (len(data) % aes.BlockSize)
	padded := make([]byte, len(data)+padLen)
	copy(padded, data)
	for i := len(data); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}
	out := make([]byte, aes.BlockSize+len(padded), aes.BlockSize+len(padded)+HMACLen)
	if _, err := io.ReadFull(rand.Reader, out[:aes.BlockSize]); err != nil {
		return nil, fmt.Errorf("generate IV: %w", err)
	}
	cipher.NewCBCEncrypter(block, out[:aes.BlockSize]).CryptBlocks(out[aes.BlockSize:], padded)
	mac := hmac.New(sha256.New, t.signingKey)
	mac.Write(out)
	return mac.Sum(out), nil
}

// VerifyHMAC reports whether the token's trailing HMAC is valid.
func (t *Token) VerifyHMAC(token []byte) bool {
	if len(token) <= HMACLen {
		return false
	}
	mac := hmac.New(sha256.New, t.signingKey)
	mac.Write(token[:len(token)-HMACLen])
	return hmac.Equal(token[len(token)-HMACLen:], mac.Sum(nil))
}

// Decrypt verifies the HMAC and returns the plaintext.
func (t *Token) Decrypt(token []byte) ([]byte, error) {
	if len(token) < aes.BlockSize+aes.BlockSize+HMACLen {
		return nil, fmt.Errorf("reticulum: token too short (%d bytes)", len(token))
	}
	if !t.VerifyHMAC(token) {
		return nil, errors.New("reticulum: token HMAC was invalid")
	}
	iv := token[:aes.BlockSize]
	ct := token[aes.BlockSize : len(token)-HMACLen]
	if len(ct)%aes.BlockSize != 0 {
		return nil, errors.New("reticulum: token ciphertext not block-aligned")
	}
	block, err := aes.NewCipher(t.encryptionKey)
	if err != nil {
		return nil, err
	}
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(pt, ct)
	padLen := int(pt[len(pt)-1])
	if padLen == 0 || padLen > aes.BlockSize || padLen > len(pt) {
		return nil, errors.New("reticulum: invalid PKCS7 padding")
	}
	for i := len(pt) - padLen; i < len(pt); i++ {
		if pt[i] != byte(padLen) {
			return nil, errors.New("reticulum: invalid PKCS7 padding")
		}
	}
	return pt[:len(pt)-padLen], nil
}
