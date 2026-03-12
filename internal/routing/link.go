package routing

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Link packet type identifiers.
const (
	PacketLinkRequest  byte = 0x10
	PacketLinkResponse byte = 0x11
	PacketLinkConfirm  byte = 0x12
	PacketLinkData     byte = 0x13

	// Wire sizes.
	LinkIDLen = 32 // SHA-256 of the link request

	// LinkRequestLen: type(1) + dest_hash(16) + ephemeral_pub(32) + random(16) = 65 bytes.
	LinkRequestLen = 1 + DestHashLen + 32 + 16
	// LinkResponseLen: type(1) + link_id(32) + ephemeral_pub(32) + signature(64) = 129 bytes.
	LinkResponseLen = 1 + LinkIDLen + 32 + ed25519.SignatureSize
	// LinkConfirmLen: type(1) + link_id(32) + proof(32) = 65 bytes.
	LinkConfirmLen = 1 + LinkIDLen + 32

	// Total 3-packet handshake: 65 + 129 + 65 = 259 bytes (fits in single Iridium SBD at 340).

	// LinkSymKeyLen is the AES-256 key length derived from ECDH.
	LinkSymKeyLen = 32
)

// LinkState represents the current state of a link.
type LinkState int

const (
	LinkStatePending     LinkState = iota // request sent, waiting for response
	LinkStateEstablished                  // ECDH complete, symmetric keys derived
	LinkStateClosed                       // explicitly closed or timed out
)

// Link represents an established or pending cryptographic link between two nodes.
type Link struct {
	ID           [LinkIDLen]byte   // SHA-256 of link request
	DestHash     [DestHashLen]byte // remote destination
	State        LinkState
	LocalEphKey  *ecdh.PrivateKey // our ephemeral X25519 key
	RemoteEphPub *ecdh.PublicKey  // their ephemeral X25519 key
	SharedSecret []byte           // raw ECDH shared secret
	SendKey      []byte           // AES-256 key for sending (derived)
	RecvKey      []byte           // AES-256 key for receiving (derived)
	SendNonce    uint64           // incrementing nonce for sending
	RecvNonce    uint64           // incrementing nonce for receiving
	CreatedAt    time.Time
	LastActivity time.Time
	IsInitiator  bool // true if we initiated the link
}

// LinkRequest is the first packet in the 3-packet handshake.
type LinkRequest struct {
	DestHash     [DestHashLen]byte
	EphemeralPub *ecdh.PublicKey // fresh X25519 key
	Random       [16]byte
}

// LinkResponse is the second packet: destination's ephemeral key + signature.
type LinkResponse struct {
	LinkID       [LinkIDLen]byte
	EphemeralPub *ecdh.PublicKey
	Signature    []byte // Ed25519 signature over (link_id + ephemeral_pub)
}

// LinkConfirm is the third packet: proof that the initiator derived the shared secret.
type LinkConfirm struct {
	LinkID [LinkIDLen]byte
	Proof  [32]byte // SHA-256(shared_secret + link_id + "confirm")
}

// MarshalLinkRequest serializes a link request to wire format.
func (lr *LinkRequest) Marshal() []byte {
	buf := make([]byte, 0, LinkRequestLen)
	buf = append(buf, PacketLinkRequest)
	buf = append(buf, lr.DestHash[:]...)
	buf = append(buf, lr.EphemeralPub.Bytes()...)
	buf = append(buf, lr.Random[:]...)
	return buf
}

// UnmarshalLinkRequest parses a link request from wire format.
func UnmarshalLinkRequest(data []byte) (*LinkRequest, error) {
	if len(data) < LinkRequestLen {
		return nil, errors.New("link request too short")
	}
	if data[0] != PacketLinkRequest {
		return nil, errors.New("not a link request")
	}
	lr := &LinkRequest{}
	pos := 1
	copy(lr.DestHash[:], data[pos:pos+DestHashLen])
	pos += DestHashLen
	pub, err := ecdh.X25519().NewPublicKey(data[pos : pos+32])
	if err != nil {
		return nil, fmt.Errorf("invalid ephemeral key: %w", err)
	}
	lr.EphemeralPub = pub
	pos += 32
	copy(lr.Random[:], data[pos:pos+16])
	return lr, nil
}

// ComputeLinkID computes the link ID as SHA-256 of the marshaled link request.
func (lr *LinkRequest) ComputeLinkID() [LinkIDLen]byte {
	data := lr.Marshal()
	return sha256.Sum256(data)
}

// MarshalLinkResponse serializes a link response.
func (resp *LinkResponse) Marshal() []byte {
	buf := make([]byte, 0, LinkResponseLen)
	buf = append(buf, PacketLinkResponse)
	buf = append(buf, resp.LinkID[:]...)
	buf = append(buf, resp.EphemeralPub.Bytes()...)
	buf = append(buf, resp.Signature...)
	return buf
}

// UnmarshalLinkResponse parses a link response.
func UnmarshalLinkResponse(data []byte) (*LinkResponse, error) {
	if len(data) < LinkResponseLen {
		return nil, errors.New("link response too short")
	}
	if data[0] != PacketLinkResponse {
		return nil, errors.New("not a link response")
	}
	resp := &LinkResponse{}
	pos := 1
	copy(resp.LinkID[:], data[pos:pos+LinkIDLen])
	pos += LinkIDLen
	pub, err := ecdh.X25519().NewPublicKey(data[pos : pos+32])
	if err != nil {
		return nil, fmt.Errorf("invalid ephemeral key: %w", err)
	}
	resp.EphemeralPub = pub
	pos += 32
	resp.Signature = make([]byte, ed25519.SignatureSize)
	copy(resp.Signature, data[pos:pos+ed25519.SignatureSize])
	return resp, nil
}

// MarshalLinkConfirm serializes a link confirmation.
func (lc *LinkConfirm) Marshal() []byte {
	buf := make([]byte, 0, LinkConfirmLen)
	buf = append(buf, PacketLinkConfirm)
	buf = append(buf, lc.LinkID[:]...)
	buf = append(buf, lc.Proof[:]...)
	return buf
}

// UnmarshalLinkConfirm parses a link confirmation.
func UnmarshalLinkConfirm(data []byte) (*LinkConfirm, error) {
	if len(data) < LinkConfirmLen {
		return nil, errors.New("link confirm too short")
	}
	if data[0] != PacketLinkConfirm {
		return nil, errors.New("not a link confirm")
	}
	lc := &LinkConfirm{}
	pos := 1
	copy(lc.LinkID[:], data[pos:pos+LinkIDLen])
	pos += LinkIDLen
	copy(lc.Proof[:], data[pos:pos+32])
	return lc, nil
}

// computeConfirmProof computes SHA-256(shared_secret + link_id + "confirm").
func computeConfirmProof(sharedSecret []byte, linkID [LinkIDLen]byte) [32]byte {
	h := sha256.New()
	h.Write(sharedSecret)
	h.Write(linkID[:])
	h.Write([]byte("confirm"))
	var proof [32]byte
	copy(proof[:], h.Sum(nil))
	return proof
}

// deriveSymKeys derives send and receive AES-256 keys from the shared secret.
// The initiator uses (key1=send, key2=recv); the responder uses (key1=recv, key2=send).
func deriveSymKeys(sharedSecret []byte, linkID [LinkIDLen]byte) (key1, key2 []byte) {
	// Key 1: SHA-256(shared_secret + link_id + "key1")
	h1 := sha256.New()
	h1.Write(sharedSecret)
	h1.Write(linkID[:])
	h1.Write([]byte("key1"))
	key1 = h1.Sum(nil)

	// Key 2: SHA-256(shared_secret + link_id + "key2")
	h2 := sha256.New()
	h2.Write(sharedSecret)
	h2.Write(linkID[:])
	h2.Write([]byte("key2"))
	key2 = h2.Sum(nil)

	return key1, key2
}

// LinkManager manages link establishment, tracking, and data encryption.
type LinkManager struct {
	mu       sync.RWMutex
	identity *Identity
	links    map[[LinkIDLen]byte]*Link
	pending  map[[LinkIDLen]byte]*Link // pending link requests (awaiting response)
}

// NewLinkManager creates a link manager for establishing encrypted links.
func NewLinkManager(identity *Identity) *LinkManager {
	return &LinkManager{
		identity: identity,
		links:    make(map[[LinkIDLen]byte]*Link),
		pending:  make(map[[LinkIDLen]byte]*Link),
	}
}

// InitiateLink creates a link request to the given destination.
// Returns the serialized request packet and the pending link.
func (lm *LinkManager) InitiateLink(destHash [DestHashLen]byte) ([]byte, *Link, error) {
	ephKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate ephemeral key: %w", err)
	}

	req := &LinkRequest{
		DestHash:     destHash,
		EphemeralPub: ephKey.PublicKey(),
	}
	if _, err := io.ReadFull(rand.Reader, req.Random[:]); err != nil {
		return nil, nil, fmt.Errorf("generate random: %w", err)
	}

	linkID := req.ComputeLinkID()
	now := time.Now()

	link := &Link{
		ID:           linkID,
		DestHash:     destHash,
		State:        LinkStatePending,
		LocalEphKey:  ephKey,
		CreatedAt:    now,
		LastActivity: now,
		IsInitiator:  true,
	}

	lm.mu.Lock()
	lm.pending[linkID] = link
	lm.mu.Unlock()

	log.Info().Str("link_id", hashHex32(linkID)).
		Str("dest", hashHex(destHash)).
		Msg("link request initiated")

	return req.Marshal(), link, nil
}

// HandleLinkRequest processes an incoming link request addressed to us.
// Returns the serialized response packet or nil if rejected.
func (lm *LinkManager) HandleLinkRequest(data []byte) ([]byte, error) {
	req, err := UnmarshalLinkRequest(data)
	if err != nil {
		return nil, err
	}

	// Verify this request is addressed to us
	if req.DestHash != lm.identity.DestHash() {
		return nil, errors.New("link request not addressed to us")
	}

	linkID := req.ComputeLinkID()

	// Generate our ephemeral key
	ephKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ephemeral key: %w", err)
	}

	// ECDH: our ephemeral × their ephemeral
	sharedSecret, err := ephKey.ECDH(req.EphemeralPub)
	if err != nil {
		return nil, fmt.Errorf("ECDH: %w", err)
	}

	// Sign: link_id + our ephemeral pub
	signable := make([]byte, 0, LinkIDLen+32)
	signable = append(signable, linkID[:]...)
	signable = append(signable, ephKey.PublicKey().Bytes()...)
	signature := lm.identity.Sign(signable)

	// Derive symmetric keys (responder: key1=recv, key2=send)
	key1, key2 := deriveSymKeys(sharedSecret, linkID)

	now := time.Now()
	link := &Link{
		ID:           linkID,
		DestHash:     [DestHashLen]byte{}, // initiator's identity unknown until path table lookup
		State:        LinkStateEstablished,
		LocalEphKey:  ephKey,
		RemoteEphPub: req.EphemeralPub,
		SharedSecret: sharedSecret,
		RecvKey:      key1, // responder receives on key1
		SendKey:      key2, // responder sends on key2
		CreatedAt:    now,
		LastActivity: now,
		IsInitiator:  false,
	}

	lm.mu.Lock()
	lm.links[linkID] = link
	lm.mu.Unlock()

	resp := &LinkResponse{
		LinkID:       linkID,
		EphemeralPub: ephKey.PublicKey(),
		Signature:    signature,
	}

	log.Info().Str("link_id", hashHex32(linkID)).Msg("link request accepted, response sent")
	return resp.Marshal(), nil
}

// HandleLinkResponse processes an incoming link response to our pending request.
// The signingPub is the destination's known public key (from the destination table).
// Returns the serialized confirm packet or an error.
func (lm *LinkManager) HandleLinkResponse(data []byte, signingPub ed25519.PublicKey) ([]byte, error) {
	resp, err := UnmarshalLinkResponse(data)
	if err != nil {
		return nil, err
	}

	lm.mu.Lock()
	link, ok := lm.pending[resp.LinkID]
	if !ok {
		lm.mu.Unlock()
		return nil, errors.New("no pending link for this ID")
	}
	delete(lm.pending, resp.LinkID)
	lm.mu.Unlock()

	// Verify signature: Ed25519(signing_key, link_id + ephemeral_pub)
	signable := make([]byte, 0, LinkIDLen+32)
	signable = append(signable, resp.LinkID[:]...)
	signable = append(signable, resp.EphemeralPub.Bytes()...)
	if !VerifyWith(signingPub, signable, resp.Signature) {
		return nil, errors.New("link response signature verification failed")
	}

	// ECDH: our ephemeral × their ephemeral
	sharedSecret, err := link.LocalEphKey.ECDH(resp.EphemeralPub)
	if err != nil {
		return nil, fmt.Errorf("ECDH: %w", err)
	}

	// Derive symmetric keys (initiator: key1=send, key2=recv)
	key1, key2 := deriveSymKeys(sharedSecret, resp.LinkID)

	link.RemoteEphPub = resp.EphemeralPub
	link.SharedSecret = sharedSecret
	link.SendKey = key1 // initiator sends on key1
	link.RecvKey = key2 // initiator receives on key2
	link.State = LinkStateEstablished
	link.LastActivity = time.Now()

	lm.mu.Lock()
	lm.links[resp.LinkID] = link
	lm.mu.Unlock()

	// Build confirm proof
	proof := computeConfirmProof(sharedSecret, resp.LinkID)
	confirm := &LinkConfirm{
		LinkID: resp.LinkID,
		Proof:  proof,
	}

	log.Info().Str("link_id", hashHex32(resp.LinkID)).Msg("link established (initiator)")
	return confirm.Marshal(), nil
}

// HandleLinkConfirm processes an incoming link confirmation.
// Verifies the proof matches the shared secret.
func (lm *LinkManager) HandleLinkConfirm(data []byte) error {
	confirm, err := UnmarshalLinkConfirm(data)
	if err != nil {
		return err
	}

	lm.mu.RLock()
	link, ok := lm.links[confirm.LinkID]
	lm.mu.RUnlock()

	if !ok {
		return errors.New("no link for this ID")
	}
	if link.State != LinkStateEstablished {
		return errors.New("link not in established state")
	}

	// Verify proof
	expected := computeConfirmProof(link.SharedSecret, confirm.LinkID)
	if confirm.Proof != expected {
		return errors.New("link confirm proof mismatch")
	}

	link.LastActivity = time.Now()
	log.Info().Str("link_id", hashHex32(confirm.LinkID)).Msg("link confirmed (responder)")
	return nil
}

// GetLink returns an established link by ID.
func (lm *LinkManager) GetLink(linkID [LinkIDLen]byte) *Link {
	lm.mu.RLock()
	defer lm.mu.RUnlock()
	return lm.links[linkID]
}

// ActiveLinks returns all established links.
func (lm *LinkManager) ActiveLinks() []*Link {
	lm.mu.RLock()
	defer lm.mu.RUnlock()
	result := make([]*Link, 0)
	for _, link := range lm.links {
		if link.State == LinkStateEstablished {
			result = append(result, link)
		}
	}
	return result
}

// CloseLink closes a link by ID.
func (lm *LinkManager) CloseLink(linkID [LinkIDLen]byte) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	if link, ok := lm.links[linkID]; ok {
		link.State = LinkStateClosed
	}
	delete(lm.links, linkID)
	delete(lm.pending, linkID)
}

// Encrypt encrypts data using the link's send key with AES-256-GCM.
// Returns the encrypted data with nonce prepended.
func (l *Link) Encrypt(plaintext []byte) ([]byte, error) {
	if l.State != LinkStateEstablished || len(l.SendKey) != LinkSymKeyLen {
		return nil, errors.New("link not established")
	}

	block, err := aes.NewCipher(l.SendKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// Nonce: 8-byte counter + 4-byte zero padding (GCM standard nonce = 12 bytes)
	nonce := make([]byte, aead.NonceSize())
	binary.BigEndian.PutUint64(nonce, l.SendNonce)
	l.SendNonce++

	ciphertext := aead.Seal(nil, nonce, plaintext, nil)

	// Prepend nonce to ciphertext
	result := make([]byte, len(nonce)+len(ciphertext))
	copy(result, nonce)
	copy(result[len(nonce):], ciphertext)
	return result, nil
}

// Decrypt decrypts data using the link's receive key with AES-256-GCM.
// Expects nonce prepended to ciphertext.
func (l *Link) Decrypt(data []byte) ([]byte, error) {
	if l.State != LinkStateEstablished || len(l.RecvKey) != LinkSymKeyLen {
		return nil, errors.New("link not established")
	}

	block, err := aes.NewCipher(l.RecvKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	if len(data) < aead.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}

	nonce := data[:aead.NonceSize()]
	ciphertext := data[aead.NonceSize():]

	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	l.RecvNonce++
	return plaintext, nil
}

func hashHex32(h [32]byte) string {
	// Show first 16 bytes (32 hex chars) for readability
	const hextable = "0123456789abcdef"
	buf := make([]byte, 32)
	for i := 0; i < 16; i++ {
		buf[i*2] = hextable[h[i]>>4]
		buf[i*2+1] = hextable[h[i]&0x0f]
	}
	return string(buf)
}
