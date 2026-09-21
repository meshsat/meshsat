package main

import (
	"testing"
	"time"
)

func TestHubUplinkSatInterface(t *testing.T) {
	now := time.Date(2026, 9, 21, 3, 0, 0, 0, time.UTC)
	type gw struct {
		connected bool
		last      time.Time
	}
	for _, tc := range []struct {
		name      string
		gws       map[string]gw
		wantIface string
		wantOK    bool
	}{
		{"9704 kit with recent traffic (both kits since 20 Sep 2026)",
			map[string]gw{"iridium_imt_0": {true, now.Add(-5 * time.Minute)}}, "iridium_imt_0", true},
		{"9704 kit indoors: connected, nothing moved for an hour, so SMS, but a forced satellite still names the 9704",
			map[string]gw{"iridium_imt_0": {true, now.Add(-time.Hour)}}, "iridium_imt_0", false},
		{"9704 kit just restarted: no activity yet",
			map[string]gw{"iridium_imt_0": {true, time.Time{}}}, "iridium_imt_0", false},
		{"9603 kit", map[string]gw{"iridium_0": {true, now.Add(-time.Minute)}}, "iridium_0", true},
		{"both modems, only the 9603 has sky",
			map[string]gw{"iridium_imt_0": {true, now.Add(-2 * time.Hour)}, "iridium_0": {true, now.Add(-time.Minute)}}, "iridium_0", true},
		{"no satellite gateway at all", map[string]gw{}, "iridium_imt_0", false},
	} {
		iface, ok := hubUplinkSatInterface(func(id string) (bool, time.Time, bool) {
			g, exists := tc.gws[id]
			return g.connected, g.last, exists
		}, now)
		if iface != tc.wantIface || ok != tc.wantOK {
			t.Errorf("%s: got (%q,%v), want (%q,%v)", tc.name, iface, ok, tc.wantIface, tc.wantOK)
		}
	}
}
