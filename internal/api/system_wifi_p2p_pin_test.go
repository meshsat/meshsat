package api

import "testing"

func TestWpsCheckDigit(t *testing.T) {
	// Known good PIN 12345670 — standard WPS example: 8th digit is 0.
	cases := []struct {
		base7 string
		want  int
	}{
		{"1234567", 0},
		{"0000000", 0},
		{"1111111", 5},
	}
	for _, c := range cases {
		if got := wpsCheckDigit(c.base7); got != c.want {
			t.Errorf("wpsCheckDigit(%q) = %d, want %d", c.base7, got, c.want)
		}
	}
}

func TestGenerateWPSPIN(t *testing.T) {
	for i := 0; i < 50; i++ {
		pin, err := generateWPSPIN()
		if err != nil {
			t.Fatalf("gen: %v", err)
		}
		if len(pin) != 8 {
			t.Fatalf("len: got %d want 8", len(pin))
		}
		for _, c := range pin {
			if c < '0' || c > '9' {
				t.Fatalf("non-digit %q in %q", c, pin)
			}
		}
		// Validate the check digit self-consistently.
		want := wpsCheckDigit(pin[:7])
		got := int(pin[7] - '0')
		if got != want {
			t.Errorf("check digit mismatch pin=%s got=%d want=%d", pin, got, want)
		}
	}
}

func TestIsValidWPSPIN(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"12345670", true},
		{"1234", true},
		{"", false},
		{"abcd", false},
		{"12345", false},
		{"1234567", false},
		{"123456789", false},
		{"1234567a", false},
	}
	for _, c := range cases {
		if got := isValidWPSPIN(c.in); got != c.want {
			t.Errorf("isValidWPSPIN(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
