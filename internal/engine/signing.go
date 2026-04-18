package engine

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/keystore"
)

// wrappedPlaceholder is the sentinel written to system_config.signing_private_key
// after the plaintext has been migrated into the wrapped column
// (system_config.signing_private_key_wrapped). The sentinel is
// specifically not hex-decodable so a bug that bypasses the wrapped
// read path will fail loudly rather than generate a new keypair.
const wrappedPlaceholder = "__WRAPPED_SEE_WRAPPED_COLUMN__"

// wrappedPrivKeyConfigKey is the system_config key under which the
// base64-encoded, master-key-wrapped private key is stored.
const wrappedPrivKeyConfigKey = "signing_private_key_wrapped"

// SigningService provides Ed25519 message signing and hash-chain audit logging.
type SigningService struct {
	mu         sync.Mutex
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	signerID   string // hex-encoded public key
	db         *database.DB
	ks         *keystore.KeyStore // set by LoadWithKeystore; optional
	lastHash   string             // last audit log hash for chain continuity
}

// NewSigningService creates a signing service. Behaviour:
//   - If the plaintext column holds a valid hex key, load it. This is
//     the legacy path; callers should follow with LoadWithKeystore to
//     migrate the key to envelope-encrypted form at rest.
//   - If the plaintext column holds the wrapped-sentinel (set by a
//     prior LoadWithKeystore), return a service whose private key is
//     not yet loaded. The caller must invoke LoadWithKeystore to
//     unwrap before Sign will produce signatures.
//   - If nothing exists yet, generate a fresh keypair and persist it
//     to plaintext — the LoadWithKeystore step will wrap it on the
//     same boot.
//
// Callers that do not integrate with a keystore (tests, fallback
// paths) still work: the service operates in legacy plaintext mode.
func NewSigningService(db *database.DB) (*SigningService, error) {
	ss := &SigningService{db: db}

	privHex, _ := db.GetSystemConfig("signing_private_key")
	pubHex, _ := db.GetSystemConfig("signing_public_key")
	wrappedB64, _ := db.GetSystemConfig(wrappedPrivKeyConfigKey)

	switch {
	case privHex == wrappedPlaceholder, privHex == "" && wrappedB64 != "":
		// Wrapped-only state. Public key must still be readable; the
		// private key stays nil until LoadWithKeystore runs.
		if pubHex == "" {
			return nil, fmt.Errorf("wrapped private key present but public key missing — corrupted state")
		}
		pubBytes, err := hex.DecodeString(pubHex)
		if err != nil || len(pubBytes) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid stored public key")
		}
		ss.publicKey = ed25519.PublicKey(pubBytes)
		ss.signerID = pubHex
		log.Info().
			Str("signer_id", ss.signerID[:16]+"...").
			Msg("signing service: wrapped key present, awaiting keystore")

	case privHex != "" && pubHex != "":
		privBytes, err := hex.DecodeString(privHex)
		if err != nil || len(privBytes) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("invalid stored private key")
		}
		ss.privateKey = ed25519.PrivateKey(privBytes)
		ss.publicKey = ss.privateKey.Public().(ed25519.PublicKey)
		ss.signerID = hex.EncodeToString(ss.publicKey)
		log.Info().Str("signer_id", ss.signerID[:16]+"...").Msg("signing service: legacy plaintext key loaded (call LoadWithKeystore to wrap)")

	default:
		// First boot. Generate, persist plaintext. LoadWithKeystore
		// on the same boot will migrate to wrapped form.
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generate ed25519 key: %w", err)
		}
		ss.privateKey = priv
		ss.publicKey = pub

		if err := db.SetSystemConfig("signing_private_key", hex.EncodeToString(priv)); err != nil {
			return nil, fmt.Errorf("persist private key: %w", err)
		}
		if err := db.SetSystemConfig("signing_public_key", hex.EncodeToString(pub)); err != nil {
			return nil, fmt.Errorf("persist public key: %w", err)
		}
		ss.signerID = hex.EncodeToString(ss.publicKey)
		log.Info().Str("signer_id", ss.signerID[:16]+"...").Msg("signing service: generated new keypair")
	}

	// Load last audit hash for chain continuity
	entries, err := db.GetAuditLog(1)
	if err == nil && len(entries) > 0 {
		ss.lastHash = entries[0].Hash
	}

	return ss, nil
}

// LoadWithKeystore attaches a keystore for envelope encryption of the
// Ed25519 private key at rest. Behaviour:
//   - If the private key is already loaded in memory (NewSigningService
//     just read it from plaintext, or generated a new one), wrap it
//     and move it from the plaintext column to the wrapped column.
//   - If the private key is not loaded (wrapped-only state on a
//     restarted bridge), read the wrapped column, unwrap, and install.
//     Also verifies the unwrapped private key matches the previously
//     stored public key — a mismatch indicates a wrong master key or
//     tampering and is fatal.
//
// Idempotent: re-calling with the same keystore is a no-op once the
// migration has run. Safe to call concurrently with Sign (guarded by
// the service mutex).
//
// Must be invoked exactly once per bridge startup, after the
// [keystore.KeyStore] has initialised. [MESHSAT-536]
func (ss *SigningService) LoadWithKeystore(ks *keystore.KeyStore) error {
	if ks == nil {
		return fmt.Errorf("nil keystore")
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.ks = ks

	// Case A: plaintext already loaded → migrate to wrapped form.
	if ss.privateKey != nil {
		wrapped, err := ks.WrapData(ss.privateKey)
		if err != nil {
			return fmt.Errorf("wrap signing key: %w", err)
		}
		wrappedB64 := base64.StdEncoding.EncodeToString(wrapped)
		if err := ss.db.SetSystemConfig(wrappedPrivKeyConfigKey, wrappedB64); err != nil {
			return fmt.Errorf("persist wrapped key: %w", err)
		}
		if err := ss.db.SetSystemConfig("signing_private_key", wrappedPlaceholder); err != nil {
			return fmt.Errorf("clear plaintext key: %w", err)
		}
		log.Info().Msg("signing service: Ed25519 private key wrapped at rest")
		return nil
	}

	// Case B: private key not loaded — read wrapped column and unwrap.
	wrappedB64, err := ss.db.GetSystemConfig(wrappedPrivKeyConfigKey)
	if err != nil || wrappedB64 == "" {
		return fmt.Errorf("no wrapped key to unwrap (private key nil and wrapped column empty)")
	}
	wrapped, err := base64.StdEncoding.DecodeString(wrappedB64)
	if err != nil {
		return fmt.Errorf("decode wrapped key: %w", err)
	}
	priv, err := ks.UnwrapData(wrapped)
	if err != nil {
		return fmt.Errorf("unwrap signing key: %w (wrong master key?)", err)
	}
	if len(priv) != ed25519.PrivateKeySize {
		return fmt.Errorf("unwrapped private key has wrong size: %d", len(priv))
	}
	loaded := ed25519.PrivateKey(priv)
	// Pub-key consistency check: detects tampering or master-key drift.
	if derived, ok := loaded.Public().(ed25519.PublicKey); !ok || !bytes.Equal(derived, ss.publicKey) {
		return fmt.Errorf("unwrapped private key does not match stored public key — corrupted state")
	}
	ss.privateKey = loaded
	log.Info().Msg("signing service: Ed25519 private key unwrapped from master-key envelope")
	return nil
}

// KeyIsWrapped reports whether the signing service is backed by a
// master-key-wrapped private key (i.e. LoadWithKeystore has run).
// Exposed for health/metrics.
func (ss *SigningService) KeyIsWrapped() bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.ks != nil
}

// SignerID returns the hex-encoded public key.
func (ss *SigningService) SignerID() string {
	return ss.signerID
}

// PublicKeyHex returns the hex-encoded public key (alias for SignerID).
func (ss *SigningService) PublicKeyHex() string {
	return ss.signerID
}

// Sign creates an Ed25519 signature of the given data. Returns nil
// (and logs a warning) when the private key has not been loaded yet
// — this happens between NewSigningService and LoadWithKeystore on a
// restart where the key is already wrapped. Callers already treat a
// nil SigningService as "signing disabled"; a nil return from Sign
// falls through the same way.
func (ss *SigningService) Sign(data []byte) []byte {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.privateKey == nil {
		log.Warn().Msg("signing service: Sign called before key loaded (wrapped-only state)")
		return nil
	}
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
