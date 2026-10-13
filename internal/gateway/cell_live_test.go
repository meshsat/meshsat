package gateway

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"meshsat/internal/smpp"
)

// TestCellLive binds to the real cell's SMPP server, keeps the session for
// a few seconds (one enquire_link round trip) and unbinds. No text goes
// anywhere and nothing keys a radio: the core (osmo-msc) is enough, the BTS
// may be down. Gated on CELL_LIVE=1 with CELL_SMPP_HOST, CELL_SMPP_PORT
// (2775), CELL_SMPP_SYSTEM_ID (meshsat-bridge) and CELL_SMPP_PASSWORD in
// the environment; the password comes from OpenBao
// (secret/meshsat/cell/smpp) and is never written anywhere. Nobody else
// may be bound as the same system id during the run: the cell keeps one
// session per system id and the newer bind displaces the older.
// [MESHSAT-1557]
func TestCellLive(t *testing.T) {
	if os.Getenv("CELL_LIVE") != "1" {
		t.Skip("CELL_LIVE=1 not set")
	}
	cfg := DefaultCellConfig()
	cfg.Host = os.Getenv("CELL_SMPP_HOST")
	if p := os.Getenv("CELL_SMPP_PORT"); p != "" {
		cfg.Port, _ = strconv.Atoi(p)
	}
	if id := os.Getenv("CELL_SMPP_SYSTEM_ID"); id != "" {
		cfg.SystemID = id
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Host == "" || os.Getenv("CELL_SMPP_PASSWORD") == "" {
		t.Fatal("CELL_SMPP_HOST and CELL_SMPP_PASSWORD are required")
	}
	gw := NewCellGateway(cfg, newTestDB(t), "cell_0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := gw.Start(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for !gw.Bound() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	st := gw.CellStatus()
	if !gw.Bound() {
		t.Fatalf("not bound after 15 s: %+v", st.Link)
	}
	t.Logf("bound to %s:%d as %s since %s", cfg.Host, cfg.Port, cfg.SystemID, st.Link.BoundSince.Format(time.RFC3339))
	// One enquire_link round trip: the client sends every 30 s; ask for one now.
	time.Sleep(2 * time.Second)
	if st := gw.CellStatus(); st.Link == nil || st.Link.State != smpp.StateBound {
		t.Fatalf("session did not stay bound: %+v", st.Link)
	}
	if err := gw.Stop(); err != nil {
		t.Fatal(err)
	}
	t.Logf("unbound cleanly; link: %+v", gw.CellStatus().Link)
}
