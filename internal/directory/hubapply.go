package directory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
)

// The directory a tenant's Hub sends (HeMB-NG stage 2 C7). The push is a
// full snapshot, signed by the Hub and verified before it reaches here; it
// is applied in one transaction together with its version, or not at all.
// Ownership is by origin: what that Hub sent is "hub:<tenant>", what this
// bridge's operator made is "local", and a snapshot replaces only its own
// Hub's rows:
//   - a Hub contact the snapshot no longer has loses the Hub's fields,
//     addresses and memberships; if the operator added an address or an
//     override to it, it stays as a local contact with no Hub authority
//     (trust and Hub version reset), otherwise it is deleted;
//   - a local edit of a Hub contact (display name, notes) is kept in
//     local_override and survives every snapshot; a local address on it
//     too;
//   - a Hub policy the snapshot no longer has gives its scope back to the
//     bridge's own default (precedence, default) or goes (contact, group);
//   - a policy the Hub marks seeded is a Hub default, never applied over
//     the bridge's own (C7.7).
// A snapshot of another tenant than the one the bridge follows, or one not
// newer than the version applied, is refused and the reason recorded. A key
// rotation has its own counter (key_version): the Hub's rotation records and
// snapshots share one version sequence, and neither may stand in for the
// other (r19 C7-R1).
// A row that cannot be stored as sent (an unknown kind, an address a local
// row holds, an id a local row has) is skipped with its reason; the rest of
// the snapshot applies. Every address this Hub sent before is taken away
// before the snapshot's are written, so an address that moved from one Hub
// contact to another lands whatever order the contacts come in (r19 C7-R2).
// [MESHSAT-1584]

// HubSnapshot is a verified full snapshot in the bridge's terms.
type HubSnapshot struct {
	TenantID string
	Version  int64
	Contacts []HubContact
	Groups   []HubGroup
	Policies []PushedPolicy
}

// HubContact is one contact of a snapshot.
type HubContact struct {
	ID, DisplayName, GivenName, FamilyName, Org, Role, Team, SIDC, Notes string
	TrustLevel                                                           int
	TrustVerifiedAt                                                      *string
	HubVersion                                                           int64
	HubEtag                                                              string
	Addresses                                                            []HubAddress
}

// HubAddress is one address of a snapshot contact.
type HubAddress struct {
	ID, Kind, Value, Subvalue, Label string
	PrimaryRank                      int
	Verified                         bool
	BearerHint                       int
	MaxCostCents                     *int
}

// HubGroup is one group of a snapshot.
type HubGroup struct {
	ID, Name, Kind, SIDC, MLSGroupID string
	HubVersion                       int64
	HubEtag                          string
	MemberIDs                        []string
}

// HubSkip is a snapshot row not stored, and why.
type HubSkip struct {
	Kind   string `json:"kind"` // contact, address, group, member
	ID     string `json:"id"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// Reasons a snapshot or a row of it is not applied.
const (
	RefuseOtherTenant  = "other_tenant"
	RefuseNotNewer     = "not_newer"
	SkipIDTakenLocally = "id_taken_locally"
	SkipUnknownKind    = "unknown_kind"
	SkipAddressTaken   = "address_taken"
	SkipNoName         = "no_display_name"
	SkipUnknownMember  = "unknown_member"
)

// HubApplyReport says what a snapshot did.
type HubApplyReport struct {
	Applied          bool              `json:"applied"`
	Reason           string            `json:"reason,omitempty"`
	Detail           string            `json:"detail,omitempty"`
	TenantID         string            `json:"tenant_id"`
	AppliedVersion   int64             `json:"applied_version"` // the snapshot version held after it
	KeyVersion       int64             `json:"key_version"`     // the version of the rotation the key is from
	Contacts         int               `json:"contacts"`
	ContactsRemoved  int               `json:"contacts_removed"`
	ContactsKept     int               `json:"contacts_kept_local"`
	Groups           int               `json:"groups"`
	PoliciesApplied  []string          `json:"policies_applied"`
	PoliciesRejected []PolicyRejection `json:"policies_rejected"`
	PoliciesReverted int               `json:"policies_reverted"`
	Skipped          []HubSkip         `json:"skipped"`
}

// HubState is what the bridge has applied of its Hub's directory: the
// version of the last snapshot, and apart from it the version of the last
// key rotation.
type HubState struct {
	TenantID      string `db:"tenant_id"      json:"tenant_id"`
	Version       int64  `db:"version"        json:"version"`
	KeyVersion    int64  `db:"key_version"    json:"key_version"`
	AppliedAt     string `db:"applied_at"     json:"applied_at"`
	RefusedReason string `db:"refused_reason" json:"refused_reason,omitempty"`
	RefusedDetail string `db:"refused_detail" json:"refused_detail,omitempty"`
	RefusedAt     string `db:"refused_at"     json:"refused_at,omitempty"`
}

// HubOrigin is the origin of what tenant's Hub sent.
func HubOrigin(tenantID string) string { return "hub:" + tenantID }

// legacyHubOrigin is what a v1 apply wrote before v75; it belongs to
// whichever Hub sends next.
const legacyHubOrigin = string(OriginHub)

// IsHubOrigin reports whether a row came from a Hub (any tenant, or v1).
func IsHubOrigin(origin string) bool {
	return origin == legacyHubOrigin || strings.HasPrefix(origin, "hub:")
}

// bridgeDefaults is the strategy each scope gets back when the Hub's
// policy for it goes: the bridge's own seed as v48 and v65 left it.
var bridgeDefaults = map[string]Strategy{
	"default:":            StrategyPrimaryOnly,
	"precedence:Override": StrategyAllBearers, "precedence:Flash": StrategyAllBearers,
	"precedence:Immediate": StrategyAllBearers, "precedence:Priority": StrategyAllBearers,
	"precedence:Routine": StrategyPrimaryOnly, "precedence:Deferred": StrategyPrimaryOnly,
}

// GetHubState reads what the bridge has applied.
func (s *SQLStore) GetHubState(ctx context.Context) (HubState, error) {
	var st HubState
	err := s.db.GetContext(ctx, &st, `SELECT tenant_id, version, key_version, applied_at, refused_reason, refused_detail, refused_at
		FROM directory_hub_state WHERE id = 1`)
	if errors.Is(err, sql.ErrNoRows) {
		return HubState{}, nil
	}
	return st, translateErr(err)
}

// refuse records why a snapshot (or rotation) was not applied.
func refuse(ctx context.Context, tx *sqlx.Tx, reason, detail string) error {
	_, err := tx.ExecContext(ctx, `UPDATE directory_hub_state SET refused_reason = ?, refused_detail = ?, refused_at = ?
		WHERE id = 1`, reason, detail, now())
	return err
}

// checkHubVersion refuses another tenant than the one followed, or a
// version not above held (the snapshot's or the key's own counter), and
// records it; ok when it may apply. what names the record: snapshot or
// rotation.
func checkHubVersion(ctx context.Context, tx *sqlx.Tx, st HubState, what, tenantID string, version, held int64, rep *HubApplyReport) (bool, error) {
	rep.TenantID, rep.AppliedVersion, rep.KeyVersion = st.TenantID, st.Version, st.KeyVersion
	switch {
	case tenantID == "":
		rep.Reason, rep.Detail = RefuseOtherTenant, "the "+what+" names no tenant"
	case st.TenantID != "" && st.TenantID != tenantID:
		rep.Reason, rep.Detail = RefuseOtherTenant, fmt.Sprintf("the bridge follows tenant %q, the %s is %q", st.TenantID, what, tenantID)
	case version <= held:
		rep.Reason, rep.Detail = RefuseNotNewer, fmt.Sprintf("%s version %d, the bridge holds %d", what, version, held)
	default:
		return true, nil
	}
	return false, refuse(ctx, tx, rep.Reason, rep.Detail)
}

// ApplyHubSnapshot applies a verified full snapshot in one transaction. A
// refusal is a report with Applied false and the reason, not an error; an
// error means nothing was applied. supported says which strategies the
// bridge implements.
func (s *SQLStore) ApplyHubSnapshot(ctx context.Context, snap HubSnapshot, supported func(Strategy) bool) (HubApplyReport, error) {
	rep := HubApplyReport{PoliciesApplied: []string{}, PoliciesRejected: []PolicyRejection{}, Skipped: []HubSkip{}}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return rep, err
	}
	defer func() { _ = tx.Rollback() }()
	var st HubState
	if err := tx.GetContext(ctx, &st, `SELECT tenant_id, version, key_version, applied_at, refused_reason, refused_detail, refused_at
		FROM directory_hub_state WHERE id = 1`); err != nil {
		return rep, fmt.Errorf("read directory state: %w", err)
	}
	ok, err := checkHubVersion(ctx, tx, st, "snapshot", snap.TenantID, snap.Version, st.Version, &rep)
	if err != nil {
		return rep, err
	}
	if !ok {
		return rep, tx.Commit()
	}
	a := &hubApply{ctx: ctx, tx: tx, origin: HubOrigin(snap.TenantID), tenant: snap.TenantID, rep: &rep, ts: now()}
	if err := a.contacts(snap.Contacts); err != nil {
		return HubApplyReport{}, err
	}
	if err := a.groups(snap.Groups); err != nil {
		return HubApplyReport{}, err
	}
	if err := a.policies(snap.Policies, supported); err != nil {
		return HubApplyReport{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE directory_hub_state SET tenant_id = ?, version = ?, applied_at = ?,
		refused_reason = '', refused_detail = '', refused_at = '' WHERE id = 1`, snap.TenantID, snap.Version, a.ts); err != nil {
		return HubApplyReport{}, fmt.Errorf("write directory state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return HubApplyReport{}, err
	}
	rep.Applied, rep.TenantID, rep.AppliedVersion, rep.KeyVersion = true, snap.TenantID, snap.Version, st.KeyVersion
	return rep, nil
}

type hubApply struct {
	ctx     context.Context
	tx      *sqlx.Tx
	origin  string
	tenant  string
	rep     *HubApplyReport
	ts      string
	applied map[string]bool // the contacts this snapshot applied
}

func (a *hubApply) skip(kind, id, reason, detail string) {
	a.rep.Skipped = append(a.rep.Skipped, HubSkip{Kind: kind, ID: id, Reason: reason, Detail: detail})
}

// owned reports whether a row of origin may be replaced by this snapshot.
func (a *hubApply) owned(origin string) bool { return origin == a.origin || origin == legacyHubOrigin }

type override struct {
	DisplayName *string `json:"display_name,omitempty"`
	Notes       *string `json:"notes,omitempty"`
}

func parseOverride(raw string) override {
	var o override
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &o)
	}
	return o
}

func (o override) empty() bool { return o.DisplayName == nil && o.Notes == nil }

func (a *hubApply) contacts(in []HubContact) error {
	ctx, tx := a.ctx, a.tx
	type row struct {
		ID            string `db:"id"`
		Origin        string `db:"origin"`
		LocalOverride string `db:"local_override"`
	}
	var existing []row
	if err := tx.SelectContext(ctx, &existing, `SELECT id, origin, local_override FROM directory_contacts`); err != nil {
		return fmt.Errorf("read contacts: %w", err)
	}
	have := make(map[string]row, len(existing))
	for _, r := range existing {
		have[r.ID] = r
	}
	// Every address this Hub sent goes first, on every contact: the
	// snapshot is the whole set, and an address that moved between two of
	// its contacts must not meet its old row (r19 C7-R2). What the
	// operator added stays, and still wins over the Hub's.
	if _, err := tx.ExecContext(ctx, `DELETE FROM directory_addresses WHERE origin IN (?, ?)`, a.origin, legacyHubOrigin); err != nil {
		return fmt.Errorf("clear hub addresses: %w", err)
	}
	a.applied = map[string]bool{}
	for _, c := range in {
		if c.ID == "" {
			continue
		}
		prev, exists := have[c.ID]
		if exists && !a.owned(prev.Origin) {
			a.skip("contact", c.ID, SkipIDTakenLocally, "a contact of this bridge has the id")
			continue
		}
		name, notes := c.DisplayName, c.Notes
		ov := parseOverride(prev.LocalOverride)
		if ov.DisplayName != nil {
			name = *ov.DisplayName
		}
		if ov.Notes != nil {
			notes = *ov.Notes
		}
		if name == "" {
			a.skip("contact", c.ID, SkipNoName, "")
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO directory_contacts (id, tenant_id, display_name, given_name, family_name, org, role, team,
				sidc, notes, trust_level, trust_verified_at, hub_version, hub_etag, origin, local_override,
				created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '{}', ?, ?)
			ON CONFLICT(id) DO UPDATE SET tenant_id = excluded.tenant_id, display_name = excluded.display_name,
				given_name = excluded.given_name, family_name = excluded.family_name, org = excluded.org,
				role = excluded.role, team = excluded.team, sidc = excluded.sidc, notes = excluded.notes,
				trust_level = excluded.trust_level, trust_verified_at = excluded.trust_verified_at,
				hub_version = excluded.hub_version, hub_etag = excluded.hub_etag, origin = excluded.origin,
				updated_at = excluded.updated_at`,
			c.ID, a.tenant, name, c.GivenName, c.FamilyName, c.Org, c.Role, c.Team, c.SIDC, notes,
			c.TrustLevel, c.TrustVerifiedAt, c.HubVersion, c.HubEtag, a.origin, a.ts, a.ts); err != nil {
			return fmt.Errorf("store contact %s: %w", c.ID, err)
		}
		for _, ad := range c.Addresses {
			if err := a.address(c.ID, ad); err != nil {
				return err
			}
		}
		a.applied[c.ID] = true
		a.rep.Contacts++
	}
	// The Hub's contacts this snapshot no longer has.
	for _, r := range existing {
		if !a.owned(r.Origin) || a.applied[r.ID] {
			continue
		}
		if err := a.dropContact(r.ID, r.LocalOverride); err != nil {
			return err
		}
	}
	return nil
}

func (a *hubApply) address(contactID string, ad HubAddress) error {
	if !Kind(ad.Kind).Valid() {
		a.skip("address", ad.ID, SkipUnknownKind, ad.Kind)
		return nil
	}
	if ad.Value == "" {
		a.skip("address", ad.ID, SkipUnknownKind, "no value")
		return nil
	}
	id := ad.ID
	if id == "" {
		id = NewID()
	}
	hint := ad.BearerHint
	if hint == 0 {
		hint = 50
	}
	res, err := a.tx.ExecContext(a.ctx, `
		INSERT INTO directory_addresses (id, contact_id, kind, value, subvalue, label, primary_rank, verified,
			bearer_hint, max_cost_cents, created_at, updated_at, origin)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`,
		id, contactID, ad.Kind, ad.Value, ad.Subvalue, ad.Label, ad.PrimaryRank, ad.Verified, hint, ad.MaxCostCents,
		a.ts, a.ts, a.origin)
	if err != nil {
		return fmt.Errorf("store address %s: %w", ad.Value, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		a.skip("address", id, SkipAddressTaken, ad.Kind+" "+ad.Value)
	}
	return nil
}

// dropContact removes what the Hub owned of a contact it no longer sends:
// its addresses and memberships. What the operator added (an address, a
// name, notes) keeps the contact as a local one, rebuilt from those alone:
// no field the Hub wrote survives, nor its trust or version (r19 C7-R6).
// Without a local name it is called by its first local address.
func (a *hubApply) dropContact(id, overrideRaw string) error {
	ctx, tx := a.ctx, a.tx
	for _, q := range []string{
		`DELETE FROM directory_addresses WHERE contact_id = ? AND origin IN (?, ?)`,
		`DELETE FROM directory_group_members WHERE contact_id = ? AND origin IN (?, ?)`,
	} {
		if _, err := tx.ExecContext(ctx, q, id, a.origin, legacyHubOrigin); err != nil {
			return fmt.Errorf("drop hub rows of %s: %w", id, err)
		}
	}
	var local []struct {
		Kind  string `db:"kind"`
		Value string `db:"value"`
	}
	if err := tx.SelectContext(ctx, &local, `SELECT kind, value FROM directory_addresses WHERE contact_id = ?
		ORDER BY primary_rank ASC, created_at ASC, id ASC`, id); err != nil {
		return err
	}
	ov := parseOverride(overrideRaw)
	if len(local) > 0 || !ov.empty() {
		name, notes := LocalStubName, ""
		if ov.DisplayName != nil {
			name = *ov.DisplayName
		} else if len(local) > 0 {
			name = local[0].Kind + " " + local[0].Value
		}
		if ov.Notes != nil {
			notes = *ov.Notes
		}
		if _, err := tx.ExecContext(ctx, `UPDATE directory_contacts SET origin = ?, tenant_id = '', display_name = ?,
			given_name = '', family_name = '', org = '', role = '', team = '', sidc = '', notes = ?,
			trust_level = 0, trust_verified_at = NULL, trust_verified_by = '', hub_version = 0, hub_etag = '',
			local_override = '{}', updated_at = ? WHERE id = ?`, string(OriginLocal), name, notes, a.ts, id); err != nil {
			return fmt.Errorf("keep %s local: %w", id, err)
		}
		a.rep.ContactsKept++
		return nil
	}
	for _, q := range []string{
		`DELETE FROM directory_contact_keys WHERE contact_id = ?`,
		`DELETE FROM directory_group_members WHERE contact_id = ?`,
		`DELETE FROM directory_contacts WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return fmt.Errorf("delete %s: %w", id, err)
		}
	}
	a.rep.ContactsRemoved++
	return nil
}

// LocalStubName names a contact the Hub no longer sends, kept for what the
// operator added, when the operator gave it no name and no address.
const LocalStubName = "Local contact"

// LocalGroupName names a group the Hub no longer sends, kept for the
// members the operator added.
const LocalGroupName = "Local group"

func (a *hubApply) groups(in []HubGroup) error {
	ctx, tx := a.ctx, a.tx
	type row struct {
		ID     string `db:"id"`
		Origin string `db:"origin"`
	}
	var existing []row
	if err := tx.SelectContext(ctx, &existing, `SELECT id, origin FROM directory_groups`); err != nil {
		return fmt.Errorf("read groups: %w", err)
	}
	have := map[string]string{}
	for _, r := range existing {
		have[r.ID] = r.Origin
	}
	sent := map[string]bool{}
	for _, g := range in {
		if g.ID == "" {
			continue
		}
		kind := GroupKind(strings.ToUpper(g.Kind))
		if kind == "" {
			kind = GroupList
		}
		if !kind.Valid() {
			a.skip("group", g.ID, SkipUnknownKind, g.Kind)
			continue
		}
		if o, ok := have[g.ID]; ok && !a.owned(o) {
			a.skip("group", g.ID, SkipIDTakenLocally, "a group of this bridge has the id")
			continue
		}
		name := g.Name
		if name == "" {
			name = g.ID
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO directory_groups (id, tenant_id, display_name, kind, sidc, mls_group_id, hub_version, hub_etag,
				created_at, updated_at, origin)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET tenant_id = excluded.tenant_id, display_name = excluded.display_name,
				kind = excluded.kind, sidc = excluded.sidc, mls_group_id = excluded.mls_group_id,
				hub_version = excluded.hub_version, hub_etag = excluded.hub_etag, origin = excluded.origin,
				updated_at = excluded.updated_at`,
			g.ID, a.tenant, name, string(kind), g.SIDC, g.MLSGroupID, g.HubVersion, g.HubEtag, a.ts, a.ts, a.origin); err != nil {
			return fmt.Errorf("store group %s: %w", g.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM directory_group_members WHERE group_id = ? AND origin IN (?, ?)`,
			g.ID, a.origin, legacyHubOrigin); err != nil {
			return fmt.Errorf("clear members of %s: %w", g.ID, err)
		}
		for _, m := range g.MemberIDs {
			if !a.applied[m] {
				a.skip("member", g.ID+"/"+m, SkipUnknownMember, "not a contact of this snapshot")
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO directory_group_members (group_id, contact_id, role, added_at, origin)
				VALUES (?, ?, '', ?, ?)`, g.ID, m, a.ts, a.origin); err != nil {
				return fmt.Errorf("store member %s of %s: %w", m, g.ID, err)
			}
		}
		sent[g.ID] = true
		a.rep.Groups++
	}
	for _, r := range existing {
		if !a.owned(r.Origin) || sent[r.ID] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM directory_group_members WHERE group_id = ? AND origin IN (?, ?)`,
			r.ID, a.origin, legacyHubOrigin); err != nil {
			return err
		}
		var local int
		if err := tx.GetContext(ctx, &local, `SELECT COUNT(*) FROM directory_group_members WHERE group_id = ?`, r.ID); err != nil {
			return err
		}
		if local > 0 {
			// Kept for the operator's members; nothing the Hub wrote
			// stays but its kind, which the members were added to.
			if _, err := tx.ExecContext(ctx, `UPDATE directory_groups SET origin = ?, tenant_id = '', display_name = ?,
				sidc = '', mls_group_id = '', hub_version = 0, hub_etag = '', updated_at = ? WHERE id = ?`,
				string(OriginLocal), LocalGroupName, a.ts, r.ID); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM directory_groups WHERE id = ?`, r.ID); err != nil {
			return err
		}
	}
	return nil
}

func (a *hubApply) policies(in []PushedPolicy, supported func(Strategy) bool) error {
	ctx, tx := a.ctx, a.tx
	applied := map[string]bool{}
	for _, p := range in {
		reason, detail := CheckPushedPolicy(p, supported)
		if reason != "" {
			a.rep.PoliciesRejected = append(a.rep.PoliciesRejected, PolicyRejection{ID: p.ID, ScopeType: p.ScopeType,
				ScopeID: p.ScopeID, Strategy: string(p.Strategy), Reason: reason, Detail: detail})
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO directory_dispatch_policy (id, scope_type, scope_id, strategy, seeded, created_at, updated_at, origin)
			VALUES (?, ?, ?, ?, 0, ?, ?, ?)
			ON CONFLICT(scope_type, scope_id) DO UPDATE SET
				strategy = excluded.strategy, seeded = 0, origin = excluded.origin, updated_at = excluded.updated_at`,
			NewID(), p.ScopeType, p.ScopeID, string(p.Strategy), a.ts, a.ts, a.origin); err != nil {
			return fmt.Errorf("apply policy %s: %w", p.ID, err)
		}
		applied[p.ScopeType+":"+p.ScopeID] = true
		a.rep.PoliciesApplied = append(a.rep.PoliciesApplied, p.ID)
	}
	type row struct {
		ID        string `db:"id"`
		ScopeType string `db:"scope_type"`
		ScopeID   string `db:"scope_id"`
		Origin    string `db:"origin"`
	}
	var owned []row
	if err := tx.SelectContext(ctx, &owned, `SELECT id, scope_type, scope_id, origin FROM directory_dispatch_policy
		WHERE origin = ? OR origin = ?`, a.origin, legacyHubOrigin); err != nil {
		return err
	}
	for _, r := range owned {
		key := r.ScopeType + ":" + r.ScopeID
		if applied[key] {
			continue
		}
		if def, ok := bridgeDefaults[key]; ok {
			if _, err := tx.ExecContext(ctx, `UPDATE directory_dispatch_policy SET strategy = ?, seeded = 1, origin = ?,
				updated_at = ? WHERE id = ?`, string(def), string(OriginLocal), a.ts, r.ID); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `DELETE FROM directory_dispatch_policy WHERE id = ?`, r.ID); err != nil {
			return err
		}
		a.rep.PoliciesReverted++
	}
	return nil
}

// SetLocalOverride is this bridge's operator editing a contact's display
// name or notes (nil leaves a field as it is). On a Hub contact the value is
// kept in local_override, so the next snapshot does not undo it; on a local
// contact it is simply the contact's value.
func (s *SQLStore) SetLocalOverride(ctx context.Context, id string, displayName, notes *string) error {
	if id == "" {
		return fmt.Errorf("%w: empty id", ErrInvalid)
	}
	if displayName != nil && *displayName == "" {
		return fmt.Errorf("%w: display_name cannot be empty", ErrInvalid)
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var cur struct {
		Origin        string `db:"origin"`
		LocalOverride string `db:"local_override"`
		DisplayName   string `db:"display_name"`
		Notes         string `db:"notes"`
	}
	if err := tx.GetContext(ctx, &cur, `SELECT origin, local_override, display_name, notes FROM directory_contacts WHERE id = ?`, id); err != nil {
		return translateErr(err)
	}
	ov := parseOverride(cur.LocalOverride)
	name, text := cur.DisplayName, cur.Notes
	if displayName != nil {
		name = *displayName
		if IsHubOrigin(cur.Origin) {
			ov.DisplayName = displayName
		}
	}
	if notes != nil {
		text = *notes
		if IsHubOrigin(cur.Origin) {
			ov.Notes = notes
		}
	}
	raw, err := json.Marshal(ov)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE directory_contacts SET display_name = ?, notes = ?, local_override = ?, updated_at = ?
		WHERE id = ?`, name, text, string(raw), now(), id); err != nil {
		return translateErr(err)
	}
	return tx.Commit()
}

// RotationKeyConfig is the system_config key of the pinned Hub directory
// key (hex PKIX DER), set at pairing and moved by a rotation.
const RotationKeyConfig = "hub_directory_signing_pubkey"

// ApplyHubRotation trusts newKeyPKIX from now on: a rotation record the
// caller verified under the key the bridge trusted. Like a snapshot it
// names the tenant the bridge follows; its version must be above the last
// rotation's (key_version), whatever snapshot the bridge holds, and it moves
// the key and key_version in one transaction. It leaves the snapshot
// version alone: the record carries no directory rows, so a snapshot of the
// same version still applies after it (r19 C7-R1). A refusal is a report
// with Applied false and the reason.
func (s *SQLStore) ApplyHubRotation(ctx context.Context, tenantID string, version int64, newKeyPKIX []byte) (HubApplyReport, error) {
	rep := HubApplyReport{PoliciesApplied: []string{}, PoliciesRejected: []PolicyRejection{}, Skipped: []HubSkip{}}
	if len(newKeyPKIX) == 0 {
		return rep, fmt.Errorf("%w: no key", ErrInvalid)
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return rep, err
	}
	defer func() { _ = tx.Rollback() }()
	var st HubState
	if err := tx.GetContext(ctx, &st, `SELECT tenant_id, version, key_version, applied_at, refused_reason, refused_detail, refused_at
		FROM directory_hub_state WHERE id = 1`); err != nil {
		return rep, fmt.Errorf("read directory state: %w", err)
	}
	ok, err := checkHubVersion(ctx, tx, st, "rotation", tenantID, version, st.KeyVersion, &rep)
	if err != nil {
		return rep, err
	}
	if !ok {
		return rep, tx.Commit()
	}
	ts := now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO system_config (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		RotationKeyConfig, fmt.Sprintf("%x", newKeyPKIX), ts); err != nil {
		return rep, fmt.Errorf("pin the new key: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE directory_hub_state SET tenant_id = ?, key_version = ? WHERE id = 1`,
		tenantID, version); err != nil {
		return rep, fmt.Errorf("write directory state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return rep, err
	}
	rep.Applied, rep.TenantID, rep.AppliedVersion, rep.KeyVersion = true, tenantID, st.Version, version
	return rep, nil
}

// ErrNotATeam: the group exists and is not of kind TEAM.
var ErrNotATeam = errors.New("directory: the group is not a team")

// TeamMembers returns the contact ids of a TEAM group, in the order they
// joined, for a team send (C8). ErrNotFound when there is no such group,
// ErrNotATeam when it is another kind.
func (s *SQLStore) TeamMembers(ctx context.Context, groupID string) ([]string, error) {
	if groupID == "" {
		return nil, fmt.Errorf("%w: empty id", ErrInvalid)
	}
	var kind string
	if err := s.db.GetContext(ctx, &kind, `SELECT kind FROM directory_groups WHERE id = ?`, groupID); err != nil {
		return nil, translateErr(err)
	}
	if GroupKind(kind) != GroupTeam {
		return nil, fmt.Errorf("%w: %s is a %s", ErrNotATeam, groupID, kind)
	}
	var ids []string
	err := s.db.SelectContext(ctx, &ids, `SELECT contact_id FROM directory_group_members WHERE group_id = ?
		ORDER BY added_at ASC, contact_id ASC`, groupID)
	return ids, translateErr(err)
}
