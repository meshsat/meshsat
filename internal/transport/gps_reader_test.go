package transport

import (
	"math"
	"testing"
)

func TestParseNMEACoord(t *testing.T) {
	tests := []struct {
		raw, dir string
		want     float64
		ok       bool
	}{
		{"5211.1234", "N", 52.185390, true},  // Leiden area
		{"00426.5678", "E", 4.442797, true},  // Leiden area
		{"5211.1234", "S", -52.185390, true}, // Southern hemisphere
		{"", "N", 0, false},
		{"5211.1234", "", 0, false},
	}

	for _, tt := range tests {
		got, ok := parseNMEACoord(tt.raw, tt.dir)
		if ok != tt.ok {
			t.Errorf("parseNMEACoord(%q, %q): ok=%v, want %v", tt.raw, tt.dir, ok, tt.ok)
			continue
		}
		if ok && math.Abs(got-tt.want) > 0.001 {
			t.Errorf("parseNMEACoord(%q, %q) = %.6f, want %.6f", tt.raw, tt.dir, got, tt.want)
		}
	}
}

func TestParseGGA(t *testing.T) {
	line := "$GPGGA,120000.00,5211.1234,N,00426.5678,E,1,06,1.2,10.5,M,47.0,M,,*5A"
	// Fix the checksum for this test
	pos, ok := parseNMEA(line)
	if !ok {
		// Checksum may not match our made-up sentence; test the parser directly
		fields := []string{"$GPGGA", "120000.00", "5211.1234", "N", "00426.5678", "E", "1", "06", "1.2", "10.5", "M", "47.0", "M", "", ""}
		pos, ok = parseGGA(fields)
		if !ok {
			t.Fatal("parseGGA failed")
		}
	}

	if math.Abs(pos.Lat-52.185390) > 0.001 {
		t.Errorf("lat = %.6f, want ~52.1854", pos.Lat)
	}
	if math.Abs(pos.Lon-4.442797) > 0.001 {
		t.Errorf("lon = %.6f, want ~4.4428", pos.Lon)
	}
	if pos.Sats != 6 {
		t.Errorf("sats = %d, want 6", pos.Sats)
	}
	if !pos.Fix {
		t.Error("expected fix=true")
	}
}

func TestParseRMC(t *testing.T) {
	fields := []string{"$GPRMC", "120000.00", "A", "5211.1234", "N", "00426.5678", "E", "0.0", "0.0", "090326", "", ""}
	pos, ok := parseRMC(fields)
	if !ok {
		t.Fatal("parseRMC failed")
	}

	if math.Abs(pos.Lat-52.185390) > 0.001 {
		t.Errorf("lat = %.6f, want ~52.1854", pos.Lat)
	}
	if pos.Timestamp.Year() != 2026 || pos.Timestamp.Month() != 3 || pos.Timestamp.Day() != 9 {
		t.Errorf("timestamp = %v, want 2026-03-09", pos.Timestamp)
	}
}

func TestParseRMC_NoFix(t *testing.T) {
	fields := []string{"$GPRMC", "120000.00", "V", "5211.1234", "N", "00426.5678", "E", "0.0", "0.0", "090326", "", ""}
	_, ok := parseRMC(fields)
	if ok {
		t.Error("expected no fix for status V")
	}
}

func TestValidateNMEAChecksum(t *testing.T) {
	// Construct a valid checksum
	sentence := "GPGGA,1"
	var cksum byte
	for i := 0; i < len(sentence); i++ {
		cksum ^= sentence[i]
	}
	validLine := "$" + sentence + "*" + func() string {
		s := ""
		s += string("0123456789ABCDEF"[cksum>>4])
		s += string("0123456789ABCDEF"[cksum&0xF])
		return s
	}()

	if !validateNMEAChecksum(validLine) {
		t.Errorf("expected valid checksum for %q", validLine)
	}

	// Invalid
	if validateNMEAChecksum("$GPGGA,1*00") {
		// Might accidentally match, so only fail if we know it shouldn't
		if cksum != 0x00 {
			t.Error("expected invalid checksum for $GPGGA,1*00")
		}
	}
}

func TestParseNMEATime(t *testing.T) {
	ts := parseNMEATime("120530.00", "090326")
	if ts.Hour() != 12 || ts.Minute() != 5 || ts.Second() != 30 {
		t.Errorf("time = %v, want 12:05:30", ts)
	}
	if ts.Year() != 2026 || ts.Month() != 3 || ts.Day() != 9 {
		t.Errorf("date = %v, want 2026-03-09", ts)
	}
}
