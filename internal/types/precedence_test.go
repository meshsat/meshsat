package types

import (
	"strings"
	"testing"
)

func TestPrecedenceValid(t *testing.T) {
	for _, p := range AllPrecedences() {
		if !p.Valid() {
			t.Errorf("AllPrecedences contains %q but Valid() is false", p)
		}
	}
	for _, bad := range []Precedence{"", "flash", "URGENT", "z", "Bogus"} {
		if bad.Valid() {
			t.Errorf("Valid(%q) = true, want false", bad)
		}
	}
}

func TestPrecedenceRank(t *testing.T) {
	ordered := AllPrecedences()
	for i, p := range ordered {
		if p.Rank() != i {
			t.Errorf("Rank(%q) = %d, want %d", p, p.Rank(), i)
		}
	}
	// Unknown → -1.
	if Precedence("Bogus").Rank() != -1 {
		t.Errorf("Rank(Bogus) = %d, want -1", Precedence("Bogus").Rank())
	}
	// Override is strictly more urgent than Flash, Flash than Immediate, …
	for i := 0; i < len(ordered)-1; i++ {
		if ordered[i].Rank() >= ordered[i+1].Rank() {
			t.Errorf("rank ordering broken: %q (%d) !< %q (%d)",
				ordered[i], ordered[i].Rank(), ordered[i+1], ordered[i+1].Rank())
		}
	}
}

func TestPrecedenceProsign(t *testing.T) {
	want := map[Precedence]string{
		PrecedenceOverride:  "",
		PrecedenceFlash:     "Z",
		PrecedenceImmediate: "O",
		PrecedencePriority:  "P",
		PrecedenceRoutine:   "R",
		PrecedenceDeferred:  "M",
	}
	for p, expected := range want {
		if got := p.Prosign(); got != expected {
			t.Errorf("Prosign(%q): got %q, want %q", p, got, expected)
		}
	}
	// Unknown → empty.
	if Precedence("Bogus").Prosign() != "" {
		t.Errorf("Prosign(Bogus) should be empty")
	}
}

func TestParsePrecedence_AllForms(t *testing.T) {
	cases := []struct {
		in   string
		want Precedence
	}{
		// Full names
		{"Override", PrecedenceOverride},
		{"OVERRIDE", PrecedenceOverride},
		{"override", PrecedenceOverride},
		{"Flash", PrecedenceFlash},
		{"FLASH", PrecedenceFlash},
		{"flash", PrecedenceFlash},
		{"Immediate", PrecedenceImmediate},
		{"Priority", PrecedencePriority},
		{"Routine", PrecedenceRoutine},
		{"Deferred", PrecedenceDeferred},

		// ACP-127 prosigns (must accept both cases)
		{"Z", PrecedenceFlash},
		{"z", PrecedenceFlash},
		{"O", PrecedenceImmediate},
		{"o", PrecedenceImmediate},
		{"P", PrecedencePriority},
		{"p", PrecedencePriority},
		{"R", PrecedenceRoutine},
		{"r", PrecedenceRoutine},
		{"M", PrecedenceDeferred},
		{"m", PrecedenceDeferred},

		// Whitespace around the value
		{"  Flash  ", PrecedenceFlash},
		{"\tz\n", PrecedenceFlash},

		// Empty → default (Routine)
		{"", PrecedenceRoutine},
		{"   ", PrecedenceRoutine},
	}
	for _, tc := range cases {
		got, err := ParsePrecedence(tc.in)
		if err != nil {
			t.Errorf("ParsePrecedence(%q): unexpected error %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParsePrecedence(%q): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParsePrecedence_Unknown(t *testing.T) {
	for _, in := range []string{"Urgent", "HIGH", "ZZ", "Flash!", "❤", "1"} {
		_, err := ParsePrecedence(in)
		if err == nil {
			t.Errorf("ParsePrecedence(%q): expected error, got nil", in)
			continue
		}
		if !strings.Contains(err.Error(), "unknown precedence") {
			t.Errorf("ParsePrecedence(%q): err=%v, want 'unknown precedence'", in, err)
		}
	}
}

func TestCanonicalOrMust(t *testing.T) {
	if got := PrecedenceFlash.CanonicalOrMust(); got != PrecedenceFlash {
		t.Errorf("valid: got %q", got)
	}
	if got := Precedence("garbage").CanonicalOrMust(); got != DefaultPrecedence {
		t.Errorf("invalid: got %q, want %q", got, DefaultPrecedence)
	}
}
