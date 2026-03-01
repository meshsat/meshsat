package database

import (
	"fmt"
	"strings"
	"time"
)

// Message represents a persisted mesh message.
type Message struct {
	ID          int64     `db:"id" json:"id"`
	PacketID    uint32    `db:"packet_id" json:"packet_id"`
	FromNode    string    `db:"from_node" json:"from_node"`
	ToNode      string    `db:"to_node" json:"to_node"`
	Channel     int       `db:"channel" json:"channel"`
	PortNum     int       `db:"portnum" json:"portnum"`
	PortNumName string    `db:"portnum_name" json:"portnum_name"`
	DecodedText string    `db:"decoded_text" json:"decoded_text"`
	RxSNR       float32   `db:"rx_snr" json:"rx_snr"`
	RxTime      int64     `db:"rx_time" json:"rx_time"`
	HopLimit    int       `db:"hop_limit" json:"hop_limit"`
	HopStart    int       `db:"hop_start" json:"hop_start"`
	Direction   string    `db:"direction" json:"direction"`
	Transport   string    `db:"transport" json:"transport"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
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
	ByTransport map[string]int `json:"by_transport"`
	ByPortNum   map[string]int `json:"by_portnum"`
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
	Status     string    `db:"status" json:"status"` // pending, sent, expired
	LastError  string    `db:"last_error" json:"last_error"`
	CreatedAt  time.Time `db:"created_at" json:"created_at"`
	UpdatedAt  time.Time `db:"updated_at" json:"updated_at"`
}

// sqliteTime formats a time for SQLite DATETIME comparison (matches CURRENT_TIMESTAMP format).
const sqliteTimeFormat = "2006-01-02 15:04:05"

// InsertDeadLetter adds a failed send to the dead-letter queue.
func (db *DB) InsertDeadLetter(packetID uint32, payload []byte, maxRetries int, nextRetry time.Time, lastError string) error {
	_, err := db.Exec(`INSERT INTO dead_letters (packet_id, payload, max_retries, next_retry, last_error)
		VALUES (?, ?, ?, ?, ?)`,
		packetID, payload, maxRetries, nextRetry.UTC().Format(sqliteTimeFormat), lastError)
	return err
}

// GetPendingDeadLetters returns dead letters ready for retry.
func (db *DB) GetPendingDeadLetters(limit int) ([]DeadLetter, error) {
	if limit <= 0 {
		limit = 10
	}
	var dls []DeadLetter
	err := db.Select(&dls,
		`SELECT * FROM dead_letters WHERE status = 'pending' AND next_retry <= CURRENT_TIMESTAMP
		 ORDER BY next_retry ASC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query dead letters: %w", err)
	}
	return dls, nil
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
