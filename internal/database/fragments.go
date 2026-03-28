package database

import "time"

// BundleFragment represents a single fragment of a DTN bundle.
type BundleFragment struct {
	ID            int64
	BundleID      string
	FragmentIndex int
	FragmentTotal int
	TotalSize     int
	Payload       []byte
	SourceIface   string
	DeliveryID    *int64
	CreatedAt     string
	ExpiresAt     *string
}

// HeldPacket represents a Reticulum packet held for late binding (no route yet).
type HeldPacket struct {
	ID          int64
	DestHash    string
	Packet      []byte
	SourceIface string
	TTLSeconds  int
	CreatedAt   string
	ExpiresAt   string
}

// InsertFragment stores a bundle fragment for reassembly.
func (db *DB) InsertFragment(bundleID string, index, total, totalSize int, payload []byte, sourceIface string, expiresAt string) error {
	_, err := db.Exec(`
		INSERT OR IGNORE INTO bundle_fragments (bundle_id, fragment_index, fragment_total, total_size, payload, source_iface, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, bundleID, index, total, totalSize, payload, sourceIface, expiresAt)
	return err
}

// GetFragments returns all fragments for a given bundle ID.
func (db *DB) GetFragments(bundleID string) ([]BundleFragment, error) {
	rows, err := db.Query(`
		SELECT id, bundle_id, fragment_index, fragment_total, total_size, payload, source_iface, created_at
		FROM bundle_fragments
		WHERE bundle_id = ?
		ORDER BY fragment_index
	`, bundleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var frags []BundleFragment
	for rows.Next() {
		var f BundleFragment
		if err := rows.Scan(&f.ID, &f.BundleID, &f.FragmentIndex, &f.FragmentTotal, &f.TotalSize, &f.Payload, &f.SourceIface, &f.CreatedAt); err != nil {
			return nil, err
		}
		frags = append(frags, f)
	}
	return frags, rows.Err()
}

// FragmentCount returns the number of received fragments for a bundle.
func (db *DB) FragmentCount(bundleID string) (int, error) {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM bundle_fragments WHERE bundle_id = ?", bundleID).Scan(&count)
	return count, err
}

// DeleteFragments removes all fragments for a completed bundle.
func (db *DB) DeleteFragments(bundleID string) error {
	_, err := db.Exec("DELETE FROM bundle_fragments WHERE bundle_id = ?", bundleID)
	return err
}

// CleanExpiredFragments removes expired incomplete bundles.
func (db *DB) CleanExpiredFragments() (int64, error) {
	res, err := db.Exec("DELETE FROM bundle_fragments WHERE expires_at IS NOT NULL AND expires_at < datetime('now')")
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// InsertHeldPacket stores a packet awaiting a route (late binding).
func (db *DB) InsertHeldPacket(destHash string, packet []byte, sourceIface string, ttlSeconds int) error {
	expiresAt := time.Now().Add(time.Duration(ttlSeconds) * time.Second).UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		INSERT INTO held_packets (dest_hash, packet, source_iface, ttl_seconds, expires_at)
		VALUES (?, ?, ?, ?, ?)
	`, destHash, packet, sourceIface, ttlSeconds, expiresAt)
	return err
}

// GetHeldPackets returns all held packets for a destination.
func (db *DB) GetHeldPackets(destHash string) ([]HeldPacket, error) {
	rows, err := db.Query(`
		SELECT id, dest_hash, packet, source_iface, ttl_seconds, created_at, expires_at
		FROM held_packets
		WHERE dest_hash = ? AND expires_at > datetime('now')
		ORDER BY created_at
	`, destHash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pkts []HeldPacket
	for rows.Next() {
		var p HeldPacket
		if err := rows.Scan(&p.ID, &p.DestHash, &p.Packet, &p.SourceIface, &p.TTLSeconds, &p.CreatedAt, &p.ExpiresAt); err != nil {
			return nil, err
		}
		pkts = append(pkts, p)
	}
	return pkts, rows.Err()
}

// DeleteHeldPacket removes a single held packet by ID.
func (db *DB) DeleteHeldPacket(id int64) error {
	_, err := db.Exec("DELETE FROM held_packets WHERE id = ?", id)
	return err
}

// CleanExpiredHeldPackets removes expired held packets.
func (db *DB) CleanExpiredHeldPackets() (int64, error) {
	res, err := db.Exec("DELETE FROM held_packets WHERE expires_at < datetime('now')")
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// HeldPacketCount returns the total number of held packets.
func (db *DB) HeldPacketCount() (int, error) {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM held_packets WHERE expires_at > datetime('now')").Scan(&count)
	return count, err
}
