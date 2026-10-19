package database

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"meshsat/internal/hubmsg"
)

// hub_msg, the Hub app route (HeMB-NG stage 2, contract C3-C6, v71). [MESHSAT-1584]

// HubTransport is the messages.transport of a message that came through the Hub app route.
const HubTransport = "hub"

// ---- the receiver's inbox (C3) ----

// hubInboxTx is the inbox inside one transaction.
type hubInboxTx struct {
	tx  *sqlx.Tx
	now int64
}

const hubInboxCols = `origin, op_id, mid, recipient, content_type, mid_expires_at, expires_at, content_sha256,
	message_id, committed_at, within_skew, unjudged`

func scanHubInbox(row interface{ Scan(...any) error }) (*hubmsg.InboxEntry, error) {
	var e hubmsg.InboxEntry
	var skew, unjudged int
	err := row.Scan(&e.Origin, &e.OpID, &e.Mid, &e.Recipient, &e.ContentType, &e.MidExpiresAt, &e.ExpiresAt,
		&e.ContentSHA256, &e.MessageID, &e.CommittedAt, &skew, &unjudged)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.WithinSkew, e.Unjudged = skew != 0, unjudged != 0
	return &e, nil
}

func (t *hubInboxTx) ByOp(origin, opID string) (*hubmsg.InboxEntry, error) {
	return scanHubInbox(t.tx.QueryRow(`SELECT `+hubInboxCols+` FROM hub_inbox WHERE origin = ? AND op_id = ?`, origin, opID))
}

func (t *hubInboxTx) ByMid(origin, mid string) (*hubmsg.InboxEntry, error) {
	return scanHubInbox(t.tx.QueryRow(`SELECT `+hubInboxCols+` FROM hub_inbox WHERE origin = ? AND mid = ?
		ORDER BY committed_at LIMIT 1`, origin, mid))
}

func (t *hubInboxTx) Count() (int, error) {
	var n int
	err := t.tx.QueryRow(`SELECT COUNT(*) FROM hub_inbox`).Scan(&n)
	return n, err
}

func (t *hubInboxTx) PruneBefore(nowMs int64) (int, error) {
	return pruneHubInbox(t.tx, nowMs)
}

func (t *hubInboxTx) Insert(e *hubmsg.InboxEntry, store bool, body []byte) error {
	if store {
		res, err := t.tx.Exec(`INSERT INTO messages (packet_id, from_node, to_node, channel, portnum, portnum_name,
			decoded_text, rx_snr, rx_time, hop_limit, hop_start, direction, transport)
			VALUES (0, ?, ?, 0, 1, 'TEXT_MESSAGE_APP', ?, 0, ?, 0, 0, 'rx', ?)`,
			e.Origin, e.Recipient, string(body), e.CommittedAt/1000, HubTransport)
		if err != nil {
			return fmt.Errorf("store the message: %w", err)
		}
		if e.MessageID, err = res.LastInsertId(); err != nil {
			return err
		}
		if _, err := t.tx.Exec(`INSERT INTO hub_inbox_handoff (message_id, state, created_at) VALUES (?, 'pending', ?)`,
			e.MessageID, t.now); err != nil {
			return fmt.Errorf("store the hand-off: %w", err)
		}
	}
	_, err := t.tx.Exec(`INSERT INTO hub_inbox (`+hubInboxCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Origin, e.OpID, e.Mid, e.Recipient, e.ContentType, e.MidExpiresAt, e.ExpiresAt, e.ContentSHA256,
		e.MessageID, e.CommittedAt, boolInt(e.WithinSkew), boolInt(e.Unjudged))
	return err
}

func pruneHubInbox(tx *sqlx.Tx, nowMs int64) (int, error) {
	res, err := tx.Exec(`DELETE FROM hub_inbox WHERE expires_at + ? < ?`,
		hubmsg.SkewMs+hubmsg.RetentionAfterExpiryMs, nowMs)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// CommitHubDelivery decides one delivery from msg/in (C3) and commits it in one transaction:
// the inbox row, and for a message not stored before, the message and its pending hand-off.
// The answer is what the bridge acknowledges, after this returns.
func (db *DB) CommitHubDelivery(d hubmsg.Delivery, now time.Time, clockEstablished bool, capacity int) (hubmsg.Answer, error) {
	var ans hubmsg.Answer
	err := db.withTx(func(tx *sqlx.Tx) error {
		var err error
		ans, err = hubmsg.Commit(&hubInboxTx{tx: tx, now: now.UnixMilli()}, d, now, clockEstablished, capacity)
		return err
	})
	return ans, err
}

// PruneHubInbox deletes the inbox entries past their retention.
func (db *DB) PruneHubInbox(now time.Time) (int, error) {
	var n int
	err := db.withTx(func(tx *sqlx.Tx) error {
		var err error
		n, err = pruneHubInbox(tx, now.UnixMilli())
		return err
	})
	return n, err
}

// HubHandoff is a stored message the access rules have not seen yet.
type HubHandoff struct {
	MessageID int64
	From      string
	To        string
	Text      string
}

// PendingHubHandoffs lists the stored messages waiting for their hand-off, oldest first.
func (db *DB) PendingHubHandoffs(limit int) ([]HubHandoff, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.Query(`SELECT h.message_id, m.from_node, m.to_node, m.decoded_text
		FROM hub_inbox_handoff h JOIN messages m ON m.id = h.message_id
		WHERE h.state = 'pending' ORDER BY h.message_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HubHandoff
	for rows.Next() {
		var h HubHandoff
		if err := rows.Scan(&h.MessageID, &h.From, &h.To, &h.Text); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// MarkHubHandoffDone records that a stored message went to the access rules.
func (db *DB) MarkHubHandoffDone(messageID int64, at time.Time) error {
	_, err := db.Exec(`UPDATE hub_inbox_handoff SET state = 'done', done_at = ? WHERE message_id = ? AND state = 'pending'`,
		at.UnixMilli(), messageID)
	return err
}

// ---- the sender's operations and their evidence (C4, C6) ----

// HubOp is one operation this bridge published on its msg/out.
type HubOp struct {
	OpID      string
	Mid       string
	Recipient string
	IntentID  string
	ExpiresAt int64
	CreatedAt int64
}

// PutHubOp records an operation before it is published, so an event that names it can be
// checked against it whenever it comes.
func (db *DB) PutHubOp(op HubOp) error {
	_, err := db.Exec(`INSERT INTO hub_ops (op_id, mid, recipient, intent_id, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		op.OpID, op.Mid, op.Recipient, op.IntentID, op.ExpiresAt, op.CreatedAt)
	return err
}

// GetHubOp returns an operation, nil when this bridge has none under the id.
func (db *DB) GetHubOp(opID string) (*HubOp, error) {
	var op HubOp
	err := db.QueryRow(`SELECT op_id, mid, recipient, intent_id, expires_at, created_at FROM hub_ops WHERE op_id = ?`, opID).
		Scan(&op.OpID, &op.Mid, &op.Recipient, &op.IntentID, &op.ExpiresAt, &op.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &op, nil
}

// AddHubOpEvent keeps one event of an operation once per event id (C6's union); valid says
// whether it joined the operation's evidence or was kept as rejected. added is false for an
// event id already kept.
func (db *DB) AddHubOpEvent(ev hubmsg.Event, valid bool, at time.Time) (added bool, err error) {
	res, err := db.Exec(`INSERT OR IGNORE INTO hub_op_events (event_id, op_id, rev, class, kind, reason, authority, at, valid, received_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, ev.EventID, ev.OpID, ev.Rev, ev.Class, ev.Kind, ev.Reason, ev.Authority, ev.At,
		boolInt(valid), at.UnixMilli())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// HubOpEvents lists an operation's kept events (valid and rejected) by revision.
func (db *DB) HubOpEvents(opID string) ([]hubmsg.Event, []bool, error) {
	rows, err := db.Query(`SELECT event_id, op_id, rev, class, kind, reason, authority, at, valid FROM hub_op_events
		WHERE op_id = ? ORDER BY rev, event_id`, opID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var evs []hubmsg.Event
	var valid []bool
	for rows.Next() {
		var e hubmsg.Event
		var v int
		if err := rows.Scan(&e.EventID, &e.OpID, &e.Rev, &e.Class, &e.Kind, &e.Reason, &e.Authority, &e.At, &v); err != nil {
			return nil, nil, err
		}
		evs, valid = append(evs, e), append(valid, v != 0)
	}
	return evs, valid, rows.Err()
}

// PruneHubOps deletes the operations, and their events, past their deadline plus the inbox's
// retention: no event about one can matter after the recipient forgot it.
func (db *DB) PruneHubOps(now time.Time) (int, error) {
	cutoff := now.UnixMilli() - hubmsg.SkewMs - hubmsg.RetentionAfterExpiryMs
	var n int
	err := db.withTx(func(tx *sqlx.Tx) error {
		if _, err := tx.Exec(`DELETE FROM hub_op_events WHERE op_id IN (SELECT op_id FROM hub_ops WHERE expires_at < ?)`, cutoff); err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM hub_ops WHERE expires_at < ?`, cutoff)
		if err != nil {
			return err
		}
		m, _ := res.RowsAffected()
		n = int(m)
		return nil
	})
	return n, err
}
