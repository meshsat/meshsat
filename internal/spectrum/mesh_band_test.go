package spectrum

import (
	"testing"
	"time"
)

// The defect MESHSAT-1203 found: the band bound to mesh_0 did not contain
// the frequency an EU_868 radio transmits on, so the mesh had no jamming
// detection. Pin both halves of the fix.

func TestMeshBandCoversEU868(t *testing.T) {
	band, ok, checked := MeshBandCoversRegion(DefaultBands, "EU_868")
	if !checked {
		t.Fatal("EU_868 must be a known region")
	}
	if !ok {
		t.Fatalf("mesh band %s (%d-%d) does not cover the EU_868 slot 869.4-869.65",
			band.Name, band.FreqLow, band.FreqHigh)
	}
	if band.InterfaceID != MeshInterfaceID {
		t.Fatalf("mesh band bound to %q, want %q", band.InterfaceID, MeshInterfaceID)
	}
}

// The 868 ISM band must NOT score an interface. 867.9 and 868.5 are LoRaWAN
// uplink channels; bound to mesh_0 a busy hall would have read as the mesh
// being jammed and could have driven the dispatcher off the mesh.
func TestISMBandIsNotBoundToAnInterface(t *testing.T) {
	for _, b := range DefaultBands {
		if b.Name == "lora_868" && b.InterfaceID != "" {
			t.Fatalf("lora_868 is bound to %q; the 868 ISM band must not score an interface", b.InterfaceID)
		}
	}
}

func TestMeshRegionSlotUnknownRegion(t *testing.T) {
	if _, _, known := MeshRegionSlot("US"); known {
		t.Fatal("US spreads its channels over 26 MHz; it must not claim a single-tune slot")
	}
	if _, ok, checked := MeshBandCoversRegion(DefaultBands, "US"); checked || ok {
		t.Fatal("an unknown region must report unchecked rather than pass or fail")
	}
}

func TestApplyMeshBandOverride(t *testing.T) {
	cases := []struct {
		name     string
		spec     string
		wantLow  int
		wantHigh int
		wantErr  bool
	}{
		{name: "hz", spec: "902000000-904000000", wantLow: 902000000, wantHigh: 904000000},
		{name: "mhz", spec: "902.0-904.0", wantLow: 902000000, wantHigh: 904000000},
		{name: "spaces", spec: " 869.3 - 869.75 ", wantLow: 869300000, wantHigh: 869750000},
		{name: "inverted", spec: "904000000-902000000", wantErr: true},
		{name: "one value", spec: "902000000", wantErr: true},
		{name: "not a number", spec: "nine-ten", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ApplyMeshBandOverride(DefaultBands, tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var found bool
			for _, b := range got {
				if b.InterfaceID != MeshInterfaceID {
					continue
				}
				found = true
				if b.FreqLow != tc.wantLow || b.FreqHigh != tc.wantHigh {
					t.Fatalf("band %d-%d, want %d-%d", b.FreqLow, b.FreqHigh, tc.wantLow, tc.wantHigh)
				}
			}
			if !found {
				t.Fatal("no mesh-bound band in the result")
			}
			// The override must not disturb the other bands.
			if len(got) != len(DefaultBands) {
				t.Fatalf("band count changed: %d, want %d", len(got), len(DefaultBands))
			}
			for i, b := range got {
				if b.InterfaceID == MeshInterfaceID {
					continue
				}
				if b != DefaultBands[i] {
					t.Fatalf("band %s was modified by the override", b.Name)
				}
			}
		})
	}
}

// An empty override is the normal case and must leave the table alone.
func TestApplyMeshBandOverrideEmpty(t *testing.T) {
	got, err := ApplyMeshBandOverride(DefaultBands, "  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, b := range got {
		if b != DefaultBands[i] {
			t.Fatalf("band %s changed on an empty override", b.Name)
		}
	}
}

// A band that carries our own radio must not adopt an interference tier on
// the 10 s dwell that suits a band we only listen to: two ordinary mesh
// transmissions about 10 s apart would satisfy it, which is exactly what put
// mesh_869 into INTERFERENCE on both kits within minutes of going live.
// [MESHSAT-1203]
func TestOwnTrafficBandNeedsLongerDwell(t *testing.T) {
	var mesh Band
	for _, b := range DefaultBands {
		if b.Name == "mesh_869" {
			mesh = b
		}
	}
	if !mesh.OwnTraffic {
		t.Fatal("mesh_869 must be flagged as carrying our own traffic")
	}

	// 20 s of interference candidate: adopted on a listen-only band,
	// rejected on the mesh band.
	listen := Band{Name: "aprs_144"}
	if got := promoteStateFor(listen, StateInterference, StateClear, 20*time.Second); got != StateInterference {
		t.Fatalf("listen-only band after 20 s = %s, want interference", got)
	}
	if got := promoteStateFor(mesh, StateInterference, StateClear, 20*time.Second); got != StateClear {
		t.Fatalf("own-traffic band after 20 s = %s, want clear", got)
	}
	// A real interferer sits there; two minutes still trips it.
	if got := promoteStateFor(mesh, StateInterference, StateClear, 121*time.Second); got != StateInterference {
		t.Fatalf("own-traffic band after 121 s = %s, want interference", got)
	}
	// Jamming is unchanged: it needs flatness >= 0.60, which structured
	// LoRa never reaches, and keeps the same 60 s dwell.
	if got := promoteStateFor(mesh, StateJamming, StateClear, 61*time.Second); got != StateJamming {
		t.Fatalf("own-traffic band jamming after 61 s = %s, want jamming", got)
	}
}

// Calibration runs on a live band, so a band carrying our own radio gets a
// couple of 40 dB transmissions inside its 30 s window. The level must ignore
// them: with the arithmetic mean the kits calibrated mesh_869 to -59.2 dB
// against a true floor of -63.5, and every threshold derived from it went
// deaf by 4 dB. [MESHSAT-1203]
func TestBaselineLevelIgnoresOwnBursts(t *testing.T) {
	quiet := make([]float64, 0, 23)
	for i := 0; i < 20; i++ {
		quiet = append(quiet, -63.5)
	}
	// three transmissions caught by the scanner during calibration
	contaminated := append(append([]float64{}, quiet...), -25.0, -22.0, -24.0)

	cleanLevel, _, _ := baselineStats(quiet)
	level, _, mad := baselineStats(contaminated)

	if diff := level - cleanLevel; diff > 0.5 || diff < -0.5 {
		t.Fatalf("level moved %.2f dB (%.2f vs %.2f) when three bursts joined the window", diff, level, cleanLevel)
	}
	if mad > 0.5 {
		t.Fatalf("MAD %.3f: the quiet samples should dominate the spread", mad)
	}
	// And the arithmetic mean is what we are avoiding: prove it would have
	// moved, so this test fails loudly if someone puts it back.
	var sum float64
	for _, v := range contaminated {
		sum += v
	}
	if arith := sum / float64(len(contaminated)); arith > cleanLevel+1 == false {
		t.Fatalf("expected the arithmetic mean (%.2f) to be pulled well above %.2f", arith, cleanLevel)
	}
}
