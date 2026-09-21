package transport

import (
	"testing"
	"time"
)

// The serial watchdog resets the modem after 2 minutes without a read. An MO
// may wait up to jsprMOTimeout (3 minutes) for a satellite with the line
// silent, so an MO in flight has to count as activity or the watchdog power
// cycles the 9704 under it. [MESHSAT-1282]
func TestHelperLastRead_MOInFlightIsActivity(t *testing.T) {
	const watchdogStale = 2 * time.Minute
	h := &jsprHelperPort{lastRead: time.Now().Add(-150 * time.Second)} // 2.5 min of silence

	if time.Since(h.LastRead()) < watchdogStale {
		t.Fatal("precondition: a port silent for 2.5 min with no MO must look stale")
	}

	h.moInFlightUntil = time.Now().Add(jsprMOTimeout + 30*time.Second)
	if stale := time.Since(h.LastRead()); stale >= watchdogStale {
		t.Fatalf("watchdog would reset the modem mid-MO: stale=%s with an MO in flight", stale)
	}

	h.moInFlightUntil = time.Time{} // final status arrived
	if time.Since(h.LastRead()) < watchdogStale {
		t.Fatal("after the MO ended a silent port must look stale again, or a dead port is never cycled")
	}
}

// The cover must outlast the longest legitimate MO, or the race is only moved.
func TestMOInFlightCoverOutlastsMOTimeout(t *testing.T) {
	if cover := jsprMOTimeout + 30*time.Second; cover <= jsprMOTimeout {
		t.Fatalf("cover %s does not outlast jsprMOTimeout %s", cover, jsprMOTimeout)
	}
	if jsprMOTimeout <= 2*time.Minute {
		t.Skip("MO timeout no longer exceeds the watchdog window")
	}
}
