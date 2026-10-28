package gateway

import (
	"testing"
	"time"
)

// MESHSAT-1736: a position event is stamped with the fix's own time, so a
// replay of an old fix is not a crew seen this minute on the Hub.
func TestAPositionEventCarriesTheFixTime(t *testing.T) {
	fix := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	ev := BuildEnrichedPositionEvent("meshsat-a1b3c2ec", "MSPA", PositionEnrichment{Lat: 52.1, Lon: 4.5, FixTime: fix}, 1800)
	if ev.Time != "2026-10-19T10:00:00Z" || ev.Start != ev.Time {
		t.Fatalf("time %s start %s", ev.Time, ev.Start)
	}
	if stale, _ := time.Parse(cotTimeFormat, ev.Stale); time.Until(stale) < 29*time.Minute {
		t.Fatalf("stale %s should be the export's, not the fix's", ev.Stale)
	}
	// No clock on the node: the moment of export, as before.
	ev = BuildEnrichedPositionEvent("meshsat-a1b3c2ec", "MSPA", PositionEnrichment{Lat: 52.1, Lon: 4.5}, 1800)
	if got, err := time.Parse(cotTimeFormat, ev.Time); err != nil || time.Since(got) > time.Minute {
		t.Fatalf("time %s (%v)", ev.Time, err)
	}
	// A fix stamped in the future (a node with a wrong clock) is not trusted.
	ev = BuildEnrichedPositionEvent("meshsat-a1b3c2ec", "MSPA", PositionEnrichment{Lat: 52.1, Lon: 4.5, FixTime: time.Now().Add(time.Hour)}, 1800)
	if got, _ := time.Parse(cotTimeFormat, ev.Time); time.Since(got) > time.Minute {
		t.Fatalf("a future fix time was used: %s", ev.Time)
	}
}
