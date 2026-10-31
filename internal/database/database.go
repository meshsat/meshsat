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
	// modernc.org/sqlite reads connection pragmas as _pragma=name(value);
	// the mattn-style _journal_mode=WAL&_busy_timeout=... keys used here
	// until 6 Sep 2026 were ignored without error, so every kit ran a
	// rollback journal at synchronous FULL (three fsyncs per commit) with
	// no busy timeout. WAL + NORMAL is one append per commit and survives
	// the abrupt power loss the kits see. Foreign keys stay off on
	// purpose: enforcement never ran, so live data must be checked with
	// PRAGMA foreign_key_check before it is switched on. [MESHSAT-820]
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)", path)
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
	return db, nil
}
