package reticulum

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
)

// Link packet wire sizes.
const (
	// LinkRequestLen: type(1) + dest_hash(16) + ephemeral_pub(32) + random(16) = 65.
	LinkRequestLen = 1 + TruncatedHashLen + EncryptionPubLen + 16
	// LinkResponseLen: type(1) + link_id(32) + ephemeral_pub(32) + signature(64) = 129.
	LinkResponseLen = 1 + LinkIDLen + EncryptionPubLen + SignatureLen
	// LinkConfirmLen: type(1) + link_id(32) + proof(32) = 65.
	LinkConfirmLen = 1 + LinkIDLen + 32
)

// LinkRequest is the first packet in the 3-packet handshake.
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

// LinkResponse is the second packet: destination's ephemeral key + signature.
type LinkResponse struct {
	LinkID       [LinkIDLen]byte
	EphemeralPub *ecdh.PublicKey
	Signature    []byte // Ed25519 over (link_id + ephemeral_pub)
}

// Marshal serializes a link response.
func (resp *LinkResponse) Marshal() []byte {
	buf := make([]byte, 0, LinkResponseLen)
	buf = append(buf, BridgeLinkResponse)
	buf = append(buf, resp.LinkID[:]...)
	buf = append(buf, resp.EphemeralPub.Bytes()...)
	buf = append(buf, resp.Signature...)
	return buf
}

// UnmarshalLinkResponse parses a link response.
func UnmarshalLinkResponse(data []byte) (*LinkResponse, error) {
	if len(data) < LinkResponseLen {
		return nil, ErrTooShort
	}
	if data[0] != BridgeLinkResponse {
		return nil, ErrWrongType
	}
	resp := &LinkResponse{}
	pos := 1
	copy(resp.LinkID[:], data[pos:pos+LinkIDLen])
	pos += LinkIDLen
	pub, err := ecdh.X25519().NewPublicKey(data[pos : pos+EncryptionPubLen])
	if err != nil {
		return nil, fmt.Errorf("reticulum: invalid ephemeral key: %w", err)
	}
	resp.EphemeralPub = pub
	pos += EncryptionPubLen
	resp.Signature = make([]byte, ed25519.SignatureSize)
	copy(resp.Signature, data[pos:pos+ed25519.SignatureSize])
	return resp, nil
}

// VerifyLinkResponse verifies the link response signature against a known
// signing public key. The signature covers (link_id + ephemeral_pub).
func (resp *LinkResponse) Verify(signingPub ed25519.PublicKey) bool {
	signable := make([]byte, 0, LinkIDLen+EncryptionPubLen)
	signable = append(signable, resp.LinkID[:]...)
	signable = append(signable, resp.EphemeralPub.Bytes()...)
	return VerifySignature(signingPub, signable, resp.Signature)
}

// LinkConfirm is the third packet: proof that the initiator derived the shared secret.
type LinkConfirm struct {
	LinkID [LinkIDLen]byte
	Proof  [32]byte // SHA-256(shared_secret + link_id + "confirm")
}

// Marshal serializes a link confirmation.
func (lc *LinkConfirm) Marshal() []byte {
	buf := make([]byte, 0, LinkConfirmLen)
	buf = append(buf, BridgeLinkConfirm)
	buf = append(buf, lc.LinkID[:]...)
	buf = append(buf, lc.Proof[:]...)
	return buf
}

// UnmarshalLinkConfirm parses a link confirmation.
func UnmarshalLinkConfirm(data []byte) (*LinkConfirm, error) {
	if len(data) < LinkConfirmLen {
		return nil, ErrTooShort
	}
	if data[0] != BridgeLinkConfirm {
		return nil, ErrWrongType
	}
	lc := &LinkConfirm{}
	pos := 1
	copy(lc.LinkID[:], data[pos:pos+LinkIDLen])
	pos += LinkIDLen
	copy(lc.Proof[:], data[pos:pos+32])
	return lc, nil
}

// ComputeConfirmProof computes SHA-256(shared_secret + link_id + "confirm").
func ComputeConfirmProof(sharedSecret []byte, linkID [LinkIDLen]byte) [32]byte {
	h := sha256.New()
	h.Write(sharedSecret)
	h.Write(linkID[:])
	h.Write([]byte("confirm"))
	var proof [32]byte
	copy(proof[:], h.Sum(nil))
	return proof
}

// DeriveSymKeys derives send and receive AES-256 keys from the ECDH shared secret.
// The initiator uses (key1=send, key2=recv); the responder uses (key1=recv, key2=send).
func DeriveSymKeys(sharedSecret []byte, linkID [LinkIDLen]byte) (key1, key2 []byte) {
	h1 := sha256.New()
	h1.Write(sharedSecret)
	h1.Write(linkID[:])
	h1.Write([]byte("key1"))
	key1 = h1.Sum(nil)

	h2 := sha256.New()
	h2.Write(sharedSecret)
	h2.Write(linkID[:])
	h2.Write([]byte("key2"))
	key2 = h2.Sum(nil)

	return key1, key2
}

// LinkResponseSignable returns the bytes that a link response signature covers:
// link_id(32) + ephemeral_pub(32).
func LinkResponseSignable(linkID [LinkIDLen]byte, ephPub *ecdh.PublicKey) []byte {
	buf := make([]byte, 0, LinkIDLen+EncryptionPubLen)
	buf = append(buf, linkID[:]...)
	buf = append(buf, ephPub.Bytes()...)
	return buf
}
