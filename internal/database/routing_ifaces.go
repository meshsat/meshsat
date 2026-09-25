package database

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// RoutingIface is a row of routing_ifaces: one dynamic Reticulum interface
// (rnode, udp, auto, kiss) managed at runtime. [MESHSAT-1350]
type RoutingIface struct {
	ID        string `db:"id" json:"id"`
	Type      string `db:"type" json:"type"`
	Enabled   bool   `db:"enabled" json:"enabled"`
	Config    string `db:"config" json:"config"`
	CreatedAt string `db:"created_at" json:"created_at"`
	UpdatedAt string `db:"updated_at" json:"updated_at"`
}

// ErrRoutingIfaceNotFound is returned when an interface id is unknown.
var ErrRoutingIfaceNotFound = errors.New("routing interface not found")

// ListRoutingIfaces returns every dynamic interface, ordered by id.
func (db *DB) ListRoutingIfaces() ([]RoutingIface, error) {
	var out []RoutingIface
	if err := db.Select(&out, `SELECT id, type, enabled, config, created_at, updated_at FROM routing_ifaces ORDER BY id`); err != nil {
		return nil, fmt.Errorf("list routing ifaces: %w", err)
	}
	return out, nil
}

// GetRoutingIface returns one dynamic interface.
func (db *DB) GetRoutingIface(id string) (*RoutingIface, error) {
	var r RoutingIface
	if err := db.Get(&r, `SELECT id, type, enabled, config, created_at, updated_at FROM routing_ifaces WHERE id = ?`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRoutingIfaceNotFound
		}
		return nil, err
	}
	return &r, nil
}

// SaveRoutingIface inserts or updates a dynamic interface.
func (db *DB) SaveRoutingIface(r RoutingIface) error {
	_, err := db.Exec(`INSERT INTO routing_ifaces (id, type, enabled, config, created_at, updated_at)
		VALUES (?, ?, ?, ?, datetime('now'), datetime('now'))
		ON CONFLICT(id) DO UPDATE SET type = excluded.type, enabled = excluded.enabled,
			config = excluded.config, updated_at = datetime('now')`,
		r.ID, r.Type, r.Enabled, r.Config)
	return err
}

// DeleteRoutingIface removes a dynamic interface row.
func (db *DB) DeleteRoutingIface(id string) error {
	res, err := db.Exec(`DELETE FROM routing_ifaces WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrRoutingIfaceNotFound
	}
	return nil
}

// NextRoutingIfaceID returns the first free "<type>_N" id for a type.
func (db *DB) NextRoutingIfaceID(ifType string) (string, error) {
	rows, err := db.ListRoutingIfaces()
	if err != nil {
		return "", err
	}
	used := make(map[string]bool, len(rows))
	for _, r := range rows {
		used[r.ID] = true
	}
	for n := 0; n < 1000; n++ {
		id := fmt.Sprintf("%s_%d", strings.ToLower(ifType), n)
		if !used[id] {
			return id, nil
		}
	}
	return "", fmt.Errorf("no free id for type %s", ifType)
}
