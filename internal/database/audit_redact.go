package database

import (
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// AuditRowsAll returns the whole audit chain, oldest first, for a full
// verification. [MESHSAT-1137]
func (db *DB) AuditRowsAll() ([]AuditLogEntry, error) {
	var rows []AuditLogEntry
	if err := db.Select(&rows, "SELECT * FROM audit_log ORDER BY id ASC"); err != nil {
		return nil, fmt.Errorf("audit rows: %w", err)
	}
	return rows, nil
}

// AuditRowsToRedact returns the rows written before cutoff that were never
// redacted, oldest first. Timestamps are RFC 3339 UTC, so text order is
// time order.
func (db *DB) AuditRowsToRedact(cutoff time.Time) ([]AuditLogEntry, error) {
	var rows []AuditLogEntry
	if err := db.Select(&rows, "SELECT * FROM audit_log WHERE redacted_at IS NULL AND timestamp < ? ORDER BY id ASC",
		cutoff.UTC().Format("2006-01-02T15:04:05Z")); err != nil {
		return nil, fmt.Errorf("audit rows to redact: %w", err)
	}
	return rows, nil
}

// RedactAuditRows replaces the detail of the given rows and marks them
// redacted, in one transaction.
func (db *DB) RedactAuditRows(details map[int64]string, at time.Time) (int64, error) {
	var n int64
	err := db.withTx(func(tx *sqlx.Tx) error {
		for id, detail := range details {
			res, err := tx.Exec(`UPDATE audit_log SET detail = ?, redacted_at = ? WHERE id = ? AND redacted_at IS NULL`,
				detail, at.UTC().Format(time.RFC3339), id)
			if err != nil {
				return fmt.Errorf("redact audit row %d: %w", id, err)
			}
			m, _ := res.RowsAffected()
			n += m
		}
		return nil
	})
	return n, err
}
