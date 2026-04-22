package database

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"meshsat/internal/spectrum"
)

// Spectrum history persistence. Backs the HistoryStore interface in
// internal/spectrum — the monitor writes every scan + transition, the
// API layer reads range queries for the main-page prefill and the
// per-band detail view, and a retention goroutine trims on a schedule.
//
// We keep this file self-contained: no joins, append-only writes,
// single-index range queries. The hot path is one INSERT per scan
// (≈1 every 30 s per band = ~0.15 writes/s total); the cold path is a
// handful of reads per page open. Neither needs transactions.

// SaveScan persists one scan sample. powers is JSON-encoded because
// SQLite compresses repeated shapes efficiently in the page cache and
// the API layer echoes the array back verbatim — we never query into
// it, so a blob is the right call here.
func (db *DB) SaveScan(ctx context.Context, row spectrum.ScanRow) error {
	powersJSON, err := json.Marshal(row.Powers)
	if err != nil {
		return fmt.Errorf("marshal powers: %w", err)
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO spectrum_scans
		  (band, ts_ms, state, avg_db, max_db, baseline_mean, baseline_std, powers)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		row.Band,
		row.TS.UnixMilli(),
		row.State,
		row.AvgDB,
		row.MaxDB,
		row.BaselineMean,
		row.BaselineStd,
		string(powersJSON),
	)
	return err
}

// SaveTransition persists one state change. These are rare (a few per
// day on a quiet site, a handful per minute when something is jamming
// the band) and drive the alert-marker overlay on the detail view.
func (db *DB) SaveTransition(ctx context.Context, row spectrum.TransitionRow) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO spectrum_transitions
		  (band, ts_ms, old_state, new_state, peak_db, peak_freq_hz, baseline_mean, baseline_std)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		row.Band,
		row.TS.UnixMilli(),
		row.OldState,
		row.NewState,
		row.PeakDB,
		row.PeakFreqHz,
		row.BaselineMean,
		row.BaselineStd,
	)
	return err
}

// LoadScansByMinutes is the fast path used to seed a panel on page
// load. Rows are returned newest-first (same convention as the live
// ring), so the UI can unshift them directly.
func (db *DB) LoadScansByMinutes(ctx context.Context, band string, minutes int) ([]spectrum.ScanRow, error) {
	if minutes <= 0 {
		minutes = 5
	}
	cutoff := time.Now().Add(-time.Duration(minutes) * time.Minute).UnixMilli()
	return db.queryScans(ctx, `
		SELECT band, ts_ms, state, avg_db, max_db, baseline_mean, baseline_std, powers
		FROM spectrum_scans
		WHERE band = ? AND ts_ms >= ?
		ORDER BY ts_ms DESC
	`, band, cutoff)
}

// LoadLatestScans returns the N most recent rows for a band regardless
// of age. Used by the waterfall seed path so a kit that's been off for
// an hour still paints the last persisted data instead of an empty
// panel — operators see the gap honestly rather than an empty UI that
// looks like "no hardware". Index-seek on (band, ts_ms DESC) makes
// this O(limit) even when the table holds millions of rows. [MESHSAT-654]
func (db *DB) LoadLatestScans(ctx context.Context, band string, limit int) ([]spectrum.ScanRow, error) {
	if limit <= 0 {
		limit = 100
	}
	return db.queryScans(ctx, `
		SELECT band, ts_ms, state, avg_db, max_db, baseline_mean, baseline_std, powers
		FROM spectrum_scans
		WHERE band = ?
		ORDER BY ts_ms DESC
		LIMIT ?
	`, band, limit)
}

// LoadScansRange covers the detail view. maxRows caps the result; if
// the real row count exceeds the cap, the caller is expected to
// downsample the response or tighten the range. Cheap to discover
// that live because the returned slice length tells the truth.
func (db *DB) LoadScansRange(ctx context.Context, band string, from, to time.Time, maxRows int) ([]spectrum.ScanRow, error) {
	if maxRows <= 0 {
		maxRows = 2000
	}
	return db.queryScans(ctx, `
		SELECT band, ts_ms, state, avg_db, max_db, baseline_mean, baseline_std, powers
		FROM spectrum_scans
		WHERE band = ? AND ts_ms >= ? AND ts_ms <= ?
		ORDER BY ts_ms DESC
		LIMIT ?
	`, band, from.UnixMilli(), to.UnixMilli(), maxRows)
}

// LoadTransitionsRange fetches the alert markers for a time range.
// Always returned newest-first to match scan ordering; the detail view
// reverses as needed for its time-ascending overlay.
func (db *DB) LoadTransitionsRange(ctx context.Context, band string, from, to time.Time) ([]spectrum.TransitionRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT band, ts_ms, old_state, new_state, peak_db, peak_freq_hz, baseline_mean, baseline_std
		FROM spectrum_transitions
		WHERE band = ? AND ts_ms >= ? AND ts_ms <= ?
		ORDER BY ts_ms DESC
	`, band, from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []spectrum.TransitionRow{}
	for rows.Next() {
		var t spectrum.TransitionRow
		var tsMs int64
		if err := rows.Scan(&t.Band, &tsMs, &t.OldState, &t.NewState,
			&t.PeakDB, &t.PeakFreqHz, &t.BaselineMean, &t.BaselineStd); err != nil {
			return nil, err
		}
		t.TS = time.UnixMilli(tsMs)
		out = append(out, t)
	}
	return out, rows.Err()
}

// TrimSpectrumHistory deletes scans and transitions older than cutoff.
// Returns the combined row count (scans + transitions) so the caller
// can log a meaningful number. The two DELETEs are independent — if
// one fails the other still runs, because forward progress on either
// side shrinks the database.
func (db *DB) TrimSpectrumHistory(ctx context.Context, cutoff time.Time) (int64, error) {
	cutoffMs := cutoff.UnixMilli()
	var total int64
	var errs []error

	res, err := db.ExecContext(ctx, `DELETE FROM spectrum_scans WHERE ts_ms < ?`, cutoffMs)
	if err != nil {
		errs = append(errs, fmt.Errorf("scans: %w", err))
	} else if n, _ := res.RowsAffected(); n > 0 {
		total += n
	}

	res, err = db.ExecContext(ctx, `DELETE FROM spectrum_transitions WHERE ts_ms < ?`, cutoffMs)
	if err != nil {
		errs = append(errs, fmt.Errorf("transitions: %w", err))
	} else if n, _ := res.RowsAffected(); n > 0 {
		total += n
	}

	if len(errs) == 1 {
		return total, errs[0]
	}
	if len(errs) > 1 {
		return total, fmt.Errorf("trim: %v + %v", errs[0], errs[1])
	}
	return total, nil
}

// queryScans is the shared scan-unpacking helper. Kept private because
// every public caller has its own WHERE clause but the row→struct
// decoding is identical.
func (db *DB) queryScans(ctx context.Context, query string, args ...interface{}) ([]spectrum.ScanRow, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []spectrum.ScanRow{}
	for rows.Next() {
		var s spectrum.ScanRow
		var tsMs int64
		var powersJSON string
		if err := rows.Scan(&s.Band, &tsMs, &s.State, &s.AvgDB, &s.MaxDB,
			&s.BaselineMean, &s.BaselineStd, &powersJSON); err != nil {
			return nil, err
		}
		s.TS = time.UnixMilli(tsMs)
		if err := json.Unmarshal([]byte(powersJSON), &s.Powers); err != nil {
			// Corrupted row — skip rather than fail the whole read.
			// Worst case the panel shows one missing sample.
			continue
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
