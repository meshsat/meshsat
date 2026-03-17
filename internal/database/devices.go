package database

import "fmt"

// Device represents a registered physical device identified by IMEI.
type Device struct {
	ID        int64   `json:"id"`
	IMEI      string  `json:"imei"`
	Label     string  `json:"label"`
	Type      string  `json:"type"`
	Notes     string  `json:"notes"`
	TenantID  string  `json:"tenant_id"`
	LastSeen  *string `json:"last_seen,omitempty"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// GetDevices returns all registered devices scoped to the given tenant.
func (db *DB) GetDevices(tenantID string) ([]Device, error) {
	rows, err := db.Query(
		"SELECT id, imei, label, type, notes, tenant_id, last_seen, created_at, updated_at FROM devices WHERE tenant_id = ? ORDER BY label, imei",
		tenantID)
	if err != nil {
		return nil, fmt.Errorf("query devices: %w", err)
	}
	defer rows.Close()
	var devices []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.IMEI, &d.Label, &d.Type, &d.Notes, &d.TenantID, &d.LastSeen, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		devices = append(devices, d)
	}
	return devices, nil
}

// GetDevicesAnyTenant returns all devices across all tenants.
// Used by system-level operations (backup, restore) without tenant context.
func (db *DB) GetDevicesAnyTenant() ([]Device, error) {
	rows, err := db.Query("SELECT id, imei, label, type, notes, tenant_id, last_seen, created_at, updated_at FROM devices ORDER BY label, imei")
	if err != nil {
		return nil, fmt.Errorf("query all devices: %w", err)
	}
	defer rows.Close()
	var devices []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.IMEI, &d.Label, &d.Type, &d.Notes, &d.TenantID, &d.LastSeen, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		devices = append(devices, d)
	}
	return devices, nil
}

// GetDevice returns a single device by ID, scoped to the given tenant.
func (db *DB) GetDevice(id int64, tenantID string) (*Device, error) {
	var d Device
	err := db.QueryRow(
		"SELECT id, imei, label, type, notes, tenant_id, last_seen, created_at, updated_at FROM devices WHERE id = ? AND tenant_id = ?",
		id, tenantID).
		Scan(&d.ID, &d.IMEI, &d.Label, &d.Type, &d.Notes, &d.TenantID, &d.LastSeen, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// GetDeviceByIMEI looks up a device by its IMEI, scoped to the given tenant.
func (db *DB) GetDeviceByIMEI(imei, tenantID string) (*Device, error) {
	var d Device
	err := db.QueryRow(
		"SELECT id, imei, label, type, notes, tenant_id, last_seen, created_at, updated_at FROM devices WHERE imei = ? AND tenant_id = ?",
		imei, tenantID).
		Scan(&d.ID, &d.IMEI, &d.Label, &d.Type, &d.Notes, &d.TenantID, &d.LastSeen, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// GetDeviceByIMEIAnyTenant looks up a device by IMEI without tenant filtering.
// Used by inbound message handlers (RockBLOCK, gateway) where tenant context is not available.
func (db *DB) GetDeviceByIMEIAnyTenant(imei string) (*Device, error) {
	var d Device
	err := db.QueryRow(
		"SELECT id, imei, label, type, notes, tenant_id, last_seen, created_at, updated_at FROM devices WHERE imei = ?",
		imei).
		Scan(&d.ID, &d.IMEI, &d.Label, &d.Type, &d.Notes, &d.TenantID, &d.LastSeen, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// GetDeviceAnyTenant returns a device by ID without tenant filtering.
// Used by engine-level code (rate limiter, dispatcher) without HTTP context.
func (db *DB) GetDeviceAnyTenant(id int64) (*Device, error) {
	var d Device
	err := db.QueryRow(
		"SELECT id, imei, label, type, notes, tenant_id, last_seen, created_at, updated_at FROM devices WHERE id = ?",
		id).
		Scan(&d.ID, &d.IMEI, &d.Label, &d.Type, &d.Notes, &d.TenantID, &d.LastSeen, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// CreateDevice inserts a new device and returns its ID.
func (db *DB) CreateDevice(imei, label, deviceType, notes, tenantID string) (int64, error) {
	res, err := db.Exec(
		"INSERT INTO devices (imei, label, type, notes, tenant_id) VALUES (?, ?, ?, ?, ?)",
		imei, label, deviceType, notes, tenantID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateDevice updates an existing device, scoped to the given tenant.
func (db *DB) UpdateDevice(id int64, label, deviceType, notes, tenantID string) error {
	res, err := db.Exec(
		"UPDATE devices SET label=?, type=?, notes=?, updated_at=datetime('now') WHERE id=? AND tenant_id=?",
		label, deviceType, notes, id, tenantID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("device %d not found for tenant %s", id, tenantID)
	}
	return nil
}

// DeleteDevice removes a device and its config versions by ID, scoped to the given tenant.
func (db *DB) DeleteDevice(id int64, tenantID string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM device_config_versions WHERE device_id = ?", id); err != nil {
		return fmt.Errorf("delete config versions for device %d: %w", id, err)
	}
	res, err := tx.Exec("DELETE FROM devices WHERE id = ? AND tenant_id = ?", id, tenantID)
	if err != nil {
		return fmt.Errorf("delete device %d: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("device %d not found for tenant %s", id, tenantID)
	}
	return tx.Commit()
}

// TouchDeviceLastSeen updates the last_seen timestamp for a device by IMEI.
// Not tenant-scoped — called by inbound message handlers without tenant context.
func (db *DB) TouchDeviceLastSeen(imei string) error {
	_, err := db.Exec("UPDATE devices SET last_seen=datetime('now') WHERE imei=?", imei)
	return err
}
