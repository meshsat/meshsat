package database

import (
	"fmt"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

// DB wraps sqlx.DB with MeshSat-specific operations.
type DB struct {
	*sqlx.DB
}

// New opens a SQLite database at the given path and runs migrations.
func New(path string) (*DB, error) {
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=ON&_synchronous=NORMAL", path)
	conn, err := sqlx.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	conn.SetMaxOpenConns(1) // SQLite write serialization
	conn.SetMaxIdleConns(1)

	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}

	db := &DB{DB: conn}
	if err := db.migrate(); err != nil {
		return nil, fmt.Errorf("run migrations: %w", err)
	}
	// [MESHSAT-687] One-shot self-heal for bond labels that drifted
	// from reality on kits provisioned before recomputeBondLabel was
	// wired into Insert/Delete. Only touches labels that look
	// auto-generated (heuristic in bond_groups.go :: looksAutoBondLabel);
	// human-authored labels are never rewritten. Runs once per bridge
	// startup — cheap (one query per bond group, typically 1–2 rows).
	db.selfHealBondLabels()
	return db, nil
}

// selfHealBondLabels walks every existing bond group and recomputes
// its label from live members. See recomputeBondLabel for the auto-
// vs-human heuristic that protects operator-authored names.
// [MESHSAT-687]
func (db *DB) selfHealBondLabels() {
	groups, err := db.GetAllBondGroups()
	if err != nil {
		return
	}
	for _, g := range groups {
		db.recomputeBondLabel(g.ID)
	}
}
