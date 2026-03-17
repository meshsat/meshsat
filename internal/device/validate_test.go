package device

import "testing"

func TestValidateIMEI(t *testing.T) {
	tests := []struct {
		name  string
		imei  string
		valid bool
	}{
		{"valid RockBLOCK IMEI", "300234063904190", true},
		{"valid generic IMEI", "490154203237518", true},
		{"valid all zeros", "000000000000000", true},
		{"valid all nines", "999999999999999", true},
		{"too short", "30023406390419", false},
		{"too long", "3002340639041901", false},
		{"empty", "", false},
		{"non-numeric letter end", "30023406390419A", false},
		{"spaces", "300234063904 90", false},
		{"letters only", "abcdefghijklmno", false},
		{"14 digits", "30023406390419", false},
		{"16 digits", "3002340639041900", false},
		{"with dash", "300234063904-90", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ValidateIMEI(tt.imei)
			if got != tt.valid {
				t.Errorf("ValidateIMEI(%q) = %v, want %v", tt.imei, got, tt.valid)
			}
		})
	}
}
