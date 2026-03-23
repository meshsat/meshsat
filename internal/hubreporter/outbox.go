package hubreporter

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	defaultMaxSize = 10000
	defaultMaxAge  = 7 * 24 * time.Hour
	maxRetries     = 5
	replayRate     = 10 // messages per second
)

// Outbox stores hub-bound messages locally when the MQTT broker is unreachable,
// and replays them in FIFO order when the connection is restored.
type Outbox struct {
	db       *sql.DB
	maxSize  int
	maxAge   time.Duration
	replayed int64
}

// NewOutbox creates a new Outbox backed by the given SQLite database.
// The hub_outbox table must already exist (created by migration v31).
func NewOutbox(db *sql.DB, maxSize int, maxAge time.Duration) *Outbox {
	if maxSize <= 0 {
		maxSize = defaultMaxSize
	}
	if maxAge <= 0 {
		maxAge = defaultMaxAge
	}
	return &Outbox{
		db:      db,
		maxSize: maxSize,
		maxAge:  maxAge,
	}
}

// Enqueue inserts a message into the outbox for later replay.
func (o *Outbox) Enqueue(topic string, payload []byte, qos byte) error {
	_, err := o.db.Exec(
		`INSERT INTO hub_outbox (topic, payload, qos) VALUES (?, ?, ?)`,
		topic, string(payload), int(qos),
	)
	if err != nil {
		return fmt.Errorf("outbox enqueue: %w", err)
	}
	log.Debug().Str("topic", topic).Int("bytes", len(payload)).Msg("outbox: message queued")
	return nil
}

// outboxMsg is an in-memory representation of a queued outbox message.
type outboxMsg struct {
	id         int64
	topic      string
	payload    string
	qos        int
	retryCount int
}

// Replay reads all queued messages in FIFO order and calls publishFn for each.
// Successfully published messages are deleted. Failed messages have their retry
// count incremented and are skipped after maxRetries attempts.
// Replay is throttled to replayRate messages per second.
// Returns the count of successfully replayed messages.
func (o *Outbox) Replay(ctx context.Context, publishFn func(topic string, payload []byte, qos byte) error) (int, error) {
	// Read all messages into memory first to avoid holding a read cursor
	// open while performing writes (required for single-connection SQLite).
	rows, err := o.db.QueryContext(ctx,
		`SELECT id, topic, payload, qos, retry_count FROM hub_outbox ORDER BY created_at ASC`,
	)
	if err != nil {
		return 0, fmt.Errorf("outbox replay query: %w", err)
	}

	var msgs []outboxMsg
	for rows.Next() {
		var m outboxMsg
		if err := rows.Scan(&m.id, &m.topic, &m.payload, &m.qos, &m.retryCount); err != nil {
			rows.Close()
			return 0, fmt.Errorf("outbox replay scan: %w", err)
		}
		msgs = append(msgs, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("outbox replay rows: %w", err)
	}

	ticker := time.NewTicker(time.Second / replayRate)
	defer ticker.Stop()

	replayed := 0
	for _, m := range msgs {
		select {
		case <-ctx.Done():
			return replayed, ctx.Err()
		case <-ticker.C:
		}

		// Skip and delete messages that have exceeded max retries
		if m.retryCount >= maxRetries {
			if _, err := o.db.ExecContext(ctx, `DELETE FROM hub_outbox WHERE id = ?`, m.id); err != nil {
				log.Warn().Err(err).Int64("id", m.id).Msg("outbox: failed to delete expired message")
			}
			continue
		}

		if err := publishFn(m.topic, []byte(m.payload), byte(m.qos)); err != nil {
			// Increment retry count on failure
			if _, dbErr := o.db.ExecContext(ctx,
				`UPDATE hub_outbox SET retry_count = retry_count + 1 WHERE id = ?`, m.id,
			); dbErr != nil {
				log.Warn().Err(dbErr).Int64("id", m.id).Msg("outbox: failed to increment retry_count")
			}
			log.Debug().Err(err).Str("topic", m.topic).Int("retry", m.retryCount+1).Msg("outbox: replay failed")
			continue
		}

		// Delete successfully published message
		if _, err := o.db.ExecContext(ctx, `DELETE FROM hub_outbox WHERE id = ?`, m.id); err != nil {
			log.Warn().Err(err).Int64("id", m.id).Msg("outbox: failed to delete replayed message")
		}
		replayed++
	}

	if replayed > 0 {
		atomic.AddInt64(&o.replayed, int64(replayed))
		log.Info().Int("count", replayed).Msg("outbox: replayed queued messages")
	}

	return replayed, nil
}

// Pending returns the number of messages waiting in the outbox.
func (o *Outbox) Pending() (int, error) {
	var count int
	err := o.db.QueryRow(`SELECT COUNT(*) FROM hub_outbox`).Scan(&count)
	return count, err
}

// OldestTimestamp returns the creation time of the oldest queued message,
// or nil if the outbox is empty.
func (o *Outbox) OldestTimestamp() (*time.Time, error) {
	var ts sql.NullString
	err := o.db.QueryRow(`SELECT MIN(created_at) FROM hub_outbox`).Scan(&ts)
	if err != nil {
		return nil, err
	}
	if !ts.Valid {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02 15:04:05", ts.String)
	if err != nil {
		return nil, fmt.Errorf("outbox parse timestamp: %w", err)
	}
	return &t, nil
}

// Cleanup removes messages that are older than maxAge or exceed maxSize (keeping newest).
func (o *Outbox) Cleanup() error {
	cutoff := time.Now().Add(-o.maxAge).Format("2006-01-02 15:04:05")
	if _, err := o.db.Exec(`DELETE FROM hub_outbox WHERE created_at < ?`, cutoff); err != nil {
		return fmt.Errorf("outbox cleanup by age: %w", err)
	}

	// Enforce maxSize — delete oldest entries that exceed the limit
	_, err := o.db.Exec(`DELETE FROM hub_outbox WHERE id NOT IN (
		SELECT id FROM hub_outbox ORDER BY created_at DESC LIMIT ?
	)`, o.maxSize)
	if err != nil {
		return fmt.Errorf("outbox cleanup by size: %w", err)
	}

	return nil
}

// Stats returns the current outbox statistics for health reporting.
func (o *Outbox) Stats() (OutboxInfo, error) {
	pending, err := o.Pending()
	if err != nil {
		return OutboxInfo{}, err
	}
	oldest, err := o.OldestTimestamp()
	if err != nil {
		return OutboxInfo{}, err
	}
	return OutboxInfo{
		Pending:  pending,
		Oldest:   oldest,
		Replayed: atomic.LoadInt64(&o.replayed),
	}, nil
}
