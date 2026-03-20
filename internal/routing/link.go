package routing

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/reticulum"
)

// Link packet type identifiers — re-exported from reticulum for processor dispatch.
const (
	PacketLinkRequest = reticulum.BridgeLinkRequest // 0x10
	PacketLinkProof   = reticulum.BridgeLinkProof   // 0x11
	PacketLinkData    = reticulum.BridgeLinkData    // 0x12
	PacketKeepalive   = reticulum.BridgeKeepalive   // 0x13

	// Wire sizes — re-exported from reticulum.
	LinkIDLen      = reticulum.LinkIDLen      // 32
	LinkRequestLen = reticulum.LinkRequestLen // 65
	LinkProofLen   = reticulum.LinkProofLen   // 129

	// LinkSymKeyLen is the AES-256 key length derived from ECDH.
	LinkSymKeyLen = reticulum.SymKeyLen // 32
)

// Type aliases for wire format types — delegate to reticulum package.
type (
	LinkRequest = reticulum.LinkRequest
	LinkProof   = reticulum.LinkProof
)

// Wire format functions — delegate to reticulum package.
var (
	UnmarshalLinkRequest = reticulum.UnmarshalLinkRequest
	UnmarshalLinkProof   = reticulum.UnmarshalLinkProof
)

// LinkState represents the current state of a link.
type LinkState int

const (
	LinkStatePending     LinkState = iota // request sent, waiting for proof
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
	SendKey      []byte           // AES-256-CBC key for sending (HKDF-derived)
	SendHMAC     []byte           // HMAC-SHA256 key for sending (HKDF-derived)
	RecvKey      []byte           // AES-256-CBC key for receiving (HKDF-derived)
	RecvHMAC     []byte           // HMAC-SHA256 key for receiving (HKDF-derived)
	CreatedAt    time.Time
	LastActivity time.Time
	IsInitiator  bool // true if we initiated the link
}

// LinkManager manages link establishment, tracking, and data encryption.
type LinkManager struct {
	mu       sync.RWMutex
	identity *Identity
	links    map[[LinkIDLen]byte]*Link
	pending  map[[LinkIDLen]byte]*Link // pending link requests (awaiting proof)
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
// Returns the serialized proof packet. The link is established immediately
// on the responder side (2-packet handshake: no confirm needed).
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
	signable := reticulum.LinkProofSignable(linkID, ephKey.PublicKey())
	signature := lm.identity.Sign(signable)

	// Derive symmetric keys via HKDF (responder: key1=recv, key2=send)
	encKey1, hmacKey1, encKey2, hmacKey2 := reticulum.DeriveSymKeys(sharedSecret, linkID)

	now := time.Now()
	link := &Link{
		ID:           linkID,
		DestHash:     [DestHashLen]byte{}, // initiator's identity unknown until path table lookup
		State:        LinkStateEstablished,
		LocalEphKey:  ephKey,
		RemoteEphPub: req.EphemeralPub,
		SharedSecret: sharedSecret,
		RecvKey:      encKey1,  // responder receives on key1
		RecvHMAC:     hmacKey1, // responder HMAC recv on hmacKey1
		SendKey:      encKey2,  // responder sends on key2
		SendHMAC:     hmacKey2, // responder HMAC send on hmacKey2
		CreatedAt:    now,
		LastActivity: now,
		IsInitiator:  false,
	}

	lm.mu.Lock()
	lm.links[linkID] = link
	lm.mu.Unlock()

	proof := &LinkProof{
		LinkID:       linkID,
		EphemeralPub: ephKey.PublicKey(),
		Signature:    signature,
	}

	log.Info().Str("link_id", hashHex32(linkID)).Msg("link request accepted, proof sent")
	return proof.Marshal(), nil
}

// HandleLinkProof processes an incoming link proof for our pending request.
// The signingPub is the destination's known public key (from the destination table).
// On success the link is established (2-packet handshake complete).
func (lm *LinkManager) HandleLinkProof(data []byte, signingPub ed25519.PublicKey) error {
	proof, err := UnmarshalLinkProof(data)
	if err != nil {
		return err
	}

	lm.mu.Lock()
	link, ok := lm.pending[proof.LinkID]
	if !ok {
		lm.mu.Unlock()
		return errors.New("no pending link for this ID")
	}
	delete(lm.pending, proof.LinkID)
	lm.mu.Unlock()

	// Verify signature
	if !proof.Verify(signingPub) {
		return errors.New("link proof signature verification failed")
	}

	// ECDH: our ephemeral × their ephemeral
	sharedSecret, err := link.LocalEphKey.ECDH(proof.EphemeralPub)
	if err != nil {
		return fmt.Errorf("ECDH: %w", err)
	}

	// Derive symmetric keys via HKDF (initiator: key1=send, key2=recv)
	encKey1, hmacKey1, encKey2, hmacKey2 := reticulum.DeriveSymKeys(sharedSecret, proof.LinkID)

	link.RemoteEphPub = proof.EphemeralPub
	link.SharedSecret = sharedSecret
	link.SendKey = encKey1   // initiator sends on key1
	link.SendHMAC = hmacKey1 // initiator HMAC send on hmacKey1
	link.RecvKey = encKey2   // initiator receives on key2
	link.RecvHMAC = hmacKey2 // initiator HMAC recv on hmacKey2
	link.State = LinkStateEstablished
	link.LastActivity = time.Now()

	lm.mu.Lock()
	lm.links[proof.LinkID] = link
	lm.mu.Unlock()

	log.Info().Str("link_id", hashHex32(proof.LinkID)).Msg("link established (initiator)")
	return nil
}

// GetLink returns an established link by ID.
func (lm *LinkManager) GetLink(linkID [LinkIDLen]byte) *Link {
	lm.mu.RLock()
	defer lm.mu.RUnlock()
	return lm.links[linkID]
}

// GetPendingLink returns a pending link by ID (awaiting proof).
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

// Encrypt encrypts data using the link's send key with AES-256-CBC + HMAC-SHA256.
// Wire format: IV(16) + ciphertext(PKCS7-padded) + HMAC(32).
func (l *Link) Encrypt(plaintext []byte) ([]byte, error) {
	if l.State != LinkStateEstablished || len(l.SendKey) != LinkSymKeyLen {
		return nil, errors.New("link not established")
	}
	return reticulum.CBCHMACEncrypt(l.SendKey, l.SendHMAC, plaintext)
}

// Decrypt verifies HMAC and decrypts data using the link's receive key.
// Expects wire format: IV(16) + ciphertext + HMAC(32).
func (l *Link) Decrypt(data []byte) ([]byte, error) {
	if l.State != LinkStateEstablished || len(l.RecvKey) != LinkSymKeyLen {
		return nil, errors.New("link not established")
	}
	return reticulum.CBCHMACDecrypt(l.RecvKey, l.RecvHMAC, data)
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
