package database

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Public warnings the Hub relayed to this Bridge (warnings.Relay,
// MESHSAT-1734): one row per digest id, the latest message type for it.

// Warning is one relayed warning as the Bridge keeps it.
type Warning struct {
	ID         string    `json:"id"` // the digest's 8-byte id, hex
	Ref        string    `json:"ref,omitempty"`
	MsgType    string    `json:"msg_type"` // Alert, Update, Cancel
	Test       bool      `json:"test"`
	Source     string    `json:"source"`
	Sent       time.Time `json:"sent"`
	Expires    time.Time `json:"expires,omitempty"`
	Severity   string    `json:"severity"`
	Urgency    string    `json:"urgency"`
	Certainty  string    `json:"certainty"`
	Category   string    `json:"category"`
	Event      string    `json:"event"`
	Language   string    `json:"language,omitempty"`
	Text       string    `json:"text"`
	Raw        []byte    `json:"-"`
	Via        string    `json:"via"` // hub or the satellite link it came on
	ReceivedAt time.Time `json:"received_at"`
	Cancelled  bool      `json:"cancelled"`
	// Bearers lists what the Bridge put it on, as JSON.
	Bearers string `json:"bearers"`
}

// warningKeep is how long a row outlives its arrival.
const warningKeep = 30 * 24 * time.Hour

// WarningSeen reports whether a digest of this id and message type was
// taken before.
func (db *DB) WarningSeen(id, msgType string) (bool, error) {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM warnings WHERE id = ? AND msg_type = ?`, id, msgType).Scan(&n); err != nil {
		return false, fmt.Errorf("warning seen: %w", err)
	}
	return n > 0, nil
}

// InsertWarning keeps one digest, replacing an earlier row of the same id
// (an Update carries the id of its own CAP message; the row it replaces is
// marked by MarkWarningReplaced). Rows older than 30 days go.
func (db *DB) InsertWarning(w *Warning) error {
	if w == nil {
		return errors.New("nil warning")
	}
	at := w.ReceivedAt
	if at.IsZero() {
		at = time.Now()
	}
	expires := ""
	if !w.Expires.IsZero() {
		expires = w.Expires.UTC().Format(time.RFC3339)
	}
	if _, err := db.Exec(`INSERT INTO warnings (id, ref, msg_type, test, source, sent, expires, severity, urgency, certainty,
		category, event, language, text, raw, via, received_at, cancelled, bearers)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET ref = excluded.ref, msg_type = excluded.msg_type, test = excluded.test,
			source = excluded.source, sent = excluded.sent, expires = excluded.expires, severity = excluded.severity,
			urgency = excluded.urgency, certainty = excluded.certainty, category = excluded.category, event = excluded.event,
			language = excluded.language, text = excluded.text, raw = excluded.raw, via = excluded.via,
			received_at = excluded.received_at, cancelled = excluded.cancelled, bearers = excluded.bearers`,
		w.ID, w.Ref, w.MsgType, boolInt(w.Test), w.Source, w.Sent.UTC().Format(time.RFC3339), expires,
		w.Severity, w.Urgency, w.Certainty, w.Category, w.Event, w.Language, w.Text, w.Raw, w.Via,
		at.UTC().Format(time.RFC3339Nano), boolInt(w.Cancelled), orJSONArray(w.Bearers)); err != nil {
		return fmt.Errorf("insert warning: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM warnings WHERE received_at < ?`, at.Add(-warningKeep).UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("prune warnings: %w", err)
	}
	return nil
}

// SetWarningBearers records what a warning went out on.
func (db *DB) SetWarningBearers(id, bearers string) error {
	_, err := db.Exec(`UPDATE warnings SET bearers = ? WHERE id = ?`, orJSONArray(bearers), id)
	return err
}

// CancelWarning marks the row of ref cancelled. It answers whether there was
// one.
func (db *DB) CancelWarning(ref string) (bool, error) {
	res, err := db.Exec(`UPDATE warnings SET cancelled = 1 WHERE id = ?`, ref)
	if err != nil {
		return false, fmt.Errorf("cancel warning: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// MarkWarningReplaced marks the row an Update replaces as cancelled (it is
// no longer the current message) and answers whether there was one.
func (db *DB) MarkWarningReplaced(ref string) (bool, error) {
	return db.CancelWarning(ref)
}

// ListWarnings lists the warnings held, newest first.
func (db *DB) ListWarnings(limit int) ([]Warning, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.Query(`SELECT id, ref, msg_type, test, source, sent, expires, severity, urgency, certainty, category,
		event, language, text, via, received_at, cancelled, bearers FROM warnings ORDER BY received_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list warnings: %w", err)
	}
	defer rows.Close()
	var out []Warning
	for rows.Next() {
		var w Warning
		var test, cancelled int
		var sent, expires, received string
		if err := rows.Scan(&w.ID, &w.Ref, &w.MsgType, &test, &w.Source, &sent, &expires, &w.Severity, &w.Urgency, &w.Certainty,
			&w.Category, &w.Event, &w.Language, &w.Text, &w.Via, &received, &cancelled, &w.Bearers); err != nil {
			return nil, fmt.Errorf("list warnings: %w", err)
		}
		w.Test, w.Cancelled = test != 0, cancelled != 0
		w.Sent, _ = time.Parse(time.RFC3339, sent)
		if expires != "" {
			w.Expires, _ = time.Parse(time.RFC3339, expires)
		}
		w.ReceivedAt, _ = time.Parse(time.RFC3339Nano, received)
		out = append(out, w)
	}
	return out, rows.Err()
}

// GetWarning answers one row by id, nil when there is none.
func (db *DB) GetWarning(id string) (*Warning, error) {
	list, err := db.listWarningByID(id)
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (db *DB) listWarningByID(id string) (*Warning, error) {
	var w Warning
	var test, cancelled int
	var sent, expires, received string
	err := db.QueryRow(`SELECT id, ref, msg_type, test, source, sent, expires, severity, urgency, certainty, category,
		event, language, text, via, received_at, cancelled, bearers FROM warnings WHERE id = ?`, id).
		Scan(&w.ID, &w.Ref, &w.MsgType, &test, &w.Source, &sent, &expires, &w.Severity, &w.Urgency, &w.Certainty,
			&w.Category, &w.Event, &w.Language, &w.Text, &w.Via, &received, &cancelled, &w.Bearers)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get warning: %w", err)
	}
	w.Test, w.Cancelled = test != 0, cancelled != 0
	w.Sent, _ = time.Parse(time.RFC3339, sent)
	if expires != "" {
		w.Expires, _ = time.Parse(time.RFC3339, expires)
	}
	w.ReceivedAt, _ = time.Parse(time.RFC3339Nano, received)
	return &w, nil
}

func orJSONArray(s string) string {
	if s == "" {
		return "[]"
	}
	return s
}
