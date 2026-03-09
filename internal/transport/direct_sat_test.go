package transport

import (
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
		{"+CSQF:5\r\nOK", 5},
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
