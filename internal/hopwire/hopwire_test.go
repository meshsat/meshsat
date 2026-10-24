package hopwire

import (
	"strings"
	"testing"
)

var six = []Link{
	{"mesh_0", Excluded}, {"aprs_0", Admitted}, {"iridium_imt_0", Recovering},
	{"cell_0", Suspect}, {"tetra_0", Retained}, {"cellular_0", Down},
}

// compact_form_round_trip: links go in interface-id order and come back as
// they went, with their states.
func TestCompactFormRoundTrip(t *testing.T) {
	f := Encode(six, 200)
	if f != "H1:a0Ag0Sc0Dk0Rm0Xt0T" {
		t.Fatalf("form %q", f)
	}
	r, err := Parse(f)
	if err != nil || r.Cut {
		t.Fatalf("parse %q: %v cut=%v", f, err, r.Cut)
	}
	want := map[string]State{}
	for _, l := range six {
		want[l.Iface] = l.State
	}
	if len(r.Links) != len(six) {
		t.Fatalf("links %v", r.Links)
	}
	for _, l := range r.Links {
		if want[l.Iface] != l.State {
			t.Errorf("%s: %c, want %c", l.Iface, l.State, want[l.Iface])
		}
	}
	// Every kind of version 1 has its letter and comes back as itself.
	for kind := range Kinds {
		in := []Link{{kind + "_12", Admitted}}
		r, err := Parse(Encode(in, 50))
		if err != nil || len(r.Links) != 1 || r.Links[0].Iface != kind+"_12" {
			t.Errorf("%s: %+v %v", kind, r, err)
		}
	}
}

// A link the form has no letter for, or a malformed id or state, is left
// out; the rest go.
func TestCompactFormSkipsWhatItCannotSay(t *testing.T) {
	f := Encode([]Link{{"webhook_0", Admitted}, {"mesh", Admitted}, {"aprs_x", Admitted}, {"mesh_1234", Admitted}, {"mesh_0", 'Q'}, {"tetra_0", Suspect}}, 100)
	if f != "H1:t0S" {
		t.Fatalf("form %q, want H1:t0S", f)
	}
	if f := Encode(nil, 10); f != "H1:" {
		t.Fatalf("no links: %q", f)
	}
}

// compact_form_cut_to_each_bearer: the per-chunk room of a STATUS-NET reply
// (APRS 10, SBD 43, SMS and cellular 67, IMT 68) and the four-chunk total;
// a cut form fits, ends in "+", parses as cut, and is the head of the full
// form; a budget under MinLen gives nothing.
func TestCompactFormCutToEachBearer(t *testing.T) {
	var many []Link
	for _, k := range []string{"aprs", "cell", "cellular", "hub", "iridium", "iridium_imt", "lxmf", "mesh", "sms", "tetra", "zigbee"} {
		for i := 0; i < 3; i++ {
			many = append(many, Link{k + "_" + string(rune('0'+i)), Admitted})
		}
	}
	full := Encode(many, 1000)
	for _, budget := range []int{10, 43, 67, 68, 40, 172, 268, 272} {
		f := Encode(many, budget)
		if len(f) > budget {
			t.Errorf("budget %d: %d bytes", budget, len(f))
		}
		r, err := Parse(f)
		if err != nil {
			t.Errorf("budget %d: %q does not parse: %v", budget, f, err)
			continue
		}
		if len(full) > budget && (!r.Cut || !strings.HasSuffix(f, Cut)) {
			t.Errorf("budget %d: cut form %q not marked", budget, f)
		}
		if head := strings.TrimSuffix(f, Cut); !strings.HasPrefix(full, head) {
			t.Errorf("budget %d: %q is not the head of the full form", budget, f)
		}
		// The cut drops whole links: one more would not have fitted.
		if r.Cut && len(r.Links) < len(many) && len(f)+3 <= budget {
			t.Errorf("budget %d: %q dropped a link that fitted", budget, f)
		}
	}
	if f := Encode(many, MinLen-1); f != "" {
		t.Errorf("budget under MinLen: %q", f)
	}
	if f := Encode(many, MinLen); f != "H1:+" {
		t.Errorf("budget MinLen: %q", f)
	}
}

// compact_form_refuses_what_it_does_not_know: anything outside the grammar
// is refused whole, never guessed at.
func TestCompactFormRefusesWhatItDoesNotKnow(t *testing.T) {
	for _, bad := range []string{"", "H2:m0A", "h1:m0A", "H1:m0", "H1:mA", "H1:m0Q", "H1:y0A", "H1:m0A ", "H1:m1234A", "H1:m0A+x", "H1:+m0A", "H1:M0A"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	if r, err := Parse("H1:"); err != nil || len(r.Links) != 0 || r.Cut {
		t.Errorf("empty form: %+v %v", r, err)
	}
	if r, err := Parse("H1:+"); err != nil || !r.Cut {
		t.Errorf("cut empty form: %+v %v", r, err)
	}
}

// Find takes the form out of a STATUS-NET body, wherever it stands.
func TestFindInAStatusNetBody(t *testing.T) {
	for body, want := range map[string]string{
		"wlan0:up agent:none H1:m0A":        "H1:m0A",
		"H1:a0X+":                           "H1:a0X+",
		"ip:10.0.0.2 gw:10.0.0.1 H1:m0Aa0S": "H1:m0Aa0S",
	} {
		if got, ok := Find(body); !ok || got != want {
			t.Errorf("%q: %q %v", body, got, ok)
		}
	}
	if _, ok := Find("wlan0:up agent:none"); ok {
		t.Error("found a form in a body without one")
	}
}
