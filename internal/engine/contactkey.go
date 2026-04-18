package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/rs/zerolog/log"

	"meshsat/internal/directory"
	"meshsat/internal/keystore"
)

// ContactKeyResolver resolves a contact-scoped key_ref (e.g.
// "contact:<uuid>") to a hex-encoded AES-256 key. It is parallel to
// [KeyResolver] (which handles legacy "channel_type:address" refs from
// the per-channel keystore) — the two coexist during the 30-day grace
// period per S2-05 / MESHSAT-548.
//
// The transform pipeline picks the right resolver by inspecting the
// key_ref prefix: anything starting with "contact:" routes here.
// [MESHSAT-537]
type ContactKeyResolver interface {
	ResolveContactKey(contactID string) (hexKey string, err error)
}

// ContactKeyService is the concrete resolver that bridges the
// directory store (where the per-contact AES key records live) and
// the keystore (which owns the master-key envelope used to unwrap the
// stored ciphertext). Downstream callers consume the narrower
// [ContactKeyResolver] interface.
type ContactKeyService struct {
	ks  *keystore.KeyStore
	dir directory.Store
}

// NewContactKeyService constructs a resolver bound to a keystore and
// a directory store. Returns nil if either dependency is nil; a nil
// service's resolver path fails loudly rather than silently dropping
// encryption.
func NewContactKeyService(ks *keystore.KeyStore, dir directory.Store) *ContactKeyService {
	if ks == nil || dir == nil {
		return nil
	}
	return &ContactKeyService{ks: ks, dir: dir}
}

// ResolveContactKey returns the hex-encoded, unwrapped AES-256 key
// for the contact's active AES256_GCM_SHARED key record. The happy
// path is: directory.ListKeys(contactID, onlyActive=true) → first
// matching record → keystore.UnwrapData → hex. An error is returned
// if no active AES key exists — callers either provision one via
// [ContactKeyService.GenerateAndStoreAES] (local path) or wait for a
// Hub directory_push delivering one (trust-anchor=hub).
func (cs *ContactKeyService) ResolveContactKey(contactID string) (string, error) {
	if cs == nil {
		return "", fmt.Errorf("ResolveContactKey: nil ContactKeyService")
	}
	if contactID == "" {
		return "", fmt.Errorf("ResolveContactKey: empty contact_id")
	}
	keys, err := cs.dir.ListKeys(context.Background(), contactID, true)
	if err != nil {
		return "", fmt.Errorf("list keys for contact %s: %w", contactID, err)
	}
	for _, k := range keys {
		if k.Kind != directory.KeyAES256GCMShared || k.Status != directory.KeyActive {
			continue
		}
		if len(k.EncryptedPriv) == 0 {
			return "", fmt.Errorf("contact %s has AES key record %s with empty encrypted_priv", contactID, k.ID)
		}
		raw, err := cs.ks.UnwrapData(k.EncryptedPriv)
		if err != nil {
			return "", fmt.Errorf("unwrap contact %s key %s: %w", contactID, k.ID, err)
		}
		if len(raw) != 32 {
			return "", fmt.Errorf("contact %s key %s: unwrapped length %d, want 32", contactID, k.ID, len(raw))
		}
		return hex.EncodeToString(raw), nil
	}
	return "", fmt.Errorf("no active AES256_GCM_SHARED key for contact %s", contactID)
}

// GenerateAndStoreAES creates a fresh random AES-256 key, wraps it
// under the master key, and persists it as an active
// AES256_GCM_SHARED record against the given contact. The new key's
// version is one greater than the highest existing AES256_GCM_SHARED
// version for the contact (starting at 1 when none exist).
//
// Returns the hex-encoded raw key so callers can hand it to out-of-
// band delivery (QR bundle, API response, etc.). The raw key is NOT
// retained anywhere after this call — subsequent reads go through
// [ContactKeyService.ResolveContactKey] which unwraps on demand.
//
// anchor describes the provenance (TrustAnchorLocal for bridge-
// originated, TrustAnchorHub for Hub-pushed, TrustAnchorQR for a
// QR-scanned bundle, etc).
func (cs *ContactKeyService) GenerateAndStoreAES(ctx context.Context, contactID string, anchor directory.TrustAnchor) (string, error) {
	if cs == nil {
		return "", fmt.Errorf("GenerateAndStoreAES: nil ContactKeyService")
	}
	if contactID == "" {
		return "", fmt.Errorf("GenerateAndStoreAES: empty contact_id")
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("random 32 bytes: %w", err)
	}
	wrapped, err := cs.ks.WrapData(raw)
	if err != nil {
		return "", fmt.Errorf("wrap contact key: %w", err)
	}

	// Next version = max existing AES version + 1. Old versions stay
	// in-place with their current status (retired if rotated via
	// [directory.Store.RetireKey], revoked if compromised).
	existing, _ := cs.dir.ListKeys(ctx, contactID, false)
	nextVersion := 1
	for _, k := range existing {
		if k.Kind == directory.KeyAES256GCMShared && k.Version >= nextVersion {
			nextVersion = k.Version + 1
		}
	}

	record := &directory.ContactKey{
		ContactID:     contactID,
		Kind:          directory.KeyAES256GCMShared,
		Version:       nextVersion,
		Status:        directory.KeyActive,
		EncryptedPriv: wrapped,
		TrustAnchor:   anchor,
	}
	if err := cs.dir.AddKey(ctx, record); err != nil {
		return "", fmt.Errorf("persist contact key: %w", err)
	}

	log.Info().
		Str("contact_id", contactID).
		Int("version", nextVersion).
		Str("trust_anchor", string(anchor)).
		Msg("contact key: generated AES256_GCM_SHARED")

	return hex.EncodeToString(raw), nil
}

// RotateAES retires every active AES256_GCM_SHARED key for the
// contact and then creates a fresh one, returning the new hex key.
// Old versions become 'retired' — ResolveContactKey will no longer
// return them, but decryption paths that still reference an older
// version (in-flight messages) can opt-in by consulting the directory
// directly.
func (cs *ContactKeyService) RotateAES(ctx context.Context, contactID string, anchor directory.TrustAnchor) (string, error) {
	if cs == nil {
		return "", fmt.Errorf("RotateAES: nil ContactKeyService")
	}
	if contactID == "" {
		return "", fmt.Errorf("RotateAES: empty contact_id")
	}
	existing, err := cs.dir.ListKeys(ctx, contactID, true)
	if err != nil {
		return "", fmt.Errorf("list keys: %w", err)
	}
	for _, k := range existing {
		if k.Kind != directory.KeyAES256GCMShared {
			continue
		}
		if err := cs.dir.RetireKey(ctx, k.ID); err != nil {
			return "", fmt.Errorf("retire key %s: %w", k.ID, err)
		}
	}
	return cs.GenerateAndStoreAES(ctx, contactID, anchor)
}
