package database

// DeleteDeviceConfig removes a single config version for pruning.
func (db *DB) DeleteDeviceConfig(deviceID int64, version int) error {
	_, err := db.Exec(
		"DELETE FROM device_config_versions WHERE device_id=? AND version=?",
		deviceID, version,
	)
	return err
}
