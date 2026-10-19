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

// HubHandoffOrigin is the identity the work made of a stored message carries in
// dispatch_work: the message, so a hand-off tried again finds what it made before.
func HubHandoffOrigin(messageID int64) string { return fmt.Sprintf("hub:%d", messageID) }

// MarkHubHandoffDone records that a stored message went to the access rules, and forgets
// the list of the work it made in the same transaction: a hand-off marked done is never
// tried again, and one not marked still finds its list.
func (db *DB) MarkHubHandoffDone(messageID int64, at time.Time) error {
	return db.withTx(func(tx *sqlx.Tx) error {
		if _, err := tx.Exec(`UPDATE hub_inbox_handoff SET state = 'done', done_at = ? WHERE message_id = ? AND state = 'pending'`,
			at.UnixMilli(), messageID); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM dispatch_work WHERE origin = ?`, HubHandoffOrigin(messageID))
		return err
	})
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
	// Submission is the payload published, kept to publish again unchanged: a repeat is how
	// a sender that lost an event asks for every event again (C4).
	Submission string
	// ReplayAt is when its events were first found owed again (ms), 0 when they are not.
	ReplayAt int64
	// ReplayGivenUpAt is when this bridge stopped asking at the bound (ms), 0 while it asks.
	ReplayGivenUpAt int64
}

const hubOpCols = `op_id, mid, recipient, intent_id, expires_at, created_at, submission, COALESCE(replay_at, 0),
	COALESCE(replay_given_up_at, 0)`

func scanHubOp(row interface{ Scan(...any) error }) (*HubOp, error) {
	var op HubOp
	err := row.Scan(&op.OpID, &op.Mid, &op.Recipient, &op.IntentID, &op.ExpiresAt, &op.CreatedAt, &op.Submission, &op.ReplayAt,
		&op.ReplayGivenUpAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &op, nil
}

// PutHubOp records an operation before it is published, so an event that names it can be
// checked against it whenever it comes.
func (db *DB) PutHubOp(op HubOp) error {
	_, err := db.Exec(`INSERT INTO hub_ops (op_id, mid, recipient, intent_id, expires_at, created_at, submission)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, op.OpID, op.Mid, op.Recipient, op.IntentID, op.ExpiresAt, op.CreatedAt, op.Submission)
	return err
}

// GetHubOp returns an operation, nil when this bridge has none under the id.
func (db *DB) GetHubOp(opID string) (*HubOp, error) {
	return scanHubOp(db.QueryRow(`SELECT `+hubOpCols+` FROM hub_ops WHERE op_id = ?`, opID))
}

// hubOpCompleted is true for an operation whose completion this bridge keeps: a valid event
// of class completed, C6's strongest. Nothing a replay could bring raises it; a weaker
// terminal (failed, expired) is not this, since C6 lets a completion follow it.
const hubOpCompleted = `EXISTS (SELECT 1 FROM hub_op_events e WHERE e.op_id = hub_ops.op_id AND e.valid = 1
	AND e.class = 'completed')`

// hubOpTerminal is true for an operation with any outcome kept: completed, failed or expired
// (a refusal is kept as failed).
const hubOpTerminal = `EXISTS (SELECT 1 FROM hub_op_events e WHERE e.op_id = hub_ops.op_id AND e.valid = 1
	AND e.class IN ('completed', 'failed', 'expired'))`

// hubOpStillMissing is true for an operation with an event really missing: a missing row
// whose event is not kept. An unnamed row (an empty event_id) never names a kept event, so it is
// always missing. A row whose event is kept by now is a cleanup still to do, not something
// missing, whether or not its strike could be written yet.
const hubOpStillMissing = `EXISTS (SELECT 1 FROM hub_op_missing m WHERE m.op_id = hub_ops.op_id
	AND NOT EXISTS (SELECT 1 FROM hub_op_events k WHERE k.event_id = m.event_id AND k.op_id = m.op_id))`

// MissingAnyEvent names, in hub_op_missing, an obligation whose event is not known (one
// carried over from v72-v73, which kept no event ids): only a completion ends it, or the
// bound.
const MissingAnyEvent = ""

// WantHubOpReplay records that an event of an operation is missing: it could not be kept,
// or read against its operation, and only a repeat brings it back (C4). The missing event
// is named (hub_op_missing) and replay_at, when the operation was first owed, is set in the
// same transaction; only that event being kept, or a completion, ends it
// (ResolveHubOpMissing, ResolveCompletedHubOp), never a repeat going out. For an operation
// this bridge does not keep it records nothing.
func (db *DB) WantHubOpReplay(opID, eventID string, at time.Time) error {
	return db.withTx(func(tx *sqlx.Tx) error {
		if _, err := tx.Exec(`UPDATE hub_ops SET replay_at = COALESCE(replay_at, ?) WHERE op_id = ?`, at.UnixMilli(), opID); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT OR IGNORE INTO hub_op_missing (op_id, event_id, since)
			SELECT ?, ?, ? WHERE EXISTS (SELECT 1 FROM hub_ops WHERE op_id = ?)`, opID, eventID, at.UnixMilli(), opID)
		return err
	})
}

// clearIdleReplay clears replay_at of an operation with nothing missing any more.
func clearIdleReplay(tx *sqlx.Tx, opID string) error {
	_, err := tx.Exec(`UPDATE hub_ops SET replay_at = NULL WHERE op_id = ? AND replay_at IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM hub_op_missing m WHERE m.op_id = hub_ops.op_id)`, opID)
	return err
}

// ResolveHubOpMissing records that an event is kept: it is not missing any more, and the
// operation is owed nothing when nothing else of it is.
func (db *DB) ResolveHubOpMissing(opID, eventID string) error {
	return db.withTx(func(tx *sqlx.Tx) error {
		if _, err := tx.Exec(`DELETE FROM hub_op_missing WHERE op_id = ? AND event_id = ?`, opID, eventID); err != nil {
			return err
		}
		return clearIdleReplay(tx, opID)
	})
}

// ResolveCompletedHubOp ends every obligation of an operation whose completion is kept;
// for one without, it changes nothing.
func (db *DB) ResolveCompletedHubOp(opID string) error {
	return db.withTx(func(tx *sqlx.Tx) error {
		if _, err := tx.Exec(`DELETE FROM hub_op_missing WHERE op_id = ? AND EXISTS (SELECT 1 FROM hub_ops WHERE op_id = ? AND `+
			hubOpCompleted+`)`, opID, opID); err != nil {
			return err
		}
		return clearIdleReplay(tx, opID)
	})
}

// ResolveKeptHubOpMissing ends, in one pass, the obligations whose event is kept by now and
// those of completed operations (where a resolve could not be written at the time).
func (db *DB) ResolveKeptHubOpMissing() (int, error) {
	var n int
	err := db.withTx(func(tx *sqlx.Tx) error {
		res, err := tx.Exec(`DELETE FROM hub_op_missing WHERE event_id != '' AND EXISTS
			(SELECT 1 FROM hub_op_events e WHERE e.event_id = hub_op_missing.event_id)`)
		if err != nil {
			return err
		}
		a, _ := res.RowsAffected()
		res, err = tx.Exec(`DELETE FROM hub_op_missing WHERE op_id IN (SELECT op_id FROM hub_ops WHERE ` + hubOpCompleted + `)`)
		if err != nil {
			return err
		}
		b, _ := res.RowsAffected()
		n = int(a + b)
		_, err = tx.Exec(`UPDATE hub_ops SET replay_at = NULL WHERE replay_at IS NOT NULL
			AND NOT EXISTS (SELECT 1 FROM hub_op_missing m WHERE m.op_id = hub_ops.op_id)`)
		return err
	})
	return n, err
}

// HubOpMissing lists an operation's missing events.
func (db *DB) HubOpMissing(opID string) ([]string, error) {
	rows, err := db.Query(`SELECT event_id FROM hub_op_missing WHERE op_id = ? ORDER BY since, event_id`, opID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// HubOpEventKept reports whether an event is kept (valid or rejected).
func (db *DB) HubOpEventKept(eventID string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM hub_op_events WHERE event_id = ?`, eventID).Scan(&n)
	return n > 0, err
}

// HubOpCompleted reports whether this bridge keeps an operation's completion.
func (db *DB) HubOpCompleted(opID string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM hub_ops WHERE op_id = ? AND `+hubOpCompleted, opID).Scan(&n)
	return n > 0, err
}

func queryHubOps(db *DB, where string, args ...any) ([]HubOp, error) {
	rows, err := db.Query(`SELECT `+hubOpCols+` FROM hub_ops WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HubOp
	for rows.Next() {
		op, err := scanHubOp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *op)
	}
	return out, rows.Err()
}

func limitOr(limit int) int {
	if limit <= 0 {
		return 50
	}
	return limit
}

// HubOpCursor is a position in a listing of operations: the sort key and the op id of the
// last one taken, zero for the start.
type HubOpCursor struct {
	Key  int64
	OpID string
}

// HubOpsOwedReplay lists, after cursor and ordered by (replay_at, op_id), the operations with
// an event still missing (hubOpStillMissing: not one whose only rows name events kept by
// now), not given up, with a kept submission, whose deadline is not more than within before
// now. The Hub answers a repeat from what it stores, a day past the deadline,
// so a repeat well inside that is a replay, never a new submission it would refuse as late.
// A caller that pages with the cursor and starts again at the end visits every one.
func (db *DB) HubOpsOwedReplay(now time.Time, within time.Duration, after HubOpCursor, limit int) ([]HubOp, error) {
	return queryHubOps(db, `replay_at IS NOT NULL AND `+hubOpStillMissing+` AND submission != '' AND replay_given_up_at IS NULL
		AND expires_at + ? >= ? AND (replay_at, op_id) > (?, ?) ORDER BY replay_at, op_id LIMIT ?`,
		within.Milliseconds(), now.UnixMilli(), after.Key, after.OpID, limitOr(limit))
}

// HubOpsToReconcile lists, after cursor and ordered by (created_at, op_id), the operations a
// process start asks the Hub for once: a kept submission, no completion kept, not given up,
// inside the same bound. An event lost with its mark leaves nothing else to find, and a
// weaker terminal kept does not mean a completion was not lost.
func (db *DB) HubOpsToReconcile(now time.Time, within time.Duration, after HubOpCursor, limit int) ([]HubOp, error) {
	return queryHubOps(db, `submission != '' AND replay_given_up_at IS NULL AND expires_at + ? >= ?
		AND NOT `+hubOpCompleted+` AND (created_at, op_id) > (?, ?) ORDER BY created_at, op_id LIMIT ?`,
		within.Milliseconds(), now.UnixMilli(), after.Key, after.OpID, limitOr(limit))
}

// HubOpsToGiveUp lists the operations past the bound whose outcome this bridge cannot say:
// one with an event still missing, or one with no outcome kept at all. The decision is made
// from the evidence, not from replay_at: a missing row whose event is kept by now (its strike
// not written yet) is not missing. One past the bound with a weaker outcome kept and nothing
// still missing has its outcome, and is not listed.
func (db *DB) HubOpsToGiveUp(now time.Time, within time.Duration, limit int) ([]HubOp, error) {
	return queryHubOps(db, `replay_given_up_at IS NULL AND expires_at + ? < ? AND NOT `+hubOpCompleted+`
		AND (`+hubOpStillMissing+` OR NOT `+hubOpTerminal+`) ORDER BY created_at, op_id LIMIT ?`,
		within.Milliseconds(), now.UnixMilli(), limitOr(limit))
}

// GiveUpHubOpReplay records that this bridge stopped asking for an operation's events at
// the bound. Its owed mark stays: given up is not recovered.
func (db *DB) GiveUpHubOpReplay(opID string, at time.Time) error {
	_, err := db.Exec(`UPDATE hub_ops SET replay_given_up_at = ? WHERE op_id = ? AND replay_given_up_at IS NULL`, at.UnixMilli(), opID)
	return err
}

// KeptHubOpEvent is an event as this bridge keeps it: the first copy under its id decides
// what it says and whether it is valid; Applied says the ledger has it.
type KeptHubOpEvent struct {
	Event   hubmsg.Event
	Valid   bool
	Applied bool
	// Added is true when this call kept it, false for a copy of one kept before.
	Added bool
}

const hubOpEventCols = `event_id, op_id, rev, class, kind, reason, authority, at, valid, applied_at IS NOT NULL`

func scanHubOpEvent(row interface{ Scan(...any) error }) (KeptHubOpEvent, error) {
	var k KeptHubOpEvent
	var valid, applied int
	e := &k.Event
	err := row.Scan(&e.EventID, &e.OpID, &e.Rev, &e.Class, &e.Kind, &e.Reason, &e.Authority, &e.At, &valid, &applied)
	k.Valid, k.Applied = valid != 0, applied != 0
	return k, err
}

// AddHubOpEvent keeps one event of an operation once per event id (C6's union), as
// received: valid says whether it joined the operation's evidence or was kept as rejected.
// It returns the event as kept, which a copy does not change.
func (db *DB) AddHubOpEvent(ev hubmsg.Event, valid bool, at time.Time) (KeptHubOpEvent, error) {
	var k KeptHubOpEvent
	err := db.withTx(func(tx *sqlx.Tx) error {
		res, err := tx.Exec(`INSERT OR IGNORE INTO hub_op_events (event_id, op_id, rev, class, kind, reason, authority, at, valid, received_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, ev.EventID, ev.OpID, ev.Rev, ev.Class, ev.Kind, ev.Reason, ev.Authority, ev.At,
			boolInt(valid), at.UnixMilli())
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if k, err = scanHubOpEvent(tx.QueryRow(`SELECT `+hubOpEventCols+` FROM hub_op_events WHERE event_id = ?`, ev.EventID)); err != nil {
			return err
		}
		k.Added = n == 1
		return nil
	})
	return k, err
}

// MarkHubOpEventApplied records that the ledger has an event.
func (db *DB) MarkHubOpEventApplied(eventID string, at time.Time) error {
	_, err := db.Exec(`UPDATE hub_op_events SET applied_at = ? WHERE event_id = ? AND applied_at IS NULL`, at.UnixMilli(), eventID)
	return err
}

// PendingHubOpEvents lists the valid events the ledger does not have yet, of operations
// still kept, oldest first.
func (db *DB) PendingHubOpEvents(limit int) ([]KeptHubOpEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.Query(`SELECT e.event_id, e.op_id, e.rev, e.class, e.kind, e.reason, e.authority, e.at, e.valid, e.applied_at IS NOT NULL
		FROM hub_op_events e JOIN hub_ops o ON o.op_id = e.op_id
		WHERE e.applied_at IS NULL AND e.valid = 1 ORDER BY e.received_at, e.event_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KeptHubOpEvent
	for rows.Next() {
		k, err := scanHubOpEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
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
		if _, err := tx.Exec(`DELETE FROM hub_op_missing WHERE op_id IN (SELECT op_id FROM hub_ops WHERE expires_at < ?)`, cutoff); err != nil {
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
