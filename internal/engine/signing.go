package engine

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// SigningService provides Ed25519 message signing and hash-chain audit logging.
type SigningService struct {
	mu         sync.Mutex
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	signerID   string // hex-encoded public key
	db         *database.DB
	lastHash   string // last audit log hash for chain continuity
}

// NewSigningService creates a signing service. If no keypair exists in system_config,
// generates a new one and persists it.
func NewSigningService(db *database.DB) (*SigningService, error) {
	ss := &SigningService{db: db}

	// Try to load existing keypair from system_config
	privHex, _ := db.GetSystemConfig("signing_private_key")
	pubHex, _ := db.GetSystemConfig("signing_public_key")

	if privHex != "" && pubHex != "" {
		privBytes, err := hex.DecodeString(privHex)
		if err != nil || len(privBytes) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("invalid stored private key")
		}
		ss.privateKey = ed25519.PrivateKey(privBytes)
		ss.publicKey = ss.privateKey.Public().(ed25519.PublicKey)
	} else {
		// Generate new keypair
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generate ed25519 key: %w", err)
		}
		ss.privateKey = priv
		ss.publicKey = pub

		// Persist to system_config
		if err := db.SetSystemConfig("signing_private_key", hex.EncodeToString(priv)); err != nil {
			return nil, fmt.Errorf("persist private key: %w", err)
		}
		if err := db.SetSystemConfig("signing_public_key", hex.EncodeToString(pub)); err != nil {
			return nil, fmt.Errorf("persist public key: %w", err)
		}
	}

	ss.signerID = hex.EncodeToString(ss.publicKey)

	// Load last audit hash for chain continuity
	entries, err := db.GetAuditLog(1)
	if err == nil && len(entries) > 0 {
		ss.lastHash = entries[0].Hash
	}

	log.Info().Str("signer_id", ss.signerID[:16]+"...").Msg("signing service initialized")
	return ss, nil
}

// SignerID returns the hex-encoded public key.
func (ss *SigningService) SignerID() string {
	return ss.signerID
}

// PublicKeyHex returns the hex-encoded public key (alias for SignerID).
func (ss *SigningService) PublicKeyHex() string {
	return ss.signerID
}

// Sign creates an Ed25519 signature of the given data.
func (ss *SigningService) Sign(data []byte) []byte {
	return ed25519.Sign(ss.privateKey, data)
}

// Verify checks a signature against a public key (hex-encoded).
func Verify(pubKeyHex string, data, signature []byte) bool {
	pubBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pubBytes), data, signature)
}

// AuditEvent records a tamper-evident audit log entry with hash chain.
func (ss *SigningService) AuditEvent(eventType string, interfaceID, direction *string, deliveryID, ruleID *int64, detail string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	// Compute hash: SHA-256(prev_hash + timestamp + event_type + detail)
	h := sha256.New()
	h.Write([]byte(ss.lastHash))
	h.Write([]byte(now))
	h.Write([]byte(eventType))
	h.Write([]byte(detail))
	hash := hex.EncodeToString(h.Sum(nil))

	entry := &database.AuditLogEntry{
		Timestamp:   now,
		InterfaceID: interfaceID,
		Direction:   direction,
		EventType:   eventType,
		DeliveryID:  deliveryID,
		RuleID:      ruleID,
		Detail:      detail,
		PrevHash:    ss.lastHash,
		Hash:        hash,
	}

	if _, err := ss.db.InsertAuditLog(entry); err != nil {
		log.Error().Err(err).Str("event", eventType).Msg("audit log: failed to insert")
		return
	}

	ss.lastHash = hash
}

// VerifyChain verifies the integrity of the last N audit log entries.
// Returns the number of valid entries and the first broken index (or -1 if all valid).
func (ss *SigningService) VerifyChain(limit int) (valid int, brokenAt int) {
	entries, err := ss.db.GetAuditLog(limit)
	if err != nil || len(entries) == 0 {
		return 0, -1
	}

	// Entries are returned newest-first, reverse for chain verification
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}

	for i, entry := range entries {
		h := sha256.New()
		h.Write([]byte(entry.PrevHash))
		h.Write([]byte(entry.Timestamp))
		h.Write([]byte(entry.EventType))
		h.Write([]byte(entry.Detail))
		expected := hex.EncodeToString(h.Sum(nil))

		if entry.Hash != expected {
			return i, i
		}
	}

	return len(entries), -1
}
