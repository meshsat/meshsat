package engine

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/gateway"
)

// Public warnings on the kit (MESHSAT-1734): a digest a satellite link
// delivers is handed to the relay before anything else reads it, and an
// alert-class delivery goes to the mesh on ALERT_APP with BEL.

func TestAWarningDigestOverSatelliteIsConsumedBeforeTheRules(t *testing.T) {
	p, db, _, gw := newInboundAuthHarness(t, "iridium_0", "iridium", "[]")
	consumed := make(chan string, 2)
	p.SetWarningInbound(func(_ context.Context, ifaceID string, wire []byte) bool {
		if len(wire) < 2 || wire[0] != 0x57 || wire[1] != 0x01 {
			return false
		}
		consumed <- ifaceID
		return true
	})
	digest := append([]byte{0x57, 0x01, 0x01, 0x02}, make([]byte, 100)...)
	gw.ch <- gateway.InboundMessage{Source: "iridium", FromAddr: "300234067500220", Text: string(digest)}
	select {
	case id := <-consumed:
		if id != "iridium_0" {
			t.Fatalf("relay saw %q, want iridium_0", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the relay never saw the digest")
	}
	// Not a digest: the relay says no and the text goes on as before (the
	// booth relay rule forwards it to the mesh).
	gw.ch <- gateway.InboundMessage{Source: "iridium", FromAddr: "300234067500220", Text: "hello from the web form"}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM message_deliveries`).Scan(&n); err == nil && n > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("a plain satellite text no longer reaches the rules")
}

func TestAnAlertClassDeliveryGoesToTheMeshOnPort11WithBEL(t *testing.T) {
	db := testDB(t)
	mesh := &mockMeshTransport{}
	d := NewDispatcher(db, channel.NewRegistry(), nil, mesh)
	w := &DeliveryWorker{db: db, channelID: "mesh_0", mesh: mesh, gate: &d.gate}
	id, _, replayed, err := d.QueueDirectSendKeyed("warnings", "warn:abc:alert:alert", "fp", "mesh_0", "WARNING Wind Severe",
		DirectSendOptions{Class: database.DeliveryClassAlert})
	if err != nil || replayed {
		t.Fatalf("queue: %v %v", err, replayed)
	}
	del, err := db.GetDelivery(id)
	if err != nil {
		t.Fatal(err)
	}
	w.deliver(context.Background(), *del)
	if after, _ := db.GetDelivery(id); after.Status != "sent" && after.Status != "delivered" {
		t.Fatalf("the alert delivery is %s", after.Status)
	}
	if len(mesh.sent) != 0 || len(mesh.raw) != 1 || mesh.raw[0].PortNum != 11 {
		t.Fatalf("mesh: text sends %d, raw %+v", len(mesh.sent), mesh.raw)
	}
	payload, _ := base64.StdEncoding.DecodeString(mesh.raw[0].Payload)
	if string(payload) != "WARNING Wind Severe\x07" {
		t.Fatalf("payload %q", payload)
	}
	// The same key again queues nothing: one row, one send.
	id2, _, replayed, err := d.QueueDirectSendKeyed("warnings", "warn:abc:alert:alert", "fp", "mesh_0", "WARNING Wind Severe",
		DirectSendOptions{Class: database.DeliveryClassAlert})
	if err != nil || !replayed || id2 != id {
		t.Fatalf("replay: %v %v %d/%d", err, replayed, id2, id)
	}
	// A plain-class text of the same words goes as a text.
	id3, _, _, err := d.QueueDirectSendKeyed("warnings", "warn:abc:alert:text", "fp", "mesh_0", "WARNING Wind Severe",
		DirectSendOptions{Class: database.DeliveryClassPlain})
	if err != nil {
		t.Fatal(err)
	}
	del3, _ := db.GetDelivery(id3)
	w.deliver(context.Background(), *del3)
	if len(mesh.sent) != 1 || mesh.sent[0].Text != "WARNING Wind Severe" || len(mesh.raw) != 1 {
		t.Fatalf("mesh after the text: sent %+v raw %d", mesh.sent, len(mesh.raw))
	}
}
