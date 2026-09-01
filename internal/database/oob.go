package database

import (
	"database/sql"
	"errors"
	"fmt"
)

// Delivery classes on message_deliveries.delivery_class. [MESHSAT-756]
const (
	// DeliveryClassMessage is an ordinary chat or relay delivery.
	DeliveryClassMessage = "message"
	// DeliveryClassOOB is an OOB management frame: it bypasses egress rules
	// and interface transforms because the frame carries its own AEAD.
	DeliveryClassOOB = "oob"
)

// ErrOOBPeerNotFound is returned when no peer matches.
var ErrOOBPeerNotFound = errors.New("oob peer not found")

// OOBPeer is one management peer. [MESHSAT-756]
type OOBPeer struct {
	PeerID     uint16  `json:"peer_id"`
	Alias      string  `json:"alias"`
	SignerID   string  `json:"signer_id,omitempty"`
	KeyRef     string  `json:"key_ref"`
	KeySource  string  `json:"key_source"` // bundle | ecdh
	LocalRole  int     `json:"local_role"` // 0 issuer, 1 importer (nonce role bit)
	Role       string  `json:"role"`       // readonly | control
	Enabled    bool    `json:"enabled"`
	Addresses  string  `json:"addresses"`  // JSON object: interface id -> bearer address
	EncPolicy  string  `json:"enc_policy"` // JSON object: interface id -> bool (absent = true)
	TxCounter  uint32  `json:"tx_counter"`
	RxHigh     uint32  `json:"rx_high"`
	RxWindow   uint64  `json:"rx_window"`
	LastSeenAt *string `json:"last_seen_at,omitempty"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
}

// OOBLogEntry is one row of oob_log.
type OOBLogEntry struct {
	ID         int64  `json:"id"`
	TS         string `json:"ts"`
	PeerID     uint16 `json:"peer_id"`
	Direction  string `json:"direction"` // in | out
	Kind       string `json:"kind"`      // request | reply | reject
	Bearer     string `json:"bearer"`
	FromAddr   string `json:"from_addr,omitempty"`
	Cmd        int    `json:"cmd"`
	Counter    uint32 `json:"counter"`
	Result     string `json:"result,omitempty"`
	Detail     string `json:"detail,omitempty"`
	DeliveryID *int64 `json:"delivery_id,omitempty"`
}

const oobPeerColumns = `peer_id, alias, signer_id, key_ref, key_source, local_role, role, enabled,
	addresses, enc_policy, tx_counter, rx_high, rx_window, last_seen_at, created_at, updated_at`

func scanOOBPeer(row interface{ Scan(dest ...any) error }) (*OOBPeer, error) {
	var p OOBPeer
	var peerID, txCounter, rxHigh, rxWindow int64
	var enabled int
	if err := row.Scan(&peerID, &p.Alias, &p.SignerID, &p.KeyRef, &p.KeySource, &p.LocalRole, &p.Role, &enabled,
		&p.Addresses, &p.EncPolicy, &txCounter, &rxHigh, &rxWindow, &p.LastSeenAt, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.PeerID = uint16(peerID)
	p.Enabled = enabled != 0
	p.TxCounter = uint32(txCounter)
	p.RxHigh = uint32(rxHigh)
	p.RxWindow = uint64(rxWindow) // stored as int64 bit pattern
	return &p, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// InsertOOBPeer creates a peer row. PeerID must be non-zero and unique.
func (db *DB) InsertOOBPeer(p *OOBPeer) error {
	if p.PeerID == 0 {
		return errors.New("oob peer id must not be zero")
	}
	if p.Addresses == "" {
		p.Addresses = "{}"
	}
	if p.EncPolicy == "" {
		p.EncPolicy = "{}"
	}
	if p.KeySource == "" {
		p.KeySource = "bundle"
	}
	if p.Role == "" {
		p.Role = "readonly"
	}
	_, err := db.Exec(`INSERT INTO oob_peers
		(peer_id, alias, signer_id, key_ref, key_source, local_role, role, enabled, addresses, enc_policy, tx_counter, rx_high, rx_window)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		int64(p.PeerID), p.Alias, p.SignerID, p.KeyRef, p.KeySource, p.LocalRole, p.Role, boolInt(p.Enabled),
		p.Addresses, p.EncPolicy, int64(p.TxCounter), int64(p.RxHigh), int64(p.RxWindow))
	if err != nil {
		return fmt.Errorf("insert oob peer: %w", err)
	}
	return nil
}

// GetOOBPeer returns a peer by wire id.
func (db *DB) GetOOBPeer(id uint16) (*OOBPeer, error) {
	p, err := scanOOBPeer(db.QueryRow(`SELECT `+oobPeerColumns+` FROM oob_peers WHERE peer_id = ?`, int64(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrOOBPeerNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get oob peer %d: %w", id, err)
	}
	return p, nil
}

// GetOOBPeerByAlias returns a peer by alias.
func (db *DB) GetOOBPeerByAlias(alias string) (*OOBPeer, error) {
	p, err := scanOOBPeer(db.QueryRow(`SELECT `+oobPeerColumns+` FROM oob_peers WHERE alias = ?`, alias))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrOOBPeerNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get oob peer %q: %w", alias, err)
	}
	return p, nil
}

// ListOOBPeers returns every peer ordered by alias.
func (db *DB) ListOOBPeers() ([]OOBPeer, error) {
	rows, err := db.Query(`SELECT ` + oobPeerColumns + ` FROM oob_peers ORDER BY alias`)
	if err != nil {
		return nil, fmt.Errorf("list oob peers: %w", err)
	}
	defer rows.Close()
	var out []OOBPeer
	for rows.Next() {
		p, err := scanOOBPeer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan oob peer: %w", err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// UpdateOOBPeer updates the operator-editable fields: alias, signer id,
// role, enabled, addresses and encryption policy. Counters and the window
// have their own writers.
func (db *DB) UpdateOOBPeer(p *OOBPeer) error {
	res, err := db.Exec(`UPDATE oob_peers SET alias = ?, signer_id = ?, role = ?, enabled = ?,
		addresses = ?, enc_policy = ?, updated_at = datetime('now') WHERE peer_id = ?`,
		p.Alias, p.SignerID, p.Role, boolInt(p.Enabled), p.Addresses, p.EncPolicy, int64(p.PeerID))
	if err != nil {
		return fmt.Errorf("update oob peer %d: %w", p.PeerID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrOOBPeerNotFound
	}
	return nil
}

// DeleteOOBPeer removes a peer row. The caller revokes the key.
func (db *DB) DeleteOOBPeer(id uint16) error {
	res, err := db.Exec(`DELETE FROM oob_peers WHERE peer_id = ?`, int64(id))
	if err != nil {
		return fmt.Errorf("delete oob peer %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrOOBPeerNotFound
	}
	return nil
}

// NextOOBTxCounter increments and returns the peer's transmit counter. The
// increment is persisted before the caller seals a frame, so a crash
// between the two never reuses a counter (and therefore a nonce).
func (db *DB) NextOOBTxCounter(id uint16) (uint32, error) {
	var n int64
	err := db.QueryRow(`UPDATE oob_peers SET tx_counter = tx_counter + 1, updated_at = datetime('now')
		WHERE peer_id = ? RETURNING tx_counter`, int64(id)).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrOOBPeerNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("next oob tx counter %d: %w", id, err)
	}
	return uint32(n), nil
}

// BumpOOBTxCounters adds delta to every peer's transmit counter. Called once
// per process start so a database restored from an older backup cannot
// rewind into counters (nonces) that were already used.
func (db *DB) BumpOOBTxCounters(delta uint32) error {
	if _, err := db.Exec(`UPDATE oob_peers SET tx_counter = tx_counter + ? WHERE tx_counter > 0`, int64(delta)); err != nil {
		return fmt.Errorf("bump oob tx counters: %w", err)
	}
	return nil
}

// SaveOOBRxWindow persists the anti-replay window after an accepted frame
// and records the peer as seen.
func (db *DB) SaveOOBRxWindow(id uint16, high uint32, window uint64) error {
	res, err := db.Exec(`UPDATE oob_peers SET rx_high = ?, rx_window = ?, last_seen_at = datetime('now'),
		updated_at = datetime('now') WHERE peer_id = ?`, int64(high), int64(window), int64(id))
	if err != nil {
		return fmt.Errorf("save oob rx window %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrOOBPeerNotFound
	}
	return nil
}

// InsertOOBLog records one frame event and returns its id.
func (db *DB) InsertOOBLog(e *OOBLogEntry) (int64, error) {
	res, err := db.Exec(`INSERT INTO oob_log (peer_id, direction, kind, bearer, from_addr, cmd, counter, result, detail, delivery_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		int64(e.PeerID), e.Direction, e.Kind, e.Bearer, e.FromAddr, e.Cmd, int64(e.Counter), e.Result, e.Detail, e.DeliveryID)
	if err != nil {
		return 0, fmt.Errorf("insert oob log: %w", err)
	}
	return res.LastInsertId()
}

// ListOOBLog returns the newest entries first. peerID < 0 means all peers.
func (db *DB) ListOOBLog(limit int, peerID int) ([]OOBLogEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT id, ts, peer_id, direction, kind, bearer, from_addr, cmd, counter, result, detail, delivery_id FROM oob_log`
	var args []any
	if peerID >= 0 {
		query += ` WHERE peer_id = ?`
		args = append(args, int64(peerID))
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list oob log: %w", err)
	}
	defer rows.Close()
	var out []OOBLogEntry
	for rows.Next() {
		var e OOBLogEntry
		var peer, counter int64
		if err := rows.Scan(&e.ID, &e.TS, &peer, &e.Direction, &e.Kind, &e.Bearer, &e.FromAddr, &e.Cmd, &counter, &e.Result, &e.Detail, &e.DeliveryID); err != nil {
			return nil, fmt.Errorf("scan oob log: %w", err)
		}
		e.PeerID = uint16(peer)
		e.Counter = uint32(counter)
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneOOBLog keeps the newest keep rows and deletes the rest.
func (db *DB) PruneOOBLog(keep int) error {
	if keep < 0 {
		keep = 0
	}
	_, err := db.Exec(`DELETE FROM oob_log WHERE id NOT IN (SELECT id FROM oob_log ORDER BY id DESC LIMIT ?)`, keep)
	if err != nil {
		return fmt.Errorf("prune oob log: %w", err)
	}
	return nil
}
