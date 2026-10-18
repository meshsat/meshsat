package engine

// The review of fa4bd47 (17 Oct 2026 22:03 CEST), B12-R1 and the
// subscriber-loss control, as supplied but for one branch: the review's
// acceptance is that the 220-byte case "either sends all accepted bytes or
// is rejected with no accepted row/ACK"; the supplied test failed on any
// rejection (t.Fatal on AcceptCustody's error), so a rejection now checks
// that nothing was queued instead. The 20-byte case is unchanged.
// [MESHSAT-1584]

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

func TestReviewCumulative5CustodyPayloadReachesWorkerIntact(t *testing.T) {
	for _, size := range []int{20, 220} {
		t.Run(strconv.Itoa(size)+"_bytes", func(t *testing.T) {
			d, db := setupTestDispatcher(t)
			t.Cleanup(func() { db.Close() })
			if _, err := db.Exec(`DELETE FROM interfaces`); err != nil {
				t.Fatal(err)
			}
			if err := db.InsertInterface(&database.Interface{ID: "mesh_0", ChannelType: "mesh", Enabled: true}); err != nil {
				t.Fatal(err)
			}
			payload := bytes.Repeat([]byte("x"), size)
			id, err := d.AcceptCustody(payload)
			if err != nil {
				var rows int
				_ = db.QueryRow(`SELECT COUNT(*) FROM message_deliveries`).Scan(&rows)
				t.Logf("accepted=%d rejected=%q rows=%d", size, err, rows)
				if size <= directPreviewMax || rows != 0 {
					t.Fatalf("rejected %d bytes (%v) with %d rows queued", size, err, rows)
				}
				return
			}
			queued, err := db.GetDelivery(id)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(queued.Payload, payload) {
				t.Fatal("queued bytes changed")
			}
			mesh := &mockMeshTransport{}
			worker := &DeliveryWorker{channelID: "mesh_0", db: db, mesh: mesh}
			worker.processBatch(context.Background())
			if len(mesh.sent) != 1 {
				t.Fatalf("sent %d requests", len(mesh.sent))
			}
			stored, err := db.GetDelivery(id)
			if err != nil {
				t.Fatal(err)
			}
			ack := ""
			if stored.AckStatus != nil {
				ack = *stored.AckStatus
			}
			t.Logf("accepted=%d queued=%d preview=%d sent=%d status=%s ack=%s", size, len(queued.Payload), len(queued.TextPreview), len(mesh.sent[0].Text), stored.Status, ack)
			if mesh.sent[0].Text != string(payload) {
				t.Fatalf("accepted %d bytes but worker sent %d and stored %s", size, len(mesh.sent[0].Text), stored.Status)
			}
		})
	}
}

func TestReviewCumulative5SlowSubscriberDropsTerminalHintWithoutDisconnect(t *testing.T) {
	p := NewProcessor(nil, nil)
	ch, unsubscribe := p.Subscribe()
	defer unsubscribe()
	for i := 0; i < cap(ch); i++ {
		p.broadcast(transport.MeshEvent{Type: "packet"})
	}
	data, err := json.Marshal(map[string]string{"intent_id": "closed", "transition": "T14"})
	if err != nil {
		t.Fatal(err)
	}
	p.broadcast(transport.MeshEvent{Type: "delivery_intent", Data: data})
	count := 0
	for len(ch) > 0 {
		e := <-ch
		if e.Type == "delivery_intent" {
			t.Fatal("expected the full subscriber queue to lose the hint")
		}
		count++
	}
	p.broadcast(transport.MeshEvent{Type: "packet"})
	if e, ok := <-ch; !ok || e.Type != "packet" {
		t.Fatal("subscriber no longer connected")
	}
	t.Logf("queued=%d terminal_hint_dropped=true still_connected=true", count)
}
