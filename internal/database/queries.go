package database

import (
	"fmt"
	"strings"
	"time"
)

// Message represents a persisted mesh message.
type Message struct {
	ID             int64     `db:"id" json:"id"`
	PacketID       uint32    `db:"packet_id" json:"packet_id"`
	FromNode       string    `db:"from_node" json:"from_node"`
	ToNode         string    `db:"to_node" json:"to_node"`
	Channel        int       `db:"channel" json:"channel"`
	PortNum        int       `db:"portnum" json:"portnum"`
	PortNumName    string    `db:"portnum_name" json:"portnum_name"`
	DecodedText    string    `db:"decoded_text" json:"decoded_text"`
	RxSNR          float32   `db:"rx_snr" json:"rx_snr"`
	RxTime         int64     `db:"rx_time" json:"rx_time"`
	HopLimit       int       `db:"hop_limit" json:"hop_limit"`
	HopStart       int       `db:"hop_start" json:"hop_start"`
	Direction      string    `db:"direction" json:"direction"`
	Transport      string    `db:"transport" json:"transport"`
	DeliveryStatus string    `db:"delivery_status" json:"delivery_status"`
	DeliveryError  *string   `db:"delivery_error" json:"delivery_error,omitempty"`
	ComposedAt     *string   `db:"composed_at" json:"composed_at,omitempty"`
	SatelliteCost  int       `db:"satellite_cost" json:"satellite_cost"`
	CreatedAt      time.Time `db:"created_at" json:"created_at"`
}

// Telemetry represents a telemetry snapshot for a node.
type Telemetry struct {
	ID            int64     `db:"id" json:"id"`
	NodeID        string    `db:"node_id" json:"node_id"`
	BatteryLevel  int       `db:"battery_level" json:"battery_level"`
	Voltage       float32   `db:"voltage" json:"voltage"`
	ChannelUtil   float32   `db:"channel_util" json:"channel_util"`
	AirUtilTx     float32   `db:"air_util_tx" json:"air_util_tx"`
	Temperature   *float32  `db:"temperature" json:"temperature"`
	Humidity      *float32  `db:"humidity" json:"humidity"`
	Pressure      *float32  `db:"pressure" json:"pressure"`
	UptimeSeconds int       `db:"uptime_seconds" json:"uptime_seconds"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`
}

// Position represents a GPS position record for a node.
type Position struct {
	ID          int64     `db:"id" json:"id"`
	NodeID      string    `db:"node_id" json:"node_id"`
	Latitude    float64   `db:"latitude" json:"latitude"`
	Longitude   float64   `db:"longitude" json:"longitude"`
	Altitude    int       `db:"altitude" json:"altitude"`
	SatsInView  int       `db:"sats_in_view" json:"sats_in_view"`
	GroundSpeed int       `db:"ground_speed" json:"ground_speed"`
	GroundTrack int       `db:"ground_track" json:"ground_track"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
}

// GatewayConfig represents stored gateway configuration.
type GatewayConfig struct {
	ID        int64     `db:"id" json:"id"`
	Type      string    `db:"type" json:"type"`
	Enabled   bool      `db:"enabled" json:"enabled"`
	Config    string    `db:"config" json:"config"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

// MessageFilter controls query parameters for message listing.
type MessageFilter struct {
	Node      string
	Since     string // RFC3339
	Until     string // RFC3339
	PortNum   *int
	Transport string
	Direction string
	Limit     int
	Offset    int
}

// MessageStats holds aggregate message counts.
type MessageStats struct {
	Total       int            `json:"total"`
	Today       int            `json:"today"`
	ByTransport map[string]int `json:"by_transport"`
	ByPortNum   map[string]int `json:"by_portnum"`
	OldestAt    string         `json:"oldest_at,omitempty"`
	NewestAt    string         `json:"newest_at,omitempty"`
}

// InsertMessage persists a mesh message.
func (db *DB) InsertMessage(m *Message) error {
	_, err := db.Exec(`INSERT INTO messages
		(packet_id, from_node, to_node, channel, portnum, portnum_name, decoded_text,
		 rx_snr, rx_time, hop_limit, hop_start, direction, transport)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.PacketID, m.FromNode, m.ToNode, m.Channel, m.PortNum, m.PortNumName, m.DecodedText,
		m.RxSNR, m.RxTime, m.HopLimit, m.HopStart, m.Direction, m.Transport)
	return err
}

// InsertTelemetry persists a telemetry snapshot.
func (db *DB) InsertTelemetry(t *Telemetry) error {
	_, err := db.Exec(`INSERT INTO telemetry
		(node_id, battery_level, voltage, channel_util, air_util_tx,
		 temperature, humidity, pressure, uptime_seconds)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.NodeID, t.BatteryLevel, t.Voltage, t.ChannelUtil, t.AirUtilTx,
		t.Temperature, t.Humidity, t.Pressure, t.UptimeSeconds)
	return err
}

// InsertPosition persists a GPS position record.
func (db *DB) InsertPosition(p *Position) error {
	_, err := db.Exec(`INSERT INTO positions
		(node_id, latitude, longitude, altitude, sats_in_view, ground_speed, ground_track)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.NodeID, p.Latitude, p.Longitude, p.Altitude, p.SatsInView, p.GroundSpeed, p.GroundTrack)
	return err
}

// GetMessages returns paginated messages matching the filter.
func (db *DB) GetMessages(f MessageFilter) ([]Message, int, error) {
	var where []string
	var args []interface{}

	if f.Node != "" {
		where = append(where, "from_node = ?")
		args = append(args, f.Node)
	}
	if f.Since != "" {
		where = append(where, "created_at >= ?")
		args = append(args, f.Since)
	}
	if f.Until != "" {
		where = append(where, "created_at <= ?")
		args = append(args, f.Until)
	}
	if f.PortNum != nil {
		where = append(where, "portnum = ?")
		args = append(args, *f.PortNum)
	}
	if f.Transport != "" {
		where = append(where, "transport = ?")
		args = append(args, f.Transport)
	}
	if f.Direction != "" {
		where = append(where, "direction = ?")
		args = append(args, f.Direction)
	}

	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	// Count total matching
	var total int
	countArgs := make([]interface{}, len(args))
	copy(countArgs, args)
	if err := db.QueryRow("SELECT COUNT(*) FROM messages"+clause, countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count messages: %w", err)
	}

	// Clamp defaults
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 50
	}

	query := "SELECT * FROM messages" + clause + " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	args = append(args, f.Limit, f.Offset)

	var msgs []Message
	if err := db.Select(&msgs, query, args...); err != nil {
		return nil, 0, fmt.Errorf("query messages: %w", err)
	}
	return msgs, total, nil
}

// GetTelemetry returns telemetry records for a node within a time range.
func (db *DB) GetTelemetry(nodeID, since, until string, limit int) ([]Telemetry, error) {
	var where []string
	var args []interface{}

	if nodeID != "" {
		where = append(where, "node_id = ?")
		args = append(args, nodeID)
	}
	if since != "" {
		where = append(where, "created_at >= ?")
		args = append(args, since)
	}
	if until != "" {
		where = append(where, "created_at <= ?")
		args = append(args, until)
	}

	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}
	if limit <= 0 || limit > 10000 {
		limit = 100
	}

	query := "SELECT * FROM telemetry" + clause + " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	var records []Telemetry
	if err := db.Select(&records, query, args...); err != nil {
		return nil, fmt.Errorf("query telemetry: %w", err)
	}
	return records, nil
}

// GetPositions returns position records for a node within a time range.
func (db *DB) GetPositions(nodeID, since, until string, limit int) ([]Position, error) {
	var where []string
	var args []interface{}

	if nodeID != "" {
		where = append(where, "node_id = ?")
		args = append(args, nodeID)
	}
	if since != "" {
		where = append(where, "created_at >= ?")
		args = append(args, since)
	}
	if until != "" {
		where = append(where, "created_at <= ?")
		args = append(args, until)
	}

	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}
	if limit <= 0 || limit > 10000 {
		limit = 100
	}

	query := "SELECT * FROM positions" + clause + " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	var records []Position
	if err := db.Select(&records, query, args...); err != nil {
		return nil, fmt.Errorf("query positions: %w", err)
	}
	return records, nil
}

// GetMessageStats returns aggregate counts by transport and portnum.
func (db *DB) GetMessageStats() (*MessageStats, error) {
	stats := &MessageStats{
		ByTransport: make(map[string]int),
		ByPortNum:   make(map[string]int),
	}

	if err := db.QueryRow("SELECT COUNT(*) FROM messages").Scan(&stats.Total); err != nil {
		return nil, fmt.Errorf("count total: %w", err)
	}

	// Today's count
	db.QueryRow("SELECT COUNT(*) FROM messages WHERE date(created_at) = date('now')").Scan(&stats.Today)

	// Date range
	var oldest, newest *string
	db.QueryRow("SELECT MIN(created_at) FROM messages").Scan(&oldest)
	db.QueryRow("SELECT MAX(created_at) FROM messages").Scan(&newest)
	if oldest != nil {
		stats.OldestAt = *oldest
	}
	if newest != nil {
		stats.NewestAt = *newest
	}

	rows, err := db.Query("SELECT transport, COUNT(*) FROM messages GROUP BY transport")
	if err != nil {
		return nil, fmt.Errorf("count by transport: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var v int
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		stats.ByTransport[k] = v
	}

	rows2, err := db.Query("SELECT portnum_name, COUNT(*) FROM messages GROUP BY portnum_name")
	if err != nil {
		return nil, fmt.Errorf("count by portnum: %w", err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var k string
		var v int
		if err := rows2.Scan(&k, &v); err != nil {
			return nil, err
		}
		stats.ByPortNum[k] = v
	}

	return stats, nil
}

// PurgeMessages deletes messages older than the given RFC3339 timestamp.
// Returns the number of deleted rows.
func (db *DB) PurgeMessages(before string) (int64, error) {
	result, err := db.Exec("DELETE FROM messages WHERE created_at < ?", before)
	if err != nil {
		return 0, fmt.Errorf("purge messages: %w", err)
	}
	return result.RowsAffected()
}

// PruneOlderThan deletes records older than the given number of days.
func (db *DB) PruneOlderThan(days int) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -days).Format(time.RFC3339)

	var total int64
	for _, table := range []string{"messages", "telemetry", "positions"} {
		res, err := db.Exec("DELETE FROM "+table+" WHERE created_at < ?", cutoff)
		if err != nil {
			return total, fmt.Errorf("prune %s: %w", table, err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// SaveGatewayConfig upserts gateway configuration.
func (db *DB) SaveGatewayConfig(gwType string, enabled bool, config string) error {
	_, err := db.Exec(`INSERT INTO gateway_config (type, enabled, config, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(type) DO UPDATE SET enabled=excluded.enabled, config=excluded.config, updated_at=CURRENT_TIMESTAMP`,
		gwType, enabled, config)
	return err
}

// GetGatewayConfig retrieves gateway configuration by type.
func (db *DB) GetGatewayConfig(gwType string) (*GatewayConfig, error) {
	var gc GatewayConfig
	if err := db.Get(&gc, "SELECT * FROM gateway_config WHERE type = ?", gwType); err != nil {
		return nil, err
	}
	return &gc, nil
}

// GetAllGatewayConfigs returns all gateway configurations.
func (db *DB) GetAllGatewayConfigs() ([]GatewayConfig, error) {
	var configs []GatewayConfig
	if err := db.Select(&configs, "SELECT * FROM gateway_config ORDER BY type"); err != nil {
		return nil, fmt.Errorf("query gateway configs: %w", err)
	}
	return configs, nil
}

// DeleteGatewayConfig removes a gateway configuration by type.
func (db *DB) DeleteGatewayConfig(gwType string) error {
	_, err := db.Exec("DELETE FROM gateway_config WHERE type = ?", gwType)
	return err
}

// HasPacket checks if a packet ID already exists (for deduplication).
func (db *DB) HasPacket(packetID uint32) (bool, error) {
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM messages WHERE packet_id = ?", packetID).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// DeadLetter represents a failed satellite send queued for retry.
type DeadLetter struct {
	ID         int64     `db:"id" json:"id"`
	PacketID   uint32    `db:"packet_id" json:"packet_id"`
	Payload    []byte    `db:"payload" json:"payload"`
	Retries    int       `db:"retries" json:"retries"`
	MaxRetries int       `db:"max_retries" json:"max_retries"`
	NextRetry  time.Time `db:"next_retry" json:"next_retry"`
	Status     string    `db:"status" json:"status"`     // pending, sent, expired, cancelled
	Priority   int       `db:"priority" json:"priority"` // 0=critical, 1=normal, 2=low
	LastError  string    `db:"last_error" json:"last_error"`
	CreatedAt  time.Time `db:"created_at" json:"created_at"`
	UpdatedAt  time.Time `db:"updated_at" json:"updated_at"`
}

// sqliteTime formats a time for SQLite DATETIME comparison (matches CURRENT_TIMESTAMP format).
const sqliteTimeFormat = "2006-01-02 15:04:05"

// InsertDeadLetter adds a failed send to the dead-letter queue with normal priority.
func (db *DB) InsertDeadLetter(packetID uint32, payload []byte, maxRetries int, nextRetry time.Time, lastError string) error {
	_, err := db.Exec(`INSERT INTO dead_letters (packet_id, payload, max_retries, next_retry, last_error, priority)
		VALUES (?, ?, ?, ?, ?, 1)`,
		packetID, payload, maxRetries, nextRetry.UTC().Format(sqliteTimeFormat), lastError)
	return err
}

// InsertDirectDeadLetter enqueues a user-composed message directly into the DLQ.
// packet_id=0 means not associated with a mesh packet. Priority: 0=critical, 1=normal, 2=low.
func (db *DB) InsertDirectDeadLetter(payload []byte, priority, maxRetries int) error {
	nextRetry := time.Now().UTC().Format(sqliteTimeFormat)
	_, err := db.Exec(`INSERT INTO dead_letters (packet_id, payload, max_retries, next_retry, last_error, priority)
		VALUES (0, ?, ?, ?, '', ?)`,
		payload, maxRetries, nextRetry, priority)
	return err
}

// GetPendingDeadLetters returns dead letters ready for retry, ordered by priority then next_retry.
func (db *DB) GetPendingDeadLetters(limit int) ([]DeadLetter, error) {
	if limit <= 0 {
		limit = 10
	}
	var dls []DeadLetter
	err := db.Select(&dls,
		`SELECT * FROM dead_letters WHERE status = 'pending' AND next_retry <= CURRENT_TIMESTAMP
		 ORDER BY priority ASC, next_retry ASC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query dead letters: %w", err)
	}
	return dls, nil
}

// GetDeadLetterQueue returns all non-cancelled, non-sent queue entries for display.
func (db *DB) GetDeadLetterQueue() ([]DeadLetter, error) {
	var dls []DeadLetter
	err := db.Select(&dls,
		`SELECT * FROM dead_letters WHERE status NOT IN ('sent', 'cancelled')
		 ORDER BY priority ASC, created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("query DLQ: %w", err)
	}
	return dls, nil
}

// CancelDeadLetter marks a dead letter as cancelled (will not be retried).
func (db *DB) CancelDeadLetter(id int64) error {
	res, err := db.Exec(
		`UPDATE dead_letters SET status = 'cancelled', updated_at = CURRENT_TIMESTAMP WHERE id = ? AND status = 'pending'`,
		id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("dead letter %d not found or not pending", id)
	}
	return nil
}

// SetDeadLetterPriority updates the priority of a pending dead letter.
func (db *DB) SetDeadLetterPriority(id int64, priority int) error {
	if priority < 0 || priority > 2 {
		return fmt.Errorf("priority must be 0 (critical), 1 (normal), or 2 (low)")
	}
	res, err := db.Exec(
		`UPDATE dead_letters SET priority = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND status = 'pending'`,
		priority, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("dead letter %d not found or not pending", id)
	}
	return nil
}

// MarkDeadLetterSent marks a dead letter as successfully sent.
func (db *DB) MarkDeadLetterSent(id int64) error {
	_, err := db.Exec(`UPDATE dead_letters SET status = 'sent', updated_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	return err
}

// UpdateDeadLetterRetry increments retry count and schedules next attempt.
func (db *DB) UpdateDeadLetterRetry(id int64, nextRetry time.Time, lastError string) error {
	_, err := db.Exec(`UPDATE dead_letters
		SET retries = retries + 1, next_retry = ?, last_error = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		nextRetry.UTC().Format(sqliteTimeFormat), lastError, id)
	return err
}

// ExpireDeadLetter marks a dead letter as expired (max retries exhausted).
func (db *DB) ExpireDeadLetter(id int64, lastError string) error {
	_, err := db.Exec(`UPDATE dead_letters SET status = 'expired', last_error = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		lastError, id)
	return err
}

// CountPendingDeadLetters returns the number of pending dead letters.
func (db *DB) CountPendingDeadLetters() (int, error) {
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM dead_letters WHERE status = 'pending'").Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// PruneDeadLetters removes sent/expired dead letters older than the given number of days.
func (db *DB) PruneDeadLetters(days int) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -days).Format(time.RFC3339)
	res, err := db.Exec("DELETE FROM dead_letters WHERE status IN ('sent', 'expired') AND updated_at < ?", cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune dead letters: %w", err)
	}
	return res.RowsAffected()
}

// ---- Forwarding Rules ----

// ForwardingRule represents a message forwarding rule.
type ForwardingRule struct {
	ID                int     `db:"id" json:"id"`
	Name              string  `db:"name" json:"name"`
	Enabled           bool    `db:"enabled" json:"enabled"`
	Priority          int     `db:"priority" json:"priority"`
	SourceType        string  `db:"source_type" json:"source_type"`
	SourceChannels    *string `db:"source_channels" json:"source_channels,omitempty"`
	SourceNodes       *string `db:"source_nodes" json:"source_nodes,omitempty"`
	SourcePortnums    *string `db:"source_portnums" json:"source_portnums,omitempty"`
	SourceKeyword     *string `db:"source_keyword" json:"source_keyword,omitempty"`
	DestType          string  `db:"dest_type" json:"dest_type"`
	SatPriority       int     `db:"sat_priority" json:"sat_priority"`
	SatMaxDelaySec    int     `db:"sat_max_delay_sec" json:"sat_max_delay_sec"`
	SatIncludePos     bool    `db:"sat_include_pos" json:"sat_include_pos"`
	SatMaxTextLen     int     `db:"sat_max_text_len" json:"sat_max_text_len"`
	PositionPrecision int     `db:"position_precision" json:"position_precision"`
	RateLimitPerMin   int     `db:"rate_limit_per_min" json:"rate_limit_per_min"`
	RateLimitWindow   int     `db:"rate_limit_window" json:"rate_limit_window"`
	MatchCount        int     `db:"match_count" json:"match_count"`
	LastMatchAt       *string `db:"last_match_at" json:"last_match_at,omitempty"`
	CreatedAt         string  `db:"created_at" json:"created_at"`
	UpdatedAt         string  `db:"updated_at" json:"updated_at"`
}

// GetForwardingRules returns all rules sorted by priority.
func (db *DB) GetForwardingRules() ([]ForwardingRule, error) {
	var rules []ForwardingRule
	err := db.Select(&rules, "SELECT * FROM forwarding_rules ORDER BY priority ASC, id ASC")
	if err != nil {
		return nil, fmt.Errorf("query forwarding rules: %w", err)
	}
	return rules, nil
}

// GetForwardingRule returns a single rule by ID.
func (db *DB) GetForwardingRule(id int) (*ForwardingRule, error) {
	var rule ForwardingRule
	if err := db.Get(&rule, "SELECT * FROM forwarding_rules WHERE id = ?", id); err != nil {
		return nil, err
	}
	return &rule, nil
}

// InsertForwardingRule creates a new forwarding rule and returns its ID.
func (db *DB) InsertForwardingRule(r *ForwardingRule) (int64, error) {
	res, err := db.Exec(`INSERT INTO forwarding_rules
		(name, enabled, priority, source_type, source_channels, source_nodes, source_portnums, source_keyword,
		 dest_type, sat_priority, sat_max_delay_sec, sat_include_pos, sat_max_text_len,
		 position_precision, rate_limit_per_min, rate_limit_window)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Name, r.Enabled, r.Priority, r.SourceType, r.SourceChannels, r.SourceNodes, r.SourcePortnums, r.SourceKeyword,
		r.DestType, r.SatPriority, r.SatMaxDelaySec, r.SatIncludePos, r.SatMaxTextLen,
		r.PositionPrecision, r.RateLimitPerMin, r.RateLimitWindow)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateForwardingRule updates an existing rule.
func (db *DB) UpdateForwardingRule(r *ForwardingRule) error {
	_, err := db.Exec(`UPDATE forwarding_rules SET
		name=?, enabled=?, priority=?, source_type=?, source_channels=?, source_nodes=?, source_portnums=?, source_keyword=?,
		dest_type=?, sat_priority=?, sat_max_delay_sec=?, sat_include_pos=?, sat_max_text_len=?,
		position_precision=?, rate_limit_per_min=?, rate_limit_window=?, updated_at=datetime('now')
		WHERE id=?`,
		r.Name, r.Enabled, r.Priority, r.SourceType, r.SourceChannels, r.SourceNodes, r.SourcePortnums, r.SourceKeyword,
		r.DestType, r.SatPriority, r.SatMaxDelaySec, r.SatIncludePos, r.SatMaxTextLen,
		r.PositionPrecision, r.RateLimitPerMin, r.RateLimitWindow, r.ID)
	return err
}

// DeleteForwardingRule removes a rule by ID.
func (db *DB) DeleteForwardingRule(id int) error {
	_, err := db.Exec("DELETE FROM forwarding_rules WHERE id = ?", id)
	return err
}

// SetForwardingRuleEnabled enables or disables a rule.
func (db *DB) SetForwardingRuleEnabled(id int, enabled bool) error {
	_, err := db.Exec("UPDATE forwarding_rules SET enabled=?, updated_at=datetime('now') WHERE id=?", enabled, id)
	return err
}

// ReorderForwardingRules sets priorities based on the given ID order.
func (db *DB) ReorderForwardingRules(ids []int) error {
	for i, id := range ids {
		if _, err := db.Exec("UPDATE forwarding_rules SET priority=?, updated_at=datetime('now') WHERE id=?", i+1, id); err != nil {
			return fmt.Errorf("reorder rule %d: %w", id, err)
		}
	}
	return nil
}

// UpdateRuleMatch increments the match count and sets last_match_at.
func (db *DB) UpdateRuleMatch(id int, matchedAt string) error {
	_, err := db.Exec("UPDATE forwarding_rules SET match_count = match_count + 1, last_match_at = ? WHERE id = ?", matchedAt, id)
	return err
}

// GetRuleStats returns match count, last match time, and estimated monthly credit cost for a rule.
func (db *DB) GetRuleStats(ruleID int) (matchCount int, lastMatch *string, monthlyCost int, err error) {
	err = db.QueryRow("SELECT match_count, last_match_at FROM forwarding_rules WHERE id = ?", ruleID).Scan(&matchCount, &lastMatch)
	if err != nil {
		return
	}
	err = db.QueryRow("SELECT COALESCE(SUM(credits), 0) FROM credit_usage WHERE rule_id = ? AND date >= date('now', '-30 days')", ruleID).Scan(&monthlyCost)
	return
}

// ---- Preset Messages ----

// PresetMessage represents a canned/preset message.
type PresetMessage struct {
	ID          int     `db:"id" json:"id"`
	Name        string  `db:"name" json:"name"`
	Text        string  `db:"text" json:"text"`
	Destination string  `db:"destination" json:"destination"`
	Icon        *string `db:"icon" json:"icon,omitempty"`
	SortOrder   int     `db:"sort_order" json:"sort_order"`
	CreatedAt   string  `db:"created_at" json:"created_at"`
}

// GetPresetMessages returns all presets sorted by sort_order.
func (db *DB) GetPresetMessages() ([]PresetMessage, error) {
	var presets []PresetMessage
	err := db.Select(&presets, "SELECT * FROM preset_messages ORDER BY sort_order ASC, id ASC")
	if err != nil {
		return nil, fmt.Errorf("query presets: %w", err)
	}
	return presets, nil
}

// InsertPresetMessage creates a new preset.
func (db *DB) InsertPresetMessage(p *PresetMessage) (int64, error) {
	res, err := db.Exec(`INSERT INTO preset_messages (name, text, destination, icon, sort_order) VALUES (?, ?, ?, ?, ?)`,
		p.Name, p.Text, p.Destination, p.Icon, p.SortOrder)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdatePresetMessage updates an existing preset.
func (db *DB) UpdatePresetMessage(p *PresetMessage) error {
	_, err := db.Exec("UPDATE preset_messages SET name=?, text=?, destination=?, icon=?, sort_order=? WHERE id=?",
		p.Name, p.Text, p.Destination, p.Icon, p.SortOrder, p.ID)
	return err
}

// DeletePresetMessage removes a preset by ID.
func (db *DB) DeletePresetMessage(id int) error {
	_, err := db.Exec("DELETE FROM preset_messages WHERE id = ?", id)
	return err
}

// ---- Delivery Status ----

// UpdateDeliveryStatus updates the delivery status of a message.
func (db *DB) UpdateDeliveryStatus(msgID int64, status string, errMsg *string) error {
	if errMsg != nil {
		_, err := db.Exec("UPDATE messages SET delivery_status=?, delivery_error=? WHERE id=?", status, *errMsg, msgID)
		return err
	}
	_, err := db.Exec("UPDATE messages SET delivery_status=? WHERE id=?", status, msgID)
	return err
}

// UpdateDeliveryStatusByPacket updates delivery status by packet_id (for ACK matching).
func (db *DB) UpdateDeliveryStatusByPacket(packetID uint32, status string) error {
	_, err := db.Exec("UPDATE messages SET delivery_status=? WHERE packet_id=? AND direction='tx'", status, packetID)
	return err
}

// InsertMessageWithStatus persists a message with initial delivery status.
func (db *DB) InsertMessageWithStatus(m *Message, deliveryStatus string) (int64, error) {
	res, err := db.Exec(`INSERT INTO messages
		(packet_id, from_node, to_node, channel, portnum, portnum_name, decoded_text,
		 rx_snr, rx_time, hop_limit, hop_start, direction, transport, delivery_status, composed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.PacketID, m.FromNode, m.ToNode, m.Channel, m.PortNum, m.PortNumName, m.DecodedText,
		m.RxSNR, m.RxTime, m.HopLimit, m.HopStart, m.Direction, m.Transport, deliveryStatus, m.ComposedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ---- Credit Usage ----

// CreditUsage records credit consumption for a satellite send.
type CreditUsage struct {
	ID        int    `db:"id" json:"id"`
	RuleID    *int   `db:"rule_id" json:"rule_id,omitempty"`
	Credits   int    `db:"credits" json:"credits"`
	MessageID *int64 `db:"message_id" json:"message_id,omitempty"`
	Date      string `db:"date" json:"date"`
	CreatedAt string `db:"created_at" json:"created_at"`
}

// InsertCreditUsage records credit consumption.
func (db *DB) InsertCreditUsage(ruleID *int, credits int, messageID *int64) error {
	_, err := db.Exec("INSERT INTO credit_usage (rule_id, credits, message_id) VALUES (?, ?, ?)",
		ruleID, credits, messageID)
	return err
}

// GetCreditUsageByDate returns daily credit totals for the given range.
func (db *DB) GetCreditUsageByDate(since, until string) ([]CreditUsage, error) {
	var usage []CreditUsage
	err := db.Select(&usage,
		`SELECT date, SUM(credits) as credits FROM credit_usage
		 WHERE date >= ? AND date <= ? GROUP BY date ORDER BY date DESC`, since, until)
	if err != nil {
		return nil, fmt.Errorf("query credit usage: %w", err)
	}
	return usage, nil
}

// GetDailyCreditTotal returns total credits used today.
func (db *DB) GetDailyCreditTotal() (int, error) {
	var total int
	err := db.QueryRow("SELECT COALESCE(SUM(credits), 0) FROM credit_usage WHERE date = date('now')").Scan(&total)
	return total, err
}

// GetMonthlyCreditTotal returns total credits used this month.
func (db *DB) GetMonthlyCreditTotal() (int, error) {
	var total int
	err := db.QueryRow("SELECT COALESCE(SUM(credits), 0) FROM credit_usage WHERE date >= date('now', 'start of month')").Scan(&total)
	return total, err
}

// GetAllTimeCreditTotal returns total credits used all time.
func (db *DB) GetAllTimeCreditTotal() (int, error) {
	var total int
	err := db.QueryRow("SELECT COALESCE(SUM(credits), 0) FROM credit_usage").Scan(&total)
	return total, err
}

// ---- Signal History ----

// SignalHistoryPoint represents a recorded signal reading.
type SignalHistoryPoint struct {
	ID        int64   `db:"id" json:"id"`
	Source    string  `db:"source" json:"source"`
	Timestamp int64   `db:"timestamp" json:"timestamp"`
	Value     float64 `db:"value" json:"value"`
}

// InsertSignalHistory records a signal bar reading.
func (db *DB) InsertSignalHistory(source string, timestamp int64, value float64) error {
	_, err := db.Exec(
		`INSERT OR IGNORE INTO signal_history (source, timestamp, value) VALUES (?, ?, ?)`,
		source, timestamp, value)
	return err
}

// GetSignalHistoryRaw returns raw signal history points within a time range.
func (db *DB) GetSignalHistoryRaw(source string, from, to int64, limit int) ([]SignalHistoryPoint, error) {
	if limit <= 0 || limit > 10000 {
		limit = 500
	}
	var points []SignalHistoryPoint
	err := db.Select(&points,
		`SELECT * FROM signal_history WHERE source = ? AND timestamp >= ? AND timestamp <= ?
		 ORDER BY timestamp DESC LIMIT ?`, source, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("query signal history: %w", err)
	}
	return points, nil
}

// SignalHistoryAggregated represents a time-bucketed signal average.
type SignalHistoryAggregated struct {
	Bucket int64   `json:"bucket"`
	Avg    float64 `json:"avg"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	Count  int     `json:"count"`
}

// GetSignalHistoryAggregated returns aggregated signal history in time buckets.
func (db *DB) GetSignalHistoryAggregated(source string, from, to int64, intervalSec int) ([]SignalHistoryAggregated, error) {
	if intervalSec <= 0 {
		intervalSec = 300 // 5 minutes default
	}
	var points []SignalHistoryAggregated
	rows, err := db.Query(
		`SELECT (timestamp / ?) * ? AS bucket,
		        AVG(value) AS avg, MIN(value) AS min, MAX(value) AS max, COUNT(*) AS count
		 FROM signal_history WHERE source = ? AND timestamp >= ? AND timestamp <= ?
		 GROUP BY bucket ORDER BY bucket ASC`,
		intervalSec, intervalSec, source, from, to)
	if err != nil {
		return nil, fmt.Errorf("query aggregated signal: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p SignalHistoryAggregated
		if err := rows.Scan(&p.Bucket, &p.Avg, &p.Min, &p.Max, &p.Count); err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	return points, nil
}

// PruneSignalHistory removes signal history older than the given number of days.
func (db *DB) PruneSignalHistory(days int) (int64, error) {
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()
	res, err := db.Exec("DELETE FROM signal_history WHERE timestamp < ?", cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune signal history: %w", err)
	}
	return res.RowsAffected()
}

// ---- System Config ----

// GetSystemConfig retrieves a system config value by key.
func (db *DB) GetSystemConfig(key string) (string, error) {
	var val string
	err := db.QueryRow("SELECT value FROM system_config WHERE key = ?", key).Scan(&val)
	return val, err
}

// SetSystemConfig upserts a system config key-value pair.
func (db *DB) SetSystemConfig(key, value string) error {
	_, err := db.Exec(
		`INSERT INTO system_config (key, value, updated_at) VALUES (?, ?, datetime('now'))
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=datetime('now')`,
		key, value)
	return err
}

// ---- Credit Summary ----

// CreditSummary aggregates credit usage with budget limits.
type CreditSummary struct {
	Today         int `json:"today"`
	Month         int `json:"month"`
	AllTime       int `json:"all_time"`
	DailyBudget   int `json:"daily_budget"`
	MonthlyBudget int `json:"monthly_budget"`
}

// GetCreditSummary returns aggregated credit counts and budget limits.
func (db *DB) GetCreditSummary() (*CreditSummary, error) {
	s := &CreditSummary{}
	var err error

	s.Today, err = db.GetDailyCreditTotal()
	if err != nil {
		return nil, fmt.Errorf("daily credits: %w", err)
	}
	s.Month, err = db.GetMonthlyCreditTotal()
	if err != nil {
		return nil, fmt.Errorf("monthly credits: %w", err)
	}
	s.AllTime, err = db.GetAllTimeCreditTotal()
	if err != nil {
		return nil, fmt.Errorf("all-time credits: %w", err)
	}

	// Read budgets from system_config (default 0 = unlimited)
	if v, e := db.GetSystemConfig("iridium_daily_budget"); e == nil {
		fmt.Sscanf(v, "%d", &s.DailyBudget)
	}
	if v, e := db.GetSystemConfig("iridium_monthly_budget"); e == nil {
		fmt.Sscanf(v, "%d", &s.MonthlyBudget)
	}

	return s, nil
}

// ---- Iridium Locations ----

// IridiumLocation represents a ground station for pass prediction.
type IridiumLocation struct {
	ID      int     `db:"id" json:"id"`
	Name    string  `db:"name" json:"name"`
	Lat     float64 `db:"lat" json:"lat"`
	Lon     float64 `db:"lon" json:"lon"`
	AltM    float64 `db:"alt_m" json:"alt_m"`
	Builtin bool    `db:"builtin" json:"builtin"`
}

// GetIridiumLocations returns all ground station locations.
func (db *DB) GetIridiumLocations() ([]IridiumLocation, error) {
	var locs []IridiumLocation
	err := db.Select(&locs, "SELECT * FROM iridium_locations ORDER BY builtin DESC, name ASC")
	if err != nil {
		return nil, fmt.Errorf("query locations: %w", err)
	}
	return locs, nil
}

// InsertIridiumLocation adds a custom location.
func (db *DB) InsertIridiumLocation(name string, lat, lon, altM float64) (int64, error) {
	res, err := db.Exec(
		"INSERT INTO iridium_locations (name, lat, lon, alt_m, builtin) VALUES (?, ?, ?, ?, 0)",
		name, lat, lon, altM)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteIridiumLocation removes a custom location (builtin locations cannot be deleted).
func (db *DB) DeleteIridiumLocation(id int) error {
	res, err := db.Exec("DELETE FROM iridium_locations WHERE id = ? AND builtin = 0", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("location %d not found or is built-in", id)
	}
	return nil
}

// ---- TLE Cache ----

// TLECacheEntry represents a cached TLE from Celestrak.
type TLECacheEntry struct {
	ID            int    `db:"id" json:"id"`
	SatelliteName string `db:"satellite_name" json:"satellite_name"`
	Line1         string `db:"line1" json:"line1"`
	Line2         string `db:"line2" json:"line2"`
	FetchedAt     int64  `db:"fetched_at" json:"fetched_at"`
}

// GetTLECache returns all cached TLE entries.
func (db *DB) GetTLECache() ([]TLECacheEntry, error) {
	var entries []TLECacheEntry
	err := db.Select(&entries, "SELECT * FROM iridium_tle_cache ORDER BY satellite_name ASC")
	if err != nil {
		return nil, fmt.Errorf("query TLE cache: %w", err)
	}
	return entries, nil
}

// GetTLECacheAge returns the age of the cache in seconds, or -1 if empty.
func (db *DB) GetTLECacheAge() (int64, error) {
	var fetchedAt int64
	err := db.QueryRow("SELECT COALESCE(MAX(fetched_at), 0) FROM iridium_tle_cache").Scan(&fetchedAt)
	if err != nil || fetchedAt == 0 {
		return -1, err
	}
	return time.Now().Unix() - fetchedAt, nil
}

// ReplaceTLECache replaces all cached TLEs with new data.
func (db *DB) ReplaceTLECache(entries []TLECacheEntry) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin TLE replace: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM iridium_tle_cache"); err != nil {
		return fmt.Errorf("clear TLE cache: %w", err)
	}
	for _, e := range entries {
		if _, err := tx.Exec(
			"INSERT INTO iridium_tle_cache (satellite_name, line1, line2, fetched_at) VALUES (?, ?, ?, ?)",
			e.SatelliteName, e.Line1, e.Line2, e.FetchedAt); err != nil {
			return fmt.Errorf("insert TLE %s: %w", e.SatelliteName, err)
		}
	}
	return tx.Commit()
}
