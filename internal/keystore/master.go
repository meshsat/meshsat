package keystore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

const (
	masterKeyLen = 32 // AES-256
	saltLen      = 16
	nonceLen     = 12 // AES-GCM nonce
)

// hkdfExtract computes HKDF-Extract: PRK = HMAC-SHA256(salt, IKM).
func hkdfExtract(salt, ikm []byte) []byte {
	mac := hmac.New(sha256.New, salt)
	mac.Write(ikm)
	return mac.Sum(nil)
}

// hkdfExpand computes HKDF-Expand to derive output keying material from PRK.
func hkdfExpand(prk, info []byte, length int) []byte {
	hashLen := sha256.Size
	n := (length + hashLen - 1) / hashLen
	okm := make([]byte, 0, n*hashLen)
	var prev []byte
	for i := 1; i <= n; i++ {
		mac := hmac.New(sha256.New, prk)
		mac.Write(prev)
		mac.Write(info)
		mac.Write([]byte{byte(i)})
		prev = mac.Sum(nil)
		okm = append(okm, prev...)
	}
	return okm[:length]
}

// deriveMasterWrappingKey derives a wrapping key for the master key.
// If passphrase is set, uses HKDF(passphrase, salt).
// Otherwise, derives from device identity (hostname + db path).
func deriveMasterWrappingKey(passphrase string, salt []byte) []byte {
	if passphrase != "" {
		prk := hkdfExtract(salt, []byte(passphrase))
		return hkdfExpand(prk, []byte("meshsat.keystore.master.v1"), masterKeyLen)
	}
	// Device-derived: hostname + db path (weaker, but protects against casual disk theft)
	hostname, _ := os.Hostname()
	dbPath := os.Getenv("MESHSAT_DB_PATH")
	if dbPath == "" {
		dbPath = "/cubeos/data/meshsat.db"
	}
	ikm := []byte(hostname + ":" + dbPath + ":meshsat.keystore.v1")
	prk := hkdfExtract(salt, ikm)
	return hkdfExpand(prk, []byte("meshsat.keystore.device.v1"), masterKeyLen)
}

// wrapKey encrypts a raw key with the wrapping key using AES-256-GCM.
// Output: [12-byte nonce][ciphertext+tag]
func wrapKey(wrappingKey, rawKey []byte) ([]byte, error) {
	block, err := aes.NewCipher(wrappingKey)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, rawKey, []byte("meshsat.keystore"))
	return append(nonce, ciphertext...), nil
}

// unwrapKey decrypts a wrapped key with the wrapping key.
func unwrapKey(wrappingKey, wrapped []byte) ([]byte, error) {
	if len(wrapped) < nonceLen+16 { // nonce + at least GCM tag
		return nil, fmt.Errorf("wrapped key too short")
	}
	block, err := aes.NewCipher(wrappingKey)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	nonce := wrapped[:nonceLen]
	ciphertext := wrapped[nonceLen:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte("meshsat.keystore"))
	if err != nil {
		return nil, fmt.Errorf("unwrap: %w", err)
	}
	return plaintext, nil
}

// bootstrapMasterKey generates a new master key, wraps it, and returns both.
func bootstrapMasterKey(passphrase string) (masterKey []byte, salt []byte, wrappedMaster string, err error) {
	masterKey = make([]byte, masterKeyLen)
	if _, err = io.ReadFull(rand.Reader, masterKey); err != nil {
		return nil, nil, "", fmt.Errorf("generate master key: %w", err)
	}
	salt = make([]byte, saltLen)
	if _, err = io.ReadFull(rand.Reader, salt); err != nil {
		return nil, nil, "", fmt.Errorf("generate salt: %w", err)
	}
	wrappingKey := deriveMasterWrappingKey(passphrase, salt)
	wrapped, err := wrapKey(wrappingKey, masterKey)
	if err != nil {
		return nil, nil, "", fmt.Errorf("wrap master key: %w", err)
	}
	return masterKey, salt, hex.EncodeToString(wrapped), nil
}

// loadMasterKey unwraps a stored master key.
func loadMasterKey(passphrase, wrappedHex, saltHex string) ([]byte, error) {
	wrapped, err := hex.DecodeString(wrappedHex)
	if err != nil {
		return nil, fmt.Errorf("decode wrapped key: %w", err)
	}
	salt, err := hex.DecodeString(saltHex)
	if err != nil {
		return nil, fmt.Errorf("decode salt: %w", err)
	}
	wrappingKey := deriveMasterWrappingKey(passphrase, salt)
	return unwrapKey(wrappingKey, wrapped)
}
