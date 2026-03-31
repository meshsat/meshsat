package database

import (
	"database/sql"
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
	ID         int64     `db:"id" json:"id"`
	Type       string    `db:"type" json:"type"`
	InstanceID string    `db:"instance_id" json:"instance_id"`
	Enabled    bool      `db:"enabled" json:"enabled"`
	Config     string    `db:"config" json:"config"`
	UpdatedAt  time.Time `db:"updated_at" json:"updated_at"`
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
	TodayText   int            `json:"today_text"`
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

	// Today's text message count (portnum 1 = TEXT_MESSAGE)
	db.QueryRow("SELECT COUNT(*) FROM messages WHERE date(created_at) = date('now') AND portnum = 1").Scan(&stats.TodayText)

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

// SaveGatewayConfig upserts gateway configuration by type (legacy — uses first instance).
func (db *DB) SaveGatewayConfig(gwType string, enabled bool, config string) error {
	return db.SaveGatewayConfigInstance(gwType, gwType+"_0", enabled, config)
}

// SaveGatewayConfigInstance upserts gateway configuration for a specific instance.
func (db *DB) SaveGatewayConfigInstance(gwType, instanceID string, enabled bool, config string) error {
	_, err := db.Exec(`INSERT INTO gateway_config (type, instance_id, enabled, config, updated_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(type, instance_id) DO UPDATE SET enabled=excluded.enabled, config=excluded.config, updated_at=CURRENT_TIMESTAMP`,
		gwType, instanceID, enabled, config)
	return err
}

// GetGatewayConfig retrieves gateway configuration by type (legacy — returns first instance).
func (db *DB) GetGatewayConfig(gwType string) (*GatewayConfig, error) {
	var gc GatewayConfig
	if err := db.Get(&gc, "SELECT * FROM gateway_config WHERE type = ? ORDER BY instance_id LIMIT 1", gwType); err != nil {
		return nil, err
	}
	return &gc, nil
}

// GetGatewayConfigByInstance retrieves gateway configuration by instance ID.
func (db *DB) GetGatewayConfigByInstance(instanceID string) (*GatewayConfig, error) {
	var gc GatewayConfig
	if err := db.Get(&gc, "SELECT * FROM gateway_config WHERE instance_id = ?", instanceID); err != nil {
		return nil, err
	}
	return &gc, nil
}

// GetGatewayConfigsByType returns all instances of a gateway type.
func (db *DB) GetGatewayConfigsByType(gwType string) ([]GatewayConfig, error) {
	var configs []GatewayConfig
	if err := db.Select(&configs, "SELECT * FROM gateway_config WHERE type = ? ORDER BY instance_id", gwType); err != nil {
		return nil, fmt.Errorf("query gateway configs by type: %w", err)
	}
	return configs, nil
}

// GetAllGatewayConfigs returns all gateway configurations.
func (db *DB) GetAllGatewayConfigs() ([]GatewayConfig, error) {
	var configs []GatewayConfig
	if err := db.Select(&configs, "SELECT * FROM gateway_config ORDER BY type, instance_id"); err != nil {
		return nil, fmt.Errorf("query gateway configs: %w", err)
	}
	return configs, nil
}

// DeleteGatewayConfig removes a gateway configuration by type (legacy — deletes first instance).
func (db *DB) DeleteGatewayConfig(gwType string) error {
	_, err := db.Exec("DELETE FROM gateway_config WHERE type = ? AND instance_id = ?", gwType, gwType+"_0")
	return err
}

// DeleteGatewayConfigInstance removes a specific gateway instance configuration.
func (db *DB) DeleteGatewayConfigInstance(instanceID string) error {
	_, err := db.Exec("DELETE FROM gateway_config WHERE instance_id = ?", instanceID)
	return err
}

// NextGatewayInstanceID returns the next available instance ID for a gateway type
// (e.g. "iridium_0" → "iridium_1" → "iridium_2").
func (db *DB) NextGatewayInstanceID(gwType string) (string, error) {
	var maxID string
	err := db.QueryRow("SELECT COALESCE(MAX(instance_id), '') FROM gateway_config WHERE type = ?", gwType).Scan(&maxID)
	if err != nil {
		return gwType + "_0", nil
	}
	if maxID == "" {
		return gwType + "_0", nil
	}
	// Parse suffix number
	for i := len(maxID) - 1; i >= 0; i-- {
		if maxID[i] == '_' {
			n := 0
			if _, err := fmt.Sscanf(maxID[i+1:], "%d", &n); err == nil {
				return fmt.Sprintf("%s_%d", gwType, n+1), nil
			}
			break
		}
	}
	return gwType + "_0", nil
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
	ID           int64     `db:"id" json:"id"`
	PacketID     uint32    `db:"packet_id" json:"packet_id"`
	Payload      []byte    `db:"payload" json:"payload"`
	Retries      int       `db:"retries" json:"retries"`
	MaxRetries   int       `db:"max_retries" json:"max_retries"`
	NextRetry    time.Time `db:"next_retry" json:"next_retry"`
	Status       string    `db:"status" json:"status"`             // pending, sent, expired, cancelled, received
	Priority     int       `db:"priority" json:"priority"`         // 0=critical, 1=normal, 2=low
	Direction    string    `db:"direction" json:"direction"`       // outbound (mesh→sat) or inbound (sat→mesh)
	TextPreview  string    `db:"text_preview" json:"text_preview"` // plaintext for display (binary payload is not human-readable)
	LastError    string    `db:"last_error" json:"last_error"`
	LastMOStatus int       `db:"last_mo_status" json:"last_mo_status"` // last SBDIX mo_status (-1 = none)
	CreatedAt    time.Time `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time `db:"updated_at" json:"updated_at"`
}

// sqliteTime formats a time for SQLite DATETIME comparison (matches CURRENT_TIMESTAMP format).
const sqliteTimeFormat = "2006-01-02 15:04:05"

// InsertDeadLetter adds a failed send to the dead-letter queue with normal priority.
func (db *DB) InsertDeadLetter(packetID uint32, payload []byte, maxRetries int, nextRetry time.Time, lastError string, textPreview string) error {
	_, err := db.Exec(`INSERT INTO dead_letters (packet_id, payload, max_retries, next_retry, last_error, priority, text_preview)
		VALUES (?, ?, ?, ?, ?, 1, ?)`,
		packetID, payload, maxRetries, nextRetry.UTC().Format(sqliteTimeFormat), lastError, textPreview)
	return err
}

// InsertDirectDeadLetter enqueues a user-composed message directly into the DLQ.
// packet_id=0 means not associated with a mesh packet. Priority: 0=critical, 1=normal, 2=low.
func (db *DB) InsertDirectDeadLetter(payload []byte, priority, maxRetries int, textPreview string) error {
	nextRetry := time.Now().UTC().Format(sqliteTimeFormat)
	_, err := db.Exec(`INSERT INTO dead_letters (packet_id, payload, max_retries, next_retry, last_error, priority, text_preview)
		VALUES (0, ?, ?, ?, '', ?, ?)`,
		payload, maxRetries, nextRetry, priority, textPreview)
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

// GetPendingDeadLettersAll returns all pending dead letters regardless of next_retry time.
// Used by opportunistic drain when signal is available — bypasses backoff timers.
func (db *DB) GetPendingDeadLettersAll(limit int) ([]DeadLetter, error) {
	if limit <= 0 {
		limit = 10
	}
	var dls []DeadLetter
	err := db.Select(&dls,
		`SELECT * FROM dead_letters WHERE status = 'pending'
		 ORDER BY priority ASC, next_retry ASC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query dead letters: %w", err)
	}
	return dls, nil
}

// GetDeadLetterQueue returns queue entries for display: pending/expired first, then recent sends/receives.
func (db *DB) GetDeadLetterQueue() ([]DeadLetter, error) {
	var dls []DeadLetter
	err := db.Select(&dls,
		`SELECT * FROM dead_letters WHERE status NOT IN ('cancelled')
		 ORDER BY CASE status WHEN 'pending' THEN 0 WHEN 'expired' THEN 1 ELSE 2 END,
		 created_at DESC LIMIT 50`)
	if err != nil {
		return nil, fmt.Errorf("query DLQ: %w", err)
	}
	return dls, nil
}

// InsertSentRecord records a successful satellite send for queue visibility.
func (db *DB) InsertSentRecord(packetID uint32, payload []byte, textPreview string) error {
	_, err := db.Exec(`INSERT INTO dead_letters
		(packet_id, payload, retries, max_retries, next_retry, status, priority, direction, text_preview)
		VALUES (?, ?, 0, 0, CURRENT_TIMESTAMP, 'sent', 1, 'outbound', ?)`,
		packetID, payload, textPreview)
	return err
}

// InsertInboundReceiveRecord records a received satellite message for queue visibility.
func (db *DB) InsertInboundReceiveRecord(payload []byte, text string) error {
	data := payload
	if data == nil && text != "" {
		data = []byte(text)
	}
	_, err := db.Exec(`INSERT INTO dead_letters
		(packet_id, payload, retries, max_retries, next_retry, status, priority, direction, text_preview)
		VALUES (0, ?, 0, 0, CURRENT_TIMESTAMP, 'received', 1, 'inbound', ?)`,
		data, text)
	return err
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
// moStatus is the SBDIX mo_status code (-1 if no SBDIX was attempted).
func (db *DB) UpdateDeadLetterRetry(id int64, nextRetry time.Time, lastError string, moStatus int) error {
	_, err := db.Exec(`UPDATE dead_letters
		SET retries = retries + 1, next_retry = ?, last_error = ?, last_mo_status = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		nextRetry.UTC().Format(sqliteTimeFormat), lastError, moStatus, id)
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

// DeleteDeadLetter permanently removes a dead letter entry by ID.
func (db *DB) DeleteDeadLetter(id int64) error {
	res, err := db.Exec("DELETE FROM dead_letters WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete dead letter: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("dead letter %d not found", id)
	}
	return nil
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

// GetLatestSignal returns the most recent non-zero signal reading from the last 10 minutes.
func (db *DB) GetLatestSignal(source string) (*SignalHistoryPoint, error) {
	cutoff := time.Now().Unix() - 600 // 10 minutes ago
	var p SignalHistoryPoint
	err := db.Get(&p,
		`SELECT * FROM signal_history WHERE source = ? AND value > 0 AND timestamp >= ?
		 ORDER BY timestamp DESC LIMIT 1`, source, cutoff)
	if err != nil {
		return nil, err
	}
	return &p, nil
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

// GetSignalHistoryRawMulti returns raw signal history for multiple sources.
// Used when no specific source is requested — returns sbd, imt, and legacy "iridium" entries.
func (db *DB) GetSignalHistoryRawMulti(sources []string, from, to int64, limit int) ([]SignalHistoryPoint, error) {
	if limit <= 0 || limit > 10000 {
		limit = 500
	}
	if len(sources) == 0 {
		return nil, nil
	}
	// Build IN clause
	args := make([]interface{}, 0, len(sources)+3)
	placeholders := make([]string, len(sources))
	for i, s := range sources {
		placeholders[i] = "?"
		args = append(args, s)
	}
	args = append(args, from, to, limit)
	query := fmt.Sprintf(
		`SELECT * FROM signal_history WHERE source IN (%s) AND timestamp >= ? AND timestamp <= ?
		 ORDER BY timestamp DESC LIMIT ?`,
		strings.Join(placeholders, ","))
	var points []SignalHistoryPoint
	err := db.Select(&points, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query signal history multi: %w", err)
	}
	return points, nil
}

// GetSignalHistoryAggregatedMulti returns aggregated signal history for multiple sources.
func (db *DB) GetSignalHistoryAggregatedMulti(sources []string, from, to int64, intervalSec int) ([]SignalHistoryAggregated, error) {
	if intervalSec <= 0 {
		intervalSec = 300
	}
	if len(sources) == 0 {
		return nil, nil
	}
	args := make([]interface{}, 0, len(sources)+4)
	placeholders := make([]string, len(sources))
	args = append(args, intervalSec, intervalSec)
	for i, s := range sources {
		placeholders[i] = "?"
		args = append(args, s)
	}
	args = append(args, from, to)
	query := fmt.Sprintf(
		`SELECT (timestamp / ?) * ? AS bucket,
		        AVG(value) AS avg, MIN(value) AS min, MAX(value) AS max, COUNT(*) AS count
		 FROM signal_history WHERE source IN (%s) AND timestamp >= ? AND timestamp <= ?
		 GROUP BY bucket ORDER BY bucket ASC`,
		strings.Join(placeholders, ","))
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query aggregated signal multi: %w", err)
	}
	defer rows.Close()
	var points []SignalHistoryAggregated
	for rows.Next() {
		var p SignalHistoryAggregated
		if err := rows.Scan(&p.Bucket, &p.Avg, &p.Min, &p.Max, &p.Count); err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	return points, nil
}

// GetLatestSignalMulti returns the most recent non-zero signal reading from any of the given sources.
func (db *DB) GetLatestSignalMulti(sources []string) (*SignalHistoryPoint, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("no sources")
	}
	cutoff := time.Now().Unix() - 600
	args := make([]interface{}, 0, len(sources)+1)
	placeholders := make([]string, len(sources))
	for i, s := range sources {
		placeholders[i] = "?"
		args = append(args, s)
	}
	args = append(args, cutoff)
	query := fmt.Sprintf(
		`SELECT * FROM signal_history WHERE source IN (%s) AND value > 0 AND timestamp >= ?
		 ORDER BY timestamp DESC LIMIT 1`,
		strings.Join(placeholders, ","))
	var p SignalHistoryPoint
	err := db.Get(&p, query, args...)
	if err != nil {
		return nil, err
	}
	return &p, nil
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

// ---- Astrocast TLE Cache ----

// GetAstrocastTLECache returns all cached Astrocast TLE entries.
func (db *DB) GetAstrocastTLECache() ([]TLECacheEntry, error) {
	var entries []TLECacheEntry
	err := db.Select(&entries, "SELECT * FROM astrocast_tle_cache ORDER BY satellite_name ASC")
	if err != nil {
		return nil, fmt.Errorf("query Astrocast TLE cache: %w", err)
	}
	return entries, nil
}

// GetAstrocastTLECacheAge returns the age of the Astrocast TLE cache in seconds, or -1 if empty.
func (db *DB) GetAstrocastTLECacheAge() (int64, error) {
	var fetchedAt int64
	err := db.QueryRow("SELECT COALESCE(MAX(fetched_at), 0) FROM astrocast_tle_cache").Scan(&fetchedAt)
	if err != nil || fetchedAt == 0 {
		return -1, err
	}
	return time.Now().Unix() - fetchedAt, nil
}

// ReplaceAstrocastTLECache replaces all cached Astrocast TLEs with new data.
func (db *DB) ReplaceAstrocastTLECache(entries []TLECacheEntry) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin Astrocast TLE replace: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM astrocast_tle_cache"); err != nil {
		return fmt.Errorf("clear Astrocast TLE cache: %w", err)
	}
	for _, e := range entries {
		if _, err := tx.Exec(
			"INSERT INTO astrocast_tle_cache (satellite_name, line1, line2, fetched_at) VALUES (?, ?, ?, ?)",
			e.SatelliteName, e.Line1, e.Line2, e.FetchedAt); err != nil {
			return fmt.Errorf("insert Astrocast TLE %s: %w", e.SatelliteName, err)
		}
	}
	return tx.Commit()
}

// ---- Pass Quality Log ----

// InsertPassQualityLog records the actual signal quality observed during a predicted pass.
func (db *DB) InsertPassQualityLog(satellite string, aos, los int64, peakElevDeg, actualBarsAvg float64, actualBarsMax, moAttempts, moSuccesses int) error {
	_, err := db.Exec(
		`INSERT INTO pass_quality_log (satellite, aos, los, peak_elev_deg, actual_bars_avg, actual_bars_max, mo_attempts, mo_successes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		satellite, aos, los, peakElevDeg, actualBarsAvg, actualBarsMax, moAttempts, moSuccesses)
	return err
}

// GetPassQualityByElevation returns the historical signal hit rate for passes in an elevation band.
// Returns the fraction of passes where actual_bars_avg >= 1 (i.e. had signal), and the sample count.
func (db *DB) GetPassQualityByElevation(elevLow, elevHigh float64, lookbackDays int) (hitRate float64, samples int, err error) {
	cutoff := time.Now().AddDate(0, 0, -lookbackDays).Format("2006-01-02 15:04:05")
	var total, hits int
	err = db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN actual_bars_avg >= 1 THEN 1 ELSE 0 END), 0)
		 FROM pass_quality_log WHERE peak_elev_deg >= ? AND peak_elev_deg < ? AND created_at >= ?`,
		elevLow, elevHigh, cutoff).Scan(&total, &hits)
	if err != nil || total == 0 {
		return 0, 0, err
	}
	return float64(hits) / float64(total), total, nil
}

// GetSignalDuringWindow returns the average and max signal bars recorded during a time window.
func (db *DB) GetSignalDuringWindow(source string, from, to int64) (avg float64, max int, count int, err error) {
	err = db.QueryRow(
		`SELECT COALESCE(AVG(value), 0), COALESCE(MAX(value), 0), COUNT(*)
		 FROM signal_history WHERE source = ? AND timestamp >= ? AND timestamp <= ?`,
		source, from, to).Scan(&avg, &max, &count)
	return
}

// GetGSSSuccessRateByElevation returns the GSS registration success rate for passes
// whose peak elevation falls within the given band, over the last lookbackDays days.
// It correlates GSS events (signal_history source='gss') with pass_quality_log entries.
func (db *DB) GetGSSSuccessRateByElevation(elevLow, elevHigh float64, lookbackDays int) (successRate float64, samples int, err error) {
	cutoff := time.Now().AddDate(0, 0, -lookbackDays).Format("2006-01-02 15:04:05")
	// For each logged pass in the elevation band, check if any GSS success occurred during AOS-LOS
	var total, successes int
	err = db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN EXISTS (
			SELECT 1 FROM signal_history WHERE source = 'gss' AND value >= 1
			AND timestamp >= p.aos AND timestamp <= p.los
		) THEN 1 ELSE 0 END), 0)
		 FROM pass_quality_log p
		 WHERE p.peak_elev_deg >= ? AND p.peak_elev_deg < ? AND p.created_at >= ?`,
		elevLow, elevHigh, cutoff).Scan(&total, &successes)
	if err != nil || total == 0 {
		return 0, 0, err
	}
	return float64(successes) / float64(total), total, nil
}

// ---- Iridium Geolocation ----

// GeolocationRecord represents a stored geolocation reading.
type GeolocationRecord struct {
	ID         int64   `db:"id" json:"id"`
	Source     string  `db:"source" json:"source"`
	Lat        float64 `db:"lat" json:"lat"`
	Lon        float64 `db:"lon" json:"lon"`
	AltKm      float64 `db:"alt_km" json:"alt_km"`
	AccuracyKm float64 `db:"accuracy_km" json:"accuracy_km"`
	Timestamp  int64   `db:"timestamp" json:"timestamp"`
}

// InsertGeolocation records an Iridium (or GPS) geolocation reading.
func (db *DB) InsertGeolocation(source string, lat, lon, altKm, accuracyKm float64, timestamp int64) error {
	_, err := db.Exec(
		`INSERT INTO iridium_geolocation (source, lat, lon, alt_km, accuracy_km, timestamp)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		source, lat, lon, altKm, accuracyKm, timestamp)
	return err
}

// GetLatestGeolocation returns the most recent geolocation reading for a given source.
func (db *DB) GetLatestGeolocation(source string) (*GeolocationRecord, error) {
	var rec GeolocationRecord
	err := db.QueryRow(
		`SELECT id, source, lat, lon, alt_km, accuracy_km, timestamp
		 FROM iridium_geolocation WHERE source = ? ORDER BY timestamp DESC LIMIT 1`,
		source).Scan(&rec.ID, &rec.Source, &rec.Lat, &rec.Lon, &rec.AltKm, &rec.AccuracyKm, &rec.Timestamp)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// GetLatestGPSPosition returns the most recent GPS position from the positions table.
func (db *DB) GetLatestGPSPosition() (*GeolocationRecord, error) {
	var lat, lon float64
	var alt int
	var ts string
	err := db.QueryRow(
		`SELECT latitude, longitude, altitude, created_at
		 FROM positions ORDER BY created_at DESC LIMIT 1`).Scan(&lat, &lon, &alt, &ts)
	if err != nil {
		return nil, err
	}
	// Parse created_at to unix timestamp
	t, _ := time.Parse("2006-01-02 15:04:05", ts)
	return &GeolocationRecord{
		Source:     "gps",
		Lat:        lat,
		Lon:        lon,
		AltKm:      float64(alt) / 1000.0,
		AccuracyKm: 0.005, // GPS accuracy ~5m
		Timestamp:  t.Unix(),
	}, nil
}

// GetAllGeolocationSources returns the latest reading from each available source.
func (db *DB) GetAllGeolocationSources() ([]GeolocationRecord, error) {
	var results []GeolocationRecord

	// Latest GPS from dedicated GPS reader (iridium_geolocation table, source="gps")
	gps, err := db.GetLatestGeolocation("gps")
	if err == nil && gps != nil {
		results = append(results, *gps)
	} else {
		// Fallback: GPS position from Meshtastic node positions table
		meshGPS, err2 := db.GetLatestGPSPosition()
		if err2 == nil && meshGPS != nil {
			results = append(results, *meshGPS)
		}
	}

	return results, nil
}

// IridiumGeoPoint represents a single AT-MSGEO reading (satellite sub-point).
type IridiumGeoPoint struct {
	ID          int64   `json:"id"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	AccuracyKm  float64 `json:"accuracy_km"`
	SatelliteID string  `json:"satellite_id"`
	Timestamp   int64   `json:"timestamp"`
}

// GetRecentIridiumGeolocations returns Iridium satellite sub-point readings
// from the last N hours, for multi-pass position visualization.
func (db *DB) GetRecentIridiumGeolocations(hours int) ([]IridiumGeoPoint, error) {
	cutoff := time.Now().Unix() - int64(hours)*3600
	rows, err := db.Query(
		`SELECT id, lat, lon, accuracy_km, COALESCE(satellite_id, ''), timestamp
		 FROM iridium_geolocation
		 WHERE source = 'iridium' AND timestamp > ?
		 ORDER BY timestamp DESC LIMIT 50`,
		cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []IridiumGeoPoint
	for rows.Next() {
		var p IridiumGeoPoint
		if err := rows.Scan(&p.ID, &p.Lat, &p.Lon, &p.AccuracyKm, &p.SatelliteID, &p.Timestamp); err != nil {
			continue
		}
		results = append(results, p)
	}
	return results, rows.Err()
}

// InsertIridiumGeolocation stores an Iridium satellite sub-point reading.
func (db *DB) InsertIridiumGeolocation(lat, lon, accuracyKm float64, satelliteID string, timestamp int64) error {
	_, err := db.Exec(
		`INSERT INTO iridium_geolocation (source, lat, lon, alt_km, accuracy_km, satellite_id, timestamp)
		 VALUES ('iridium', ?, ?, 0, ?, ?, ?)`,
		lat, lon, accuracyKm, satelliteID, timestamp)
	return err
}

// ============================================================================
// Cellular Signal History
// ============================================================================

// CellularSignalPoint represents a cellular signal reading.
type CellularSignalPoint struct {
	ID         int64  `db:"id" json:"id"`
	Timestamp  int64  `db:"timestamp" json:"timestamp"`
	Bars       int    `db:"bars" json:"bars"`
	DBm        int    `db:"dbm" json:"dbm"`
	Technology string `db:"technology" json:"technology"`
	Operator   string `db:"operator" json:"operator"`
}

// InsertCellularSignal persists a cellular signal reading.
func (db *DB) InsertCellularSignal(timestamp int64, bars, dbm int, technology, operator string) error {
	_, err := db.Exec(
		`INSERT OR IGNORE INTO cellular_signal_history (timestamp, bars, dbm, technology, operator)
		 VALUES (?, ?, ?, ?, ?)`,
		timestamp, bars, dbm, technology, operator)
	return err
}

// GetLatestCellularSignal returns the most recent cellular signal reading.
func (db *DB) GetLatestCellularSignal() (*CellularSignalPoint, error) {
	cutoff := time.Now().Unix() - 600 // 10 minutes
	row := db.QueryRow(
		`SELECT id, timestamp, bars, dbm, technology, operator
		 FROM cellular_signal_history WHERE timestamp >= ?
		 ORDER BY timestamp DESC LIMIT 1`, cutoff)
	var p CellularSignalPoint
	err := row.Scan(&p.ID, &p.Timestamp, &p.Bars, &p.DBm, &p.Technology, &p.Operator)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// NeighborRecord represents a persisted neighbor info record.
type NeighborRecord struct {
	ID                int64   `json:"id"`
	NodeID            uint32  `json:"node_id"`
	NeighborNodeID    uint32  `json:"neighbor_node_id"`
	SNR               float32 `json:"snr"`
	LastRxTime        int64   `json:"last_rx_time"`
	BroadcastInterval int     `json:"broadcast_interval"`
	CreatedAt         string  `json:"created_at"`
}

// InsertNeighborInfo persists neighbor info from a NeighborInfo packet.
func (db *DB) InsertNeighborInfo(nodeID, neighborNodeID uint32, snr float32, lastRxTime uint32, broadcastInterval uint32) error {
	_, err := db.Exec(`INSERT INTO neighbor_info (node_id, neighbor_node_id, snr, last_rx_time, broadcast_interval)
		VALUES (?, ?, ?, ?, ?)`, nodeID, neighborNodeID, snr, lastRxTime, broadcastInterval)
	return err
}

// GetNeighborInfo returns the latest neighbor info for a node.
func (db *DB) GetNeighborInfo(nodeID uint32, limit int) ([]NeighborRecord, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	query := `SELECT id, node_id, neighbor_node_id, snr, last_rx_time, broadcast_interval, created_at
		FROM neighbor_info`
	var rows *sql.Rows
	var err error
	if nodeID > 0 {
		rows, err = db.Query(query+` WHERE node_id = ? ORDER BY created_at DESC LIMIT ?`, nodeID, limit)
	} else {
		rows, err = db.Query(query+` ORDER BY created_at DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []NeighborRecord
	for rows.Next() {
		var r NeighborRecord
		if err := rows.Scan(&r.ID, &r.NodeID, &r.NeighborNodeID, &r.SNR, &r.LastRxTime, &r.BroadcastInterval, &r.CreatedAt); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, nil
}

// RangeTestRecord represents a persisted range test result.
type RangeTestRecord struct {
	ID        int64   `json:"id"`
	FromNode  string  `json:"from_node"`
	ToNode    string  `json:"to_node"`
	Text      string  `json:"text"`
	RxSNR     float32 `json:"rx_snr"`
	RxRSSI    int     `json:"rx_rssi"`
	HopLimit  int     `json:"hop_limit"`
	HopStart  int     `json:"hop_start"`
	Direction string  `json:"direction"`
	CreatedAt string  `json:"created_at"`
}

// InsertRangeTest persists a range test result.
func (db *DB) InsertRangeTest(fromNode, toNode, text string, rxSNR float32, rxRSSI, hopLimit, hopStart int, direction string) error {
	_, err := db.Exec(`INSERT INTO range_tests (from_node, to_node, text, rx_snr, rx_rssi, hop_limit, hop_start, direction)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, fromNode, toNode, text, rxSNR, rxRSSI, hopLimit, hopStart, direction)
	return err
}

// GetRangeTests returns range test history.
func (db *DB) GetRangeTests(limit int) ([]RangeTestRecord, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := db.Query(`SELECT id, from_node, to_node, text, rx_snr, rx_rssi, hop_limit, hop_start, direction, created_at
		FROM range_tests ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []RangeTestRecord
	for rows.Next() {
		var r RangeTestRecord
		if err := rows.Scan(&r.ID, &r.FromNode, &r.ToNode, &r.Text, &r.RxSNR, &r.RxRSSI, &r.HopLimit, &r.HopStart, &r.Direction, &r.CreatedAt); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, nil
}

// GetCellularSignalHistory returns cellular signal history within a time range.
func (db *DB) GetCellularSignalHistory(from, to int64, limit int) ([]CellularSignalPoint, error) {
	if limit <= 0 || limit > 10000 {
		limit = 500
	}
	rows, err := db.Query(
		`SELECT id, timestamp, bars, dbm, technology, operator
		 FROM cellular_signal_history WHERE timestamp >= ? AND timestamp <= ?
		 ORDER BY timestamp DESC LIMIT ?`, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []CellularSignalPoint
	for rows.Next() {
		var p CellularSignalPoint
		if err := rows.Scan(&p.ID, &p.Timestamp, &p.Bars, &p.DBm, &p.Technology, &p.Operator); err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	return points, nil
}

// ── SMS Contacts ──

// SMSContact represents a contact in the address book.
type SMSContact struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Phone     string    `json:"phone"`
	Notes     string    `json:"notes"`
	AutoFwd   bool      `json:"auto_fwd"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// GetSMSContacts returns all contacts.
func (db *DB) GetSMSContacts() ([]SMSContact, error) {
	rows, err := db.Query("SELECT id, name, phone, notes, auto_fwd, created_at, updated_at FROM sms_contacts ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var contacts []SMSContact
	for rows.Next() {
		var c SMSContact
		if err := rows.Scan(&c.ID, &c.Name, &c.Phone, &c.Notes, &c.AutoFwd, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		contacts = append(contacts, c)
	}
	return contacts, nil
}

// CreateSMSContact creates a new SMS contact.
func (db *DB) CreateSMSContact(name, phone, notes string, autoFwd bool) (int64, error) {
	res, err := db.Exec("INSERT INTO sms_contacts (name, phone, notes, auto_fwd) VALUES (?, ?, ?, ?)", name, phone, notes, autoFwd)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateSMSContact updates an existing contact.
func (db *DB) UpdateSMSContact(id int64, name, phone, notes string, autoFwd bool) error {
	_, err := db.Exec("UPDATE sms_contacts SET name=?, phone=?, notes=?, auto_fwd=?, updated_at=CURRENT_TIMESTAMP WHERE id=?", name, phone, notes, autoFwd, id)
	return err
}

// DeleteSMSContact removes a contact.
func (db *DB) DeleteSMSContact(id int64) error {
	_, err := db.Exec("DELETE FROM sms_contacts WHERE id=?", id)
	return err
}

// ── Webhook Log ──

// WebhookLogEntry represents a webhook activity record.
type WebhookLogEntry struct {
	ID        int64     `json:"id"`
	Direction string    `json:"direction"`
	URL       string    `json:"url"`
	Method    string    `json:"method"`
	Status    int       `json:"status"`
	Payload   string    `json:"payload"`
	Response  string    `json:"response"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// InsertWebhookLog records a webhook event.
func (db *DB) InsertWebhookLog(direction, url, method string, status int, payload, response, errMsg string) error {
	_, err := db.Exec("INSERT INTO webhook_log (direction, url, method, status, payload, response, error) VALUES (?, ?, ?, ?, ?, ?, ?)",
		direction, url, method, status, payload, response, errMsg)
	return err
}

// ============================================================================
// SMS Message History
// ============================================================================

// SMSMessageRecord represents a stored SMS message.
type SMSMessageRecord struct {
	ID        int64  `json:"id"`
	Direction string `json:"direction"` // rx or tx
	Phone     string `json:"phone"`
	Text      string `json:"text"`
	Status    string `json:"status"` // delivered, sent, failed
	Error     string `json:"error,omitempty"`
	Timestamp int64  `json:"timestamp"`
	CreatedAt string `json:"created_at"`
}

// InsertSMSMessage persists an SMS message (sent or received).
func (db *DB) InsertSMSMessage(direction, phone, text, status string, timestamp int64) (int64, error) {
	result, err := db.Exec(
		`INSERT INTO sms_messages (direction, phone, text, status, timestamp) VALUES (?, ?, ?, ?, ?)`,
		direction, phone, text, status, timestamp)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// IsDuplicateSMS checks if an identical SMS (same direction=rx, phone, text) was
// inserted within the last windowSec seconds. Used to suppress modem re-sends.
func (db *DB) IsDuplicateSMS(phone, text string, windowSec int) (bool, error) {
	cutoff := time.Now().Unix() - int64(windowSec)
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM sms_messages WHERE direction='rx' AND phone=? AND text=? AND timestamp>?`,
		phone, text, cutoff).Scan(&count)
	return count > 0, err
}

// GetSMSMessages returns recent SMS messages.
func (db *DB) GetSMSMessages(limit, offset int) ([]SMSMessageRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := db.Query(
		`SELECT id, direction, phone, text, status, error, timestamp, created_at
		 FROM sms_messages ORDER BY timestamp DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var msgs []SMSMessageRecord
	for rows.Next() {
		var m SMSMessageRecord
		if err := rows.Scan(&m.ID, &m.Direction, &m.Phone, &m.Text, &m.Status, &m.Error, &m.Timestamp, &m.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}

// ============================================================================
// Cell Broadcast Alerts
// ============================================================================

// CellBroadcast represents a cell broadcast alert (EU-Alert, WEA, CMAS).
type CellBroadcast struct {
	ID           int64  `json:"id"`
	SerialNumber int    `json:"serial_number"`
	MessageID    int    `json:"message_id"`
	Channel      int    `json:"channel"`
	Severity     string `json:"severity"`
	Text         string `json:"text"`
	Acknowledged bool   `json:"acknowledged"`
	Timestamp    int64  `json:"timestamp"`
	CreatedAt    string `json:"created_at"`
}

// InsertCellBroadcast persists a cell broadcast alert.
func (db *DB) InsertCellBroadcast(serialNumber, messageID, channel int, severity, text string, timestamp int64) (int64, error) {
	result, err := db.Exec(
		`INSERT INTO cell_broadcasts (serial_number, message_id, channel, severity, text, timestamp) VALUES (?, ?, ?, ?, ?, ?)`,
		serialNumber, messageID, channel, severity, text, timestamp)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetCellBroadcasts returns recent cell broadcast alerts.
func (db *DB) GetCellBroadcasts(limit int, unackedOnly bool) ([]CellBroadcast, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT id, serial_number, message_id, channel, severity, text, acknowledged, timestamp, created_at
		FROM cell_broadcasts`
	if unackedOnly {
		query += ` WHERE acknowledged = 0`
	}
	query += ` ORDER BY timestamp DESC LIMIT ?`
	rows, err := db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var alerts []CellBroadcast
	for rows.Next() {
		var a CellBroadcast
		var acked int
		if err := rows.Scan(&a.ID, &a.SerialNumber, &a.MessageID, &a.Channel, &a.Severity, &a.Text, &acked, &a.Timestamp, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.Acknowledged = acked != 0
		alerts = append(alerts, a)
	}
	return alerts, nil
}

// AckCellBroadcast marks a cell broadcast alert as acknowledged.
func (db *DB) AckCellBroadcast(id int64) error {
	_, err := db.Exec(`UPDATE cell_broadcasts SET acknowledged = 1 WHERE id = ?`, id)
	return err
}

// ============================================================================
// Cell Tower Info
// ============================================================================

// CellInfoRecord represents a cell tower info reading.
type CellInfoRecord struct {
	ID          int64  `json:"id"`
	MCC         string `json:"mcc"`
	MNC         string `json:"mnc"`
	LAC         string `json:"lac"`
	CellID      string `json:"cell_id"`
	NetworkType string `json:"network_type"`
	RSRP        *int   `json:"rsrp,omitempty"`
	RSRQ        *int   `json:"rsrq,omitempty"`
	Timestamp   int64  `json:"timestamp"`
	CreatedAt   string `json:"created_at"`
}

// InsertCellInfo persists a cell tower info reading.
func (db *DB) InsertCellInfo(mcc, mnc, lac, cellID, networkType string, rsrp, rsrq *int, timestamp int64) error {
	_, err := db.Exec(
		`INSERT INTO cell_info (mcc, mnc, lac, cell_id, network_type, rsrp, rsrq, timestamp) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		mcc, mnc, lac, cellID, networkType, rsrp, rsrq, timestamp)
	return err
}

// GetLatestCellInfo returns the most recent cell tower info.
func (db *DB) GetLatestCellInfo() (*CellInfoRecord, error) {
	var r CellInfoRecord
	err := db.QueryRow(
		`SELECT id, mcc, mnc, lac, cell_id, network_type, rsrp, rsrq, timestamp, created_at
		 FROM cell_info ORDER BY timestamp DESC LIMIT 1`).Scan(
		&r.ID, &r.MCC, &r.MNC, &r.LAC, &r.CellID, &r.NetworkType, &r.RSRP, &r.RSRQ, &r.Timestamp, &r.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// GetWebhookLog returns recent webhook activity.
func (db *DB) GetWebhookLog(limit int) ([]WebhookLogEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.Query("SELECT id, direction, url, method, status, payload, response, error, created_at FROM webhook_log ORDER BY created_at DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []WebhookLogEntry
	for rows.Next() {
		var e WebhookLogEntry
		if err := rows.Scan(&e.ID, &e.Direction, &e.URL, &e.Method, &e.Status, &e.Payload, &e.Response, &e.Error, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// ═══════════════════════════════════════════════════════════════════════════════
// SIM Card Management
// ═══════════════════════════════════════════════════════════════════════════════

// SIMCard represents a saved SIM card with its settings.
type SIMCard struct {
	ID        int64      `json:"id"`
	ICCID     string     `json:"iccid"`
	Label     string     `json:"label"`
	Phone     string     `json:"phone"`
	PIN       string     `json:"pin,omitempty"`
	Notes     string     `json:"notes"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// GetSIMCards returns all saved SIM cards.
func (db *DB) GetSIMCards() ([]SIMCard, error) {
	rows, err := db.Query("SELECT id, iccid, label, phone, pin, notes, last_seen, created_at, updated_at FROM sim_cards ORDER BY label")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cards []SIMCard
	for rows.Next() {
		var c SIMCard
		if err := rows.Scan(&c.ID, &c.ICCID, &c.Label, &c.Phone, &c.PIN, &c.Notes, &c.LastSeen, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		cards = append(cards, c)
	}
	return cards, nil
}

// GetSIMCardByICCID looks up a SIM card by its ICCID.
func (db *DB) GetSIMCardByICCID(iccid string) (*SIMCard, error) {
	var c SIMCard
	err := db.QueryRow("SELECT id, iccid, label, phone, pin, notes, last_seen, created_at, updated_at FROM sim_cards WHERE iccid=?", iccid).
		Scan(&c.ID, &c.ICCID, &c.Label, &c.Phone, &c.PIN, &c.Notes, &c.LastSeen, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// CreateSIMCard creates a new SIM card entry.
func (db *DB) CreateSIMCard(iccid, label, phone, pin, notes string) (int64, error) {
	res, err := db.Exec("INSERT INTO sim_cards (iccid, label, phone, pin, notes) VALUES (?, ?, ?, ?, ?)", iccid, label, phone, pin, notes)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateSIMCard updates an existing SIM card.
func (db *DB) UpdateSIMCard(id int64, label, phone, pin, notes string) error {
	_, err := db.Exec("UPDATE sim_cards SET label=?, phone=?, pin=?, notes=?, updated_at=CURRENT_TIMESTAMP WHERE id=?", label, phone, pin, notes, id)
	return err
}

// DeleteSIMCard removes a SIM card.
func (db *DB) DeleteSIMCard(id int64) error {
	_, err := db.Exec("DELETE FROM sim_cards WHERE id=?", id)
	return err
}

// TouchSIMCardLastSeen updates the last_seen timestamp for a SIM card.
func (db *DB) TouchSIMCardLastSeen(iccid string) error {
	_, err := db.Exec("UPDATE sim_cards SET last_seen=CURRENT_TIMESTAMP WHERE iccid=?", iccid)
	return err
}

// ---- Key Bundles (cross-platform key exchange) ----

// KeyBundleRow represents a stored key bundle entry.
type KeyBundleRow struct {
	ID           int64  `db:"id"`
	ChannelType  string `db:"channel_type"`
	Address      string `db:"address"`
	EncryptedKey []byte `db:"encrypted_key"`
	KeyVersion   int    `db:"key_version"`
	Status       string `db:"status"`
	ExpiresAt    string `db:"expires_at"`
	CreatedAt    string `db:"created_at"`
}

// InsertKeyBundle stores a wrapped key.
func (db *DB) InsertKeyBundle(channelType, address string, encryptedKey []byte, version int) error {
	_, err := db.Exec(
		`INSERT INTO key_bundles (channel_type, address, encrypted_key, key_version) VALUES (?, ?, ?, ?)`,
		channelType, address, encryptedKey, version)
	return err
}

// GetActiveKeyBundle returns the active key for a channel+address.
func (db *DB) GetActiveKeyBundle(channelType, address string) (*KeyBundleRow, error) {
	var row KeyBundleRow
	err := db.Get(&row,
		`SELECT * FROM key_bundles WHERE channel_type = ? AND address = ? AND status = 'active'
		 ORDER BY key_version DESC LIMIT 1`, channelType, address)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// GetLatestKeyVersion returns the highest key version for a channel+address.
func (db *DB) GetLatestKeyVersion(channelType, address string) (int, error) {
	var version int
	err := db.QueryRow(
		`SELECT COALESCE(MAX(key_version), 0) FROM key_bundles WHERE channel_type = ? AND address = ?`,
		channelType, address).Scan(&version)
	return version, err
}

// RetireKeyBundle retires the active key for a channel+address with an expiry time.
func (db *DB) RetireKeyBundle(channelType, address string, expiresAt time.Time) error {
	_, err := db.Exec(
		`UPDATE key_bundles SET status = 'retired', expires_at = ? WHERE channel_type = ? AND address = ? AND status = 'active'`,
		expiresAt.Format("2006-01-02 15:04:05"), channelType, address)
	return err
}

// MaxKeyVersion returns the highest version number for a channel+address across all statuses. [MESHSAT-447]
func (db *DB) MaxKeyVersion(channelType, address string) int {
	var v int
	_ = db.Get(&v, `SELECT COALESCE(MAX(key_version), 0) FROM key_bundles WHERE channel_type = ? AND address = ?`,
		channelType, address)
	return v
}

// RevokeKeyBundle revokes all keys for a channel+address.
func (db *DB) RevokeKeyBundle(channelType, address string) error {
	_, err := db.Exec(
		`UPDATE key_bundles SET status = 'revoked' WHERE channel_type = ? AND address = ?`,
		channelType, address)
	return err
}

// ListKeyBundles returns all key bundles (for admin listing).
func (db *DB) ListKeyBundles() ([]KeyBundleRow, error) {
	var rows []KeyBundleRow
	err := db.Select(&rows, `SELECT * FROM key_bundles ORDER BY channel_type, address, key_version DESC`)
	return rows, err
}

// KeyBundleStats returns counts by status.
func (db *DB) KeyBundleStats() (active, retired, revoked int, err error) {
	err = db.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN status='active' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status='retired' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status='revoked' THEN 1 ELSE 0 END), 0)
		FROM key_bundles`).Scan(&active, &retired, &revoked)
	return
}

// ---- Received Resources (Reticulum resource transfer) ----

// ReceivedResource represents a file received via Reticulum resource transfer.
type ReceivedResource struct {
	ID          int64  `db:"id" json:"id"`
	Hash        string `db:"hash" json:"hash"`
	Filename    string `db:"filename" json:"filename"`
	ContentType string `db:"content_type" json:"content_type"`
	Size        int    `db:"size" json:"size"`
	SourceIface string `db:"source_iface" json:"source_iface"`
	CreatedAt   string `db:"created_at" json:"created_at"`
}

// InsertReceivedResource stores a resource received via Reticulum.
func (db *DB) InsertReceivedResource(hash, filename, contentType, sourceIface string, data []byte) (int64, error) {
	res, err := db.Exec(
		`INSERT OR REPLACE INTO received_resources (hash, filename, content_type, size, data, source_iface)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		hash, filename, contentType, len(data), data, sourceIface)
	if err != nil {
		return 0, fmt.Errorf("insert received resource: %w", err)
	}
	return res.LastInsertId()
}

// GetReceivedResources lists all received resources (without data blobs).
func (db *DB) GetReceivedResources(limit int) ([]ReceivedResource, error) {
	if limit <= 0 {
		limit = 50
	}
	var resources []ReceivedResource
	err := db.Select(&resources,
		`SELECT id, hash, filename, content_type, size, source_iface, created_at
		 FROM received_resources ORDER BY id DESC LIMIT ?`, limit)
	return resources, err
}

// GetReceivedResourceData retrieves the binary data for a received resource.
func (db *DB) GetReceivedResourceData(hash string) ([]byte, error) {
	var data []byte
	err := db.QueryRow("SELECT data FROM received_resources WHERE hash = ?", hash).Scan(&data)
	if err != nil {
		return nil, fmt.Errorf("resource not found: %w", err)
	}
	return data, nil
}

// DeleteReceivedResource removes a received resource.
func (db *DB) DeleteReceivedResource(hash string) error {
	res, err := db.Exec("DELETE FROM received_resources WHERE hash = ?", hash)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("resource not found")
	}
	return nil
}

// ---- Credential Cache (cert/credential management) ----

// CredentialCacheRow represents a cached credential entry.
type CredentialCacheRow struct {
	ID              string `db:"id" json:"id"`
	Provider        string `db:"provider" json:"provider"`
	Name            string `db:"name" json:"name"`
	CredType        string `db:"cred_type" json:"cred_type"`
	EncryptedData   []byte `db:"encrypted_data" json:"-"`
	CertNotAfter    string `db:"cert_not_after" json:"cert_not_after,omitempty"`
	CertSubject     string `db:"cert_subject" json:"cert_subject,omitempty"`
	CertFingerprint string `db:"cert_fingerprint" json:"cert_fingerprint,omitempty"`
	Version         int    `db:"version" json:"version"`
	Source          string `db:"source" json:"source"`
	Applied         int    `db:"applied" json:"applied"`
	ReceivedAt      string `db:"received_at" json:"received_at"`
	UpdatedAt       string `db:"updated_at" json:"updated_at"`
}

// InsertCredentialCache stores or replaces a credential in the cache.
func (db *DB) InsertCredentialCache(row *CredentialCacheRow) error {
	_, err := db.Exec(
		`INSERT OR REPLACE INTO credential_cache
		 (id, provider, name, cred_type, encrypted_data, cert_not_after, cert_subject, cert_fingerprint, version, source, applied, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		row.ID, row.Provider, row.Name, row.CredType, row.EncryptedData,
		row.CertNotAfter, row.CertSubject, row.CertFingerprint,
		row.Version, row.Source, row.Applied)
	return err
}

// GetCredentialCache returns a credential by ID.
func (db *DB) GetCredentialCache(id string) (*CredentialCacheRow, error) {
	var row CredentialCacheRow
	err := db.Get(&row, "SELECT * FROM credential_cache WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListCredentialCache returns all cached credentials (metadata only, no encrypted_data).
func (db *DB) ListCredentialCache() ([]CredentialCacheRow, error) {
	var rows []CredentialCacheRow
	err := db.Select(&rows,
		`SELECT id, provider, name, cred_type, cert_not_after, cert_subject, cert_fingerprint,
		 version, source, applied, received_at, updated_at
		 FROM credential_cache ORDER BY provider, name`)
	return rows, err
}

// GetCredentialsByProvider returns credentials for a specific provider.
func (db *DB) GetCredentialsByProvider(provider string) ([]CredentialCacheRow, error) {
	var rows []CredentialCacheRow
	err := db.Select(&rows, "SELECT * FROM credential_cache WHERE provider = ? ORDER BY version DESC", provider)
	return rows, err
}

// DeleteCredentialCache removes a credential from the cache.
func (db *DB) DeleteCredentialCache(id string) error {
	res, err := db.Exec("DELETE FROM credential_cache WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("credential not found")
	}
	return nil
}

// SetCredentialApplied marks a credential as applied (in use by a gateway).
func (db *DB) SetCredentialApplied(id string, applied bool) error {
	v := 0
	if applied {
		v = 1
	}
	_, err := db.Exec("UPDATE credential_cache SET applied = ?, updated_at = datetime('now') WHERE id = ?", v, id)
	return err
}

// ListExpiringCredentials returns credentials with cert_not_after within the given days.
func (db *DB) ListExpiringCredentials(withinDays int) ([]CredentialCacheRow, error) {
	var rows []CredentialCacheRow
	cutoff := time.Now().AddDate(0, 0, withinDays).Format("2006-01-02 15:04:05")
	err := db.Select(&rows,
		`SELECT id, provider, name, cred_type, cert_not_after, cert_subject, cert_fingerprint,
		 version, source, applied, received_at, updated_at
		 FROM credential_cache
		 WHERE cert_not_after != '' AND cert_not_after <= ?
		 ORDER BY cert_not_after ASC`, cutoff)
	return rows, err
}
