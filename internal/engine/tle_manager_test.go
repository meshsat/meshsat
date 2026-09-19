package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"meshsat/internal/database"
)

// Passes are predicted with no network from the snapshot shipped in the binary, and a
// refresh falls back to the TLE API when Celestrak cannot be reached (MESHSAT-1240).

func TestBundledSnapshotIsTheIridiumNextConstellation(t *testing.T) {
	entries := parse3LE(bundledTLEs, 0)
	if len(entries) < 66 {
		t.Fatalf("bundled snapshot has %d element sets, want the constellation", len(entries))
	}
	for _, e := range entries {
		if !iridiumNextName.MatchString(e.SatelliteName) {
			t.Fatalf("%q is not an Iridium NEXT satellite", e.SatelliteName)
		}
	}
}

func TestChooseTLEsPrefersTheNewerSet(t *testing.T) {
	bundled := parse3LE(bundledTLEs, 0)
	older := make([]database.TLECacheEntry, len(bundled))
	copy(older, bundled)
	older[0].Line1 = bundled[0].Line1[:18] + "25001.00000000" + bundled[0].Line1[32:]
	for i := 1; i < len(older); i++ {
		older[i].Line1 = older[0].Line1
	}
	if _, src := chooseTLEs(nil, bundled); src != "bundled" {
		t.Errorf("empty cache: source %s, want bundled", src)
	}
	if _, src := chooseTLEs(older, bundled); src != "bundled" {
		t.Errorf("cache older than the snapshot: source %s, want bundled", src)
	}
	if _, src := chooseTLEs(bundled, older); src != "downloaded" {
		t.Errorf("cache newer than the snapshot: source %s, want downloaded", src)
	}
}

func TestPassesArePredictedFromTheSnapshotAlone(t *testing.T) {
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer dead.Close()
	t.Setenv("CELESTRAK_IRIDIUM_URL", dead.URL)
	t.Setenv("TLE_API_URL", dead.URL+"/?page=")

	m := NewTLEManager(db)
	ctx, cancel := context.WithCancel(context.Background())
	m.Start(ctx)
	defer func() { cancel(); m.Stop() }()

	start := tleEpochUnix(parse3LE(bundledTLEs, 0)[0].Line1)
	passes, err := m.GeneratePasses(52.16, 4.51, 0, 6, 5, start)
	if err != nil {
		t.Fatalf("no passes offline: %v", err)
	}
	if len(passes) < 20 {
		t.Fatalf("got %d passes in 6 h over Leiden, want the constellation's", len(passes))
	}
	if src, age := m.DataInfo(); src != "bundled" || age < 0 {
		t.Fatalf("DataInfo = %s, %d; want bundled with an age", src, age)
	}
}

func TestRefreshFallsBackToTheTLEAPI(t *testing.T) {
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	sample := parse3LE(bundledTLEs, 0)[:2]
	celestrak := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer celestrak.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "MeshSat-Bridge") {
			t.Errorf("User-Agent %q", ua)
		}
		member := func(name, l1, l2 string) string {
			return fmt.Sprintf(`{"name":%q,"line1":%q,"line2":%q}`, name, l1, l2)
		}
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = fmt.Fprintf(w, `{"member":[%s,%s],"view":{"next":"/?page=2"}}`,
				member(sample[0].SatelliteName, sample[0].Line1, sample[0].Line2),
				member("IRIDIUM 33 DEB", sample[0].Line1, sample[0].Line2))
		default:
			_, _ = fmt.Fprintf(w, `{"member":[%s,%s],"view":{}}`,
				member(sample[1].SatelliteName, sample[1].Line1, sample[1].Line2),
				member("IRIDIUM 7", sample[1].Line1, sample[1].Line2))
		}
	}))
	defer api.Close()
	t.Setenv("CELESTRAK_IRIDIUM_URL", celestrak.URL)
	t.Setenv("TLE_API_URL", api.URL+"/?page=")

	m := NewTLEManager(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.RefreshTLEs(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	stored, err := db.GetTLECache()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 {
		t.Fatalf("stored %d element sets, want the 2 Iridium NEXT ones", len(stored))
	}
	if src, _ := m.DataInfo(); src != "tle-api" {
		t.Fatalf("source %s, want tle-api", src)
	}
}
