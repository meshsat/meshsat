package transport

import (
	"math"
	"testing"
)

// ============================================================================
// SBDIX Response Parser Tests
// ============================================================================

func TestParseSBDIX_Success(t *testing.T) {
	// Typical successful response
	resp := "\r\n+SBDIX: 0, 1, 0, 0, 0, 0\r\n\r\nOK\r\n"
	result, err := parseSBDIX(resp)
	if err != nil {
		t.Fatalf("parseSBDIX failed: %v", err)
	}
	if result.moStatus != 0 {
		t.Errorf("moStatus = %d, want 0", result.moStatus)
	}
	if result.moMSN != 1 {
		t.Errorf("moMSN = %d, want 1", result.moMSN)
	}
	if result.mtStatus != 0 {
		t.Errorf("mtStatus = %d, want 0", result.mtStatus)
	}
	if result.mtQueued != 0 {
		t.Errorf("mtQueued = %d, want 0", result.mtQueued)
	}
}

func TestParseSBDIX_WithMT(t *testing.T) {
	// Response with MT message piggybacked
	resp := "+SBDIX: 0, 5, 1, 3, 42, 2\r\nOK"
	result, err := parseSBDIX(resp)
	if err != nil {
		t.Fatalf("parseSBDIX failed: %v", err)
	}
	if result.moStatus != 0 {
		t.Errorf("moStatus = %d, want 0", result.moStatus)
	}
	if result.mtStatus != 1 {
		t.Errorf("mtStatus = %d, want 1 (MT received)", result.mtStatus)
	}
	if result.mtLength != 42 {
		t.Errorf("mtLength = %d, want 42", result.mtLength)
	}
	if result.mtQueued != 2 {
		t.Errorf("mtQueued = %d, want 2", result.mtQueued)
	}
}

func TestParseSBDIX_NoNetwork(t *testing.T) {
	// mo_status=32 — no network service
	resp := "+SBDIX: 32, 0, 0, 0, 0, 0\r\nOK"
	result, err := parseSBDIX(resp)
	if err != nil {
		t.Fatalf("parseSBDIX failed: %v", err)
	}
	if result.moStatus != 32 {
		t.Errorf("moStatus = %d, want 32", result.moStatus)
	}
	if result.statusText() == "success" {
		t.Error("status 32 should not be 'success'")
	}
}

func TestParseSBDIX_TruncatedPrefix(t *testing.T) {
	// Serial read can consume "+SBD" prefix, leaving "IX: <fields>"
	resp := "IX: 0, 2, 0, 0, 0, 0\r\nOK"
	result, err := parseSBDIX(resp)
	if err != nil {
		t.Fatalf("parseSBDIX failed on truncated prefix: %v", err)
	}
	if result.moStatus != 0 || result.moMSN != 2 {
		t.Errorf("unexpected result: %+v", result)
	}
}

func TestParseSBDIX_Invalid(t *testing.T) {
	_, err := parseSBDIX("garbage response")
	if err == nil {
		t.Error("expected error for garbage input")
	}

	_, err = parseSBDIX("+SBDIX: 0, 1")
	if err == nil {
		t.Error("expected error for too few fields")
	}
}

func TestParseSBDIX_StatusText(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{0, "success"},
		{1, "success"},
		{4, "success"},
		{32, "MO status 32"},
		{36, "MO status 36"},
	}
	for _, tt := range tests {
		r := sbdixResult{moStatus: tt.status}
		if got := r.statusText(); got != tt.want {
			t.Errorf("statusText(moStatus=%d) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

// ============================================================================
// SBDSX Response Parser Tests
// ============================================================================

func TestParseSBDSX_Clean(t *testing.T) {
	resp := "+SBDSX: 0, 0, 0, 0, 0, 0\r\nOK"
	s, err := parseSBDSX(resp)
	if err != nil {
		t.Fatalf("parseSBDSX failed: %v", err)
	}
	if s.MOFlag || s.MTFlag || s.RAFlag || s.MTWaiting != 0 {
		t.Errorf("expected clean status, got %+v", s)
	}
}

func TestParseSBDSX_RingAlert(t *testing.T) {
	// RA flag set, MT waiting
	resp := "+SBDSX: 0, 0, 0, 0, 1, 3\r\nOK"
	s, err := parseSBDSX(resp)
	if err != nil {
		t.Fatalf("parseSBDSX failed: %v", err)
	}
	if !s.RAFlag {
		t.Error("expected RAFlag=true")
	}
	if s.MTWaiting != 3 {
		t.Errorf("MTWaiting = %d, want 3", s.MTWaiting)
	}
}

func TestParseSBDSX_MOPending(t *testing.T) {
	resp := "+SBDSX: 1, 5, 0, 0, 0, 0\r\nOK"
	s, err := parseSBDSX(resp)
	if err != nil {
		t.Fatalf("parseSBDSX failed: %v", err)
	}
	if !s.MOFlag {
		t.Error("expected MOFlag=true")
	}
}

func TestParseSBDSX_Invalid(t *testing.T) {
	_, err := parseSBDSX("no sbdsx here")
	if err == nil {
		t.Error("expected error for missing +SBDSX")
	}
}

// ============================================================================
// CSQ Signal Parser Tests
// ============================================================================

func TestParseCSQ(t *testing.T) {
	tests := []struct {
		resp string
		want int
	}{
		{"+CSQ:3\r\nOK", 3},
		{"+CSQF:5\r\nOK", 5}, // Real firmware returns "+CSQF:" for AT+CSQF (ModemManager confirms)
		{"+CSQ: 0\r\nOK", 0},
		{"+CSQ:6\r\nOK", 0},  // out of range (0-5)
		{"+CSQ:-1\r\nOK", 0}, // negative
		{"garbage", 0},
	}
	for _, tt := range tests {
		got := parseCSQ(tt.resp)
		if got != tt.want {
			t.Errorf("parseCSQ(%q) = %d, want %d", tt.resp, got, tt.want)
		}
	}
}

// ============================================================================
// AT Value Parser Tests
// ============================================================================

func TestParseATValue(t *testing.T) {
	tests := []struct {
		resp string
		want string
	}{
		{"AT+CGSN\r\n300234065432100\r\nOK\r\n", "300234065432100"},
		{"\r\nIRIDIUM 9603N\r\n\r\nOK\r\n", "IRIDIUM 9603N"},
		{"", ""},
		{"OK", ""},
	}
	for _, tt := range tests {
		got := parseATValue(tt.resp)
		if got != tt.want {
			t.Errorf("parseATValue(%q) = %q, want %q", tt.resp, got, tt.want)
		}
	}
}

// ============================================================================
// ECEF to Geodetic Conversion Tests
// ============================================================================

func TestECEFToGeodetic(t *testing.T) {
	tests := []struct {
		name    string
		x, y, z float64
		wantLat float64
		wantLon float64
		tolDeg  float64
	}{
		{
			name: "equator prime meridian (0,0)",
			x:    6376, y: 0, z: 0,
			wantLat: 0, wantLon: 0,
			tolDeg: 0.1,
		},
		{
			name: "north pole",
			x:    0, y: 0, z: 6376,
			wantLat: 90, wantLon: 0,
			tolDeg: 0.1,
		},
		{
			name: "south pole",
			x:    0, y: 0, z: -6376,
			wantLat: -90, wantLon: 0,
			tolDeg: 0.1,
		},
		{
			name: "approximate Leiden NL (~52N, ~4.5E)",
			// ECEF for 52°N, 4.5°E at earth radius ~6371 km:
			// x = R*cos(52)*cos(4.5) ≈ 3910
			// y = R*cos(52)*sin(4.5) ≈ 308
			// z = R*sin(52) ≈ 5020
			// Rounded to 4 km resolution:
			x: 3912, y: 308, z: 5020,
			wantLat: 52, wantLon: 4.5,
			tolDeg: 1.0, // 4 km resolution ≈ ~0.04° but we're generous
		},
		{
			name: "southern hemisphere, western hemisphere",
			// Approx Buenos Aires: 34.6°S, 58.4°W
			// x = R*cos(-34.6)*cos(-58.4) ≈ 2748
			// y = R*cos(-34.6)*sin(-58.4) ≈ -4468
			// z = R*sin(-34.6) ≈ -3620
			x: 2748, y: -4468, z: -3620,
			wantLat: -34.6, wantLon: -58.4,
			tolDeg: 1.0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lat, lon := ecefToGeodetic(tt.x, tt.y, tt.z)
			if math.Abs(lat-tt.wantLat) > tt.tolDeg {
				t.Errorf("lat = %.2f, want %.2f (±%.1f°)", lat, tt.wantLat, tt.tolDeg)
			}
			if math.Abs(lon-tt.wantLon) > tt.tolDeg {
				t.Errorf("lon = %.2f, want %.2f (±%.1f°)", lon, tt.wantLon, tt.tolDeg)
			}
		})
	}
}

func TestParseMSGEO_ECEF(t *testing.T) {
	// Simulated response: satellite over Leiden area
	resp := "-MSGEO: 3912, 308, 5020, 1A2B3C4D\r\nOK"
	info, err := parseMSGEO(resp)
	if err != nil {
		t.Fatalf("parseMSGEO failed: %v", err)
	}
	// Should be approximately 52°N, 4.5°E
	if math.Abs(info.Lat-52.0) > 2.0 {
		t.Errorf("lat = %.2f, expected ~52", info.Lat)
	}
	if math.Abs(info.Lon-4.5) > 2.0 {
		t.Errorf("lon = %.2f, expected ~4.5", info.Lon)
	}
	if info.Accuracy != 200.0 {
		t.Errorf("accuracy = %.0f, expected 200", info.Accuracy)
	}
}

func TestParseMSGEO_ZeroCoords(t *testing.T) {
	resp := "-MSGEO: 0, 0, 0, 00000000\r\nOK"
	_, err := parseMSGEO(resp)
	if err == nil {
		t.Error("expected error for zero ECEF coordinates")
	}
}

func TestParseMSGEO_MalformedShort(t *testing.T) {
	resp := "-MSGEO: 3912, 308\r\nOK" // only 2 fields, need 4
	_, err := parseMSGEO(resp)
	if err == nil {
		t.Error("expected error for malformed MSGEO (too few fields)")
	}
}

func TestParseMSGEO_Missing(t *testing.T) {
	_, err := parseMSGEO("OK")
	if err == nil {
		t.Error("expected error for missing -MSGEO prefix")
	}
}

func TestParseIridiumTimestamp(t *testing.T) {
	// Iridium epoch: 2007-03-08 03:50:35 UTC
	// Tick = 0 should return epoch
	ts := parseIridiumTimestamp("0")
	// Zero ticks falls through to time.Now() fallback (err != nil for "0" as base-16)
	// Actually "0" parses as 0 in hex, but ticks==0 triggers fallback
	if ts.Year() < 2025 {
		t.Errorf("zero ticks should fallback to now, got %v", ts)
	}

	// Non-zero tick: 0x10000 = 65536 ticks × 90ms = 5,898,240 ms ≈ 1h 38m
	ts2 := parseIridiumTimestamp("10000")
	expected := iridiumEpoch.Add(65536 * 90 * 1000000) // nanoseconds
	if math.Abs(float64(ts2.Unix()-expected.Unix())) > 1 {
		t.Errorf("timestamp mismatch: got %v, expected %v", ts2, expected)
	}
}

// ============================================================================
// MSSTM Parser Tests
// ============================================================================

func TestParseMSSTM_Valid(t *testing.T) {
	resp := "\r\n-MSSTM: 3a2b1c00\r\n\r\nOK\r\n"
	result, err := parseMSSTM(resp)
	if err != nil {
		t.Fatalf("parseMSSTM failed: %v", err)
	}
	if !result.IsValid {
		t.Error("expected IsValid=true")
	}
	if result.SystemTime != 0x3a2b1c00 {
		t.Errorf("SystemTime = %x, want 3a2b1c00", result.SystemTime)
	}
	if result.EpochUTC == "" {
		t.Error("EpochUTC should not be empty")
	}
}

func TestParseMSSTM_NoNetwork(t *testing.T) {
	resp := "\r\n-MSSTM: no network service\r\n\r\nOK\r\n"
	result, err := parseMSSTM(resp)
	if err != nil {
		t.Fatalf("parseMSSTM failed: %v", err)
	}
	if result.IsValid {
		t.Error("expected IsValid=false for no network")
	}
}

func TestParseMSSTM_Missing(t *testing.T) {
	_, err := parseMSSTM("OK")
	if err == nil {
		t.Error("expected error for missing MSSTM prefix")
	}
}

func TestParseMSSTM_Epoch(t *testing.T) {
	// Tick 0 should correspond to MSSTM epoch (May 11, 2014 14:23:55 UTC)
	resp := "-MSSTM: 00000000\r\nOK\r\n"
	result, err := parseMSSTM(resp)
	if err != nil {
		t.Fatalf("parseMSSTM failed: %v", err)
	}
	if result.EpochUTC != "2014-05-11T14:23:55Z" {
		t.Errorf("epoch = %s, want 2014-05-11T14:23:55Z", result.EpochUTC)
	}
}
