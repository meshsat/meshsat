package gateway

import (
	"context"
	"sync"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// A number whose chat key sealed the text gets that ciphertext; the others
// get the message as before. The history keeps the words for both and marks
// the sealed one; the packet feed never shows its ciphertext.
func TestCellular_SealedTextPerNumber(t *testing.T) {
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cell := newFakeCellTransport()
	gw := NewCellularGateway(CellularConfig{SMSPrefix: "MeshSat", MaxSMSSegments: 3}, cell, db)
	var mu sync.Mutex
	feed := map[string]PacketRecord{}
	gw.SetPacketSink(func(r PacketRecord) {
		mu.Lock()
		feed[r.To] = r
		mu.Unlock()
	}, "cellular_0")

	const sealedNo, plainNo = "+31600000001", "+31600000002"
	const sealed = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA="
	if err := gw.sendSMSSync(context.Background(), &transport.MeshMessage{
		DecodedText:     "meet at six",
		PlainText:       "meet at six",
		SMSDestinations: []string{sealedNo, plainNo},
		SMSTexts:        map[string]string{sealedNo: sealed},
	}); err != nil {
		t.Fatal(err)
	}

	onAir := map[string]string{}
	for _, s := range cell.sends() {
		onAir[s[0]] = s[1]
	}
	if onAir[sealedNo] != sealed || onAir[plainNo] != "meet at six" {
		t.Fatalf("on air: %v", onAir)
	}
	rows, err := db.GetSMSMessages(10, 0)
	if err != nil || len(rows) != 2 {
		t.Fatalf("history: %v %v", rows, err)
	}
	for _, r := range rows {
		if r.Text != "meet at six" || r.Encrypted != (r.Phone == sealedNo) || r.Direction != "tx" || r.Status != "sent" {
			t.Fatalf("history row %+v", r)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if feed[sealedNo].Text != "" || feed[plainNo].Text != "meet at six" {
		t.Fatalf("packet feed: sealed %q, plain %q", feed[sealedNo].Text, feed[plainNo].Text)
	}
}
