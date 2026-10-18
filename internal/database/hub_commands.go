package database

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// HubCommandWindow is how long a Hub command's request id is remembered.
const HubCommandWindow = 24 * time.Hour

// ClaimHubCommand records a Hub command's request id before it runs. fresh
// is true when the id was not seen within HubCommandWindow; otherwise
// response is the reply the first copy published, empty while that copy
// still runs. Older entries are pruned on the way. [MESHSAT-1584]
func (db *DB) ClaimHubCommand(requestID, cmd string, now time.Time) (fresh bool, response string, err error) {
	err = db.withTx(func(tx *sqlx.Tx) error {
		if _, err := tx.Exec(`DELETE FROM hub_command_log WHERE received_at < ?`, now.Add(-HubCommandWindow).Unix()); err != nil {
			return err
		}
		var prev string
		switch err := tx.QueryRow(`SELECT response FROM hub_command_log WHERE request_id = ?`, requestID).Scan(&prev); {
		case err == nil:
			response = prev
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if _, err := tx.Exec(`INSERT INTO hub_command_log (request_id, cmd, received_at) VALUES (?, ?, ?)`,
			requestID, cmd, now.Unix()); err != nil {
			return err
		}
		fresh = true
		return nil
	})
	if err != nil {
		return false, "", fmt.Errorf("claim hub command %s: %w", requestID, err)
	}
	return fresh, response, nil
}

// FinishHubCommand stores the reply a Hub command published.
func (db *DB) FinishHubCommand(requestID, response string) error {
	if _, err := db.Exec(`UPDATE hub_command_log SET response = ? WHERE request_id = ?`, response, requestID); err != nil {
		return fmt.Errorf("finish hub command %s: %w", requestID, err)
	}
	return nil
}
