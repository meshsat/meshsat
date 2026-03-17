package database

import "fmt"

// Device represents a registered physical device identified by IMEI.
type Device struct {
	ID        int64   `json:"id"`
	IMEI      string  `json:"imei"`
	Label     string  `json:"label"`
	Type      string  `json:"type"`
	Notes     string  `json:"notes"`
	LastSeen  *string `json:"last_seen,omitempty"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// GetDevices returns all registered devices.
func (db *DB) GetDevices() ([]Device, error) {
	rows, err := db.Query("SELECT id, imei, label, type, notes, last_seen, created_at, updated_at FROM devices ORDER BY label, imei")
	if err != nil {
		return nil, fmt.Errorf("query devices: %w", err)
	}
	defer rows.Close()
	var devices []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.IMEI, &d.Label, &d.Type, &d.Notes, &d.LastSeen, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		devices = append(devices, d)
	}
	return devices, nil
}

// GetDevice returns a single device by ID.
func (db *DB) GetDevice(id int64) (*Device, error) {
	var d Device
	err := db.QueryRow("SELECT id, imei, label, type, notes, last_seen, created_at, updated_at FROM devices WHERE id=?", id).
		Scan(&d.ID, &d.IMEI, &d.Label, &d.Type, &d.Notes, &d.LastSeen, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// GetDeviceByIMEI looks up a device by its IMEI.
func (db *DB) GetDeviceByIMEI(imei string) (*Device, error) {
	var d Device
	err := db.QueryRow("SELECT id, imei, label, type, notes, last_seen, created_at, updated_at FROM devices WHERE imei=?", imei).
		Scan(&d.ID, &d.IMEI, &d.Label, &d.Type, &d.Notes, &d.LastSeen, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// CreateDevice inserts a new device and returns its ID.
func (db *DB) CreateDevice(imei, label, deviceType, notes string) (int64, error) {
	res, err := db.Exec("INSERT INTO devices (imei, label, type, notes) VALUES (?, ?, ?, ?)", imei, label, deviceType, notes)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateDevice updates an existing device.
func (db *DB) UpdateDevice(id int64, label, deviceType, notes string) error {
	_, err := db.Exec("UPDATE devices SET label=?, type=?, notes=?, updated_at=datetime('now') WHERE id=?", label, deviceType, notes, id)
	return err
}

// DeleteDevice removes a device and its config versions by ID.
func (db *DB) DeleteDevice(id int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM device_config_versions WHERE device_id = ?", id); err != nil {
		return fmt.Errorf("delete config versions for device %d: %w", id, err)
	}
	if _, err := tx.Exec("DELETE FROM devices WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete device %d: %w", id, err)
	}
	return tx.Commit()
}

// TouchDeviceLastSeen updates the last_seen timestamp for a device by IMEI.
func (db *DB) TouchDeviceLastSeen(imei string) error {
	_, err := db.Exec("UPDATE devices SET last_seen=datetime('now') WHERE imei=?", imei)
	return err
}
