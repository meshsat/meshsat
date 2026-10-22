package database

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// LXMFMidRetention is how long an LXMF message id is remembered: a repeat
// of the message inside it is dropped (MeshSat Android keeps its ids as
// long). [MESHSAT-1641]
const LXMFMidRetention = 30 * 24 * time.Hour

// InsertLXMFMessageOnce files an inbound LXMF message that carries a mid and
// records (origin, mid) in one transaction: both or neither (r40 review
// R40-R2). inserted is false for a repeat, the pair being known, with
// nothing written. A failure writes nothing either, so the message can come
// again and be taken then. origin is the sender's LXMF source hash.
func (db *DB) InsertLXMFMessageOnce(origin, mid string, now time.Time, m *Message, status string) (inserted bool, id int64, err error) {
	err = db.withTx(func(tx *sqlx.Tx) error {
		res, err := tx.Exec(`INSERT OR IGNORE INTO lxmf_mids (origin, mid, received_at) VALUES (?, ?, ?)`,
			origin, mid, now.UnixMilli())
		if err != nil {
			return fmt.Errorf("record message id: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return err
		}
		if id, err = insertMessage(tx, m, status); err != nil {
			return fmt.Errorf("file message: %w", err)
		}
		inserted = true
		return nil
	})
	if err != nil {
		return false, 0, err
	}
	return inserted, id, nil
}

// PruneLXMFMids forgets the ids taken in before LXMFMidRetention.
func (db *DB) PruneLXMFMids(now time.Time) (int64, error) {
	res, err := db.Exec(`DELETE FROM lxmf_mids WHERE received_at < ?`, now.Add(-LXMFMidRetention).UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// EnsureDeliveryMid answers the LXMF message id of a ledger row, assigning a
// new UUIDv7 the first time (r40 review, decision 2). The assignment is one
// conditional write, so callers racing for the same row all get the one
// that landed, and a retry, a restart or another route gets it again.
func (db *DB) EnsureDeliveryMid(deliveryID int64) (string, error) {
	fresh, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	if _, err := db.Exec(`UPDATE message_deliveries SET mid = ? WHERE id = ? AND mid = ''`, fresh.String(), deliveryID); err != nil {
		return "", err
	}
	var mid string
	if err := db.Get(&mid, `SELECT mid FROM message_deliveries WHERE id = ?`, deliveryID); err != nil {
		return "", err
	}
	if mid == "" {
		return "", fmt.Errorf("delivery %d has no mid", deliveryID)
	}
	return mid, nil
}

// backfillDeliveryMids gives every LXMF ledger row that is no intent's
// attempt and has no mid yet one, in one transaction (v81).
func (db *DB) backfillDeliveryMids() error {
	return db.withTx(func(tx *sqlx.Tx) error {
		var ids []int64
		if err := tx.Select(&ids, `SELECT id FROM message_deliveries
			WHERE delivery_class = ? AND COALESCE(intent_id, '') = '' AND mid = ''`, DeliveryClassLXMF); err != nil {
			return err
		}
		for _, id := range ids {
			u, err := uuid.NewV7()
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE message_deliveries SET mid = ? WHERE id = ? AND mid = ''`, u.String(), id); err != nil {
				return err
			}
		}
		return nil
	})
}
