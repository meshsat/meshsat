package gateway

import (
	"testing"
	"time"
)

// The numbers are the ones both kits showed on 20 Sep 2026 while each
// PicoAPRS was discarding its peer, and the ones they show now. [MESHSAT-1284]
func TestSerialTNCReceiveHint(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tx, rx  int64
		age     time.Duration
		wantSet bool
	}{
		{"units swapped: tx climbs, rx zero", 120, 0, 40 * time.Minute, true},
		{"units swapped: a stray frame does not hide it", 120, 3, 40 * time.Minute, true},
		{"healthy link, about 80 percent decoded", 268, 220, 90 * time.Minute, false},
		{"lossy but alive link, 15 percent", 100, 15, 90 * time.Minute, false},
		{"too early after a restart", 12, 0, 2 * time.Minute, false},
		{"nothing sent yet", 3, 0, 40 * time.Minute, false},
	} {
		got := serialTNCReceiveHint(tc.tx, tc.rx, tc.age)
		if (got != "") != tc.wantSet {
			t.Errorf("%s: hint=%q, want set=%v", tc.name, got, tc.wantSet)
		}
	}
}
