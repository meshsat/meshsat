package api

import "testing"

// TestValidateSignerID makes sure the id regex catches everything that
// is NOT a lower-case 64-char hex blob.
func TestValidateSignerID(t *testing.T) {
	ok := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cases := []struct {
		in   string
		want string
	}{
		{ok, ok},
		{"  " + ok + " ", ok}, // trim
		{"0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF", ok}, // uppercase → lower
		{"", ""},
		{"zzz", ""},              // too short
		{ok + "extra", ""},       // too long
		{"../../etc/passwd", ""}, // path traversal
		{"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdeG", ""}, // non-hex
	}
	for _, c := range cases {
		if got := validateSignerID(c.in); got != c.want {
			t.Errorf("validateSignerID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestBLEAddrFromManifest pulls the BLE address out of a manifest blob
// and returns "" on malformed / missing.
func TestBLEAddrFromManifest(t *testing.T) {
	blob := `{
		"protocol": 1,
		"signer_id": "xx",
		"bearers": [
			{"type":"mesh","address":"meshsat"},
			{"type":"ble","address":"AA:BB:CC:DD:EE:FF"},
			{"type":"sms","address":"+31611111111"}
		]
	}`
	if got := bleAddrFromManifest(blob); got != "AA:BB:CC:DD:EE:FF" {
		t.Errorf("BLE addr: got %q", got)
	}
	if bleAddrFromManifest("") != "" {
		t.Error("empty blob should yield empty addr")
	}
	if bleAddrFromManifest("not-json") != "" {
		t.Error("bad json should yield empty addr")
	}
	noBleBlob := `{"bearers":[{"type":"mesh"}]}`
	if bleAddrFromManifest(noBleBlob) != "" {
		t.Error("manifest without BLE bearer should yield empty addr")
	}
}
