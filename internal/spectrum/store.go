package spectrum

import (
	"context"
	"time"
)

// ScanRow is one persisted scan-event sample. It mirrors the fields
// carried on SpectrumEvent.Kind==EventScan but is decoupled from the
// SSE wire type so the database layer can round-trip rows without the
// monitor importing database types.
type ScanRow struct {
	TS           time.Time `json:"ts"`
	Band         string    `json:"band"`
	State        string    `json:"state"`
	AvgDB        float64   `json:"avg_db"`
	MaxDB        float64   `json:"max_db"`
	BaselineMean float64   `json:"baseline_mean"`
	BaselineStd  float64   `json:"baseline_std"`
	Powers       []float64 `json:"powers"`
}

// TransitionRow is one persisted state transition (clear → interference
// → jamming → clear). Used to draw alert markers on the detail-view
// waterfall so operators can see exactly when an event started and
// ended.
type TransitionRow struct {
	TS           time.Time `json:"ts"`
	Band         string    `json:"band"`
	OldState     string    `json:"old_state"`
	NewState     string    `json:"new_state"`
	PeakDB       float64   `json:"peak_db"`
	PeakFreqHz   int64     `json:"peak_freq_hz"`
	BaselineMean float64   `json:"baseline_mean"`
	BaselineStd  float64   `json:"baseline_std"`
}

// HistoryStore is the persistence contract the monitor relies on.
// Implementations live outside this package (internal/database) so the
// spectrum package stays free of SQL. All methods are best-effort from
// the monitor's perspective: a DB outage must never wedge live scanning
// or jamming detection — errors are logged and swallowed.
type HistoryStore interface {
	SaveScan(ctx context.Context, row ScanRow) error
	SaveTransition(ctx context.Context, row TransitionRow) error
	LoadScansByMinutes(ctx context.Context, band string, minutes int) ([]ScanRow, error)
	LoadScansRange(ctx context.Context, band string, from, to time.Time, maxRows int) ([]ScanRow, error)
	LoadTransitionsRange(ctx context.Context, band string, from, to time.Time) ([]TransitionRow, error)
	TrimSpectrumHistory(ctx context.Context, cutoff time.Time) (int64, error)
}

// RetentionSettings controls how far back we keep scan rows. Default
// is 24 h; operator can widen via MESHSAT_SPECTRUM_RETENTION_HOURS up
// to the hard cap. The cap exists so an operator mis-setting the env
// to something absurd (10000 h) can't silently grow the DB beyond the
// SD card — the monitor logs a warning and clamps.
type RetentionSettings struct {
	Hours int
}

const (
	// DefaultRetentionHours is the shipped default — a full day of
	// history covers the 6 h detail view with plenty of headroom and
	// still keeps the DB footprint tiny (~5 MB across 5 bands).
	DefaultRetentionHours = 24

	// MaxRetentionHours is the hard upper bound. 7 days × 5 bands ×
	// ~2 rows/min × ~45 bins × 8 B ≈ 36 MB, which is our worst-case.
	MaxRetentionHours = 168 // 7 days
)

// ClampRetention returns a sane retention in hours, clamped to
// [1, MaxRetentionHours]. A zero or negative input becomes the default
// so operators who blank the env var still get the safe fallback.
func ClampRetention(hours int) int {
	if hours <= 0 {
		return DefaultRetentionHours
	}
	if hours > MaxRetentionHours {
		return MaxRetentionHours
	}
	return hours
}
