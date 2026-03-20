package reticulum

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
)

// Link packet wire sizes.
const (
	// LinkRequestLen: type(1) + dest_hash(16) + ephemeral_pub(32) + random(16) = 65.
	LinkRequestLen = 1 + TruncatedHashLen + EncryptionPubLen + 16
	// LinkProofLen: type(1) + link_id(32) + ephemeral_pub(32) + signature(64) = 129.
	LinkProofLen = 1 + LinkIDLen + EncryptionPubLen + SignatureLen
)

// LinkRequest is the first packet in the 2-packet handshake.
type LinkRequest struct {
	DestHash     [DestHashLen]byte
	EphemeralPub *ecdh.PublicKey // fresh X25519 key
	Random       [16]byte
}

// Marshal serializes a link request to wire format.
func (lr *LinkRequest) Marshal() []byte {
	buf := make([]byte, 0, LinkRequestLen)
	buf = append(buf, BridgeLinkRequest)
	buf = append(buf, lr.DestHash[:]...)
	buf = append(buf, lr.EphemeralPub.Bytes()...)
	buf = append(buf, lr.Random[:]...)
	return buf
}

// UnmarshalLinkRequest parses a link request from wire format.
func UnmarshalLinkRequest(data []byte) (*LinkRequest, error) {
	if len(data) < LinkRequestLen {
		return nil, ErrTooShort
	}
	if data[0] != BridgeLinkRequest {
		return nil, ErrWrongType
	}
	lr := &LinkRequest{}
	pos := 1
	copy(lr.DestHash[:], data[pos:pos+DestHashLen])
	pos += DestHashLen
	pub, err := ecdh.X25519().NewPublicKey(data[pos : pos+EncryptionPubLen])
	if err != nil {
		return nil, fmt.Errorf("reticulum: invalid ephemeral key: %w", err)
	}
	lr.EphemeralPub = pub
	pos += EncryptionPubLen
	copy(lr.Random[:], data[pos:pos+16])
	return lr, nil
}

// ComputeLinkID computes the link ID as SHA-256 of the marshaled link request.
func (lr *LinkRequest) ComputeLinkID() [LinkIDLen]byte {
	return sha256.Sum256(lr.Marshal())
}

// LinkProof is the second (and final) packet: responder's ephemeral key + signature.
// This completes the 2-packet Reticulum link handshake.
type LinkProof struct {
	LinkID       [LinkIDLen]byte
	EphemeralPub *ecdh.PublicKey
	Signature    []byte // Ed25519 over (link_id + ephemeral_pub)
}

// Marshal serializes a link proof.
func (lp *LinkProof) Marshal() []byte {
	buf := make([]byte, 0, LinkProofLen)
	buf = append(buf, BridgeLinkProof)
	buf = append(buf, lp.LinkID[:]...)
	buf = append(buf, lp.EphemeralPub.Bytes()...)
	buf = append(buf, lp.Signature...)
	return buf
}

// UnmarshalLinkProof parses a link proof.
func UnmarshalLinkProof(data []byte) (*LinkProof, error) {
	if len(data) < LinkProofLen {
		return nil, ErrTooShort
	}
	if data[0] != BridgeLinkProof {
		return nil, ErrWrongType
	}
	lp := &LinkProof{}
	pos := 1
	copy(lp.LinkID[:], data[pos:pos+LinkIDLen])
	pos += LinkIDLen
	pub, err := ecdh.X25519().NewPublicKey(data[pos : pos+EncryptionPubLen])
	if err != nil {
		return nil, fmt.Errorf("reticulum: invalid ephemeral key: %w", err)
	}
	lp.EphemeralPub = pub
	pos += EncryptionPubLen
	lp.Signature = make([]byte, ed25519.SignatureSize)
	copy(lp.Signature, data[pos:pos+ed25519.SignatureSize])
	return lp, nil
}

// Verify checks the link proof signature against a known signing public key.
// The signature covers (link_id + ephemeral_pub).
func (lp *LinkProof) Verify(signingPub ed25519.PublicKey) bool {
	signable := LinkProofSignable(lp.LinkID, lp.EphemeralPub)
	return VerifySignature(signingPub, signable, lp.Signature)
}

// LinkProofSignable returns the bytes that a link proof signature covers:
// link_id(32) + ephemeral_pub(32).
func LinkProofSignable(linkID [LinkIDLen]byte, ephPub *ecdh.PublicKey) []byte {
	buf := make([]byte, 0, LinkIDLen+EncryptionPubLen)
	buf = append(buf, linkID[:]...)
	buf = append(buf, ephPub.Bytes()...)
	return buf
}

// ---------------------------------------------------------------------------
// HKDF-SHA256 key derivation (RFC 5869)
// ---------------------------------------------------------------------------

// hkdfExtract computes HKDF-Extract: PRK = HMAC-SHA256(salt, IKM).
func hkdfExtract(salt, ikm []byte) []byte {
	mac := hmac.New(sha256.New, salt)
	mac.Write(ikm)
	return mac.Sum(nil)
}

// hkdfExpand computes HKDF-Expand: derives output keying material from PRK.
func hkdfExpand(prk, info []byte, length int) []byte {
	mac := hmac.New(sha256.New, prk)
	var prev []byte
	result := make([]byte, 0, length)
	counter := byte(1)
	for len(result) < length {
		mac.Reset()
		mac.Write(prev)
		mac.Write(info)
		mac.Write([]byte{counter})
		prev = mac.Sum(nil)
		result = append(result, prev...)
		counter++
	}
	return result[:length]
}

// DeriveSymKeys derives four AES-256 and HMAC-SHA256 keys from the ECDH
// shared secret using HKDF-SHA256. Returns (encKey1, hmacKey1, encKey2, hmacKey2).
//
// The initiator uses (encKey1, hmacKey1) for sending and (encKey2, hmacKey2)
// for receiving. The responder reverses the assignment.
func DeriveSymKeys(sharedSecret []byte, linkID [LinkIDLen]byte) (encKey1, hmacKey1, encKey2, hmacKey2 []byte) {
	prk := hkdfExtract(linkID[:], sharedSecret)

	encKey1 = hkdfExpand(prk, []byte("meshsat.link.enc1"), SymKeyLen)
	hmacKey1 = hkdfExpand(prk, []byte("meshsat.link.hmac1"), HMACLen)
	encKey2 = hkdfExpand(prk, []byte("meshsat.link.enc2"), SymKeyLen)
	hmacKey2 = hkdfExpand(prk, []byte("meshsat.link.hmac2"), HMACLen)
	return
}

// ---------------------------------------------------------------------------
// AES-256-CBC + HMAC-SHA256 encrypt-then-MAC
// ---------------------------------------------------------------------------

// CBCHMACEncrypt encrypts plaintext with AES-256-CBC and appends HMAC-SHA256.
// Wire format: IV(16) + ciphertext(N, PKCS7-padded) + HMAC(32).
func CBCHMACEncrypt(encKey, hmacKey, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, err
	}

	// PKCS7 pad
	padLen := aes.BlockSize - (len(plaintext) % aes.BlockSize)
	padded := make([]byte, len(plaintext)+padLen)
	copy(padded, plaintext)
	for i := len(plaintext); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}

	// Random IV
	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, fmt.Errorf("generate IV: %w", err)
	}

	// CBC encrypt
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)

	// Assemble: IV + ciphertext
	result := make([]byte, 0, aes.BlockSize+len(ciphertext)+HMACLen)
	result = append(result, iv...)
	result = append(result, ciphertext...)

	// HMAC-SHA256 over IV + ciphertext (encrypt-then-MAC)
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write(result) // covers IV + ciphertext
	result = append(result, mac.Sum(nil)...)

	return result, nil
}

// CBCHMACDecrypt verifies HMAC-SHA256 and decrypts AES-256-CBC ciphertext.
// Expects wire format: IV(16) + ciphertext(N) + HMAC(32).
func CBCHMACDecrypt(encKey, hmacKey, data []byte) ([]byte, error) {
	minLen := aes.BlockSize + aes.BlockSize + HMACLen // IV + at least 1 block + HMAC
	if len(data) < minLen {
		return nil, fmt.Errorf("reticulum: ciphertext too short (%d < %d)", len(data), minLen)
	}

	// Split: IV | ciphertext | HMAC tag
	tagStart := len(data) - HMACLen
	ivAndCT := data[:tagStart]
	tag := data[tagStart:]

	// Verify HMAC first (encrypt-then-MAC: verify before decrypt)
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write(ivAndCT)
	expected := mac.Sum(nil)
	if !hmac.Equal(tag, expected) {
		return nil, fmt.Errorf("reticulum: HMAC verification failed")
	}

	iv := ivAndCT[:aes.BlockSize]
	ciphertext := ivAndCT[aes.BlockSize:]

	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("reticulum: ciphertext not block-aligned")
	}

	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, err
	}

	// CBC decrypt
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, ciphertext)

	// PKCS7 unpad
	if len(plaintext) == 0 {
		return nil, fmt.Errorf("reticulum: empty plaintext after decrypt")
	}
	padLen := int(plaintext[len(plaintext)-1])
	if padLen == 0 || padLen > aes.BlockSize || padLen > len(plaintext) {
		return nil, fmt.Errorf("reticulum: invalid PKCS7 padding")
	}
	for i := len(plaintext) - padLen; i < len(plaintext); i++ {
		if plaintext[i] != byte(padLen) {
			return nil, fmt.Errorf("reticulum: invalid PKCS7 padding")
		}
	}
	return plaintext[:len(plaintext)-padLen], nil
}
