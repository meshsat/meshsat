package gateway

import (
	"context"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// An SMS written on this kit (the dashboard; no mesh sender) goes as typed:
// to the Hub in the clear, to anyone else as the interface's ciphertext, and
// never with a "[MeshSat] !00000000:" attribution. The SMS history keeps the
// words, not the ciphertext. [MESHSAT-756]
func TestCellular_LocalSendAndHistory(t *testing.T) {
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cell := newFakeCellTransport()
	gw := NewCellularGateway(CellularConfig{SMSPrefix: "MeshSat", MaxSMSSegments: 1,
		PlaintextPeers: []string{"+3197010258258"}}, cell, db)
	ctx := context.Background()

	// Plain, no mesh sender, to an ordinary number with no egress chain: as typed.
	if err := gw.sendSMSSync(ctx, &transport.MeshMessage{DecodedText: "see you at S27", SMSDestinations: []string{"+31600000001"}}); err != nil {
		t.Fatal(err)
	}
	// Encrypted by the worker: ciphertext on air, words in the history.
	if err := gw.sendSMSSync(ctx, &transport.MeshMessage{DecodedText: "\\x01Q2lwaGVy", Encrypted: true, PlainText: "the words",
		SMSDestinations: []string{"+31600000002"}}); err != nil {
		t.Fatal(err)
	}
	cell.mu.Lock()
	sent := append([][2]string{}, cell.sent...)
	cell.mu.Unlock()
	if len(sent) != 2 || sent[0][1] != "see you at S27" || sent[1][1] != "\\x01Q2lwaGVy" {
		t.Fatalf("on air: %v", sent)
	}
	hist, err := db.GetSMSMessages(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	texts := map[string]bool{}
	for _, h := range hist {
		texts[h.Text] = true
	}
	if !texts["see you at S27"] || !texts["the words"] || texts["\\x01Q2lwaGVy"] {
		t.Fatalf("history %v, want the words and no ciphertext", texts)
	}
}
