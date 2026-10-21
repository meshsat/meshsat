package main

import (
	"encoding/json"
	"strings"
	"testing"

	"meshsat/internal/hubreporter"
)

// A satellite gateway's birth entry carries its modem and declares a sealing
// send chain, so the Hub keeps that modem's ciphertext opaque; a chain
// without encryption, or none, declares nothing and the Hub reads as before.
func TestSatBirthEntryDeclaresASealingChain(t *testing.T) {
	base := hubreporter.InterfaceInfo{Name: "iridium_imt", Type: "iridium_imt", Status: "online"}
	cases := []struct {
		chain  string
		sealed bool
	}{
		{`[{"type":"smaz2g","params":{"dict":"meshtastic"}},{"type":"encrypt","params":{"key_ref":"iridium:shared"}}]`, true},
		{`[{"type":"smaz2"},{"type":"encrypt","params":{"key_ref":"iridium:shared"}},{"type":"base64"}]`, true},
		{`[{"type":"smaz2","params":{"dict":"meshtastic"}}]`, false},
		{`[]`, false},
		{``, false},
		{`not a chain`, true}, // unreadable: treated as sealing, as a send would be
	}
	for _, c := range cases {
		got := satBirthEntry(base, "300434060000001", c.chain)
		if got.IMEI != "300434060000001" || got.EgressSealed != c.sealed || got.Name != "iridium_imt" || got.Status != "online" {
			t.Fatalf("chain %q: %+v, want sealed=%v", c.chain, got, c.sealed)
		}
		b, _ := json.Marshal(got)
		if strings.Contains(string(b), `"egress_sealed"`) != c.sealed {
			t.Fatalf("chain %q: birth JSON %s", c.chain, b)
		}
	}
}
