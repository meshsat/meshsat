package hemb

import "testing"

// HeMB is off unless MESHSAT_HEMB_EXPERIMENTAL says true. [MESHSAT-1581]
func TestExperimental(t *testing.T) {
	for v, want := range map[string]bool{"": false, "false": false, "0": false, "no": false, "maybe": false,
		"true": true, "TRUE": true, " 1 ": true, "yes": true, "on": true} {
		t.Setenv("MESHSAT_HEMB_EXPERIMENTAL", v)
		if got := Experimental(); got != want {
			t.Errorf("MESHSAT_HEMB_EXPERIMENTAL=%q: %v, want %v", v, got, want)
		}
	}
}
