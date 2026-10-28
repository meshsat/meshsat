package database

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// LXMFAttachment is one voice note or photo an LXMF message carried, filed
// beside the message row (messages.id) its text went under; Index is its
// place in the message. The bytes are read separately. [MESHSAT-1735]
type LXMFAttachment struct {
	ID         int64  `db:"id" json:"-"`
	MessageID  int64  `db:"message_id" json:"message_id"`
	LXMFHash   string `db:"lxmf_hash" json:"lxmf_hash"`
	Index      int    `db:"idx" json:"index"`
	Kind       string `db:"kind" json:"kind"`
	Mode       int    `db:"mode" json:"mode,omitempty"`
	Format     string `db:"format" json:"format,omitempty"`
	Size       int    `db:"size" json:"size"`
	ReceivedAt string `db:"received_at" json:"received_at"`
}

// ErrNoAttachment: the message has no attachment at that index.
var ErrNoAttachment = errors.New("no such attachment")

// InsertLXMFAttachments files a message's attachments, in one transaction,
// under its row id and LXMF hash; data[i] belongs to atts[i]. A second call
// for the same message replaces nothing: the rows already there stay.
func (db *DB) InsertLXMFAttachments(messageID int64, lxmfHash string, atts []LXMFAttachment, data [][]byte) error {
	if len(atts) == 0 {
		return nil
	}
	if len(data) != len(atts) {
		return errors.New("insert lxmf attachments: one data slice per attachment")
	}
	return db.withTx(func(tx *sqlx.Tx) error {
		for i, a := range atts {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO lxmf_attachments (message_id, lxmf_hash, idx, kind, mode, format, size, data)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, messageID, lxmfHash, i, a.Kind, a.Mode, a.Format, len(data[i]), data[i]); err != nil {
				return fmt.Errorf("insert lxmf attachment %d: %w", i, err)
			}
		}
		return nil
	})
}

// MessageExists reports whether a message row is there.
func (db *DB) MessageExists(id int64) (bool, error) {
	var one int
	err := db.QueryRow(`SELECT 1 FROM messages WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ListLXMFAttachments lists a message's attachments in order, without bytes.
func (db *DB) ListLXMFAttachments(messageID int64) ([]LXMFAttachment, error) {
	out := []LXMFAttachment{}
	err := db.Select(&out, `SELECT id, message_id, lxmf_hash, idx, kind, mode, format, size, received_at
		FROM lxmf_attachments WHERE message_id = ? ORDER BY idx`, messageID)
	return out, err
}

// GetLXMFAttachment reads one attachment and its bytes; ErrNoAttachment
// when the message has none at that index.
func (db *DB) GetLXMFAttachment(messageID int64, index int) (LXMFAttachment, []byte, error) {
	var a LXMFAttachment
	var data []byte
	err := db.QueryRow(`SELECT id, message_id, lxmf_hash, idx, kind, mode, format, size, received_at, data
		FROM lxmf_attachments WHERE message_id = ? AND idx = ?`, messageID, index).
		Scan(&a.ID, &a.MessageID, &a.LXMFHash, &a.Index, &a.Kind, &a.Mode, &a.Format, &a.Size, &a.ReceivedAt, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return a, nil, ErrNoAttachment
	}
	return a, data, err
}

// LXMFMessageIDByHash finds the message row an LXMF hash (64 hex) was filed
// under, through its attachments; ErrNoAttachment when none carried any.
func (db *DB) LXMFMessageIDByHash(lxmfHash string) (int64, error) {
	var id int64
	err := db.QueryRow(`SELECT message_id FROM lxmf_attachments WHERE lxmf_hash = ? ORDER BY id LIMIT 1`, lxmfHash).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNoAttachment
	}
	return id, err
}

// LXMFAttachmentBytes is what every stored attachment weighs together.
func (db *DB) LXMFAttachmentBytes() (int64, error) {
	var n int64
	err := db.QueryRow(`SELECT COALESCE(SUM(size), 0) FROM lxmf_attachments`).Scan(&n)
	return n, err
}

// EvictLXMFAttachments drops the oldest attachments until the store holds
// at most maxBytes; it returns how many went. maxBytes <= 0 evicts nothing.
func (db *DB) EvictLXMFAttachments(maxBytes int64) (int, error) {
	if maxBytes <= 0 {
		return 0, nil
	}
	// Newest first over a running sum: what pushes the sum past the cap goes.
	res, err := db.Exec(`DELETE FROM lxmf_attachments WHERE id IN (
		SELECT id FROM (SELECT id, SUM(size) OVER (ORDER BY id DESC) AS running FROM lxmf_attachments) t WHERE t.running > ?)`, maxBytes)
	if err != nil {
		return 0, fmt.Errorf("evict lxmf attachments: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
