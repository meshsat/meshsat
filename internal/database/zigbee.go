package database

import (
	"database/sql"
	"fmt"
)

// ZigBeeDevice is the persisted record for a paired zigbee device.
// IEEE address is the canonical key — short addresses can change after a
// rejoin. Sensor "last_*" columns are updated in place on every report so
// the device list endpoint can render current readings without joining the
// time-series table; full history lives in zigbee_sensor_readings.
type ZigBeeDevice struct {
	IEEEAddr     string   `json:"ieee_addr"`
	ShortAddr    int      `json:"short_addr"`
	Alias        string   `json:"alias"`
	Manufacturer string   `json:"manufacturer"`
	Model        string   `json:"model"`
	DeviceType   string   `json:"device_type"`
	Endpoint     int      `json:"endpoint"`
	FirstSeen    string   `json:"first_seen"`
	LastSeen     string   `json:"last_seen"`
	LQI          int      `json:"lqi"`
	BatteryPct   int      `json:"battery_pct"`
	LastTemp     *float64 `json:"last_temp,omitempty"`
	LastHumidity *float64 `json:"last_humidity,omitempty"`
	LastOnOff    int      `json:"last_onoff"`
	MessageCount int      `json:"message_count"`
}

// ZigBeeSensorReading is one row in the time-series sensor history.
type ZigBeeSensorReading struct {
	ID        int64    `json:"id"`
	Timestamp string   `json:"ts"`
	IEEEAddr  string   `json:"ieee_addr"`
	Cluster   int      `json:"cluster"`
	Attribute int      `json:"attribute"`
	ValueNum  *float64 `json:"value_num,omitempty"`
	ValueText string   `json:"value_text,omitempty"`
	Unit      string   `json:"unit"`
	LQI       int      `json:"lqi"`
}

// ZigBeeDeviceRouting controls where sensor events from one device fan out.
// Defaults (to_tak=1, to_hub=1, to_log=1, to_mesh=0) match the typical
// MeshSat use case: forward to TAK for situational awareness, archive to
// the hub, log locally, but stay off the mesh broadcast channel.
type ZigBeeDeviceRouting struct {
	IEEEAddr    string `json:"ieee_addr"`
	ToTAK       bool   `json:"to_tak"`
	ToMesh      bool   `json:"to_mesh"`
	ToHub       bool   `json:"to_hub"`
	ToLog       bool   `json:"to_log"`
	CoTType     string `json:"cot_type"`
	MinInterval int    `json:"min_interval_sec"`
	UpdatedAt   string `json:"updated_at"`
}

// UpsertZigBeeDevice writes or updates a device record. Used on join,
// announce, and every sensor report so last_seen tracks the most recent
// activity. Preserves alias and *all* sensor "last_*" fields when not
// explicitly updated — callers can pass nil pointers for the float columns
// to leave them untouched.
func (db *DB) UpsertZigBeeDevice(d *ZigBeeDevice) error {
	_, err := db.Exec(`
		INSERT INTO zigbee_devices (
			ieee_addr, short_addr, alias, manufacturer, model, device_type,
			endpoint, first_seen, last_seen, lqi, battery_pct,
			last_temp, last_humidity, last_onoff, message_count
		) VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'), ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ieee_addr) DO UPDATE SET
			short_addr    = excluded.short_addr,
			-- alias is intentionally NOT overwritten (user-set),
			manufacturer  = CASE WHEN excluded.manufacturer != '' THEN excluded.manufacturer ELSE manufacturer END,
			model         = CASE WHEN excluded.model != ''        THEN excluded.model        ELSE model        END,
			device_type   = CASE WHEN excluded.device_type != ''  THEN excluded.device_type  ELSE device_type  END,
			endpoint      = CASE WHEN excluded.endpoint > 0       THEN excluded.endpoint     ELSE endpoint     END,
			last_seen     = excluded.last_seen,
			lqi           = excluded.lqi,
			battery_pct   = CASE WHEN excluded.battery_pct >= 0   THEN excluded.battery_pct  ELSE battery_pct  END,
			last_temp     = COALESCE(excluded.last_temp,     last_temp),
			last_humidity = COALESCE(excluded.last_humidity, last_humidity),
			last_onoff    = CASE WHEN excluded.last_onoff >= 0    THEN excluded.last_onoff   ELSE last_onoff   END,
			message_count = message_count + 1
	`, d.IEEEAddr, d.ShortAddr, d.Alias, d.Manufacturer, d.Model, d.DeviceType,
		d.Endpoint, d.LQI, d.BatteryPct,
		d.LastTemp, d.LastHumidity, d.LastOnOff, 1)
	if err != nil {
		return fmt.Errorf("upsert zigbee device %s: %w", d.IEEEAddr, err)
	}
	return nil
}

// SetZigBeeDeviceAlias updates only the user-given alias for a device.
func (db *DB) SetZigBeeDeviceAlias(ieeeAddr, alias string) error {
	res, err := db.Exec(`UPDATE zigbee_devices SET alias = ? WHERE ieee_addr = ?`, alias, ieeeAddr)
	if err != nil {
		return fmt.Errorf("set alias for %s: %w", ieeeAddr, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteZigBeeDevice removes a device + all its sensor readings + routing.
func (db *DB) DeleteZigBeeDevice(ieeeAddr string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM zigbee_sensor_readings WHERE ieee_addr = ?`, ieeeAddr); err != nil {
		return fmt.Errorf("delete readings: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM zigbee_device_routing WHERE ieee_addr = ?`, ieeeAddr); err != nil {
		return fmt.Errorf("delete routing: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM zigbee_devices WHERE ieee_addr = ?`, ieeeAddr); err != nil {
		return fmt.Errorf("delete device: %w", err)
	}
	return tx.Commit()
}

// GetZigBeeDevice returns a single device by IEEE address.
func (db *DB) GetZigBeeDevice(ieeeAddr string) (*ZigBeeDevice, error) {
	var d ZigBeeDevice
	err := db.QueryRow(`
		SELECT ieee_addr, short_addr, alias, manufacturer, model, device_type,
		       endpoint, first_seen, last_seen, lqi, battery_pct,
		       last_temp, last_humidity, last_onoff, message_count
		FROM zigbee_devices WHERE ieee_addr = ?`, ieeeAddr).Scan(
		&d.IEEEAddr, &d.ShortAddr, &d.Alias, &d.Manufacturer, &d.Model, &d.DeviceType,
		&d.Endpoint, &d.FirstSeen, &d.LastSeen, &d.LQI, &d.BatteryPct,
		&d.LastTemp, &d.LastHumidity, &d.LastOnOff, &d.MessageCount,
	)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// GetZigBeeDeviceByShort looks up a device by its current short address.
// Used by the transport on inbound messages where only the 16-bit address
// is known; falls back to nil if no IEEE binding exists yet.
func (db *DB) GetZigBeeDeviceByShort(shortAddr int) (*ZigBeeDevice, error) {
	var d ZigBeeDevice
	err := db.QueryRow(`
		SELECT ieee_addr, short_addr, alias, manufacturer, model, device_type,
		       endpoint, first_seen, last_seen, lqi, battery_pct,
		       last_temp, last_humidity, last_onoff, message_count
		FROM zigbee_devices WHERE short_addr = ?
		ORDER BY last_seen DESC LIMIT 1`, shortAddr).Scan(
		&d.IEEEAddr, &d.ShortAddr, &d.Alias, &d.Manufacturer, &d.Model, &d.DeviceType,
		&d.Endpoint, &d.FirstSeen, &d.LastSeen, &d.LQI, &d.BatteryPct,
		&d.LastTemp, &d.LastHumidity, &d.LastOnOff, &d.MessageCount,
	)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// ListZigBeeDevices returns all known devices, most recently active first.
func (db *DB) ListZigBeeDevices() ([]ZigBeeDevice, error) {
	rows, err := db.Query(`
		SELECT ieee_addr, short_addr, alias, manufacturer, model, device_type,
		       endpoint, first_seen, last_seen, lqi, battery_pct,
		       last_temp, last_humidity, last_onoff, message_count
		FROM zigbee_devices ORDER BY last_seen DESC`)
	if err != nil {
		return nil, fmt.Errorf("list zigbee devices: %w", err)
	}
	defer rows.Close()
	var out []ZigBeeDevice
	for rows.Next() {
		var d ZigBeeDevice
		if err := rows.Scan(&d.IEEEAddr, &d.ShortAddr, &d.Alias, &d.Manufacturer, &d.Model, &d.DeviceType,
			&d.Endpoint, &d.FirstSeen, &d.LastSeen, &d.LQI, &d.BatteryPct,
			&d.LastTemp, &d.LastHumidity, &d.LastOnOff, &d.MessageCount); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// InsertZigBeeSensorReading appends one row to the time-series table.
func (db *DB) InsertZigBeeSensorReading(r *ZigBeeSensorReading) error {
	_, err := db.Exec(`
		INSERT INTO zigbee_sensor_readings (ieee_addr, cluster, attribute, value_num, value_text, unit, lqi)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.IEEEAddr, r.Cluster, r.Attribute, r.ValueNum, r.ValueText, r.Unit, r.LQI)
	if err != nil {
		return fmt.Errorf("insert sensor reading: %w", err)
	}
	return nil
}

// GetZigBeeSensorHistory returns readings for one device over the last
// `hoursBack` hours, ordered oldest→newest so charts can plot in time order.
// `hoursBack=0` returns everything (no time filter).
func (db *DB) GetZigBeeSensorHistory(ieeeAddr string, hoursBack int) ([]ZigBeeSensorReading, error) {
	q := `SELECT id, ts, ieee_addr, cluster, attribute, value_num, value_text, unit, lqi
	      FROM zigbee_sensor_readings WHERE ieee_addr = ?`
	args := []interface{}{ieeeAddr}
	if hoursBack > 0 {
		q += ` AND ts >= datetime('now', ?)`
		args = append(args, fmt.Sprintf("-%d hours", hoursBack))
	}
	q += ` ORDER BY ts ASC LIMIT 5000`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query sensor history: %w", err)
	}
	defer rows.Close()
	var out []ZigBeeSensorReading
	for rows.Next() {
		var r ZigBeeSensorReading
		if err := rows.Scan(&r.ID, &r.Timestamp, &r.IEEEAddr, &r.Cluster, &r.Attribute,
			&r.ValueNum, &r.ValueText, &r.Unit, &r.LQI); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// PruneZigBeeSensorReadings keeps the last `keepDays` of history.
func (db *DB) PruneZigBeeSensorReadings(keepDays int) error {
	if keepDays <= 0 {
		return nil
	}
	_, err := db.Exec(`DELETE FROM zigbee_sensor_readings WHERE ts < datetime('now', ?)`,
		fmt.Sprintf("-%d days", keepDays))
	return err
}

// GetZigBeeRouting returns the routing config for one device, applying
// defaults (TAK + Hub + log on, mesh off) when no row exists yet.
func (db *DB) GetZigBeeRouting(ieeeAddr string) (*ZigBeeDeviceRouting, error) {
	var r ZigBeeDeviceRouting
	var toTak, toMesh, toHub, toLog int
	err := db.QueryRow(`
		SELECT ieee_addr, to_tak, to_mesh, to_hub, to_log, cot_type, min_interval, updated_at
		FROM zigbee_device_routing WHERE ieee_addr = ?`, ieeeAddr).Scan(
		&r.IEEEAddr, &toTak, &toMesh, &toHub, &toLog, &r.CoTType, &r.MinInterval, &r.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return &ZigBeeDeviceRouting{
			IEEEAddr: ieeeAddr,
			ToTAK:    true,
			ToMesh:   false,
			ToHub:    true,
			ToLog:    true,
			CoTType:  "",
		}, nil
	}
	if err != nil {
		return nil, err
	}
	r.ToTAK = toTak != 0
	r.ToMesh = toMesh != 0
	r.ToHub = toHub != 0
	r.ToLog = toLog != 0
	return &r, nil
}

// SetZigBeeRouting upserts the routing config for one device.
func (db *DB) SetZigBeeRouting(r *ZigBeeDeviceRouting) error {
	_, err := db.Exec(`
		INSERT INTO zigbee_device_routing (ieee_addr, to_tak, to_mesh, to_hub, to_log, cot_type, min_interval, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(ieee_addr) DO UPDATE SET
			to_tak       = excluded.to_tak,
			to_mesh      = excluded.to_mesh,
			to_hub       = excluded.to_hub,
			to_log       = excluded.to_log,
			cot_type     = excluded.cot_type,
			min_interval = excluded.min_interval,
			updated_at   = excluded.updated_at`,
		r.IEEEAddr, boolToInt(r.ToTAK), boolToInt(r.ToMesh), boolToInt(r.ToHub), boolToInt(r.ToLog),
		r.CoTType, r.MinInterval)
	if err != nil {
		return fmt.Errorf("set routing for %s: %w", r.IEEEAddr, err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
