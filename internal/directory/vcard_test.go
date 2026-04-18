package directory

import (
	"bytes"
	"strings"
	"testing"
)

// TestVCard_RoundTrip writes a realistic MeshSat contact to vCard,
// parses the result back, and asserts every field we care about
// survived. Covers the SMS→TEL;TYPE=cell mapping, the X-MESHSAT-*
// extension family, and the TEAM / ROLE / SIDC / TRUST-LEVEL
// auxiliary fields. [MESHSAT-541]
func TestVCard_RoundTrip(t *testing.T) {
	alice := Contact{
		ID:          "00000000-0000-4000-8000-000000000001",
		DisplayName: "Alice Kowalski",
		GivenName:   "Alice",
		FamilyName:  "Kowalski",
		Org:         "Meshsat Rescue",
		Role:        "Medic",
		Team:        "Red",
		SIDC:        "SFGPUCI----I",
		Notes:       "Primary SAR contact",
		TrustLevel:  TrustInPerson,
		Addresses: []Address{
			{Kind: KindSMS, Value: "+31612345678"},
			{Kind: KindSMS, Value: "+31687654321"}, // secondary SMS
			{Kind: KindMeshtastic, Value: "!abcd1234"},
			{Kind: KindAPRS, Value: "PA1A-9"},
			{Kind: KindIridiumSBD, Value: "300234012345670"},
			{Kind: KindTAK, Value: "ALICE.MEDIC"},
			{Kind: KindReticulum, Value: "abcdef0123456789"},
			{Kind: KindEmail, Value: "alice@example.com"},
		},
	}

	var buf bytes.Buffer
	if err := WriteVCards(&buf, []Contact{alice}); err != nil {
		t.Fatalf("WriteVCards: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "BEGIN:VCARD") || !strings.Contains(got, "END:VCARD") {
		t.Fatalf("output missing BEGIN/END: %q", got)
	}
	if !strings.Contains(got, "VERSION:4.0") {
		t.Error("VERSION:4.0 not emitted")
	}
	if !strings.Contains(got, "FN:Alice Kowalski") {
		t.Error("FN not emitted")
	}
	if !strings.Contains(got, "X-MESHSAT-SIDC:SFGPUCI----I") {
		t.Error("SIDC extension not emitted")
	}
	if !strings.Contains(got, "X-MESHSAT-TRUST-LEVEL:3") {
		t.Error("TRUST-LEVEL not emitted")
	}

	parsed, err := ParseVCards(&buf)
	if err != nil {
		t.Fatalf("ParseVCards: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("expected 1 contact, got %d", len(parsed))
	}
	// buf was drained — re-serialise to compare.
	buf.Reset()
	_ = WriteVCards(&buf, []Contact{alice})
	parsed, err = ParseVCards(&buf)
	if err != nil {
		t.Fatal(err)
	}
	bob := parsed[0]
	if bob.DisplayName != alice.DisplayName {
		t.Errorf("FN: %q vs %q", bob.DisplayName, alice.DisplayName)
	}
	if bob.GivenName != "Alice" || bob.FamilyName != "Kowalski" {
		t.Errorf("N split: given=%q family=%q", bob.GivenName, bob.FamilyName)
	}
	if bob.Team != "Red" {
		t.Errorf("team: %q", bob.Team)
	}
	if bob.Role != "Medic" {
		t.Errorf("role: %q", bob.Role)
	}
	if bob.SIDC != "SFGPUCI----I" {
		t.Errorf("sidc: %q", bob.SIDC)
	}
	if bob.TrustLevel != TrustInPerson {
		t.Errorf("trust_level: %d", bob.TrustLevel)
	}
	if bob.ID != alice.ID {
		t.Errorf("uid: %q vs %q", bob.ID, alice.ID)
	}

	// Every bearer address should survive.
	wantKinds := map[Kind]string{
		KindSMS:        "+31612345678", // first one primary
		KindMeshtastic: "!abcd1234",
		KindAPRS:       "PA1A-9",
		KindIridiumSBD: "300234012345670",
		KindTAK:        "ALICE.MEDIC",
		KindReticulum:  "abcdef0123456789",
		KindEmail:      "alice@example.com",
	}
	for kind, want := range wantKinds {
		found := false
		for _, a := range bob.Addresses {
			if a.Kind == kind && a.Value == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("kind %s value %q not present in %+v", kind, want, bob.Addresses)
		}
	}

	// The second SMS should come back too (not deduped).
	smsCount := 0
	for _, a := range bob.Addresses {
		if a.Kind == KindSMS {
			smsCount++
		}
	}
	if smsCount != 2 {
		t.Errorf("expected 2 SMS addresses, got %d", smsCount)
	}
}

// TestVCard_UnknownKindsSkipped proves we don't panic on X-* headers
// that aren't in the MESHSAT extension set.
func TestVCard_UnknownKindsSkipped(t *testing.T) {
	input := `BEGIN:VCARD
VERSION:4.0
FN:Charlie
X-CUSTOM-THING:whatever
X-MESHSAT-MESHTASTIC:!1234abcd
X-MESHSAT-UNKNOWN-KIND:ignored
END:VCARD
`
	parsed, err := ParseVCards(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 1 {
		t.Fatalf("count: %d", len(parsed))
	}
	if len(parsed[0].Addresses) != 1 {
		t.Errorf("addresses: %+v", parsed[0].Addresses)
	}
}

// TestVCard_MissingFNRejected — contact with no FN or N is invalid.
func TestVCard_MissingFNRejected(t *testing.T) {
	input := `BEGIN:VCARD
VERSION:4.0
NOTE:nothing here
END:VCARD
`
	_, err := ParseVCards(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for missing FN/N")
	}
}

// TestVCard_NFallbackToDisplayName — an N-only vCard produces a
// sensible DisplayName.
func TestVCard_NFallbackToDisplayName(t *testing.T) {
	input := `BEGIN:VCARD
VERSION:4.0
N:Kowalski;Alice;;;
EMAIL:alice@example.com
END:VCARD
`
	parsed, err := ParseVCards(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if parsed[0].DisplayName != "Alice Kowalski" {
		t.Errorf("fallback DisplayName: %q", parsed[0].DisplayName)
	}
}

// TestVCard_EscapingRoundTrip — commas, semicolons, backslashes,
// newlines in string fields survive the escape/unescape pair.
func TestVCard_EscapingRoundTrip(t *testing.T) {
	c := Contact{
		DisplayName: "Note, with; odd\\chars",
		Notes:       "Line 1\nLine 2",
		Addresses: []Address{
			{Kind: KindEmail, Value: "edge@example.com"},
		},
	}
	var buf bytes.Buffer
	if err := WriteVCards(&buf, []Contact{c}); err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseVCards(&buf)
	if err != nil {
		t.Fatal(err)
	}
	got := parsed[0]
	if got.DisplayName != c.DisplayName {
		t.Errorf("DisplayName escape mismatch:\n got: %q\nwant: %q", got.DisplayName, c.DisplayName)
	}
	if got.Notes != c.Notes {
		t.Errorf("Notes escape mismatch:\n got: %q\nwant: %q", got.Notes, c.Notes)
	}
}

// TestVCard_MultipleRecords — a file containing two BEGIN/END
// blocks yields two contacts in order.
func TestVCard_MultipleRecords(t *testing.T) {
	input := `BEGIN:VCARD
VERSION:4.0
FN:First Person
END:VCARD
BEGIN:VCARD
VERSION:4.0
FN:Second Person
X-MESHSAT-TEAM:Blue
END:VCARD
`
	parsed, err := ParseVCards(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 2 {
		t.Fatalf("count: %d", len(parsed))
	}
	if parsed[0].DisplayName != "First Person" || parsed[1].Team != "Blue" {
		t.Errorf("order/content: %+v", parsed)
	}
}
