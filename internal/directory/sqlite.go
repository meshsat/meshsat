package directory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// SQLStore binds [Store] to a SQLite database (via sqlx). The
// caller owns the *sqlx.DB and its lifecycle; the Store layer
// performs no migrations of its own — it assumes the schema from
// migrations.go v44-v48 is already in place.
type SQLStore struct {
	db *sqlx.DB
}

// NewSQLStore wraps a sqlx handle. Not a migrator.
func NewSQLStore(db *sqlx.DB) *SQLStore {
	return &SQLStore{db: db}
}

// NewID generates a UUIDv4 string suitable for any directory_* PK.
// Legacy rows backfilled by v44/v45 use 32-char hex; both coexist
// safely in a TEXT PRIMARY KEY column.
func NewID() string {
	return uuid.NewString()
}

// now returns the stored textual timestamp in the same format
// SQLite's datetime('now') produces, so Go-generated and
// SQL-generated timestamps sort identically.
func now() string {
	return time.Now().UTC().Format("2006-01-02 15:04:05")
}

// -- Contacts ----------------------------------------------------------

const contactCols = `
	id, tenant_id, display_name, given_name, family_name, org, role, team,
	sidc, notes, trust_level, trust_verified_at, trust_verified_by,
	hub_version, hub_etag, origin, legacy_contact_id, created_at, updated_at`

func (s *SQLStore) CreateContact(ctx context.Context, c *Contact) error {
	if c == nil {
		return fmt.Errorf("%w: nil contact", ErrInvalid)
	}
	if c.DisplayName == "" {
		return fmt.Errorf("%w: display_name required", ErrInvalid)
	}
	if c.ID == "" {
		c.ID = NewID()
	}
	if c.Origin == "" {
		c.Origin = OriginLocal
	}
	ts := now()
	if c.CreatedAt == "" {
		c.CreatedAt = ts
	}
	c.UpdatedAt = ts

	_, err := s.db.NamedExecContext(ctx, `
		INSERT INTO directory_contacts (`+contactCols+`) VALUES (
			:id, :tenant_id, :display_name, :given_name, :family_name, :org,
			:role, :team, :sidc, :notes, :trust_level, :trust_verified_at,
			:trust_verified_by, :hub_version, :hub_etag, :origin,
			:legacy_contact_id, :created_at, :updated_at)`, c)
	return translateErr(err)
}

func (s *SQLStore) GetContact(ctx context.Context, id string) (*Contact, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: empty id", ErrInvalid)
	}
	var c Contact
	err := s.db.GetContext(ctx, &c,
		`SELECT `+contactCols+` FROM directory_contacts WHERE id = ?`, id)
	if err != nil {
		return nil, translateErr(err)
	}
	return &c, nil
}

// Resolve returns the contact identified by id together with every
// address and every key row (regardless of status — callers that want
// only active keys use ListKeys).
func (s *SQLStore) Resolve(ctx context.Context, id string) (*Contact, error) {
	c, err := s.GetContact(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.attach(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// FindByAddress returns the contact owning the given (kind, value)
// address, with all addresses and keys eagerly loaded.
func (s *SQLStore) FindByAddress(ctx context.Context, k Kind, value string) (*Contact, error) {
	if !k.Valid() {
		return nil, fmt.Errorf("%w: unknown kind %q", ErrInvalid, k)
	}
	if value == "" {
		return nil, fmt.Errorf("%w: empty value", ErrInvalid)
	}
	var c Contact
	err := s.db.GetContext(ctx, &c, `
		SELECT `+qualified("dc", contactCols)+`
		FROM directory_contacts dc
		JOIN directory_addresses da ON da.contact_id = dc.id
		WHERE da.kind = ? AND da.value = ?`, string(k), value)
	if err != nil {
		return nil, translateErr(err)
	}
	if err := s.attach(ctx, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// attach loads Addresses and Keys for c in-place.
func (s *SQLStore) attach(ctx context.Context, c *Contact) error {
	addrs, err := s.ListAddresses(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("attach addresses: %w", err)
	}
	c.Addresses = addrs
	keys, err := s.ListKeys(ctx, c.ID, false)
	if err != nil {
		return fmt.Errorf("attach keys: %w", err)
	}
	c.Keys = keys
	return nil
}

func (s *SQLStore) ListContacts(ctx context.Context, f ContactFilter) ([]Contact, error) {
	q := strings.Builder{}
	q.WriteString(`SELECT ` + contactCols + ` FROM directory_contacts WHERE 1=1`)
	args := []any{}
	if f.TenantID != "" {
		q.WriteString(` AND tenant_id = ?`)
		args = append(args, f.TenantID)
	}
	if f.Team != "" {
		q.WriteString(` AND team = ?`)
		args = append(args, f.Team)
	}
	if f.Role != "" {
		q.WriteString(` AND role = ?`)
		args = append(args, f.Role)
	}
	if f.NameLike != "" {
		q.WriteString(` AND display_name LIKE ?`)
		args = append(args, f.NameLike)
	}
	if f.Origin != "" {
		q.WriteString(` AND origin = ?`)
		args = append(args, string(f.Origin))
	}
	q.WriteString(` ORDER BY display_name ASC`)
	if f.Limit > 0 {
		q.WriteString(` LIMIT ?`)
		args = append(args, f.Limit)
		if f.Offset > 0 {
			q.WriteString(` OFFSET ?`)
			args = append(args, f.Offset)
		}
	}
	var out []Contact
	if err := s.db.SelectContext(ctx, &out, q.String(), args...); err != nil {
		return nil, translateErr(err)
	}
	return out, nil
}

func (s *SQLStore) UpdateContact(ctx context.Context, c *Contact) error {
	if c == nil || c.ID == "" {
		return fmt.Errorf("%w: nil or empty id", ErrInvalid)
	}
	c.UpdatedAt = now()
	res, err := s.db.NamedExecContext(ctx, `
		UPDATE directory_contacts SET
			tenant_id=:tenant_id, display_name=:display_name,
			given_name=:given_name, family_name=:family_name, org=:org,
			role=:role, team=:team, sidc=:sidc, notes=:notes,
			trust_level=:trust_level, trust_verified_at=:trust_verified_at,
			trust_verified_by=:trust_verified_by, hub_version=:hub_version,
			hub_etag=:hub_etag, origin=:origin, updated_at=:updated_at
		WHERE id=:id`, c)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteContact removes the contact and all dependent rows (addresses,
// keys, group memberships). Runs in an explicit transaction because
// the production DSN does not reliably enforce ON DELETE CASCADE (see
// contacts.DeleteContact for the same defence).
func (s *SQLStore) DeleteContact(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty id", ErrInvalid)
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, q := range []string{
		`DELETE FROM directory_addresses       WHERE contact_id = ?`,
		`DELETE FROM directory_contact_keys    WHERE contact_id = ?`,
		`DELETE FROM directory_group_members   WHERE contact_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return fmt.Errorf("cascade %q: %w", q, err)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM directory_contacts WHERE id = ?`, id)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// -- Addresses ---------------------------------------------------------

const addrCols = `
	id, contact_id, kind, value, subvalue, label, primary_rank, verified,
	bearer_hint, max_cost_cents, created_at, updated_at`

func (s *SQLStore) AddAddress(ctx context.Context, a *Address) error {
	if a == nil {
		return fmt.Errorf("%w: nil address", ErrInvalid)
	}
	if a.ContactID == "" {
		return fmt.Errorf("%w: contact_id required", ErrInvalid)
	}
	if !a.Kind.Valid() {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalid, a.Kind)
	}
	if a.Value == "" {
		return fmt.Errorf("%w: value required", ErrInvalid)
	}
	if a.ID == "" {
		a.ID = NewID()
	}
	ts := now()
	if a.CreatedAt == "" {
		a.CreatedAt = ts
	}
	a.UpdatedAt = ts
	if a.BearerHint == 0 {
		a.BearerHint = 50
	}
	_, err := s.db.NamedExecContext(ctx, `
		INSERT INTO directory_addresses (`+addrCols+`) VALUES (
			:id, :contact_id, :kind, :value, :subvalue, :label, :primary_rank,
			:verified, :bearer_hint, :max_cost_cents, :created_at, :updated_at)`, a)
	return translateErr(err)
}

func (s *SQLStore) GetAddress(ctx context.Context, id string) (*Address, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: empty id", ErrInvalid)
	}
	var a Address
	err := s.db.GetContext(ctx, &a,
		`SELECT `+addrCols+` FROM directory_addresses WHERE id = ?`, id)
	if err != nil {
		return nil, translateErr(err)
	}
	return &a, nil
}

func (s *SQLStore) ListAddresses(ctx context.Context, contactID string) ([]Address, error) {
	if contactID == "" {
		return nil, fmt.Errorf("%w: empty contact_id", ErrInvalid)
	}
	var addrs []Address
	err := s.db.SelectContext(ctx, &addrs, `
		SELECT `+addrCols+` FROM directory_addresses
		WHERE contact_id = ? ORDER BY primary_rank ASC, kind ASC, value ASC`, contactID)
	if err != nil {
		return nil, translateErr(err)
	}
	return addrs, nil
}

func (s *SQLStore) UpdateAddress(ctx context.Context, a *Address) error {
	if a == nil || a.ID == "" {
		return fmt.Errorf("%w: nil or empty id", ErrInvalid)
	}
	a.UpdatedAt = now()
	res, err := s.db.NamedExecContext(ctx, `
		UPDATE directory_addresses SET
			kind=:kind, value=:value, subvalue=:subvalue, label=:label,
			primary_rank=:primary_rank, verified=:verified,
			bearer_hint=:bearer_hint, max_cost_cents=:max_cost_cents,
			updated_at=:updated_at
		WHERE id=:id`, a)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) DeleteAddress(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty id", ErrInvalid)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM directory_addresses WHERE id = ?`, id)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// -- Keys --------------------------------------------------------------

const keyCols = `
	id, contact_id, kind, version, status, public_data, encrypted_priv,
	valid_from, valid_until, rotated_at, trust_anchor, created_at`

func (s *SQLStore) AddKey(ctx context.Context, k *ContactKey) error {
	if k == nil {
		return fmt.Errorf("%w: nil key", ErrInvalid)
	}
	if k.ContactID == "" {
		return fmt.Errorf("%w: contact_id required", ErrInvalid)
	}
	if k.Kind == "" {
		return fmt.Errorf("%w: kind required", ErrInvalid)
	}
	if k.ID == "" {
		k.ID = NewID()
	}
	if k.Version == 0 {
		k.Version = 1
	}
	if k.Status == "" {
		k.Status = KeyActive
	}
	if k.TrustAnchor == "" {
		k.TrustAnchor = TrustAnchorLocal
	}
	ts := now()
	if k.ValidFrom == "" {
		k.ValidFrom = ts
	}
	if k.CreatedAt == "" {
		k.CreatedAt = ts
	}
	_, err := s.db.NamedExecContext(ctx, `
		INSERT INTO directory_contact_keys (`+keyCols+`) VALUES (
			:id, :contact_id, :kind, :version, :status, :public_data,
			:encrypted_priv, :valid_from, :valid_until, :rotated_at,
			:trust_anchor, :created_at)`, k)
	return translateErr(err)
}

func (s *SQLStore) GetKey(ctx context.Context, id string) (*ContactKey, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: empty id", ErrInvalid)
	}
	var k ContactKey
	err := s.db.GetContext(ctx, &k,
		`SELECT `+keyCols+` FROM directory_contact_keys WHERE id = ?`, id)
	if err != nil {
		return nil, translateErr(err)
	}
	return &k, nil
}

func (s *SQLStore) ListKeys(ctx context.Context, contactID string, onlyActive bool) ([]ContactKey, error) {
	if contactID == "" {
		return nil, fmt.Errorf("%w: empty contact_id", ErrInvalid)
	}
	q := `SELECT ` + keyCols + ` FROM directory_contact_keys WHERE contact_id = ?`
	args := []any{contactID}
	if onlyActive {
		q += ` AND status = 'active'`
	}
	q += ` ORDER BY kind ASC, version DESC`
	var keys []ContactKey
	if err := s.db.SelectContext(ctx, &keys, q, args...); err != nil {
		return nil, translateErr(err)
	}
	return keys, nil
}

func (s *SQLStore) RetireKey(ctx context.Context, id string) error {
	return s.setKeyStatus(ctx, id, KeyRetired)
}

func (s *SQLStore) RevokeKey(ctx context.Context, id string) error {
	return s.setKeyStatus(ctx, id, KeyRevoked)
}

func (s *SQLStore) setKeyStatus(ctx context.Context, id string, st KeyStatus) error {
	if id == "" {
		return fmt.Errorf("%w: empty id", ErrInvalid)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE directory_contact_keys SET status = ?, rotated_at = ? WHERE id = ?`,
		string(st), now(), id)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// -- Groups ------------------------------------------------------------

const groupCols = `
	id, tenant_id, display_name, kind, sidc, mls_group_id,
	hub_version, hub_etag, created_at, updated_at`

func (s *SQLStore) CreateGroup(ctx context.Context, g *Group) error {
	if g == nil {
		return fmt.Errorf("%w: nil group", ErrInvalid)
	}
	if g.DisplayName == "" {
		return fmt.Errorf("%w: display_name required", ErrInvalid)
	}
	if g.Kind == "" {
		return fmt.Errorf("%w: kind required", ErrInvalid)
	}
	if g.ID == "" {
		g.ID = NewID()
	}
	ts := now()
	if g.CreatedAt == "" {
		g.CreatedAt = ts
	}
	g.UpdatedAt = ts
	_, err := s.db.NamedExecContext(ctx, `
		INSERT INTO directory_groups (`+groupCols+`) VALUES (
			:id, :tenant_id, :display_name, :kind, :sidc, :mls_group_id,
			:hub_version, :hub_etag, :created_at, :updated_at)`, g)
	return translateErr(err)
}

func (s *SQLStore) GetGroup(ctx context.Context, id string) (*Group, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: empty id", ErrInvalid)
	}
	var g Group
	err := s.db.GetContext(ctx, &g, `SELECT `+groupCols+` FROM directory_groups WHERE id = ?`, id)
	if err != nil {
		return nil, translateErr(err)
	}
	// Load members (contact IDs only — callers that need full contacts use ListGroupMembers).
	mids, err := s.memberIDs(ctx, id)
	if err != nil {
		return nil, err
	}
	g.Members = mids
	return &g, nil
}

func (s *SQLStore) memberIDs(ctx context.Context, groupID string) ([]string, error) {
	var ids []string
	err := s.db.SelectContext(ctx, &ids,
		`SELECT contact_id FROM directory_group_members WHERE group_id = ? ORDER BY added_at ASC`, groupID)
	return ids, translateErr(err)
}

func (s *SQLStore) ListGroups(ctx context.Context, tenantID string) ([]Group, error) {
	q := `SELECT ` + groupCols + ` FROM directory_groups`
	args := []any{}
	if tenantID != "" {
		q += ` WHERE tenant_id = ?`
		args = append(args, tenantID)
	}
	q += ` ORDER BY display_name ASC`
	var groups []Group
	if err := s.db.SelectContext(ctx, &groups, q, args...); err != nil {
		return nil, translateErr(err)
	}
	return groups, nil
}

func (s *SQLStore) UpdateGroup(ctx context.Context, g *Group) error {
	if g == nil || g.ID == "" {
		return fmt.Errorf("%w: nil or empty id", ErrInvalid)
	}
	g.UpdatedAt = now()
	res, err := s.db.NamedExecContext(ctx, `
		UPDATE directory_groups SET
			tenant_id=:tenant_id, display_name=:display_name, kind=:kind,
			sidc=:sidc, mls_group_id=:mls_group_id, hub_version=:hub_version,
			hub_etag=:hub_etag, updated_at=:updated_at
		WHERE id=:id`, g)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) DeleteGroup(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty id", ErrInvalid)
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM directory_group_members WHERE group_id = ?`, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM directory_groups WHERE id = ?`, id)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *SQLStore) AddMember(ctx context.Context, groupID, contactID, role string) error {
	if groupID == "" || contactID == "" {
		return fmt.Errorf("%w: empty group_id or contact_id", ErrInvalid)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO directory_group_members (group_id, contact_id, role, added_at)
		VALUES (?, ?, ?, ?)`, groupID, contactID, role, now())
	return translateErr(err)
}

func (s *SQLStore) RemoveMember(ctx context.Context, groupID, contactID string) error {
	if groupID == "" || contactID == "" {
		return fmt.Errorf("%w: empty group_id or contact_id", ErrInvalid)
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM directory_group_members WHERE group_id = ? AND contact_id = ?`,
		groupID, contactID)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) ListGroupMembers(ctx context.Context, groupID string) ([]Contact, error) {
	if groupID == "" {
		return nil, fmt.Errorf("%w: empty group_id", ErrInvalid)
	}
	var contacts []Contact
	err := s.db.SelectContext(ctx, &contacts, `
		SELECT `+qualified("dc", contactCols)+`
		FROM directory_contacts dc
		JOIN directory_group_members m ON m.contact_id = dc.id
		WHERE m.group_id = ? ORDER BY dc.display_name ASC`, groupID)
	if err != nil {
		return nil, translateErr(err)
	}
	return contacts, nil
}

// -- Dispatch policy ---------------------------------------------------

const policyCols = `
	id, scope_type, scope_id, strategy, max_cost_cents, max_latency_ms,
	allow_bearers, deny_bearers, precedence_override, created_at, updated_at`

func (s *SQLStore) GetPolicy(ctx context.Context, scope PolicyScope, scopeID string) (*DispatchPolicy, error) {
	if scope == "" {
		return nil, fmt.Errorf("%w: empty scope_type", ErrInvalid)
	}
	var p DispatchPolicy
	err := s.db.GetContext(ctx, &p, `
		SELECT `+policyCols+` FROM directory_dispatch_policy
		WHERE scope_type = ? AND scope_id = ?`, string(scope), scopeID)
	if err != nil {
		return nil, translateErr(err)
	}
	return &p, nil
}

func (s *SQLStore) ListPolicies(ctx context.Context) ([]DispatchPolicy, error) {
	var out []DispatchPolicy
	err := s.db.SelectContext(ctx, &out,
		`SELECT `+policyCols+` FROM directory_dispatch_policy ORDER BY scope_type ASC, scope_id ASC`)
	return out, translateErr(err)
}

func (s *SQLStore) UpsertPolicy(ctx context.Context, p *DispatchPolicy) error {
	if p == nil {
		return fmt.Errorf("%w: nil policy", ErrInvalid)
	}
	if p.ScopeType == "" || p.Strategy == "" {
		return fmt.Errorf("%w: scope_type and strategy required", ErrInvalid)
	}
	if p.ID == "" {
		p.ID = NewID()
	}
	if p.AllowBearers == "" {
		p.AllowBearers = "[]"
	}
	if p.DenyBearers == "" {
		p.DenyBearers = "[]"
	}
	if p.PrecedenceOverride == "" {
		p.PrecedenceOverride = "{}"
	}
	ts := now()
	if p.CreatedAt == "" {
		p.CreatedAt = ts
	}
	p.UpdatedAt = ts
	_, err := s.db.NamedExecContext(ctx, `
		INSERT INTO directory_dispatch_policy (`+policyCols+`) VALUES (
			:id, :scope_type, :scope_id, :strategy, :max_cost_cents,
			:max_latency_ms, :allow_bearers, :deny_bearers,
			:precedence_override, :created_at, :updated_at)
		ON CONFLICT(scope_type, scope_id) DO UPDATE SET
			strategy = excluded.strategy,
			max_cost_cents = excluded.max_cost_cents,
			max_latency_ms = excluded.max_latency_ms,
			allow_bearers = excluded.allow_bearers,
			deny_bearers = excluded.deny_bearers,
			precedence_override = excluded.precedence_override,
			updated_at = excluded.updated_at`, p)
	return translateErr(err)
}

func (s *SQLStore) DeletePolicy(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty id", ErrInvalid)
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM directory_dispatch_policy WHERE id = ?`, id)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// -- helpers -----------------------------------------------------------

// qualified prefixes every column identifier in colsCSV with alias.
// SQLite doesn't error on unqualified column lists against a JOIN but
// sqlx's struct-scanning gets upset when two tables share a column
// name (e.g. id, created_at), so we namespace the primary table.
func qualified(alias, colsCSV string) string {
	var sb strings.Builder
	for i, raw := range strings.Split(colsCSV, ",") {
		col := strings.TrimSpace(raw)
		if col == "" {
			continue
		}
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(alias + "." + col)
	}
	return sb.String()
}

// translateErr maps a database error into the sentinel the Store
// contract exposes. UNIQUE / PRIMARY KEY collisions become ErrConflict;
// sql.ErrNoRows becomes ErrNotFound; everything else passes through.
func translateErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	msg := err.Error()
	if strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "PRIMARY KEY") {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return err
}
