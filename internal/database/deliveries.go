package database

import (
	"fmt"
	"strings"
	"time"
)

// MessageDelivery represents a single delivery attempt for a message to a channel.
type MessageDelivery struct {
	ID          int64      `json:"id"`
	MsgRef      string     `json:"msg_ref"`
	RuleID      *int64     `json:"rule_id,omitempty"`
	Channel     string     `json:"channel"`
	Status      string     `json:"status"` // queued, sending, sent, delivered, failed, retry, dead
	Priority    int        `json:"priority"`
	Payload     []byte     `json:"payload,omitempty"`
	TextPreview string     `json:"text_preview"`
	Retries     int        `json:"retries"`
	MaxRetries  int        `json:"max_retries"`
	NextRetry   *time.Time `json:"next_retry,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	ChannelRef  string     `json:"channel_ref,omitempty"`
	Cost        int        `json:"cost"`
	Visited     string     `json:"visited"`              // JSON array of visited interface IDs (loop prevention)
	TTLSeconds  int        `json:"ttl_seconds"`          // 0 means no expiry
	ExpiresAt   *string    `json:"expires_at,omitempty"` // UTC timestamp when delivery expires
	QoSLevel    int        `json:"qos_level"`            // QoS level from access rule (default 1)
	Signature   []byte     `json:"signature,omitempty"`  // Ed25519 signature (64 bytes)
	SignerID    string     `json:"signer_id,omitempty"`  // hex-encoded Ed25519 public key
	CreatedAt   string     `json:"created_at"`
	UpdatedAt   string     `json:"updated_at"`
}

// DeliveryFilter specifies query filters for listing deliveries.
type DeliveryFilter struct {
	Channel string
	Status  string
	MsgRef  string
	Limit   int
	Offset  int
}

// DeliveryStats holds counts by channel and status.
type DeliveryStats struct {
	Channel string `json:"channel"`
	Status  string `json:"status"`
	Count   int    `json:"count"`
}

// InsertDelivery creates a new delivery row and returns its ID.
func (db *DB) InsertDelivery(d MessageDelivery) (int64, error) {
	visited := d.Visited
	if visited == "" {
		visited = "[]"
	}
	res, err := db.Exec(`INSERT INTO message_deliveries
		(msg_ref, rule_id, channel, status, priority, payload, text_preview, max_retries, next_retry, visited, ttl_seconds, expires_at, qos_level, signature, signer_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.MsgRef, d.RuleID, d.Channel, d.Status, d.Priority, d.Payload, d.TextPreview, d.MaxRetries, d.NextRetry, visited,
		d.TTLSeconds, d.ExpiresAt, d.QoSLevel, d.Signature, d.SignerID)
	if err != nil {
		return 0, fmt.Errorf("insert delivery: %w", err)
	}
	return res.LastInsertId()
}

// GetDelivery returns a single delivery by ID.
func (db *DB) GetDelivery(id int64) (*MessageDelivery, error) {
	row := db.QueryRow(`SELECT id, msg_ref, rule_id, channel, status, priority, payload, text_preview,
		retries, max_retries, next_retry, last_error, channel_ref, cost, visited,
		ttl_seconds, expires_at, qos_level, created_at, updated_at
		FROM message_deliveries WHERE id = ?`, id)

	var d MessageDelivery
	err := row.Scan(&d.ID, &d.MsgRef, &d.RuleID, &d.Channel, &d.Status, &d.Priority, &d.Payload,
		&d.TextPreview, &d.Retries, &d.MaxRetries, &d.NextRetry, &d.LastError, &d.ChannelRef,
		&d.Cost, &d.Visited, &d.TTLSeconds, &d.ExpiresAt, &d.QoSLevel, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get delivery %d: %w", id, err)
	}
	return &d, nil
}

// GetDeliveries returns deliveries matching the filter.
func (db *DB) GetDeliveries(f DeliveryFilter) ([]MessageDelivery, error) {
	var where []string
	var args []interface{}

	if f.Channel != "" {
		where = append(where, "channel = ?")
		args = append(args, f.Channel)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.MsgRef != "" {
		where = append(where, "msg_ref = ?")
		args = append(args, f.MsgRef)
	}

	query := "SELECT id, msg_ref, rule_id, channel, status, priority, payload, text_preview, retries, max_retries, next_retry, last_error, channel_ref, cost, visited, ttl_seconds, expires_at, qos_level, created_at, updated_at FROM message_deliveries"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY created_at DESC"

	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	query += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, f.Offset)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get deliveries: %w", err)
	}
	defer rows.Close()

	var result []MessageDelivery
	for rows.Next() {
		var d MessageDelivery
		if err := rows.Scan(&d.ID, &d.MsgRef, &d.RuleID, &d.Channel, &d.Status, &d.Priority, &d.Payload,
			&d.TextPreview, &d.Retries, &d.MaxRetries, &d.NextRetry, &d.LastError, &d.ChannelRef,
			&d.Cost, &d.Visited, &d.TTLSeconds, &d.ExpiresAt, &d.QoSLevel, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan delivery: %w", err)
		}
		result = append(result, d)
	}
	return result, nil
}

// GetPendingDeliveries returns deliveries ready for processing on a channel.
func (db *DB) GetPendingDeliveries(channel string, limit int) ([]MessageDelivery, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := db.Query(`SELECT id, msg_ref, rule_id, channel, status, priority, payload, text_preview,
		retries, max_retries, next_retry, last_error, channel_ref, cost, visited,
		ttl_seconds, expires_at, qos_level, created_at, updated_at
		FROM message_deliveries
		WHERE channel = ? AND status IN ('queued', 'retry')
		  AND (next_retry IS NULL OR next_retry <= datetime('now'))
		  AND (priority = 0 OR expires_at IS NULL OR expires_at > datetime('now'))
		ORDER BY priority ASC, created_at ASC
		LIMIT ?`, channel, limit)
	if err != nil {
		return nil, fmt.Errorf("get pending deliveries: %w", err)
	}
	defer rows.Close()

	var result []MessageDelivery
	for rows.Next() {
		var d MessageDelivery
		if err := rows.Scan(&d.ID, &d.MsgRef, &d.RuleID, &d.Channel, &d.Status, &d.Priority, &d.Payload,
			&d.TextPreview, &d.Retries, &d.MaxRetries, &d.NextRetry, &d.LastError, &d.ChannelRef,
			&d.Cost, &d.Visited, &d.TTLSeconds, &d.ExpiresAt, &d.QoSLevel, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan pending delivery: %w", err)
		}
		result = append(result, d)
	}
	return result, nil
}

// SetDeliveryStatus updates the status, error, and channel ref of a delivery.
func (db *DB) SetDeliveryStatus(id int64, status, lastError, channelRef string) error {
	_, err := db.Exec(`UPDATE message_deliveries SET status = ?, last_error = ?, channel_ref = ?, updated_at = datetime('now') WHERE id = ?`,
		status, lastError, channelRef, id)
	if err != nil {
		return fmt.Errorf("update delivery status %d: %w", id, err)
	}
	return nil
}

// UpdateDeliveryRetry sets the next retry time and increments the retry count.
func (db *DB) UpdateDeliveryRetry(id int64, nextRetry time.Time, retries int, lastError string) error {
	_, err := db.Exec(`UPDATE message_deliveries SET status = 'retry', retries = ?, next_retry = ?, last_error = ?, updated_at = datetime('now') WHERE id = ?`,
		retries, nextRetry.UTC().Format("2006-01-02 15:04:05"), lastError, id)
	if err != nil {
		return fmt.Errorf("update delivery retry %d: %w", id, err)
	}
	return nil
}

// UpdateDeliveryCost increments the cost counter for a delivery.
func (db *DB) UpdateDeliveryCost(id int64, cost int) error {
	_, err := db.Exec(`UPDATE message_deliveries SET cost = cost + ?, updated_at = datetime('now') WHERE id = ?`, cost, id)
	return err
}

// GetDeliveriesByMessage returns all deliveries for a given message reference.
func (db *DB) GetDeliveriesByMessage(msgRef string) ([]MessageDelivery, error) {
	return db.GetDeliveries(DeliveryFilter{MsgRef: msgRef, Limit: 50})
}

// DeliveryStatsAll returns delivery counts grouped by channel and status.
func (db *DB) DeliveryStatsAll() ([]DeliveryStats, error) {
	rows, err := db.Query(`SELECT channel, status, COUNT(*) FROM message_deliveries GROUP BY channel, status ORDER BY channel, status`)
	if err != nil {
		return nil, fmt.Errorf("delivery stats: %w", err)
	}
	defer rows.Close()

	var result []DeliveryStats
	for rows.Next() {
		var s DeliveryStats
		if err := rows.Scan(&s.Channel, &s.Status, &s.Count); err != nil {
			return nil, fmt.Errorf("scan delivery stats: %w", err)
		}
		result = append(result, s)
	}
	return result, nil
}

// CancelDelivery sets a pending delivery to 'dead' status.
func (db *DB) CancelDelivery(id int64) error {
	res, err := db.Exec(`UPDATE message_deliveries SET status = 'dead', last_error = 'cancelled', updated_at = datetime('now')
		WHERE id = ? AND status IN ('queued', 'retry')`, id)
	if err != nil {
		return fmt.Errorf("cancel delivery %d: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("delivery %d not cancellable (not queued/retry)", id)
	}
	return nil
}

// RetryDelivery forces an immediate retry of a failed/dead delivery.
func (db *DB) RetryDelivery(id int64) error {
	res, err := db.Exec(`UPDATE message_deliveries SET status = 'queued', next_retry = NULL, updated_at = datetime('now')
		WHERE id = ? AND status IN ('failed', 'dead')`, id)
	if err != nil {
		return fmt.Errorf("retry delivery %d: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("delivery %d not retryable (not failed/dead)", id)
	}
	return nil
}

// QueueDepth returns the number of active (non-terminal) deliveries for a channel.
func (db *DB) QueueDepth(channel string) (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM message_deliveries WHERE channel = ? AND status IN ('queued', 'retry', 'held', 'sending')`, channel).Scan(&count)
	return count, err
}

// QueueBytes returns the total payload size of active deliveries for a channel.
func (db *DB) QueueBytes(channel string) (int64, error) {
	var total int64
	err := db.QueryRow(`SELECT COALESCE(SUM(LENGTH(payload)), 0) FROM message_deliveries WHERE channel = ? AND status IN ('queued', 'retry', 'held', 'sending')`, channel).Scan(&total)
	return total, err
}

// LowestActivePriority returns the lowest priority (highest number) of active
// deliveries in a channel's queue. Returns -1 if no evictable delivery exists.
// Only considers non-P0 deliveries (P0 critical messages are never evicted).
func (db *DB) LowestActivePriority(channel string) (int, error) {
	var priority int
	err := db.QueryRow(`SELECT priority FROM message_deliveries
		WHERE channel = ? AND status IN ('queued', 'retry', 'held')
		  AND priority > 0
		ORDER BY priority DESC
		LIMIT 1`, channel).Scan(&priority)
	if err != nil {
		return -1, err
	}
	return priority, nil
}

// EvictLowestPriority removes the single lowest-priority (highest priority number)
// active delivery from a channel's queue, marking it 'dead'. Returns the number
// of rows affected. Only evicts non-P0 deliveries.
func (db *DB) EvictLowestPriority(channel string) (int64, error) {
	res, err := db.Exec(`UPDATE message_deliveries SET status = 'dead',
		last_error = 'evicted: queue full, lower priority', updated_at = datetime('now')
		WHERE id = (
			SELECT id FROM message_deliveries
			WHERE channel = ? AND status IN ('queued', 'retry', 'held')
			  AND priority > 0
			ORDER BY priority DESC, created_at DESC
			LIMIT 1
		)`, channel)
	if err != nil {
		return 0, fmt.Errorf("evict delivery for %s: %w", channel, err)
	}
	return res.RowsAffected()
}

// CancelRunawayDeliveries kills queued/retry deliveries whose retry count exceeds
// their max_retries setting. This cleans up deliveries that accumulated excessive
// retries due to bugs (e.g. SBDIX parse failures causing false retries).
// Deliveries with max_retries=0 (infinite) are capped at the safetyLimit.
func (db *DB) CancelRunawayDeliveries(safetyLimit int) (int64, error) {
	res, err := db.Exec(`UPDATE message_deliveries
		SET status = 'dead', last_error = 'cancelled: exceeded retry limit on startup cleanup', updated_at = datetime('now')
		WHERE status IN ('queued', 'retry')
		  AND ((max_retries > 0 AND retries >= max_retries)
		    OR (max_retries = 0 AND retries >= ?))`,
		safetyLimit)
	if err != nil {
		return 0, fmt.Errorf("cancel runaway deliveries: %w", err)
	}
	return res.RowsAffected()
}

// RecoverStaleDeliveries resets deliveries stuck in 'sending' status back to 'retry'.
// This happens when the process crashes or restarts mid-delivery.
func (db *DB) RecoverStaleDeliveries() (int64, error) {
	res, err := db.Exec(`UPDATE message_deliveries SET status = 'retry', last_error = 'recovered after restart', next_retry = datetime('now'), updated_at = datetime('now')
		WHERE status = 'sending'`)
	if err != nil {
		return 0, fmt.Errorf("recover stale deliveries: %w", err)
	}
	return res.RowsAffected()
}

// ExpireDeliveries marks all expired queued/retry/held deliveries as 'expired'.
// P0 critical messages (priority=0) are exempt — they never expire.
func (db *DB) ExpireDeliveries() (int64, error) {
	res, err := db.Exec(`UPDATE message_deliveries SET status = 'expired', updated_at = datetime('now')
		WHERE status IN ('queued', 'retry', 'held') AND expires_at IS NOT NULL AND expires_at <= datetime('now')
		  AND priority > 0`)
	if err != nil {
		return 0, fmt.Errorf("expire deliveries: %w", err)
	}
	return res.RowsAffected()
}

// ExpireDeliveriesForChannel marks expired deliveries for a specific channel.
// P0 critical messages (priority=0) are exempt — they never expire.
func (db *DB) ExpireDeliveriesForChannel(channel string) (int64, error) {
	res, err := db.Exec(`UPDATE message_deliveries SET status = 'expired', updated_at = datetime('now')
		WHERE channel = ? AND status IN ('queued', 'retry', 'held') AND expires_at IS NOT NULL AND expires_at <= datetime('now')
		  AND priority > 0`, channel)
	if err != nil {
		return 0, fmt.Errorf("expire deliveries for %s: %w", channel, err)
	}
	return res.RowsAffected()
}

// HoldDeliveriesForChannel moves queued/retry deliveries to 'held' status for a channel.
// Called when an interface goes offline — deliveries are preserved but won't be attempted.
func (db *DB) HoldDeliveriesForChannel(channel string) (int64, error) {
	res, err := db.Exec(`UPDATE message_deliveries SET status = 'held', held_at = datetime('now'), updated_at = datetime('now')
		WHERE channel = ? AND status IN ('queued', 'retry')`, channel)
	if err != nil {
		return 0, fmt.Errorf("hold deliveries for %s: %w", channel, err)
	}
	return res.RowsAffected()
}

// UnholdDeliveriesForChannel moves held deliveries back to 'queued' status for a channel.
// Called when an interface comes back online. Extends expires_at by the duration spent
// in held state (TTL clock pauses while held).
func (db *DB) UnholdDeliveriesForChannel(channel string) (int64, error) {
	// Extend expires_at by (now - held_at) seconds for deliveries that have both
	// a held_at timestamp and an expires_at. This pauses the TTL clock while held.
	res, err := db.Exec(`UPDATE message_deliveries
		SET status = 'queued',
		    expires_at = CASE
		        WHEN expires_at IS NOT NULL AND held_at IS NOT NULL
		        THEN datetime(expires_at, '+' || CAST((strftime('%s', 'now') - strftime('%s', held_at)) AS TEXT) || ' seconds')
		        ELSE expires_at
		    END,
		    held_at = NULL,
		    updated_at = datetime('now')
		WHERE channel = ? AND status = 'held'`, channel)
	if err != nil {
		return 0, fmt.Errorf("unhold deliveries for %s: %w", channel, err)
	}
	return res.RowsAffected()
}
