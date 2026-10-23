package spectrum

import (
	"context"
	"testing"
	"time"
)

// setVerdict puts a band in a state as a scan checked it at.
func setVerdict(m *SpectrumMonitor, band string, st SpectrumState, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = true
	m.status[band].State = st
	m.status[band].CheckedAt = at
}

// The 144.8 MHz band covers the APRS link the intents use (aprs_0) as well
// as the AX.25 link it was bound to: both ride the same radio. [MESHSAT-1650]
func TestJamVerdictCoversTheAPRSLink(t *testing.T) {
	m := NewSpectrumMonitor(nil, DefaultBands)
	setVerdict(m, "aprs_144", StateJamming, time.Now())
	for _, id := range []string{"aprs_0", "ax25_0"} {
		if jammed, known := m.JamVerdict(id); !jammed || !known {
			t.Errorf("%s: jammed=%v known=%v, want a known jam", id, jammed, known)
		}
		if !m.IsJammed(id) {
			t.Errorf("IsJammed(%s) = false", id)
		}
	}
}

// A verdict no scan has refreshed lately, or one from a monitor that is
// suspended or off, says nothing: unknown, never jammed.
func TestJamVerdictStaleOrSuspendedIsUnknown(t *testing.T) {
	m := NewSpectrumMonitor(nil, DefaultBands)
	setVerdict(m, "mesh_869", StateJamming, time.Now().Add(-2*VerdictMaxAge))
	if jammed, known := m.JamVerdict(MeshInterfaceID); jammed || known {
		t.Errorf("stale: jammed=%v known=%v, want unknown", jammed, known)
	}
	if m.IsJammed(MeshInterfaceID) {
		t.Error("a stale jam still reads as jammed")
	}

	setVerdict(m, "mesh_869", StateJamming, time.Now())
	m.mu.Lock()
	m.suspended = true
	m.mu.Unlock()
	if jammed, known := m.JamVerdict(MeshInterfaceID); jammed || known {
		t.Errorf("suspended: jammed=%v known=%v, want unknown", jammed, known)
	}

	m.mu.Lock()
	m.suspended, m.enabled = false, false
	m.mu.Unlock()
	if jammed, known := m.JamVerdict(MeshInterfaceID); jammed || known {
		t.Errorf("monitor off: jammed=%v known=%v, want unknown", jammed, known)
	}
}

// A fresh clear verdict is known; a link no band watches is unknown.
func TestJamVerdictClearAndUnwatched(t *testing.T) {
	m := NewSpectrumMonitor(nil, DefaultBands)
	setVerdict(m, "mesh_869", StateClear, time.Now())
	if jammed, known := m.JamVerdict(MeshInterfaceID); jammed || !known {
		t.Errorf("clear: jammed=%v known=%v, want known clear", jammed, known)
	}
	if jammed, known := m.JamVerdict("tetra_0"); jammed || known {
		t.Errorf("tetra_0: jammed=%v known=%v, want unknown (no band)", jammed, known)
	}
}

// The mesh band has its own power floor, the LoRa neighbour's, instead of
// the -45 dB default that left it 5 dB less sensitive than the 868 band
// the same tuner sees.
func TestMeshBandHasItsOwnPowerFloor(t *testing.T) {
	if got := PowerFloorForBand("mesh_869"); got != PowerFloorLoRa {
		t.Fatalf("mesh_869 floor = %.0f, want the LoRa floor %.0f", got, PowerFloorLoRa)
	}
	// A barrage at -47 dB: flat and filling the band, between the two
	// floors. It must vote jamming on the mesh band.
	powers := make([]float64, 18)
	for i := range powers {
		powers[i] = -47
	}
	m := NewSpectrumMonitor(nil, DefaultBands)
	if got := m.evaluate("mesh_869", powers, -47, -47, &Baseline{Mean: -63}); got != StateJamming {
		t.Errorf("mesh_869 barrage at -47 dB voted %v, want jamming", got)
	}
}

// fixedScanner returns the same quiet spectrum on every scan.
type fixedScanner struct{}

func (fixedScanner) Available() bool   { return true }
func (fixedScanner) Info() ScannerInfo { return ScannerInfo{BinaryPath: "fixed"} }
func (fixedScanner) Scan(context.Context, int, int, int, int) ([]float64, error) {
	p := make([]float64, 18)
	for i := range p {
		p[i] = -63
	}
	return p, nil
}

// A scan stamps the band, so the verdict it leaves is known until it
// ages out.
func TestScanStampsTheVerdict(t *testing.T) {
	m := NewSpectrumMonitor(fixedScanner{}, DefaultBands[1:2])
	m.mu.Lock()
	m.enabled = true
	m.baseline["mesh_869"] = &Baseline{Mean: -63}
	m.mu.Unlock()
	if _, known := m.JamVerdict(MeshInterfaceID); known {
		t.Fatal("known before any scan")
	}
	m.scanAllBands(context.Background())
	if jammed, known := m.JamVerdict(MeshInterfaceID); jammed || !known {
		t.Fatalf("after a quiet scan: jammed=%v known=%v, want known clear", jammed, known)
	}
}

// The verdict's age limit is the classifier's own recovery dwell: a jam
// cannot clear faster than RecoveryPersistenceSec of clear scans, so holding
// a verdict that long after its last scan says no more than the classifier
// already does. One limit for the monitor and every consumer. [MESHSAT-1650]
func TestJamVerdictAgeBoundary(t *testing.T) {
	if VerdictMaxAge != RecoveryPersistenceSec*time.Second {
		t.Fatalf("VerdictMaxAge = %v, want the recovery dwell %v", VerdictMaxAge, RecoveryPersistenceSec*time.Second)
	}
	m := NewSpectrumMonitor(nil, DefaultBands)
	setVerdict(m, "mesh_869", StateJamming, time.Now().Add(-VerdictMaxAge+time.Second))
	if jammed, known := m.JamVerdict(MeshInterfaceID); !jammed || !known {
		t.Errorf("1 s inside the limit: jammed=%v known=%v, want a known jam", jammed, known)
	}
	setVerdict(m, "mesh_869", StateJamming, time.Now().Add(-VerdictMaxAge-time.Second))
	if _, known := m.JamVerdict(MeshInterfaceID); known {
		t.Error("1 s past the limit: still known")
	}
}

// A reader whose round takes longer than half the limit (the rtl_power_fftw
// rollback path) gets two of its own rounds, so a band scanned once a round
// does not flicker to unknown between its scans.
func TestJamVerdictSlowReaderGetsTwoRounds(t *testing.T) {
	m := NewSpectrumMonitor(nil, DefaultBands)
	m.mu.Lock()
	m.lastRound = 40 * time.Second
	m.mu.Unlock()
	setVerdict(m, "mesh_869", StateJamming, time.Now().Add(-50*time.Second))
	if jammed, known := m.JamVerdict(MeshInterfaceID); !jammed || !known {
		t.Errorf("50 s old with 40 s rounds: jammed=%v known=%v, want a known jam", jammed, known)
	}
	setVerdict(m, "mesh_869", StateJamming, time.Now().Add(-81*time.Second))
	if _, known := m.JamVerdict(MeshInterfaceID); known {
		t.Error("81 s old with 40 s rounds: still known")
	}
}

// A completed round records how long it took.
func TestScanRecordsItsRound(t *testing.T) {
	m := NewSpectrumMonitor(fixedScanner{}, DefaultBands[1:2])
	m.mu.Lock()
	m.enabled = true
	m.baseline["mesh_869"] = &Baseline{Mean: -63}
	m.mu.Unlock()
	m.scanAllBands(context.Background())
	m.mu.RLock()
	r := m.lastRound
	m.mu.RUnlock()
	if r <= 0 {
		t.Errorf("lastRound = %v after a round", r)
	}
}
