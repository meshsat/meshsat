package database

import (
	"fmt"
	"strings"
	"time"
)

// CellPeer is one phone on the MeshSat 2G cell that texted the kit, as
// cell_peers remembers it. [MESHSAT-1557]
type CellPeer struct {
	MSISDN      string  `db:"msisdn" json:"msisdn"`
	FirstSeenAt string  `db:"first_seen_at" json:"first_seen_at"`
	LastRxAt    *string `db:"last_rx_at" json:"last_rx_at,omitempty"`
	LastTxAt    *string `db:"last_tx_at" json:"last_tx_at,omitempty"`
	RxCount     int64   `db:"rx_count" json:"rx_count"`
	TxCount     int64   `db:"tx_count" json:"tx_count"`
	Reachable   *bool   `db:"reachable" json:"reachable,omitempty"` // nil: the cell never said
	ReachableAt *string `db:"reachable_at" json:"reachable_at,omitempty"`
	LastDialled string  `db:"last_dialled" json:"last_dialled"`
}

// TouchCellPeerRx records a text from msisdn, dialled to the number given.
func (db *DB) TouchCellPeerRx(msisdn, dialled string) error {
	_, err := db.Exec(`INSERT INTO cell_peers (msisdn, last_rx_at, rx_count, last_dialled)
		VALUES (?, datetime('now'), 1, ?)
		ON CONFLICT(msisdn) DO UPDATE SET last_rx_at = datetime('now'), rx_count = rx_count + 1, last_dialled = excluded.last_dialled`,
		msisdn, dialled)
	if err != nil {
		return fmt.Errorf("touch cell peer rx: %w", err)
	}
	return nil
}

// TouchCellPeerTx records a text delivered to msisdn.
func (db *DB) TouchCellPeerTx(msisdn string) error {
	_, err := db.Exec(`INSERT INTO cell_peers (msisdn, last_tx_at, tx_count)
		VALUES (?, datetime('now'), 1)
		ON CONFLICT(msisdn) DO UPDATE SET last_tx_at = datetime('now'), tx_count = tx_count + 1`, msisdn)
	if err != nil {
		return fmt.Errorf("touch cell peer tx: %w", err)
	}
	return nil
}

// SetCellPeerReachable records what the cell's alert_notification said.
func (db *DB) SetCellPeerReachable(msisdn string, reachable bool) error {
	v := 0
	if reachable {
		v = 1
	}
	_, err := db.Exec(`INSERT INTO cell_peers (msisdn, reachable, reachable_at)
		VALUES (?, ?, datetime('now'))
		ON CONFLICT(msisdn) DO UPDATE SET reachable = excluded.reachable, reachable_at = datetime('now')`, msisdn, v)
	if err != nil {
		return fmt.Errorf("set cell peer reachable: %w", err)
	}
	return nil
}

// RecentCellPeers lists the phones that texted the kit since the time
// given, newest first.
func (db *DB) RecentCellPeers(since time.Time) ([]string, error) {
	rows, err := db.Query(`SELECT msisdn FROM cell_peers WHERE last_rx_at IS NOT NULL AND last_rx_at >= ?
		ORDER BY last_rx_at DESC`, since.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, fmt.Errorf("recent cell peers: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListCellPeers returns every phone the cell gateway has seen, newest
// activity first.
func (db *DB) ListCellPeers() ([]CellPeer, error) {
	rows, err := db.Query(`SELECT msisdn, first_seen_at, last_rx_at, last_tx_at, rx_count, tx_count, reachable, reachable_at, last_dialled
		FROM cell_peers ORDER BY COALESCE(MAX(last_rx_at, last_tx_at), last_rx_at, last_tx_at, first_seen_at) DESC`)
	if err != nil {
		return nil, fmt.Errorf("list cell peers: %w", err)
	}
	defer rows.Close()
	out := []CellPeer{}
	for rows.Next() {
		var p CellPeer
		var reachable *int
		if err := rows.Scan(&p.MSISDN, &p.FirstSeenAt, &p.LastRxAt, &p.LastTxAt, &p.RxCount, &p.TxCount, &reachable, &p.ReachableAt, &p.LastDialled); err != nil {
			return nil, err
		}
		if reachable != nil {
			b := *reachable == 1
			p.Reachable = &b
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// NudgeDeliveries makes every delivery on the channel that waits for a
// retry to one of the destinations due now: the cell said the phone is
// back. It returns how many rows it moved. [MESHSAT-1557]
func (db *DB) NudgeDeliveries(channel string, destinations []string) (int64, error) {
	if len(destinations) == 0 {
		return 0, nil
	}
	marks := strings.Repeat("?,", len(destinations))
	args := []interface{}{channel}
	for _, d := range destinations {
		args = append(args, d)
	}
	res, err := db.Exec(`UPDATE message_deliveries SET next_retry = datetime('now'), updated_at = datetime('now')
		WHERE channel = ? AND status = 'retry' AND destination IN (`+marks[:len(marks)-1]+`)`, args...)
	if err != nil {
		return 0, fmt.Errorf("nudge deliveries: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// CellTXRow is one line of the cell's transmit log. [MESHSAT-1578]
type CellTXRow struct {
	ID       int64   `json:"id"`
	At       string  `json:"at"`
	Event    string  `json:"event"`
	ARFCN    int     `json:"arfcn"`
	FreqMHz  float64 `json:"freq_mhz"`
	Setting  string  `json:"setting"`
	Output   string  `json:"output"`
	Purpose  string  `json:"purpose"`
	Operator string  `json:"operator"`
	Licence  string  `json:"licence"`
}

// InsertCellTX appends a transmit-log row.
func (db *DB) InsertCellTX(at time.Time, event string, arfcn int, freqMHz float64, setting, output, purpose, operator, licence string) error {
	_, err := db.Exec(`INSERT INTO cell_tx_log (at, event, arfcn, freq_mhz, setting, output, purpose, operator, licence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		at.UTC().Format("2006-01-02 15:04:05"), event, arfcn, freqMHz, setting, output, purpose, operator, licence)
	if err != nil {
		return fmt.Errorf("insert cell tx log: %w", err)
	}
	return nil
}

// ListCellTX returns the transmit log, oldest first, at most limit rows
// (0 = all).
func (db *DB) ListCellTX(limit int) ([]CellTXRow, error) {
	q := `SELECT id, at, event, arfcn, freq_mhz, setting, output, purpose, operator, licence FROM cell_tx_log ORDER BY id ASC`
	var rows interface {
		Next() bool
		Scan(dest ...any) error
		Close() error
		Err() error
	}
	var err error
	if limit > 0 {
		rows, err = db.Query(q+" LIMIT ?", limit)
	} else {
		rows, err = db.Query(q)
	}
	if err != nil {
		return nil, fmt.Errorf("list cell tx log: %w", err)
	}
	defer rows.Close()
	out := []CellTXRow{}
	for rows.Next() {
		var r CellTXRow
		if err := rows.Scan(&r.ID, &r.At, &r.Event, &r.ARFCN, &r.FreqMHz, &r.Setting, &r.Output, &r.Purpose, &r.Operator, &r.Licence); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountCellTX counts the transmit-log rows.
func (db *DB) CountCellTX() (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM cell_tx_log`).Scan(&n)
	return n, err
}
