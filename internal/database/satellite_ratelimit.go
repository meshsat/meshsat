package database

import (
	"fmt"
	"time"
)

// SatelliteRateLimit holds per-device rate limit configuration.
type SatelliteRateLimit struct {
	DeviceID           int64   `json:"device_id"`
	DailyLimit         int     `json:"daily_limit"`
	MonthlyLimit       int     `json:"monthly_limit"`
	BurstSize          int     `json:"burst_size"`
	RefillRate         float64 `json:"refill_rate"`
	Enabled            bool    `json:"enabled"`
	OverrideUntil      *string `json:"override_until,omitempty"`
	UpdatedAt          string  `json:"updated_at"`
	DailyCreditLimit   int     `json:"daily_credit_limit"`
	MonthlyCreditLimit int     `json:"monthly_credit_limit"`
}

// SatelliteUsage holds daily usage counters for a device.
type SatelliteUsage struct {
	ID       int64  `json:"id"`
	DeviceID int64  `json:"device_id"`
	Day      string `json:"day"`
	Sends    int    `json:"sends"`
	Credits  int    `json:"credits"`
}

// GetSatelliteRateLimit returns the rate limit config for a device.
func (db *DB) GetSatelliteRateLimit(deviceID int64) (*SatelliteRateLimit, error) {
	var rl SatelliteRateLimit
	var enabled int
	err := db.QueryRow(
		"SELECT device_id, daily_limit, monthly_limit, burst_size, refill_rate, enabled, override_until, updated_at, daily_credit_limit, monthly_credit_limit FROM satellite_rate_limits WHERE device_id=?",
		deviceID,
	).Scan(&rl.DeviceID, &rl.DailyLimit, &rl.MonthlyLimit, &rl.BurstSize, &rl.RefillRate, &enabled, &rl.OverrideUntil, &rl.UpdatedAt, &rl.DailyCreditLimit, &rl.MonthlyCreditLimit)
	if err != nil {
		return nil, err
	}
	rl.Enabled = enabled != 0
	return &rl, nil
}

// GetAllSatelliteRateLimits returns rate limit configs for all devices.
func (db *DB) GetAllSatelliteRateLimits() ([]SatelliteRateLimit, error) {
	rows, err := db.Query("SELECT device_id, daily_limit, monthly_limit, burst_size, refill_rate, enabled, override_until, updated_at, daily_credit_limit, monthly_credit_limit FROM satellite_rate_limits ORDER BY device_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var limits []SatelliteRateLimit
	for rows.Next() {
		var rl SatelliteRateLimit
		var enabled int
		if err := rows.Scan(&rl.DeviceID, &rl.DailyLimit, &rl.MonthlyLimit, &rl.BurstSize, &rl.RefillRate, &enabled, &rl.OverrideUntil, &rl.UpdatedAt, &rl.DailyCreditLimit, &rl.MonthlyCreditLimit); err != nil {
			return nil, err
		}
		rl.Enabled = enabled != 0
		limits = append(limits, rl)
	}
	return limits, nil
}

// UpsertSatelliteRateLimit creates or updates a device's rate limit config.
func (db *DB) UpsertSatelliteRateLimit(rl SatelliteRateLimit) error {
	enabled := 0
	if rl.Enabled {
		enabled = 1
	}
	_, err := db.Exec(
		`INSERT INTO satellite_rate_limits (device_id, daily_limit, monthly_limit, burst_size, refill_rate, enabled, daily_credit_limit, monthly_credit_limit, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))
		 ON CONFLICT(device_id) DO UPDATE SET
		   daily_limit=excluded.daily_limit, monthly_limit=excluded.monthly_limit,
		   burst_size=excluded.burst_size, refill_rate=excluded.refill_rate,
		   enabled=excluded.enabled, daily_credit_limit=excluded.daily_credit_limit,
		   monthly_credit_limit=excluded.monthly_credit_limit, updated_at=datetime('now')`,
		rl.DeviceID, rl.DailyLimit, rl.MonthlyLimit, rl.BurstSize, rl.RefillRate, enabled,
		rl.DailyCreditLimit, rl.MonthlyCreditLimit,
	)
	return err
}

// DeleteSatelliteRateLimit removes a device's rate limit config.
func (db *DB) DeleteSatelliteRateLimit(deviceID int64) error {
	_, err := db.Exec("DELETE FROM satellite_rate_limits WHERE device_id=?", deviceID)
	return err
}

// SetSatelliteRateLimitOverride sets a temporary override that bypasses rate limiting until the given time.
func (db *DB) SetSatelliteRateLimitOverride(deviceID int64, until time.Time) error {
	ts := until.UTC().Format("2006-01-02 15:04:05")
	_, err := db.Exec("UPDATE satellite_rate_limits SET override_until=?, updated_at=datetime('now') WHERE device_id=?", ts, deviceID)
	return err
}

// ClearSatelliteRateLimitOverride removes any active override for a device.
func (db *DB) ClearSatelliteRateLimitOverride(deviceID int64) error {
	_, err := db.Exec("UPDATE satellite_rate_limits SET override_until=NULL, updated_at=datetime('now') WHERE device_id=?", deviceID)
	return err
}

// IncrementSatelliteUsage atomically increments the daily send/credit counters for a device.
func (db *DB) IncrementSatelliteUsage(deviceID int64, sends, credits int) error {
	day := time.Now().UTC().Format("2006-01-02")
	_, err := db.Exec(
		`INSERT INTO satellite_usage (device_id, day, sends, credits)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(device_id, day) DO UPDATE SET
		   sends=satellite_usage.sends + excluded.sends,
		   credits=satellite_usage.credits + excluded.credits`,
		deviceID, day, sends, credits,
	)
	return err
}

// GetSatelliteUsageToday returns today's usage for a device.
func (db *DB) GetSatelliteUsageToday(deviceID int64) (sends, credits int, err error) {
	day := time.Now().UTC().Format("2006-01-02")
	err = db.QueryRow("SELECT COALESCE(sends,0), COALESCE(credits,0) FROM satellite_usage WHERE device_id=? AND day=?", deviceID, day).Scan(&sends, &credits)
	if err != nil {
		return 0, 0, nil // no usage today
	}
	return sends, credits, nil
}

// GetSatelliteUsageMonth returns the total sends/credits for a device in the current month.
func (db *DB) GetSatelliteUsageMonth(deviceID int64) (sends, credits int, err error) {
	month := time.Now().UTC().Format("2006-01")
	err = db.QueryRow(
		"SELECT COALESCE(SUM(sends),0), COALESCE(SUM(credits),0) FROM satellite_usage WHERE device_id=? AND day LIKE ?",
		deviceID, month+"%",
	).Scan(&sends, &credits)
	if err != nil {
		return 0, 0, nil
	}
	return sends, credits, nil
}

// GetSatelliteUsageHistory returns daily usage rows for a device within a date range.
func (db *DB) GetSatelliteUsageHistory(deviceID int64, from, to string) ([]SatelliteUsage, error) {
	rows, err := db.Query(
		"SELECT id, device_id, day, sends, credits FROM satellite_usage WHERE device_id=? AND day>=? AND day<=? ORDER BY day DESC",
		deviceID, from, to,
	)
	if err != nil {
		return nil, fmt.Errorf("query satellite usage: %w", err)
	}
	defer rows.Close()
	var usage []SatelliteUsage
	for rows.Next() {
		var u SatelliteUsage
		if err := rows.Scan(&u.ID, &u.DeviceID, &u.Day, &u.Sends, &u.Credits); err != nil {
			return nil, err
		}
		usage = append(usage, u)
	}
	return usage, nil
}

// ResetSatelliteUsage clears all usage counters for a device (admin override).
func (db *DB) ResetSatelliteUsage(deviceID int64) error {
	_, err := db.Exec("DELETE FROM satellite_usage WHERE device_id=?", deviceID)
	return err
}
