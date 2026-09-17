package spectrum

import (
	"fmt"
	"strconv"
	"strings"
)

// The band that watches the mesh must contain the mesh's own channel.
//
// Until MESHSAT-1203 the only LoRa band was lora_868 (868.0-868.6, later
// 867.8-868.6) and it was bound to mesh_0, i.e. it was the jamming detector
// for the Meshtastic link. Meshtastic does not transmit there. In EU_868 the
// firmware's region table puts the mesh in the 869.4-869.65 "g3" sub-band
// (500 mW ERP, 10 % duty cycle), one 250 kHz channel wide, so LongFast sits
// at 869.525 MHz: freqStart + bandwidth/2. The monitored window was 900 kHz
// below it and could not have seen a single mesh transmission.
//
// Two consequences, and the second is the one that matters this week:
//   - the mesh had no jamming detection at all, and
//   - what lora_868 actually watches is the 868 ISM sub-band, which carries
//     LoRaWAN uplinks (867.9 and 868.5 are both standard channels, both
//     measured on the kits on 17 Sep). Bound to mesh_0, a hall full of
//     LoRaWAN gateways would have been read as the mesh being jammed and
//     could have driven the dispatcher off the mesh at a LoRaWAN conference.
//
// So lora_868 keeps scanning but is no longer bound to an interface, and
// mesh_869 is the band bound to mesh_0.
//
// Static windows are enough: a band's frequency is a property of the radio,
// not of the moment. The one input that is not static is the region, which
// is why it is checked against the radio at startup rather than assumed.

// MeshRegionSlot returns the frequency range a Meshtastic region uses for
// the mesh itself, in Hz. Only regions whose whole slot fits inside one tune
// (SingleHopSpanHz) are listed: a region like US (902-928 MHz) spreads its
// channels over 26 MHz and needs a window centred on the configured channel
// number, which is a different job from this table. `known` is false for
// those, and MESHSAT_SPECTRUM_MESH_BAND overrides the band by hand.
func MeshRegionSlot(region string) (lowHz, highHz int, known bool) {
	switch strings.ToUpper(strings.TrimSpace(region)) {
	case "EU_868":
		return 869400000, 869650000, true
	case "EU_433":
		return 433000000, 434000000, true
	}
	return 0, 0, false
}

// MeshBandCoversRegion reports whether the band bound to the mesh contains
// the whole slot the radio's region transmits in. False means the monitor is
// watching a frequency the radio does not use — the defect MESHSAT-1203 found.
func MeshBandCoversRegion(bands []Band, region string) (band Band, ok bool, checked bool) {
	low, high, known := MeshRegionSlot(region)
	if !known {
		return Band{}, false, false
	}
	for _, b := range bands {
		if b.InterfaceID != MeshInterfaceID {
			continue
		}
		return b, b.FreqLow <= low && b.FreqHigh >= high, true
	}
	return Band{}, false, true
}

// MeshInterfaceID is the interface the mesh band scores for.
const MeshInterfaceID = "mesh_0"

// ApplyMeshBandOverride retunes the mesh-bound band from an operator string,
// for a region this build has no slot for or a radio on an override
// frequency. Accepts "869300000-869750000" or "869.3-869.75" (MHz).
func ApplyMeshBandOverride(bands []Band, spec string) ([]Band, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return bands, nil
	}
	parts := strings.Split(spec, "-")
	if len(parts) != 2 {
		return bands, fmt.Errorf("want low-high, got %q", spec)
	}
	low, err := parseFreqHz(parts[0])
	if err != nil {
		return bands, err
	}
	high, err := parseFreqHz(parts[1])
	if err != nil {
		return bands, err
	}
	if high <= low {
		return bands, fmt.Errorf("high (%d Hz) must be above low (%d Hz)", high, low)
	}
	out := make([]Band, 0, len(bands))
	found := false
	for _, b := range bands {
		if b.InterfaceID == MeshInterfaceID {
			b.FreqLow, b.FreqHigh = low, high
			found = true
		}
		out = append(out, b)
	}
	if !found {
		return bands, fmt.Errorf("no band is bound to %s", MeshInterfaceID)
	}
	return out, nil
}

// parseFreqHz reads a frequency as Hz or, when it carries a decimal point or
// is implausibly small for Hz, as MHz.
func parseFreqHz(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty frequency")
	}
	if strings.Contains(s, ".") {
		mhz, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, fmt.Errorf("bad frequency %q: %w", s, err)
		}
		return int(mhz * 1e6), nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("bad frequency %q: %w", s, err)
	}
	if v < 1000000 { // nobody monitors below 1 MHz with this hardware
		return v * 1000000, nil
	}
	return v, nil
}
