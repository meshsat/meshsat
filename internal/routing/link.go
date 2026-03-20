package routing

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/reticulum"
)

// Link packet type identifiers — re-exported from reticulum for processor dispatch.
const (
	PacketLinkRequest  = reticulum.BridgeLinkRequest  // 0x10
	PacketLinkResponse = reticulum.BridgeLinkResponse // 0x11
	PacketLinkConfirm  = reticulum.BridgeLinkConfirm  // 0x12
	PacketLinkData     = reticulum.BridgeLinkData     // 0x13
	PacketKeepalive    = reticulum.BridgeKeepalive    // 0x14

	// Wire sizes — re-exported from reticulum.
	LinkIDLen       = reticulum.LinkIDLen       // 32
	LinkRequestLen  = reticulum.LinkRequestLen  // 65
	LinkResponseLen = reticulum.LinkResponseLen // 129
	LinkConfirmLen  = reticulum.LinkConfirmLen  // 65

	// LinkSymKeyLen is the AES-256 key length derived from ECDH.
	LinkSymKeyLen = reticulum.SymKeyLen // 32
)

// Type aliases for wire format types — delegate to reticulum package.
type (
	LinkRequest  = reticulum.LinkRequest
	LinkResponse = reticulum.LinkResponse
	LinkConfirm  = reticulum.LinkConfirm
)

// Wire format functions — delegate to reticulum package.
var (
	UnmarshalLinkRequest  = reticulum.UnmarshalLinkRequest
	UnmarshalLinkResponse = reticulum.UnmarshalLinkResponse
	UnmarshalLinkConfirm  = reticulum.UnmarshalLinkConfirm
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
	if _, err := rand.Read(req.Random[:]); err != nil {
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
	signable := reticulum.LinkResponseSignable(linkID, ephKey.PublicKey())
	signature := lm.identity.Sign(signable)

	// Derive symmetric keys (responder: key1=recv, key2=send)
	key1, key2 := reticulum.DeriveSymKeys(sharedSecret, linkID)

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

	// Verify signature
	if !resp.Verify(signingPub) {
		return nil, errors.New("link response signature verification failed")
	}

	// ECDH: our ephemeral × their ephemeral
	sharedSecret, err := link.LocalEphKey.ECDH(resp.EphemeralPub)
	if err != nil {
		return nil, fmt.Errorf("ECDH: %w", err)
	}

	// Derive symmetric keys (initiator: key1=send, key2=recv)
	key1, key2 := reticulum.DeriveSymKeys(sharedSecret, resp.LinkID)

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
	proof := reticulum.ComputeConfirmProof(sharedSecret, resp.LinkID)
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
	expected := reticulum.ComputeConfirmProof(link.SharedSecret, confirm.LinkID)
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

// GetPendingLink returns a pending link by ID (awaiting response).
func (lm *LinkManager) GetPendingLink(linkID [LinkIDLen]byte) *Link {
	lm.mu.RLock()
	defer lm.mu.RUnlock()
	return lm.pending[linkID]
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
	const hextable = "0123456789abcdef"
	buf := make([]byte, 32)
	for i := 0; i < 16; i++ {
		buf[i*2] = hextable[h[i]>>4]
		buf[i*2+1] = hextable[h[i]&0x0f]
	}
	return string(buf)
}
