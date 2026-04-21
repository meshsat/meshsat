package database

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"meshsat/internal/spectrum"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSaveAndLoadSpectrumScan(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	now := time.Now().Add(-2 * time.Minute)
	row := spectrum.ScanRow{
		TS:           now,
		Band:         "lora_868",
		State:        "clear",
		AvgDB:        -57.3,
		MaxDB:        -35.1,
		BaselineMean: -58.0,
		BaselineStd:  0.4,
		Powers:       []float64{-57.5, -57.2, -57.9, -35.1, -58.0},
	}
	if err := db.SaveScan(ctx, row); err != nil {
		t.Fatalf("save: %v", err)
	}

	rows, err := db.LoadScansByMinutes(ctx, "lora_868", 5)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	got := rows[0]
	if got.Band != "lora_868" || got.State != "clear" {
		t.Fatalf("field mismatch: %+v", got)
	}
	if got.AvgDB != -57.3 || got.MaxDB != -35.1 {
		t.Fatalf("numeric mismatch: %+v", got)
	}
	if len(got.Powers) != 5 {
		t.Fatalf("powers len: %d", len(got.Powers))
	}
}

func TestLoadScansByMinutesFiltersAge(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	now := time.Now()
	// One fresh row (within 5 minute window) and one old row (10 min ago)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.SaveScan(ctx, spectrum.ScanRow{TS: now.Add(-1 * time.Minute), Band: "x", State: "clear", Powers: []float64{-50}}))
	must(db.SaveScan(ctx, spectrum.ScanRow{TS: now.Add(-10 * time.Minute), Band: "x", State: "clear", Powers: []float64{-50}}))

	rows, err := db.LoadScansByMinutes(ctx, "x", 5)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("age filter broken: got %d rows", len(rows))
	}
}

func TestLoadScansRangeCapsMaxRows(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	now := time.Now()
	for i := 0; i < 10; i++ {
		if err := db.SaveScan(ctx, spectrum.ScanRow{
			TS: now.Add(time.Duration(-i) * time.Second), Band: "b", State: "clear", Powers: []float64{-50},
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.LoadScansRange(ctx, "b", now.Add(-time.Minute), now.Add(time.Second), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows (cap), got %d", len(rows))
	}
	// Newest-first ordering
	if rows[0].TS.Before(rows[1].TS) {
		t.Fatalf("ordering: %v before %v", rows[0].TS, rows[1].TS)
	}
}

func TestSaveAndLoadTransition(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now()
	tr := spectrum.TransitionRow{
		TS:           now,
		Band:         "gps_l1",
		OldState:     "clear",
		NewState:     "jamming",
		PeakDB:       -22.0,
		PeakFreqHz:   1575420000,
		BaselineMean: -60.0,
		BaselineStd:  0.1,
	}
	if err := db.SaveTransition(ctx, tr); err != nil {
		t.Fatal(err)
	}
	rows, err := db.LoadTransitionsRange(ctx, "gps_l1", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 transition, got %d", len(rows))
	}
	if rows[0].NewState != "jamming" || rows[0].PeakFreqHz != 1575420000 {
		t.Fatalf("mismatch: %+v", rows[0])
	}
}

func TestTrimSpectrumHistoryOnlyDeletesOld(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	recent := now.Add(-time.Hour)

	for _, ts := range []time.Time{old, old, recent} {
		if err := db.SaveScan(ctx, spectrum.ScanRow{TS: ts, Band: "b", State: "clear", Powers: []float64{-50}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SaveTransition(ctx, spectrum.TransitionRow{TS: old, Band: "b", OldState: "clear", NewState: "jamming"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveTransition(ctx, spectrum.TransitionRow{TS: recent, Band: "b", OldState: "jamming", NewState: "clear"}); err != nil {
		t.Fatal(err)
	}

	n, err := db.TrimSpectrumHistory(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// 2 old scans + 1 old transition = 3 rows removed
	if n != 3 {
		t.Fatalf("trim count: want 3, got %d", n)
	}
	rows, _ := db.LoadScansRange(ctx, "b", now.Add(-72*time.Hour), now.Add(time.Hour), 100)
	if len(rows) != 1 {
		t.Fatalf("want 1 surviving scan, got %d", len(rows))
	}
	tr, _ := db.LoadTransitionsRange(ctx, "b", now.Add(-72*time.Hour), now.Add(time.Hour))
	if len(tr) != 1 {
		t.Fatalf("want 1 surviving transition, got %d", len(tr))
	}
}

func TestClampRetention(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, spectrum.DefaultRetentionHours},
		{-5, spectrum.DefaultRetentionHours},
		{1, 1},
		{24, 24},
		{168, 168},
		{169, spectrum.MaxRetentionHours},
		{10000, spectrum.MaxRetentionHours},
	}
	for _, c := range cases {
		if got := spectrum.ClampRetention(c.in); got != c.want {
			t.Errorf("ClampRetention(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
