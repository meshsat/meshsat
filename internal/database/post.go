package database

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The Community Post's rows (internal/post, MESHSAT-1733): what people wrote
// on a kit's captive portal, what came back for a pickup code, the opt-in
// safe-and-well board, and the per-phone quota counters. Times are RFC 3339
// UTC strings, as the TAK chat threads keep theirs.

// Post statuses, in the order a post goes through them.
const (
	PostStatusPending = "pending_approval" // waiting for an operator on the kit
	PostStatusQueued  = "queued"           // approved; a delivery row exists
	PostStatusSent    = "sent"             // the delivery row reports sent
	PostStatusReplied = "replied"          // at least one reply came back
	PostStatusRefused = "refused"          // an operator refused it
	PostStatusFailed  = "failed"           // the delivery row died
)

// Post classes.
const (
	PostClassRoutine   = "routine"
	PostClassEmergency = "emergency"
)

// PostMessage is one message written on the portal.
type PostMessage struct {
	Code       string    `json:"code"`
	Session    string    `json:"session"`
	Recipient  string    `json:"recipient"`
	Text       string    `json:"text"`
	Name       string    `json:"name"`
	Class      string    `json:"class"`
	Status     string    `json:"status"`
	Lang       string    `json:"lang"`
	MsgRef     string    `json:"msg_ref,omitempty"`
	DeliveryID int64     `json:"delivery_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// PostReply is one reply that came back for a code.
type PostReply struct {
	ID         int64     `json:"id"`
	Code       string    `json:"code"`
	From       string    `json:"from"`
	Text       string    `json:"text"`
	ReceivedAt time.Time `json:"received_at"`
}

// PostBoardEntry is one line on the safe-and-well board.
type PostBoardEntry struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Place     string    `json:"place"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// PostSession is one phone's counters.
type PostSession struct {
	ClientKey  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	HourBucket string
	HourCount  int
	DayBucket  string
	DayCount   int
	SignedIn   bool
}

const postMessageCols = `code, session, recipient, text, name, class, status, lang, msg_ref, delivery_id, created_at, updated_at`

func postTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parsePostTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

type postScanner interface{ Scan(dest ...any) error }

func scanPostMessage(s postScanner) (*PostMessage, error) {
	var m PostMessage
	var created, updated string
	if err := s.Scan(&m.Code, &m.Session, &m.Recipient, &m.Text, &m.Name, &m.Class, &m.Status, &m.Lang,
		&m.MsgRef, &m.DeliveryID, &created, &updated); err != nil {
		return nil, err
	}
	m.CreatedAt, m.UpdatedAt = parsePostTime(created), parsePostTime(updated)
	return &m, nil
}

// InsertPostMessage stores a new post. The code must be unused.
func (db *DB) InsertPostMessage(m *PostMessage) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	m.UpdatedAt = m.CreatedAt
	_, err := db.Exec(`INSERT INTO post_messages (`+postMessageCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Code, m.Session, m.Recipient, m.Text, m.Name, m.Class, m.Status, m.Lang, m.MsgRef, m.DeliveryID,
		postTime(m.CreatedAt), postTime(m.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert post: %w", err)
	}
	return nil
}

// GetPostMessage answers nil, nil for an unknown code.
func (db *DB) GetPostMessage(code string) (*PostMessage, error) {
	m, err := scanPostMessage(db.QueryRow(`SELECT `+postMessageCols+` FROM post_messages WHERE code = ?`, code))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get post: %w", err)
	}
	return m, nil
}

// ListPostMessages lists posts in the given statuses (all when empty), newest
// first, at most limit.
func (db *DB) ListPostMessages(statuses []string, limit int) ([]PostMessage, error) {
	q := `SELECT ` + postMessageCols + ` FROM post_messages`
	args := []any{}
	if len(statuses) > 0 {
		q += ` WHERE status IN (`
		for i, s := range statuses {
			if i > 0 {
				q += `, `
			}
			q += `?`
			args = append(args, s)
		}
		q += `)`
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	if limit <= 0 {
		limit = 100
	}
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list posts: %w", err)
	}
	defer rows.Close()
	var out []PostMessage
	for rows.Next() {
		m, err := scanPostMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("list posts: %w", err)
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// LatestOpenPostTo is the most recent post to recipient that left the kit
// (sent or already replied to), for a reply that names no code.
func (db *DB) LatestOpenPostTo(recipient string) (*PostMessage, error) {
	m, err := scanPostMessage(db.QueryRow(`SELECT `+postMessageCols+` FROM post_messages
		WHERE recipient = ? AND status IN (?, ?, ?) ORDER BY created_at DESC LIMIT 1`,
		recipient, PostStatusSent, PostStatusReplied, PostStatusQueued))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("latest post to recipient: %w", err)
	}
	return m, nil
}

// SetPostStatus moves a post to status; msgRef and deliveryID are kept when
// given, so the queued row stays linked to its delivery.
func (db *DB) SetPostStatus(code, status, msgRef string, deliveryID int64) error {
	_, err := db.Exec(`UPDATE post_messages SET status = ?,
		msg_ref = CASE WHEN ? <> '' THEN ? ELSE msg_ref END,
		delivery_id = CASE WHEN ? > 0 THEN ? ELSE delivery_id END,
		updated_at = ? WHERE code = ?`,
		status, msgRef, msgRef, deliveryID, deliveryID, postTime(time.Now()), code)
	if err != nil {
		return fmt.Errorf("set post status: %w", err)
	}
	return nil
}

// InsertPostReply stores a reply under its code and marks the post replied.
func (db *DB) InsertPostReply(r *PostReply) error {
	if r.ReceivedAt.IsZero() {
		r.ReceivedAt = time.Now().UTC()
	}
	res, err := db.Exec(`INSERT INTO post_replies (code, from_addr, text, received_at) VALUES (?, ?, ?, ?)`,
		r.Code, r.From, r.Text, postTime(r.ReceivedAt))
	if err != nil {
		return fmt.Errorf("insert post reply: %w", err)
	}
	r.ID, _ = res.LastInsertId()
	if _, err := db.Exec(`UPDATE post_messages SET status = ?, updated_at = ? WHERE code = ? AND status IN (?, ?, ?)`,
		PostStatusReplied, postTime(r.ReceivedAt), r.Code, PostStatusQueued, PostStatusSent, PostStatusReplied); err != nil {
		return fmt.Errorf("mark post replied: %w", err)
	}
	return nil
}

// ListPostReplies lists a code's replies, oldest first.
func (db *DB) ListPostReplies(code string) ([]PostReply, error) {
	rows, err := db.Query(`SELECT id, code, from_addr, text, received_at FROM post_replies WHERE code = ? ORDER BY received_at ASC, id ASC`, code)
	if err != nil {
		return nil, fmt.Errorf("list post replies: %w", err)
	}
	defer rows.Close()
	var out []PostReply
	for rows.Next() {
		var r PostReply
		var at string
		if err := rows.Scan(&r.ID, &r.Code, &r.From, &r.Text, &at); err != nil {
			return nil, fmt.Errorf("list post replies: %w", err)
		}
		r.ReceivedAt = parsePostTime(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertPostBoardEntry adds a line to the board; expired lines are pruned on
// the way.
func (db *DB) InsertPostBoardEntry(e *PostBoardEntry, nameKey string) error {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	res, err := db.Exec(`INSERT INTO post_board (name, name_key, status, place, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		e.Name, nameKey, e.Status, e.Place, postTime(e.CreatedAt), postTime(e.ExpiresAt))
	if err != nil {
		return fmt.Errorf("insert board entry: %w", err)
	}
	e.ID, _ = res.LastInsertId()
	_, _ = db.Exec(`DELETE FROM post_board WHERE expires_at < ?`, postTime(e.CreatedAt))
	return nil
}

// SearchPostBoard lists unexpired board lines whose name key contains q
// (every line when q is empty), newest first, at most limit.
func (db *DB) SearchPostBoard(q string, now time.Time, limit int) ([]PostBoardEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.Query(`SELECT id, name, status, place, created_at, expires_at FROM post_board
		WHERE expires_at >= ? AND (? = '' OR instr(name_key, ?) > 0) ORDER BY created_at DESC LIMIT ?`,
		postTime(now), q, q, limit)
	if err != nil {
		return nil, fmt.Errorf("search board: %w", err)
	}
	defer rows.Close()
	var out []PostBoardEntry
	for rows.Next() {
		var e PostBoardEntry
		var created, expires string
		if err := rows.Scan(&e.ID, &e.Name, &e.Status, &e.Place, &created, &expires); err != nil {
			return nil, fmt.Errorf("search board: %w", err)
		}
		e.CreatedAt, e.ExpiresAt = parsePostTime(created), parsePostTime(expires)
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeletePostBoardEntry removes one line (an operator's request, or the
// person's own).
func (db *DB) DeletePostBoardEntry(id int64) error {
	if _, err := db.Exec(`DELETE FROM post_board WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete board entry: %w", err)
	}
	return nil
}

// GetPostSession reads a phone's counters; nil, nil when unknown.
func (db *DB) GetPostSession(clientKey string) (*PostSession, error) {
	var s PostSession
	var created, seen string
	var signed int
	err := db.QueryRow(`SELECT client_key, created_at, last_seen_at, hour_bucket, hour_count, day_bucket, day_count, signed_in
		FROM post_sessions WHERE client_key = ?`, clientKey).Scan(&s.ClientKey, &created, &seen, &s.HourBucket, &s.HourCount,
		&s.DayBucket, &s.DayCount, &signed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get post session: %w", err)
	}
	s.CreatedAt, s.LastSeenAt, s.SignedIn = parsePostTime(created), parsePostTime(seen), signed != 0
	return &s, nil
}

// TakePostSlot counts one post for a phone against its hour and day
// ceilings, in one statement, and answers whether it was allowed. The
// buckets are the clock hour and the UTC day; a bucket that moved on starts
// at zero. A ceiling of 0 or less means no ceiling.
func (db *DB) TakePostSlot(clientKey string, now time.Time, perHour, perDay int) (bool, error) {
	now = now.UTC()
	hour, day := now.Format("2006010215"), now.Format("20060102")
	ts := postTime(now)
	res, err := db.Exec(`INSERT INTO post_sessions (client_key, created_at, last_seen_at, hour_bucket, hour_count, day_bucket, day_count)
		VALUES (?, ?, ?, ?, 1, ?, 1)
		ON CONFLICT(client_key) DO UPDATE SET
			last_seen_at = excluded.last_seen_at,
			hour_count = CASE WHEN hour_bucket = excluded.hour_bucket THEN hour_count + 1 ELSE 1 END,
			hour_bucket = excluded.hour_bucket,
			day_count = CASE WHEN day_bucket = excluded.day_bucket THEN day_count + 1 ELSE 1 END,
			day_bucket = excluded.day_bucket
		WHERE (? <= 0 OR hour_bucket <> excluded.hour_bucket OR hour_count < ?)
		  AND (? <= 0 OR day_bucket <> excluded.day_bucket OR day_count < ?)`,
		clientKey, ts, ts, hour, day, perHour, perHour, perDay, perDay)
	if err != nil {
		return false, fmt.Errorf("take post slot: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// MarkPostSignedIn records that a phone opened the portal, so the captive
// detection answers it "no portal" from then on.
func (db *DB) MarkPostSignedIn(clientKey string, now time.Time) error {
	ts := postTime(now)
	_, err := db.Exec(`INSERT INTO post_sessions (client_key, created_at, last_seen_at, signed_in) VALUES (?, ?, ?, 1)
		ON CONFLICT(client_key) DO UPDATE SET signed_in = 1, last_seen_at = excluded.last_seen_at`, clientKey, ts, ts)
	if err != nil {
		return fmt.Errorf("mark post signed in: %w", err)
	}
	return nil
}

// PrunePostSessions forgets phones not seen since before.
func (db *DB) PrunePostSessions(before time.Time) error {
	if _, err := db.Exec(`DELETE FROM post_sessions WHERE last_seen_at < ?`, postTime(before)); err != nil {
		return fmt.Errorf("prune post sessions: %w", err)
	}
	return nil
}

// CountPostMessages counts posts per status.
func (db *DB) CountPostMessages() (map[string]int, error) {
	rows, err := db.Query(`SELECT status, COUNT(*) FROM post_messages GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("count posts: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, fmt.Errorf("count posts: %w", err)
		}
		out[s] = n
	}
	return out, rows.Err()
}
