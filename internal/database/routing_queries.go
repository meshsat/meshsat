package database

import "time"

// RoutingDestination represents a row in the routing_destinations table.
type RoutingDestination struct {
	DestHash      string    `db:"dest_hash"`
	SigningPub    []byte    `db:"signing_pub"`
	EncryptionPub []byte    `db:"encryption_pub"`
	AppData       []byte    `db:"app_data"`
	HopCount      int       `db:"hop_count"`
	SourceIface   string    `db:"source_iface"`
	FirstSeen     time.Time `db:"first_seen"`
	LastSeen      time.Time `db:"last_seen"`
	AnnounceCount int       `db:"announce_count"`
}

// UpsertRoutingDestination inserts or updates a routing destination.
func (db *DB) UpsertRoutingDestination(destHash string, signingPub, encryptionPub, appData []byte, hopCount int, sourceIface string, firstSeen, lastSeen time.Time, announceCount int) error {
	_, err := db.Exec(`
		INSERT INTO routing_destinations (dest_hash, signing_pub, encryption_pub, app_data, hop_count, source_iface, first_seen, last_seen, announce_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(dest_hash) DO UPDATE SET
			hop_count = CASE WHEN excluded.hop_count <= routing_destinations.hop_count THEN excluded.hop_count ELSE routing_destinations.hop_count END,
			source_iface = CASE WHEN excluded.hop_count <= routing_destinations.hop_count THEN excluded.source_iface ELSE routing_destinations.source_iface END,
			app_data = CASE WHEN length(excluded.app_data) > 0 THEN excluded.app_data ELSE routing_destinations.app_data END,
			last_seen = excluded.last_seen,
			announce_count = excluded.announce_count`,
		destHash, signingPub, encryptionPub, appData, hopCount, sourceIface,
		firstSeen.UTC().Format("2006-01-02T15:04:05Z"),
		lastSeen.UTC().Format("2006-01-02T15:04:05Z"),
		announceCount)
	return err
}

// GetRoutingDestinations returns all known routing destinations.
func (db *DB) GetRoutingDestinations() ([]RoutingDestination, error) {
	rows, err := db.Queryx("SELECT dest_hash, signing_pub, encryption_pub, app_data, hop_count, source_iface, first_seen, last_seen, announce_count FROM routing_destinations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var dests []RoutingDestination
	for rows.Next() {
		var d RoutingDestination
		var firstStr, lastStr string
		if err := rows.Scan(&d.DestHash, &d.SigningPub, &d.EncryptionPub, &d.AppData, &d.HopCount, &d.SourceIface, &firstStr, &lastStr); err != nil {
			continue
		}
		d.FirstSeen, _ = time.Parse("2006-01-02T15:04:05Z", firstStr)
		d.LastSeen, _ = time.Parse("2006-01-02T15:04:05Z", lastStr)
		dests = append(dests, d)
	}
	return dests, nil
}

// GetRoutingDestination returns a single routing destination by hash.
func (db *DB) GetRoutingDestination(destHash string) (*RoutingDestination, error) {
	var d RoutingDestination
	var firstStr, lastStr string
	err := db.QueryRow(
		"SELECT dest_hash, signing_pub, encryption_pub, app_data, hop_count, source_iface, first_seen, last_seen, announce_count FROM routing_destinations WHERE dest_hash = ?",
		destHash,
	).Scan(&d.DestHash, &d.SigningPub, &d.EncryptionPub, &d.AppData, &d.HopCount, &d.SourceIface, &firstStr, &lastStr, &d.AnnounceCount)
	if err != nil {
		return nil, err
	}
	d.FirstSeen, _ = time.Parse("2006-01-02T15:04:05Z", firstStr)
	d.LastSeen, _ = time.Parse("2006-01-02T15:04:05Z", lastStr)
	return &d, nil
}

// DeleteRoutingDestination removes a routing destination by hash.
func (db *DB) DeleteRoutingDestination(destHash string) error {
	_, err := db.Exec("DELETE FROM routing_destinations WHERE dest_hash = ?", destHash)
	return err
}
