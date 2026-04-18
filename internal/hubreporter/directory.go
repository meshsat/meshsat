package hubreporter

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
)

// DirectoryApplier is the narrow contract the directory_push handler
// uses to write a received snapshot into the bridge's local
// directory_* tables. The concrete implementation lives in
// cmd/meshsat/main.go (thin adapter over internal/directory.SQLStore)
// so this package stays free of a hard dependency on the bridge's
// storage layer. [MESHSAT-540]
type DirectoryApplier interface {
	// ApplySnapshot replaces the tenant's directory state with the
	// given snapshot. The handler verifies the ECDSA-P256 signature
	// before invoking this, so implementations may treat the payload
	// as trusted.
	ApplySnapshot(ctx context.Context, snap *DirectorySnapshot) error
}

// TrustAnchorStore lets the bridge persist (and later read back) the
// Hub's directory-signing public key across restarts. Keys are PKIX
// DER-encoded ECDSA-P256 pubkeys.
type TrustAnchorStore interface {
	SetDirectoryTrustAnchor(pubkey []byte) error
	GetDirectoryTrustAnchor() ([]byte, error)
}

// DirectorySnapshot mirrors the Hub-side directory.Snapshot wire
// shape. The two types are not imported across the repo boundary;
// instead they are kept byte-compatible via canonical JSON and a
// narrow set of fields the bridge actually consumes.
type DirectorySnapshot struct {
	TenantID  string             `json:"tenant_id"`
	Version   int64              `json:"version"`
	Etag      string             `json:"etag,omitempty"`
	SignedAt  time.Time          `json:"signed_at"`
	Signature []byte             `json:"signature,omitempty"`
	Contacts  []DirectoryContact `json:"contacts"`
	Groups    []DirectoryGroup   `json:"groups,omitempty"`
	Policies  []DirectoryPolicy  `json:"policies,omitempty"`
}

// DirectoryContact carries the fields from the Hub's directory.Contact
// that the bridge persists. Unknown fields round-trip through the
// canonical-JSON signature verification because the bridge echoes
// them back via ApplySnapshot.
type DirectoryContact struct {
	ID              string                `json:"id"`
	TenantID        string                `json:"tenant_id"`
	DisplayName     string                `json:"display_name"`
	GivenName       string                `json:"given_name,omitempty"`
	FamilyName      string                `json:"family_name,omitempty"`
	Org             string                `json:"org,omitempty"`
	Role            string                `json:"role,omitempty"`
	Team            string                `json:"team,omitempty"`
	SIDC            string                `json:"sidc,omitempty"`
	Notes           string                `json:"notes,omitempty"`
	TrustLevel      int                   `json:"trust_level,omitempty"`
	TrustVerifiedAt *time.Time            `json:"trust_verified_at,omitempty"`
	Origin          string                `json:"origin,omitempty"`
	HubVersion      int64                 `json:"hub_version,omitempty"`
	HubEtag         string                `json:"hub_etag,omitempty"`
	Addresses       []DirectoryAddress    `json:"addresses"`
	Keys            []DirectoryContactKey `json:"keys,omitempty"`
	PolicyID        string                `json:"policy_id,omitempty"`
	GroupIDs        []string              `json:"group_ids,omitempty"`
	CreatedAt       time.Time             `json:"created_at"`
	UpdatedAt       time.Time             `json:"updated_at"`
}

type DirectoryAddress struct {
	ID           string `json:"id,omitempty"`
	Kind         string `json:"kind"`
	Value        string `json:"value"`
	Subvalue     string `json:"subvalue,omitempty"`
	Label        string `json:"label,omitempty"`
	PrimaryRank  int    `json:"primary_rank"`
	Verified     bool   `json:"verified,omitempty"`
	BearerHint   int    `json:"bearer_hint,omitempty"`
	MaxCostCents *int   `json:"max_cost_cents,omitempty"`
}

type DirectoryContactKey struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Version     int       `json:"version"`
	Status      string    `json:"status"`
	Public      []byte    `json:"public,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	TrustAnchor string    `json:"trust_anchor,omitempty"`
	Algorithm   string    `json:"algorithm,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type DirectoryGroup struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenant_id"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind,omitempty"`
	SIDC       string    `json:"sidc,omitempty"`
	MLSGroupID string    `json:"mls_group_id,omitempty"`
	HubVersion int64     `json:"hub_version,omitempty"`
	HubEtag    string    `json:"hub_etag,omitempty"`
	MemberIDs  []string  `json:"member_ids"`
	PolicyID   string    `json:"policy_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type DirectoryPolicy struct {
	ID                string        `json:"id"`
	Name              string        `json:"name,omitempty"`
	ScopeType         string        `json:"scope_type,omitempty"`
	ScopeID           string        `json:"scope_id,omitempty"`
	Strategy          string        `json:"strategy,omitempty"`
	Preferred         []string      `json:"preferred,omitempty"`
	Fallback          []string      `json:"fallback,omitempty"`
	RequireEncryption bool          `json:"require_encryption,omitempty"`
	MaxRetries        int           `json:"max_retries,omitempty"`
	RetryDelay        time.Duration `json:"retry_delay,omitempty"`
	CreatedAt         time.Time     `json:"created_at"`
	UpdatedAt         time.Time     `json:"updated_at"`
}

// SetDirectoryApplier wires the concrete snapshot applier. When unset
// the directory_push handler fails-closed with a clear error so the
// operator can spot the misconfiguration rather than silently
// dropping snapshots.
func (ch *CommandHandler) SetDirectoryApplier(a DirectoryApplier) { ch.dirApplier = a }

// SetTrustAnchorStore wires the Hub's directory-signing public-key
// persister. On bridges with no pinned anchor yet, the handler fails
// closed rather than trusting the snapshot payload.
func (ch *CommandHandler) SetTrustAnchorStore(s TrustAnchorStore) { ch.trustStore = s }

// handleDirectoryPush verifies the snapshot signature against the
// pinned Hub trust anchor (PKIX-DER ECDSA-P256 pubkey) and then
// invokes DirectoryApplier. Byte-equality with the Hub's canonical
// form (api.CanonicalSnapshotBytes) is achieved by round-tripping
// through encoding/json with the Signature field zeroed — the same
// strategy the Hub uses before signing.
func (ch *CommandHandler) handleDirectoryPush(cmd Command) (json.RawMessage, error) {
	if ch.dirApplier == nil {
		return nil, fmt.Errorf("directory applier not configured")
	}
	if ch.trustStore == nil {
		return nil, fmt.Errorf("directory trust anchor not configured")
	}
	var snap DirectorySnapshot
	if err := json.Unmarshal(cmd.Payload, &snap); err != nil {
		return nil, fmt.Errorf("parse directory_push payload: %w", err)
	}
	if len(snap.Signature) == 0 {
		return nil, fmt.Errorf("directory_push: unsigned snapshot rejected")
	}
	pubPKIX, err := ch.trustStore.GetDirectoryTrustAnchor()
	if err != nil || len(pubPKIX) == 0 {
		return nil, fmt.Errorf("directory_push: no pinned trust anchor (run pairing first)")
	}
	pubAny, err := x509.ParsePKIXPublicKey(pubPKIX)
	if err != nil {
		return nil, fmt.Errorf("directory_push: parse pinned pubkey: %w", err)
	}
	pub, ok := pubAny.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("directory_push: pinned pubkey is not ECDSA-P256")
	}
	// Recreate the signed bytes by zeroing Signature on a copy and
	// marshalling via encoding/json — matches the Hub's
	// api.CanonicalSnapshotBytes routine exactly.
	toVerify := snap
	toVerify.Signature = nil
	canonical, err := json.Marshal(&toVerify)
	if err != nil {
		return nil, fmt.Errorf("directory_push: canonicalise: %w", err)
	}
	digest := sha256.Sum256(canonical)
	if !ecdsa.VerifyASN1(pub, digest[:], snap.Signature) {
		return nil, fmt.Errorf("directory_push: signature verification failed")
	}

	if err := ch.dirApplier.ApplySnapshot(context.Background(), &snap); err != nil {
		return nil, fmt.Errorf("directory_push: apply: %w", err)
	}
	log.Info().
		Str("tenant_id", snap.TenantID).
		Int64("version", snap.Version).
		Int("contacts", len(snap.Contacts)).
		Int("groups", len(snap.Groups)).
		Int("policies", len(snap.Policies)).
		Str("request_id", cmd.RequestID).
		Msg("commander: directory_push applied")
	result := struct {
		TenantID string `json:"tenant_id"`
		Version  int64  `json:"version"`
		Applied  bool   `json:"applied"`
	}{snap.TenantID, snap.Version, true}
	out, _ := json.Marshal(result)
	return out, nil
}

// handleDirectoryTrustAnchorRotate stores a new Hub directory-signing
// public key, replacing the pinned anchor. Subsequent directory_push
// snapshots are verified against the new key; snapshots signed by
// the previous key are rejected.
func (ch *CommandHandler) handleDirectoryTrustAnchorRotate(cmd Command) (json.RawMessage, error) {
	if ch.trustStore == nil {
		return nil, fmt.Errorf("directory trust anchor store not configured")
	}
	var p struct {
		PublicKey []byte `json:"public_key"`
		Algorithm string `json:"algorithm"`
		Version   int    `json:"version"`
	}
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, fmt.Errorf("parse directory_trust_anchor_rotate payload: %w", err)
	}
	if len(p.PublicKey) == 0 {
		return nil, fmt.Errorf("directory_trust_anchor_rotate: public_key required")
	}
	if p.Algorithm != "" && p.Algorithm != "ecdsa-p256" {
		return nil, fmt.Errorf("directory_trust_anchor_rotate: unsupported algorithm %q", p.Algorithm)
	}
	// Validate the PKIX-DER shape before persisting so operators see
	// a rejection at the MQTT layer rather than a later silent
	// failure inside directory_push.
	parsed, err := x509.ParsePKIXPublicKey(p.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("directory_trust_anchor_rotate: pubkey parse: %w", err)
	}
	if _, ok := parsed.(*ecdsa.PublicKey); !ok {
		return nil, fmt.Errorf("directory_trust_anchor_rotate: pubkey is not ECDSA")
	}
	if err := ch.trustStore.SetDirectoryTrustAnchor(p.PublicKey); err != nil {
		return nil, fmt.Errorf("directory_trust_anchor_rotate: persist: %w", err)
	}
	log.Info().Int("version", p.Version).Msg("commander: directory trust anchor rotated")
	return json.RawMessage(`{"rotated":true}`), nil
}
