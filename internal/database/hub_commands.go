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

// HubCommandClaim is what ClaimHubCommand found for a request id.
type HubCommandClaim struct {
	// Fresh: run the command now. True for an id not seen within the
	// window, and for one an earlier process claimed and never finished
	// (it died mid-command).
	Fresh bool
	// Response is the reply the first copy published; empty while the
	// command runs in this process.
	Response string
	// Conflict: the id was seen with other arguments. Not run.
	Conflict bool
}

// ClaimHubCommand records a Hub command's request id, the process that runs
// it (runID) and a digest of its arguments, before it runs. Older entries
// are pruned on the way. [MESHSAT-1584]
//
// What it does not cover: a command whose effect committed in the database
// (a send_text's delivery row) when the process died before FinishHubCommand
// stored the reply runs again in the next process. That window is the time
// between the two writes; the effect and the claim are not one transaction.
func (db *DB) ClaimHubCommand(requestID, cmd, argsSHA256, runID string, now time.Time) (HubCommandClaim, error) {
	var c HubCommandClaim
	err := db.withTx(func(tx *sqlx.Tx) error {
		if _, err := tx.Exec(`DELETE FROM hub_command_log WHERE received_at < ?`, now.Add(-HubCommandWindow).Unix()); err != nil {
			return err
		}
		var prevResp, prevRun, prevArgs string
		switch err := tx.QueryRow(`SELECT response, run_id, args_sha256 FROM hub_command_log WHERE request_id = ?`, requestID).
			Scan(&prevResp, &prevRun, &prevArgs); {
		case err == nil:
			switch {
			case prevArgs != "" && prevArgs != argsSHA256:
				c.Conflict = true
			case prevResp == "" && prevRun != runID:
				// Claimed by a process that is gone, never finished.
				if _, err := tx.Exec(`UPDATE hub_command_log SET run_id = ?, received_at = ? WHERE request_id = ?`,
					runID, now.Unix(), requestID); err != nil {
					return err
				}
				c.Fresh = true
			default:
				c.Response = prevResp
			}
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if _, err := tx.Exec(`INSERT INTO hub_command_log (request_id, cmd, received_at, run_id, args_sha256) VALUES (?, ?, ?, ?, ?)`,
			requestID, cmd, now.Unix(), runID, argsSHA256); err != nil {
			return err
		}
		c.Fresh = true
		return nil
	})
	if err != nil {
		return HubCommandClaim{}, fmt.Errorf("claim hub command %s: %w", requestID, err)
	}
	return c, nil
}

// FinishHubCommand stores the reply a Hub command published.
func (db *DB) FinishHubCommand(requestID, response string) error {
	if _, err := db.Exec(`UPDATE hub_command_log SET response = ? WHERE request_id = ?`, response, requestID); err != nil {
		return fmt.Errorf("finish hub command %s: %w", requestID, err)
	}
	return nil
}
