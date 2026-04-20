package engine

import "testing"

func TestDeviceTypeMatchesChannelType(t *testing.T) {
	cases := []struct {
		dt, ct string
		want   bool
	}{
		{"meshtastic", "mesh", true},      // the Meshtastic alias
		{"iridium", "iridium", true},      // 9603 SBD
		{"iridium", "iridium_imt", false}, // IMT needs probe
		{"cellular", "cellular", true},
		{"zigbee", "zigbee", true},
		{"gps", "mesh", false},
		{"", "mesh", false},
		{"meshtastic", "", false},
		{"ambiguous", "mesh", false},
		{"unknown", "mesh", false},
	}
	for _, c := range cases {
		if got := deviceTypeMatchesChannelType(c.dt, c.ct); got != c.want {
			t.Errorf("deviceTypeMatchesChannelType(%q,%q)=%v, want %v", c.dt, c.ct, got, c.want)
		}
	}
}
