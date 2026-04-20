package database

import (
	"database/sql"
	"fmt"
)

// TrustedPeer is a durable row in the cross-bearer identity fabric.
// See migration v51 and the federation package for context.
// [MESHSAT-636]
//
// Timestamps are stored as ISO-8601 strings — matches the rest of the
// database/ package (hemb_events, directory_* etc.) because the
// modernc.org/sqlite driver scans DATETIME into driver.Value of type
// string. Callers that need time.Time can parse on the way out.
type TrustedPeer struct {
	SignerID           string `db:"signer_id"            json:"signer_id"`
	RoutingIdentity    string `db:"routing_identity"     json:"routing_identity"`
	Alias              string `db:"alias"                json:"alias"`
	ManifestJSON       string `db:"manifest_json"        json:"manifest_json"`
	ManifestVerifiedAt string `db:"manifest_verified_at" json:"manifest_verified_at,omitempty"`
	AutoFederate       bool   `db:"auto_federate"        json:"auto_federate"`
	CreatedAt          string `db:"created_at"           json:"created_at"`
	UpdatedAt          string `db:"updated_at"           json:"updated_at"`
}

// UpsertTrustedPeer inserts or updates a peer row keyed by signer_id.
// ManifestVerifiedAt is set to the current time whenever the manifest
// bytes change. AutoFederate flips to whatever the caller passes
// unless the row already exists — in that case we preserve the
// operator's prior toggle (matching the "don't surprise me" principle
// from the epic discussion).
func (db *DB) UpsertTrustedPeer(signerID, routingIdentity, alias, manifestJSON string, autoFederateDefault bool) error {
	if signerID == "" {
		return fmt.Errorf("trusted_peers: empty signer_id")
	}
	// Does a row exist?
	var existingAuto int
	err := db.Get(&existingAuto, `SELECT auto_federate FROM trusted_peers WHERE signer_id = ?`, signerID)
	switch {
	case err == sql.ErrNoRows:
		flag := 0
		if autoFederateDefault {
			flag = 1
		}
		_, err = db.Exec(
			`INSERT INTO trusted_peers (signer_id, routing_identity, alias, manifest_json, manifest_verified_at, auto_federate)
			 VALUES (?, ?, ?, ?, datetime('now'), ?)`,
			signerID, routingIdentity, alias, manifestJSON, flag,
		)
		return err
	case err != nil:
		return fmt.Errorf("trusted_peers: probe: %w", err)
	default:
		// Preserve the operator's existing auto_federate toggle.
		_, err = db.Exec(
			`UPDATE trusted_peers
			   SET routing_identity = ?,
			       alias = COALESCE(NULLIF(?, ''), alias),
			       manifest_json = ?,
			       manifest_verified_at = datetime('now'),
			       updated_at = datetime('now')
			 WHERE signer_id = ?`,
			routingIdentity, alias, manifestJSON, signerID,
		)
		return err
	}
}

// GetTrustedPeer returns a single row by signer_id, or sql.ErrNoRows.
func (db *DB) GetTrustedPeer(signerID string) (*TrustedPeer, error) {
	var p TrustedPeer
	err := db.Get(&p, `SELECT
		signer_id, routing_identity, alias, manifest_json,
		manifest_verified_at, auto_federate, created_at, updated_at
	 FROM trusted_peers WHERE signer_id = ?`, signerID)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ListTrustedPeers returns all rows, newest updated_at first.
func (db *DB) ListTrustedPeers() ([]TrustedPeer, error) {
	var peers []TrustedPeer
	err := db.Select(&peers, `SELECT
		signer_id, routing_identity, alias, manifest_json,
		manifest_verified_at, auto_federate, created_at, updated_at
	 FROM trusted_peers ORDER BY updated_at DESC`)
	return peers, err
}

// SetTrustedPeerAutoFederate toggles the auto_federate flag. Returns
// sql.ErrNoRows if the peer doesn't exist.
func (db *DB) SetTrustedPeerAutoFederate(signerID string, on bool) error {
	flag := 0
	if on {
		flag = 1
	}
	res, err := db.Exec(
		`UPDATE trusted_peers SET auto_federate = ?, updated_at = datetime('now') WHERE signer_id = ?`,
		flag, signerID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteTrustedPeer removes a peer row. Returns true if a row was
// deleted, false if none matched.
func (db *DB) DeleteTrustedPeer(signerID string) (bool, error) {
	res, err := db.Exec(`DELETE FROM trusted_peers WHERE signer_id = ?`, signerID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
