package spectrum

import "testing"

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
