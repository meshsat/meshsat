package engine

import (
	"testing"
	"time"

	"meshsat/internal/database"
)

func newHealthDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, l := range []database.Interface{
		{ID: "iridium_0", ChannelType: "iridium"},
		{ID: "cellular_0", ChannelType: "cellular"},
		{ID: "aprs_0", ChannelType: "aprs"},
		{ID: "tetra_0", ChannelType: "tetra"},
		{ID: "odd_0", ChannelType: "odd"},
	} {
		l.Label, l.Enabled, l.Config, l.IngressTransforms, l.EgressTransforms = l.ID, true, "{}", "[]", "[]"
		if err := db.InsertInterface(&l); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// The SignalRecorder writes bars per modem ("sbd", "imt"), the cellular
// recorder writes cellular_signal_history, and nothing is written under an
// interface id, so each link reads its own modem's readings. The GSS rows
// are session outcomes (0 or 1), not bars. [MESHSAT-1653]
func TestHealthScoreReadsEachModemsSignal(t *testing.T) {
	db := newHealthDB(t)
	now := time.Now().Unix()
	for _, r := range []struct {
		src string
		at  int64
		v   float64
	}{{"sbd", now - 60, 4}, {"imt", now - 60, 3}, {"sbd_gss", now, 1}, {"imt_gss", now, 1}, {"gss", now, 1}} {
		if err := db.InsertSignalHistory(r.src, r.at, r.v); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.InsertCellularSignal(now, 2, -95, "LTE", "test"); err != nil {
		t.Fatal(err)
	}
	h := NewHealthScorer(db)
	for _, c := range []struct {
		iface, source string
		signal        int
	}{
		{"iridium_0", "sbd", 80},
		{"iridium_imt_0", "imt", 60},
		{"cellular_0", "cellular", 40},
		{"mesh_0", "", 0},
	} {
		s := h.Score(c.iface)
		if s.Signal != c.signal || s.SignalSource != c.source {
			t.Errorf("%s: signal %d from %q, want %d from %q", c.iface, s.Signal, s.SignalSource, c.signal, c.source)
		}
	}
}

// A modem that lost its satellite or its cell reads 0 now, not the last
// time it had bars; a reading older than ten minutes says nothing.
func TestHealthScoreSignalIsTheLatestFreshReading(t *testing.T) {
	db := newHealthDB(t)
	now := time.Now().Unix()
	_ = db.InsertSignalHistory("sbd", now-300, 4)
	_ = db.InsertSignalHistory("sbd", now-30, 0)
	_ = db.InsertSignalHistory("imt", now-700, 5)
	_ = db.InsertCellularSignal(now-700, 4, -80, "LTE", "test")
	h := NewHealthScorer(db)
	if s := h.Score("iridium_0"); s.Signal != 0 || s.SignalSource != "sbd" {
		t.Errorf("iridium_0: signal %d from %q, want 0 from sbd (the latest reading)", s.Signal, s.SignalSource)
	}
	for _, id := range []string{"iridium_imt_0", "cellular_0"} {
		if s := h.Score(id); s.Signal != 0 || s.SignalSource != "" {
			t.Errorf("%s: signal %d from %q, want none (the reading is 11 min old)", id, s.Signal, s.SignalSource)
		}
	}
}

// Cost is what a message costs on the link: the metered links are the
// ones the intent engine bills (Iridium SBD and IMT, cellular SMS); every
// other radio is free, the 2G cell included (our own network).
func TestHealthScoreCostByChannelType(t *testing.T) {
	db := newHealthDB(t)
	h := NewHealthScorer(db)
	for id, want := range map[string]int{
		"iridium_0": 30, "iridium_imt_0": 30, "cellular_0": 60,
		"mesh_0": 100, "aprs_0": 100, "tetra_0": 100, "lxmf_0": 100, "cell_0": 100,
		"odd_0": 50,
	} {
		if got := h.Score(id).Cost; got != want {
			t.Errorf("%s: cost %d, want %d", id, got, want)
		}
	}
}

// With no delivery in 24 h the success rate is 1.0 by design (a quiet link
// isn't a failing one), and Samples says it rests on nothing.
func TestHealthScoreSamplesSayWhenThereIsNoData(t *testing.T) {
	db := newHealthDB(t)
	h := NewHealthScorer(db)
	if s := h.Score("mesh_0"); s.SuccessRate != 1.0 || s.Samples != 0 {
		t.Fatalf("no data: rate %.2f over %d, want 1.00 over 0", s.SuccessRate, s.Samples)
	}
	for _, st := range []string{"sent", "failed", "queued"} {
		if _, err := db.InsertDelivery(database.MessageDelivery{MsgRef: "r-" + st, Channel: "mesh_0", Status: st}); err != nil {
			t.Fatal(err)
		}
	}
	if s := h.Score("mesh_0"); s.SuccessRate != 0.5 || s.Samples != 2 {
		t.Fatalf("one sent, one failed, one queued: rate %.2f over %d, want 0.50 over 2", s.SuccessRate, s.Samples)
	}
}
