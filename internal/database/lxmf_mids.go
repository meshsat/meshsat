package database

import "time"

// LXMFMidRetention is how long an LXMF message id is remembered: a repeat
// of the message inside it is dropped (MeshSat Android keeps its ids as
// long). [MESHSAT-1641]
const LXMFMidRetention = 30 * 24 * time.Hour

// ClaimLXMFMid records that the message mid from origin was taken in. It
// answers true the first time, false for a repeat.
func (db *DB) ClaimLXMFMid(origin, mid string, now time.Time) (bool, error) {
	res, err := db.Exec(`INSERT OR IGNORE INTO lxmf_mids (origin, mid, received_at) VALUES (?, ?, ?)`,
		origin, mid, now.UnixMilli())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// PruneLXMFMids forgets the ids taken in before LXMFMidRetention.
func (db *DB) PruneLXMFMids(now time.Time) (int64, error) {
	res, err := db.Exec(`DELETE FROM lxmf_mids WHERE received_at < ?`, now.Add(-LXMFMidRetention).UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
