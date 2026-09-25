package database

import "time"

// RNSPath is a row of rns_paths (MESHSAT-1348).
type RNSPath struct {
	DestHash    string
	NextHop     string
	Hops        int
	Iface       string
	ExpiresAt   time.Time
	RandomBlobs []byte
	AnnounceRaw []byte
}

// UpsertRNSPath stores or replaces a path.
func (db *DB) UpsertRNSPath(p RNSPath) error {
	_, err := db.Exec(`
		INSERT INTO rns_paths (dest_hash, next_hop, hops, iface, expires_at, random_blobs, announce_raw, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(dest_hash) DO UPDATE SET
			next_hop = excluded.next_hop, hops = excluded.hops, iface = excluded.iface,
			expires_at = excluded.expires_at, random_blobs = excluded.random_blobs,
			announce_raw = excluded.announce_raw, updated_at = datetime('now')`,
		p.DestHash, p.NextHop, p.Hops, p.Iface, p.ExpiresAt.Unix(), p.RandomBlobs, p.AnnounceRaw)
	return err
}

// GetRNSPaths returns every stored path whose expiry is not older than a week.
func (db *DB) GetRNSPaths() ([]RNSPath, error) {
	rows, err := db.Query(`SELECT dest_hash, next_hop, hops, iface, expires_at, random_blobs, announce_raw
		FROM rns_paths WHERE expires_at > ?`, time.Now().Add(-7*24*time.Hour).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RNSPath
	for rows.Next() {
		var p RNSPath
		var exp int64
		if err := rows.Scan(&p.DestHash, &p.NextHop, &p.Hops, &p.Iface, &exp, &p.RandomBlobs, &p.AnnounceRaw); err != nil {
			return nil, err
		}
		p.ExpiresAt = time.Unix(exp, 0)
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteRNSPath removes a path.
func (db *DB) DeleteRNSPath(destHash string) error {
	_, err := db.Exec(`DELETE FROM rns_paths WHERE dest_hash = ?`, destHash)
	return err
}
