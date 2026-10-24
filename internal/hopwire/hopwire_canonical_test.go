package hopwire

import "testing"

// find_takes_the_last_form (R5): the replier appends its form last, so a
// word that only looks like a form earlier in the body (an SSID, say) is not
// taken for it.
func TestFindTakesTheLastForm(t *testing.T) {
	got, ok := Find("ip:10.0.0.2 wpa:H1:m0A H1:m0Aa0X")
	if !ok || got != "H1:m0Aa0X" {
		t.Fatalf("%q %v, want the last form", got, ok)
	}
	if got, _ := Find("H1:a0X ssid H1:m0X"); got != "H1:m0X" {
		t.Fatalf("%q, want H1:m0X", got)
	}
}

// form_is_canonical (R6): one link per interface and no leading zeros, both
// ways: Parse refuses them and Encode never writes them.
func TestCompactFormIsCanonical(t *testing.T) {
	for _, bad := range []string{"H1:m0Am0X", "H1:a0Am0Aa0A", "H1:m00A", "H1:m01A", "H1:a007X"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	for _, good := range []string{"H1:m0A", "H1:m10A", "H1:m0Am1X", "H1:a100S"} {
		if _, err := Parse(good); err != nil {
			t.Errorf("%q refused: %v", good, err)
		}
	}
	if f := Encode([]Link{{"mesh_01", Admitted}, {"mesh_0", Excluded}, {"mesh_0", Admitted}}, 100); f != "H1:m0X" {
		t.Fatalf("form %q, want H1:m0X (no leading zero, one link per interface)", f)
	}
}
