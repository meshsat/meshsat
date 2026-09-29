package transport

import (
	"encoding/json"
	"strings"
	"testing"
)

// Critical lets a send past the SBD credit budget, so only the Bridge's own
// SOS may set it: it is never written into a MeshMessage's JSON (the
// envelope a delivery row keeps and a routing rule relays), and no JSON,
// whatever it spells, sets it when read back. [MESHSAT-1431]
func TestMeshMessage_CriticalNeverTravelsInJSON(t *testing.T) {
	out, err := json.Marshal(MeshMessage{PortNum: 1, DecodedText: "SOS", Critical: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(out)), "critical") {
		t.Fatalf("Critical is on the wire: %s", out)
	}
	for _, in := range []string{
		`{"portnum":1,"decoded_text":"hello","Critical":true}`,
		`{"portnum":1,"decoded_text":"hello","critical":true}`,
		`{"portnum":1,"decoded_text":"hello","CRITICAL":true}`,
	} {
		var m MeshMessage
		if err := json.Unmarshal([]byte(in), &m); err != nil {
			t.Fatal(err)
		}
		if m.Critical || m.DecodedText != "hello" {
			t.Fatalf("%s read back as critical %v, text %q", in, m.Critical, m.DecodedText)
		}
	}
}
