package database

import (
	"fmt"
	"time"
)

// KitPowerRow is one kernel boot of the kit as the bridge saw it: from the
// first heartbeat in that boot to the last. [MESHSAT-1842]
type KitPowerRow struct {
	BootID    string `db:"boot_id" json:"boot_id"`
	FirstSeen string `db:"first_seen" json:"first_seen"`
	LastSeen  string `db:"last_seen" json:"last_seen"`
}

// Hours is the row's span in hours.
func (r KitPowerRow) Hours() float64 {
	a, err1 := time.Parse(time.RFC3339, r.FirstSeen)
	b, err2 := time.Parse(time.RFC3339, r.LastSeen)
	if err1 != nil || err2 != nil || b.Before(a) {
		return 0
	}
	return b.Sub(a).Hours()
}

// TouchKitPower records that the bridge runs in this boot now: the first
// call of a boot writes first_seen, every call moves last_seen.
func (db *DB) TouchKitPower(bootID string, now time.Time) error {
	if bootID == "" {
		return fmt.Errorf("kit power: no boot id")
	}
	ts := now.UTC().Format(time.RFC3339)
	_, err := db.Exec(`INSERT INTO kit_power (boot_id, first_seen, last_seen) VALUES (?, ?, ?)
		ON CONFLICT(boot_id) DO UPDATE SET last_seen = excluded.last_seen`, bootID, ts, ts)
	if err != nil {
		return fmt.Errorf("kit power: %w", err)
	}
	return nil
}

// KitPower lists every recorded boot, oldest first.
func (db *DB) KitPower() ([]KitPowerRow, error) {
	var rows []KitPowerRow
	if err := db.Select(&rows, `SELECT boot_id, first_seen, last_seen FROM kit_power ORDER BY first_seen`); err != nil {
		return nil, fmt.Errorf("kit power: %w", err)
	}
	return rows, nil
}
