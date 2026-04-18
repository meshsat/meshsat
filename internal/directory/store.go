// Package directory is the unified identity and addressing layer for
// MeshSat Bridge. Contacts are SCIM-shaped people/entities with N
// typed addresses, per-contact key material, optional group
// membership, and an optional dispatch policy driving
// Dispatcher.SendToRecipient (MESHSAT-544 / S2-01). IDs are opaque
// strings (UUIDv4 for new writes; legacy 32-char hex from the v44
// backfill); callers must never assume a format. The directory layer
// is storage-agnostic — consumers depend on the [Store] interface
// and the in-tree [SQLStore] binds it to SQLite over sqlx.
//
// Scope owner: MESHSAT-535 (S1-02).
package directory

import (
	"context"
	"errors"
)

// Kind is the canonical bearer-kind enum. Values mirror the stored
// `directory_addresses.kind` strings. Use the exported constants;
// never type a literal.
type Kind string

const (
	KindSMS        Kind = "SMS"
	KindMeshtastic Kind = "MESHTASTIC"
	KindAPRS       Kind = "APRS"
	KindIridiumSBD Kind = "IRIDIUM_SBD"
	KindIridiumIMT Kind = "IRIDIUM_IMT"
	KindCellular   Kind = "CELLULAR"
	KindTAK        Kind = "TAK"
	KindReticulum  Kind = "RETICULUM"
	KindZigBee     Kind = "ZIGBEE"
	KindBLE        Kind = "BLE"
	KindWebhook    Kind = "WEBHOOK"
	KindEmail      Kind = "EMAIL"
	KindMQTT       Kind = "MQTT"
)

// AllKinds is the full set of recognised bearer kinds, in the order
// we surface them in the UI pills.
var AllKinds = []Kind{
	KindSMS, KindMeshtastic, KindAPRS, KindIridiumSBD, KindIridiumIMT,
	KindCellular, KindTAK, KindReticulum, KindZigBee, KindBLE,
	KindWebhook, KindEmail, KindMQTT,
}

// Valid reports whether k is a recognised bearer kind.
func (k Kind) Valid() bool {
	for _, x := range AllKinds {
		if x == k {
			return true
		}
	}
	return false
}

// TrustLevel follows the Threema-style 0..3 ladder; see
// UX-AUDIT-AND-REDESIGN.md §6.3. Levels 0 and 1 accept ROUTINE
// messages silently; level ≥ 2 is required to send ≥ IMMEDIATE
// without a confirmation modal (enforced in the Compose view).
type TrustLevel int

const (
	TrustUnknown  TrustLevel = 0
	TrustAuto     TrustLevel = 1
	TrustQR       TrustLevel = 2
	TrustInPerson TrustLevel = 3
)

// Origin traces where a contact row came from. Used by the UI to
// decide whether the operator can rename / delete the row locally
// without diverging from Hub.
type Origin string

const (
	OriginLocal    Origin = "local"
	OriginHub      Origin = "hub"
	OriginImported Origin = "imported"
	OriginQR       Origin = "qr"
)

// KeyKind classifies key material attached to a contact. Public-only
// keys (anything with _PUB) store bytes in PublicData; private halves
// and symmetric keys live wrapped under the master key in
// EncryptedPriv.
type KeyKind string

const (
	KeyAES256GCMShared    KeyKind = "AES256_GCM_SHARED"
	KeyX25519LongTermPub  KeyKind = "X25519_LT_PUB"
	KeyX25519LongTermPriv KeyKind = "X25519_LT_PRIV"
	KeyX25519PreKeyPub    KeyKind = "X25519_PREKEY_PUB"
	KeyX25519PreKeyPriv   KeyKind = "X25519_PREKEY_PRIV"
	KeyEd25519SignPub     KeyKind = "ED25519_SIGN_PUB"
	KeyEd25519SignPriv    KeyKind = "ED25519_SIGN_PRIV"
	KeyMLSKeyPackage      KeyKind = "MLS_KEY_PACKAGE"
	KeyMLSLeafNode        KeyKind = "MLS_LEAF_NODE"
)

// KeyStatus is the lifecycle state of a key version.
type KeyStatus string

const (
	KeyActive  KeyStatus = "active"
	KeyRetired KeyStatus = "retired"
	KeyRevoked KeyStatus = "revoked"
)

// TrustAnchor records why we believe a given key belongs to the
// contact. "hub" keys were delivered by a signed directory_push;
// "qr" keys were scanned in-person; "in_person" is explicitly
// confirmed by the operator.
type TrustAnchor string

const (
	TrustAnchorHub      TrustAnchor = "hub"
	TrustAnchorQR       TrustAnchor = "qr"
	TrustAnchorManual   TrustAnchor = "manual"
	TrustAnchorInPerson TrustAnchor = "in_person"
	TrustAnchorLocal    TrustAnchor = "local"
)

// GroupKind differentiates teams, roles, lists, and MLS groups.
type GroupKind string

const (
	GroupTeam GroupKind = "TEAM"
	GroupRole GroupKind = "ROLE"
	GroupList GroupKind = "LIST"
	GroupMLS  GroupKind = "MLS_GROUP"
)

// Strategy is the dispatch mechanism for routing a message to a
// recipient. See UX-AUDIT-AND-REDESIGN.md §4.2.3.
type Strategy string

const (
	StrategyPrimaryOnly     Strategy = "PRIMARY_ONLY"
	StrategyAnyReachable    Strategy = "ANY_REACHABLE"
	StrategyOrderedFallback Strategy = "ORDERED_FALLBACK"
	StrategyHeMBBonded      Strategy = "HEMB_BONDED"
	StrategyAllBearers      Strategy = "ALL_BEARERS"
)

// PolicyScope describes what a DispatchPolicy row applies to. The
// Dispatcher resolves in this order: caller opts → contact → group
// → precedence → default.
type PolicyScope string

const (
	ScopeContact    PolicyScope = "contact"
	ScopeGroup      PolicyScope = "group"
	ScopePrecedence PolicyScope = "precedence"
	ScopeDefault    PolicyScope = "default"
)

// Contact is the unified person / entity record.
type Contact struct {
	ID              string     `db:"id"                json:"id"`
	TenantID        string     `db:"tenant_id"         json:"tenant_id"`
	DisplayName     string     `db:"display_name"      json:"display_name"`
	GivenName       string     `db:"given_name"        json:"given_name,omitempty"`
	FamilyName      string     `db:"family_name"       json:"family_name,omitempty"`
	Org             string     `db:"org"               json:"org,omitempty"`
	Role            string     `db:"role"              json:"role,omitempty"`
	Team            string     `db:"team"              json:"team,omitempty"`
	SIDC            string     `db:"sidc"              json:"sidc,omitempty"`
	Notes           string     `db:"notes"             json:"notes,omitempty"`
	TrustLevel      TrustLevel `db:"trust_level"       json:"trust_level"`
	TrustVerifiedAt *string    `db:"trust_verified_at" json:"trust_verified_at,omitempty"`
	TrustVerifiedBy string     `db:"trust_verified_by" json:"trust_verified_by,omitempty"`
	HubVersion      int64      `db:"hub_version"       json:"hub_version"`
	HubEtag         string     `db:"hub_etag"          json:"hub_etag,omitempty"`
	Origin          Origin     `db:"origin"            json:"origin"`
	LegacyContactID *int64     `db:"legacy_contact_id" json:"-"`
	CreatedAt       string     `db:"created_at"        json:"created_at"`
	UpdatedAt       string     `db:"updated_at"        json:"updated_at"`

	// Populated by Resolve and FindByAddress. Zero-length on lean
	// Get/List queries.
	Addresses []Address    `db:"-" json:"addresses,omitempty"`
	Keys      []ContactKey `db:"-" json:"keys,omitempty"`
}

// PrimaryAddress returns the address with the lowest primary_rank for
// the given kind, or nil if none exists. Rank 0 = primary; ties
// resolved by ID. Callers that need multi-kind resolution should
// iterate Addresses directly.
func (c *Contact) PrimaryAddress(k Kind) *Address {
	var best *Address
	for i := range c.Addresses {
		a := &c.Addresses[i]
		if a.Kind != k {
			continue
		}
		if best == nil || a.PrimaryRank < best.PrimaryRank {
			best = a
		}
	}
	return best
}

// AddressByKind returns all addresses of a given kind in primary_rank
// order (primary first). Useful for ORDERED_FALLBACK dispatch.
func (c *Contact) AddressByKind(k Kind) []Address {
	var out []Address
	for _, a := range c.Addresses {
		if a.Kind == k {
			out = append(out, a)
		}
	}
	// Stable sort by primary_rank ascending.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].PrimaryRank < out[j-1].PrimaryRank; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Address is a single reachable endpoint for a contact.
type Address struct {
	ID           string `db:"id"             json:"id"`
	ContactID    string `db:"contact_id"     json:"contact_id"`
	Kind         Kind   `db:"kind"           json:"kind"`
	Value        string `db:"value"          json:"value"`
	Subvalue     string `db:"subvalue"       json:"subvalue,omitempty"`
	Label        string `db:"label"          json:"label,omitempty"`
	PrimaryRank  int    `db:"primary_rank"   json:"primary_rank"`
	Verified     bool   `db:"verified"       json:"verified"`
	BearerHint   int    `db:"bearer_hint"    json:"bearer_hint"`
	MaxCostCents *int   `db:"max_cost_cents" json:"max_cost_cents,omitempty"`
	CreatedAt    string `db:"created_at"     json:"created_at"`
	UpdatedAt    string `db:"updated_at"     json:"updated_at"`
}

// ContactKey is a single key version attached to a contact.
type ContactKey struct {
	ID            string      `db:"id"             json:"id"`
	ContactID     string      `db:"contact_id"     json:"contact_id"`
	Kind          KeyKind     `db:"kind"           json:"kind"`
	Version       int         `db:"version"        json:"version"`
	Status        KeyStatus   `db:"status"         json:"status"`
	PublicData    []byte      `db:"public_data"    json:"public_data,omitempty"`
	EncryptedPriv []byte      `db:"encrypted_priv" json:"-"` // never serialised
	ValidFrom     string      `db:"valid_from"     json:"valid_from"`
	ValidUntil    *string     `db:"valid_until"    json:"valid_until,omitempty"`
	RotatedAt     *string     `db:"rotated_at"     json:"rotated_at,omitempty"`
	TrustAnchor   TrustAnchor `db:"trust_anchor"   json:"trust_anchor"`
	CreatedAt     string      `db:"created_at"     json:"created_at"`
}

// Group is a team / role / distribution list / MLS group.
type Group struct {
	ID          string    `db:"id"           json:"id"`
	TenantID    string    `db:"tenant_id"    json:"tenant_id"`
	DisplayName string    `db:"display_name" json:"display_name"`
	Kind        GroupKind `db:"kind"         json:"kind"`
	SIDC        string    `db:"sidc"         json:"sidc,omitempty"`
	MLSGroupID  string    `db:"mls_group_id" json:"mls_group_id,omitempty"`
	HubVersion  int64     `db:"hub_version"  json:"hub_version"`
	HubEtag     string    `db:"hub_etag"     json:"hub_etag,omitempty"`
	CreatedAt   string    `db:"created_at"   json:"created_at"`
	UpdatedAt   string    `db:"updated_at"   json:"updated_at"`
	Members     []string  `db:"-"            json:"members,omitempty"`
}

// DispatchPolicy is a per-scope routing strategy. AllowBearers,
// DenyBearers, and PrecedenceOverride are JSON-encoded on the way in
// and out of SQLite; callers marshal/unmarshal explicitly when they
// need the structured form.
type DispatchPolicy struct {
	ID                 string      `db:"id"                  json:"id"`
	ScopeType          PolicyScope `db:"scope_type"          json:"scope_type"`
	ScopeID            string      `db:"scope_id"            json:"scope_id"`
	Strategy           Strategy    `db:"strategy"            json:"strategy"`
	MaxCostCents       *int        `db:"max_cost_cents"      json:"max_cost_cents,omitempty"`
	MaxLatencyMs       *int        `db:"max_latency_ms"      json:"max_latency_ms,omitempty"`
	AllowBearers       string      `db:"allow_bearers"       json:"allow_bearers"`
	DenyBearers        string      `db:"deny_bearers"        json:"deny_bearers"`
	PrecedenceOverride string      `db:"precedence_override" json:"precedence_override"`
	CreatedAt          string      `db:"created_at"          json:"created_at"`
	UpdatedAt          string      `db:"updated_at"          json:"updated_at"`
}

// ContactFilter is the selection criteria for ListContacts.
// Empty fields match any; NameLike uses SQL LIKE with wildcards.
type ContactFilter struct {
	TenantID string
	Team     string
	Role     string
	NameLike string
	Origin   Origin
	Limit    int
	Offset   int
}

// Sentinel errors returned by the Store.
var (
	ErrNotFound = errors.New("directory: not found")
	ErrConflict = errors.New("directory: conflict")
	ErrInvalid  = errors.New("directory: invalid argument")
)

// Store is the persistence interface for the directory. Concrete
// implementations must be safe for concurrent use.
type Store interface {
	// Contacts.
	CreateContact(ctx context.Context, c *Contact) error
	GetContact(ctx context.Context, id string) (*Contact, error)
	Resolve(ctx context.Context, id string) (*Contact, error)
	FindByAddress(ctx context.Context, k Kind, value string) (*Contact, error)
	ListContacts(ctx context.Context, f ContactFilter) ([]Contact, error)
	UpdateContact(ctx context.Context, c *Contact) error
	DeleteContact(ctx context.Context, id string) error

	// Addresses.
	AddAddress(ctx context.Context, a *Address) error
	GetAddress(ctx context.Context, id string) (*Address, error)
	ListAddresses(ctx context.Context, contactID string) ([]Address, error)
	UpdateAddress(ctx context.Context, a *Address) error
	DeleteAddress(ctx context.Context, id string) error

	// Keys.
	AddKey(ctx context.Context, k *ContactKey) error
	GetKey(ctx context.Context, id string) (*ContactKey, error)
	ListKeys(ctx context.Context, contactID string, onlyActive bool) ([]ContactKey, error)
	RetireKey(ctx context.Context, id string) error
	RevokeKey(ctx context.Context, id string) error

	// Groups.
	CreateGroup(ctx context.Context, g *Group) error
	GetGroup(ctx context.Context, id string) (*Group, error)
	ListGroups(ctx context.Context, tenantID string) ([]Group, error)
	UpdateGroup(ctx context.Context, g *Group) error
	DeleteGroup(ctx context.Context, id string) error
	AddMember(ctx context.Context, groupID, contactID, role string) error
	RemoveMember(ctx context.Context, groupID, contactID string) error
	ListGroupMembers(ctx context.Context, groupID string) ([]Contact, error)

	// Dispatch policy.
	GetPolicy(ctx context.Context, scope PolicyScope, scopeID string) (*DispatchPolicy, error)
	ListPolicies(ctx context.Context) ([]DispatchPolicy, error)
	UpsertPolicy(ctx context.Context, p *DispatchPolicy) error
	DeletePolicy(ctx context.Context, id string) error
}
