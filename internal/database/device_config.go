package database

import "fmt"

// DeviceConfigVersion represents a single versioned config snapshot for a device.
type DeviceConfigVersion struct {
	ID        int64  `json:"id"`
	DeviceID  int64  `json:"device_id"`
	Version   int    `json:"version"`
	YAML      string `json:"yaml"`
	Comment   string `json:"comment"`
	CreatedAt string `json:"created_at"`
}

// GetDeviceConfigVersions returns all config versions for a device, newest first.
func (db *DB) GetDeviceConfigVersions(deviceID int64) ([]DeviceConfigVersion, error) {
	rows, err := db.Query(
		"SELECT id, device_id, version, yaml, comment, created_at FROM device_config_versions WHERE device_id=? ORDER BY version DESC",
		deviceID,
	)
	if err != nil {
		return nil, fmt.Errorf("query device config versions: %w", err)
	}
	defer rows.Close()
	var versions []DeviceConfigVersion
	for rows.Next() {
		var v DeviceConfigVersion
		if err := rows.Scan(&v.ID, &v.DeviceID, &v.Version, &v.YAML, &v.Comment, &v.CreatedAt); err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	return versions, nil
}

// GetDeviceConfigVersion returns a specific config version for a device.
func (db *DB) GetDeviceConfigVersion(deviceID int64, version int) (*DeviceConfigVersion, error) {
	var v DeviceConfigVersion
	err := db.QueryRow(
		"SELECT id, device_id, version, yaml, comment, created_at FROM device_config_versions WHERE device_id=? AND version=?",
		deviceID, version,
	).Scan(&v.ID, &v.DeviceID, &v.Version, &v.YAML, &v.Comment, &v.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// GetDeviceConfigLatest returns the latest config version for a device, or nil if none exist.
func (db *DB) GetDeviceConfigLatest(deviceID int64) (*DeviceConfigVersion, error) {
	var v DeviceConfigVersion
	err := db.QueryRow(
		"SELECT id, device_id, version, yaml, comment, created_at FROM device_config_versions WHERE device_id=? ORDER BY version DESC LIMIT 1",
		deviceID,
	).Scan(&v.ID, &v.DeviceID, &v.Version, &v.YAML, &v.Comment, &v.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// CreateDeviceConfigVersion inserts a new config version for a device.
// The version number is auto-incremented from the current max for this device.
func (db *DB) CreateDeviceConfigVersion(deviceID int64, yamlContent, comment string) (*DeviceConfigVersion, error) {
	var maxVersion int
	err := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM device_config_versions WHERE device_id=?", deviceID).Scan(&maxVersion)
	if err != nil {
		return nil, fmt.Errorf("get max version: %w", err)
	}
	newVersion := maxVersion + 1

	res, err := db.Exec(
		"INSERT INTO device_config_versions (device_id, version, yaml, comment) VALUES (?, ?, ?, ?)",
		deviceID, newVersion, yamlContent, comment,
	)
	if err != nil {
		return nil, fmt.Errorf("insert config version: %w", err)
	}
	_ = res
	return db.GetDeviceConfigVersion(deviceID, newVersion)
}
