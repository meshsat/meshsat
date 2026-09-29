package gateway

import (
	"encoding/json"
	"testing"
)

// A config sent back as GET gave it keeps the stored secrets. [MESHSAT-1412]
func TestUnmaskSecrets(t *testing.T) {
	stored := `{"callsign":"PA0XYZ","aprs_is_passcode":"12345","dyndns":{"token":"tok-1","host":"x"},"headers":{"Authorization":"Bearer s"}}`
	incoming := `{"callsign":"PA0ABC","aprs_is_passcode":"****","dyndns":{"token":"****","host":"y"},"headers":{"Authorization":"****"},"new_secret":"****"}`
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(unmaskSecrets(incoming, stored)), &got); err != nil {
		t.Fatal(err)
	}
	if got["callsign"] != "PA0ABC" || got["aprs_is_passcode"] != "12345" {
		t.Errorf("top level: %v", got)
	}
	if dd := got["dyndns"].(map[string]interface{}); dd["token"] != "tok-1" || dd["host"] != "y" {
		t.Errorf("nested: %v", dd)
	}
	if h := got["headers"].(map[string]interface{}); h["Authorization"] != "Bearer s" {
		t.Errorf("header: %v", h)
	}
	if got["new_secret"] != "****" {
		t.Errorf("a field the stored config lacks: %v", got["new_secret"])
	}
	// A real new secret replaces the stored one.
	if out := unmaskSecrets(`{"aprs_is_passcode":"999"}`, stored); out != `{"aprs_is_passcode":"999"}` {
		t.Errorf("a new secret: %s", out)
	}
	// Not JSON: untouched.
	if out := unmaskSecrets("x", stored); out != "x" {
		t.Errorf("not JSON: %s", out)
	}
}

// Android's "Where a text goes with no recipient" may be left empty.
// [MESHSAT-1412]
func TestCellularConfig_NoDefaultNumberIsAllowed(t *testing.T) {
	cfg, err := ParseCellularConfig(`{"destination_numbers":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("no default number refused: %v", err)
	}
}

// Secrets inside a list of objects come back too, matched by position.
func TestUnmaskSecrets_Lists(t *testing.T) {
	stored := `{"webhooks":[{"url":"a","token":"t1"},{"url":"b","token":"t2"}]}`
	got := unmaskSecrets(`{"webhooks":[{"url":"a","token":"****"},{"url":"b2","token":"****"},{"url":"c","token":"****"}]}`, stored)
	want := `{"webhooks":[{"token":"t1","url":"a"},{"token":"t2","url":"b2"},{"token":"****","url":"c"}]}`
	if got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}
