package hubreporter

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
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
	// ApplySnapshot applies a full snapshot in one transaction with its
	// version (C7.4). The handler verifies the signature before invoking
	// this, so implementations may treat the payload as trusted. A
	// snapshot of another tenant, or not newer than the version applied,
	// is a report with Applied false and the reason, not an error.
	ApplySnapshot(ctx context.Context, snap *DirectorySnapshot) (DirectoryApplyReport, error)
	// ApplyRotation trusts newKeyPKIX from now on, with the record's
	// version, after the handler verified the record under the key the
	// bridge trusted (C7.6). The record's version is checked against the
	// last rotation's, never against the snapshot the bridge holds.
	ApplyRotation(ctx context.Context, tenantID string, version int64, newKeyPKIX []byte) (DirectoryApplyReport, error)
	// State is what the bridge holds: the tenant it follows, the version of
	// the last snapshot applied and of the last rotation applied.
	State(ctx context.Context) (DirectoryState, error)
}

// DirectoryState is what the bridge holds of its Hub's directory.
type DirectoryState struct {
	TenantID   string
	Version    int64 // the last snapshot applied
	KeyVersion int64 // the last key rotation applied
}

// RefuseUnknownKey: the envelope is signed by a key the bridge does not
// trust (any more). The answer names the key it does trust, so a Hub that
// lost the answer to a rotation, or a replica that signed before it
// reloaded the new key, learns where the bridge stands (r19 C7-R1).
const RefuseUnknownKey = "unknown_key"

// DirectoryApplyReport is what a directory_push did: whether it applied,
// and why not; each policy row applied or refused by name, the supported
// rows of a push applying whatever the others are (HeMB-NG r3 D12, S2);
// the rows it could not store, with their reasons. [MESHSAT-1584]
type DirectoryApplyReport struct {
	Applied          bool              `json:"applied"`
	Reason           string            `json:"reason,omitempty"`
	Detail           string            `json:"detail,omitempty"`
	AppliedVersion   int64             `json:"applied_version"` // the snapshot version held after the command
	KeyVersion       int64             `json:"key_version"`     // the version of the rotation the trusted key is from
	Contacts         int               `json:"contacts"`
	ContactsRemoved  int               `json:"contacts_removed"`
	ContactsKept     int               `json:"contacts_kept_local"`
	Groups           int               `json:"groups"`
	PoliciesApplied  []string          `json:"policies_applied"`
	PoliciesRejected []PolicyRejection `json:"policies_rejected"`
	PoliciesReverted int               `json:"policies_reverted"`
	Skipped          []DirectorySkip   `json:"skipped"`
}

// DirectorySkip is a snapshot row the bridge did not store, and why.
type DirectorySkip struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// PolicyRejection is a pushed policy row the bridge did not apply.
// Reason: invalid, unsupported_scope, unsupported_strategy,
// unsupported_field or seeded_default.
type PolicyRejection struct {
	ID        string `json:"id"`
	ScopeType string `json:"scope_type"`
	ScopeID   string `json:"scope_id"`
	Strategy  string `json:"strategy"`
	Reason    string `json:"reason"`
	Detail    string `json:"detail,omitempty"`
}

// TrustAnchorStore lets the bridge persist (and later read back) the
// Hub's directory-signing public key across restarts. Keys are PKIX
// DER-encoded ECDSA-P256 pubkeys.
type TrustAnchorStore interface {
	SetDirectoryTrustAnchor(pubkey []byte) error
	GetDirectoryTrustAnchor() ([]byte, error)
}

// DirectorySnapshot mirrors the Hub-side directory.Snapshot wire
// shape. The two types are not imported across the repo boundary.
// The signature is checked over these types marshalled again, so every
// type here must carry every field of the Hub's, with the same JSON tag
// in the same order, whether or not the bridge uses it: a field missing
// here is dropped by encoding/json and the signature fails (B5-R2, Hub
// contact metadata). testdata/hub-directory-push-*.json are payloads
// signed by the Hub's own code; their test fails when the types drift.
// [MESHSAT-1584]
type DirectorySnapshot struct {
	// Format is how the snapshot arrived, "v1" or "v2"; not on the wire.
	Format    string             `json:"-"`
	TenantID  string             `json:"tenant_id"`
	Version   int64              `json:"version"`
	Etag      string             `json:"etag,omitempty"`
	SignedAt  time.Time          `json:"signed_at"`
	Signature []byte             `json:"signature,omitempty"`
	Contacts  []DirectoryContact `json:"contacts"`
	Groups    []DirectoryGroup   `json:"groups,omitempty"`
	Policies  []DirectoryPolicy  `json:"policies,omitempty"`
}

// DirectoryContact mirrors the Hub's directory.Contact field for field
// (meshsat-hub internal/directory/store.go). The bridge persists only
// some of them; the rest (keys, policy and group ids, metadata) are
// here so the signature verifies.
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
	Metadata        map[string]string     `json:"metadata,omitempty"`
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
	// Seeded is the Hub's mark on its own precedence defaults (v2, C7.7);
	// absent from a v1 snapshot, so its bytes are unchanged.
	Seeded *bool `json:"seeded,omitempty"`
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

// handleDirectoryPush verifies a directory_push and applies it. A v2
// envelope ({"v":2,...}) is verified over its exact payload bytes BEFORE
// the payload is parsed, so a field this bridge does not know is still
// covered by the signature (C7.2). A v1 snapshot is verified as before: by
// re-encoding what was parsed with the Signature zeroed, which matches the
// Hub's api.CanonicalSnapshotBytes only while these types carry every
// field the Hub sends. Either way the key is the one the bridge pinned.
// The answer says whether it applied, the version the bridge holds after
// it, and the key it trusts. [MESHSAT-540, MESHSAT-1584]
func (ch *CommandHandler) handleDirectoryPush(cmd Command) (json.RawMessage, error) {
	if ch.dirApplier == nil {
		return nil, fmt.Errorf("directory applier not configured")
	}
	if ch.trustStore == nil {
		return nil, fmt.Errorf("directory trust anchor not configured")
	}
	pubPKIX, err := ch.trustStore.GetDirectoryTrustAnchor()
	if err != nil || len(pubPKIX) == 0 {
		return nil, fmt.Errorf("directory_push: no pinned trust anchor (run pairing first)")
	}
	var probe struct {
		V int `json:"v"`
	}
	_ = json.Unmarshal(cmd.Payload, &probe)
	var snap DirectorySnapshot
	if probe.V == EnvelopeVersion {
		payload, err := OpenEnvelope(cmd.Payload, pubPKIX)
		if errors.Is(err, ErrUnknownKey) {
			return ch.unknownKeyAnswer(cmd, "directory_push", pubPKIX, err)
		}
		if err != nil {
			return nil, fmt.Errorf("directory_push: %w", err)
		}
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &head); err != nil {
			return nil, fmt.Errorf("directory_push: parse payload: %w", err)
		}
		if head.Type != "" && head.Type != "snapshot" {
			return nil, fmt.Errorf("directory_push: the payload is a %q, not a snapshot", head.Type)
		}
		if err := json.Unmarshal(payload, &snap); err != nil {
			return nil, fmt.Errorf("directory_push: parse payload: %w", err)
		}
		snap.Signature = nil
		snap.Format = "v2"
	} else {
		if err := verifyV1(cmd.Payload, pubPKIX, &snap); err != nil {
			return nil, err
		}
		snap.Format = "v1"
	}

	report, err := ch.dirApplier.ApplySnapshot(context.Background(), &snap)
	if err != nil {
		return nil, fmt.Errorf("directory_push: apply: %w", err)
	}
	for _, rj := range report.PoliciesRejected {
		log.Warn().Str("policy_id", rj.ID).Str("scope", rj.ScopeType+":"+rj.ScopeID).Str("strategy", rj.Strategy).
			Str("reason", rj.Reason).Str("detail", rj.Detail).Str("request_id", cmd.RequestID).
			Msg("commander: directory_push policy not applied")
	}
	for _, sk := range report.Skipped {
		log.Warn().Str("kind", sk.Kind).Str("id", sk.ID).Str("reason", sk.Reason).Str("detail", sk.Detail).
			Str("request_id", cmd.RequestID).Msg("commander: directory_push row not stored")
	}
	ev := log.Info()
	if !report.Applied {
		ev = log.Warn().Str("reason", report.Reason).Str("detail", report.Detail)
	}
	ev.Str("tenant_id", snap.TenantID).Int64("version", snap.Version).Int64("applied_version", report.AppliedVersion).
		Int64("key_version", report.KeyVersion).
		Str("format", snap.Format).Bool("applied", report.Applied).Int("contacts", report.Contacts).
		Int("groups", report.Groups).Int("policies", len(snap.Policies)).
		Int("policies_applied", len(report.PoliciesApplied)).Int("policies_rejected", len(report.PoliciesRejected)).
		Int("skipped", len(report.Skipped)).Str("request_id", cmd.RequestID).Msg("commander: directory_push")
	return directoryAnswer(snap.TenantID, snap.Version, snap.Format, KeyID(pubPKIX), report)
}

// directoryAnswer is the result a directory command answers with (the
// Hub's DirectoryPushAnswer).
func directoryAnswer(tenantID string, version int64, format, keyID string, report DirectoryApplyReport) (json.RawMessage, error) {
	if report.PoliciesApplied == nil {
		report.PoliciesApplied = []string{}
	}
	if report.PoliciesRejected == nil {
		report.PoliciesRejected = []PolicyRejection{}
	}
	if report.Skipped == nil {
		report.Skipped = []DirectorySkip{}
	}
	return json.Marshal(struct {
		TenantID string `json:"tenant_id"`
		Version  int64  `json:"version"`
		Format   string `json:"format,omitempty"`
		KeyID    string `json:"key_id"`
		DirectoryApplyReport
	}{tenantID, version, format, keyID, report})
}

// unknownKeyAnswer answers an envelope signed by a key the bridge does not
// trust: not applied, reason unknown_key, with what the bridge holds and the
// id of the key it trusts. Nothing of the payload is read. The key id is
// public (the Hub serves the key), so naming it gives nothing away.
func (ch *CommandHandler) unknownKeyAnswer(cmd Command, what string, trusted []byte, cause error) (json.RawMessage, error) {
	st, err := ch.dirApplier.State(context.Background())
	if err != nil {
		return nil, fmt.Errorf("%s: %w (and the directory state: %v)", what, cause, err)
	}
	log.Warn().Str("command", what).Err(cause).Str("key_id", KeyID(trusted)).Int64("applied_version", st.Version).
		Int64("key_version", st.KeyVersion).Str("request_id", cmd.RequestID).
		Msg("commander: directory envelope signed by a key this bridge does not trust")
	return directoryAnswer(st.TenantID, 0, "v2", KeyID(trusted), DirectoryApplyReport{Reason: RefuseUnknownKey,
		Detail: cause.Error(), AppliedVersion: st.Version, KeyVersion: st.KeyVersion})
}

// verifyV1 checks a v1 snapshot's signature: the snapshot re-encoded with
// Signature zeroed, as the Hub signed it.
func verifyV1(raw []byte, pubPKIX []byte, snap *DirectorySnapshot) error {
	if err := json.Unmarshal(raw, snap); err != nil {
		return fmt.Errorf("parse directory_push payload: %w", err)
	}
	if len(snap.Signature) == 0 {
		return fmt.Errorf("directory_push: unsigned snapshot rejected")
	}
	pub, err := p256Key(pubPKIX)
	if err != nil {
		return fmt.Errorf("directory_push: pinned pubkey: %w", err)
	}
	toVerify := *snap
	toVerify.Signature = nil
	canonical, err := json.Marshal(&toVerify)
	if err != nil {
		return fmt.Errorf("directory_push: canonicalise: %w", err)
	}
	digest := sha256.Sum256(canonical)
	if !ecdsa.VerifyASN1(pub, digest[:], snap.Signature) {
		// Still refused. Say so when the cause may be a Hub field this
		// bridge's types do not carry: dropped on decode, it changes the
		// bytes the signature covers.
		strict := json.NewDecoder(bytes.NewReader(raw))
		strict.DisallowUnknownFields()
		if err := strict.Decode(&DirectorySnapshot{}); err != nil {
			return fmt.Errorf("directory_push: signature verification failed; the snapshot has a field this bridge does not know (%v), update the bridge", err)
		}
		return fmt.Errorf("directory_push: signature verification failed")
	}
	return nil
}

// handleDirectoryRotate takes one rotation record (C7.6): signed by the key
// the bridge trusts, it names the new key, its id and a version; the
// bridge trusts the new key from then on, if the record is of the tenant it
// follows and above the last rotation it applied. A record signed by a key
// the bridge no longer trusts (one it already followed, sent again because
// the answer was lost) is answered with the key it trusts. [MESHSAT-1584]
func (ch *CommandHandler) handleDirectoryRotate(cmd Command) (json.RawMessage, error) {
	if ch.dirApplier == nil || ch.trustStore == nil {
		return nil, fmt.Errorf("directory applier or trust anchor not configured")
	}
	pinned, err := ch.trustStore.GetDirectoryTrustAnchor()
	if err != nil || len(pinned) == 0 {
		return nil, fmt.Errorf("directory_rotate: no pinned trust anchor")
	}
	payload, err := OpenEnvelope(cmd.Payload, pinned)
	if errors.Is(err, ErrUnknownKey) {
		return ch.unknownKeyAnswer(cmd, "directory_rotate", pinned, err)
	}
	if err != nil {
		return nil, fmt.Errorf("directory_rotate: %w", err)
	}
	var rec struct {
		Type       string `json:"type"`
		TenantID   string `json:"tenant_id"`
		Version    int64  `json:"version"`
		NewKeyID   string `json:"new_key_id"`
		NewKeyPKIX []byte `json:"new_key_pkix"`
	}
	if err := json.Unmarshal(payload, &rec); err != nil {
		return nil, fmt.Errorf("directory_rotate: parse record: %w", err)
	}
	if rec.Type != "rotation" {
		return nil, fmt.Errorf("directory_rotate: the payload is a %q, not a rotation", rec.Type)
	}
	if KeyID(rec.NewKeyPKIX) != rec.NewKeyID {
		return nil, fmt.Errorf("directory_rotate: the new key does not hash to its id")
	}
	if _, err := p256Key(rec.NewKeyPKIX); err != nil {
		return nil, fmt.Errorf("directory_rotate: new key: %w", err)
	}
	report, err := ch.dirApplier.ApplyRotation(context.Background(), rec.TenantID, rec.Version, rec.NewKeyPKIX)
	if err != nil {
		return nil, fmt.Errorf("directory_rotate: apply: %w", err)
	}
	trusted := KeyID(pinned)
	if report.Applied {
		trusted = rec.NewKeyID
		log.Info().Str("tenant_id", rec.TenantID).Int64("version", rec.Version).Str("key_id", trusted).
			Int64("applied_version", report.AppliedVersion).Msg("commander: directory key rotated by a signed record")
	} else {
		log.Warn().Str("tenant_id", rec.TenantID).Int64("version", rec.Version).Str("reason", report.Reason).
			Str("detail", report.Detail).Msg("commander: directory_rotate not applied")
	}
	return directoryAnswer(rec.TenantID, rec.Version, "v2", trusted, report)
}

// handleDirectoryTrustAnchorRotate refuses the unsigned rotate earlier
// bridges took: anyone who could publish it could make the bridge trust
// their key (C7.6). A key moves only by a directory_rotate record signed by
// the key the bridge trusts. [MESHSAT-1584]
func (ch *CommandHandler) handleDirectoryTrustAnchorRotate(cmd Command) (json.RawMessage, error) {
	log.Warn().Str("request_id", cmd.RequestID).Msg("commander: unsigned directory_trust_anchor_rotate refused")
	return nil, fmt.Errorf("directory_trust_anchor_rotate refused: the directory key moves only by a directory_rotate record signed by the key this bridge trusts")
}
