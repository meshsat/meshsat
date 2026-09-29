package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
)

// A delivery the send gate refuses ends cancelled where it stands and never
// reaches the mesh; one it lets through goes. Without a gate, everything
// goes. The SOS refuses the legs of an SOS that was cancelled this way,
// even one whose send was under way and failed. [MESHSAT-1446]
func TestSendGate_ARefusedDeliveryIsCancelledNotSent(t *testing.T) {
	db := testDB(t)
	mesh := &mockMeshTransport{}
	d := NewDispatcher(db, channel.NewRegistry(), nil, mesh)
	d.SetSendGate(func(del database.MessageDelivery) bool { return !strings.HasPrefix(del.MsgRef, "sos-1-") })
	w := &DeliveryWorker{db: db, channelID: "mesh_0", mesh: mesh, gate: &d.gate}
	queue := func(ref string) *database.MessageDelivery {
		t.Helper()
		id, got, err := d.QueueDirectSendTo("mesh_0", "SOS: Anna needs help.", DirectSendOptions{
			MsgRef: ref, Class: database.DeliveryClassSOS, Critical: true, RetryForever: true,
		})
		if err != nil || got != ref {
			t.Fatalf("queue %s: %v (ref %q)", ref, err, got)
		}
		del, err := db.GetDelivery(id)
		if err != nil {
			t.Fatal(err)
		}
		return del
	}

	refused := queue("sos-1-mesh")
	w.deliver(context.Background(), *refused)
	if after, _ := db.GetDelivery(refused.ID); after.Status != "dead" || after.LastError != "cancelled" {
		t.Fatalf("the refused delivery is %s %q, want dead cancelled", after.Status, after.LastError)
	}
	if len(mesh.sent) != 0 {
		t.Fatalf("the mesh got %d sends from a refused delivery", len(mesh.sent))
	}

	allowed := queue("sos-2-mesh")
	w.deliver(context.Background(), *allowed)
	if after, _ := db.GetDelivery(allowed.ID); (after.Status != "sent" && after.Status != "delivered") || len(mesh.sent) != 1 {
		t.Fatalf("the allowed delivery is %s, the mesh got %d", after.Status, len(mesh.sent))
	}

	d.SetSendGate(nil)
	again := queue("sos-1-again")
	w.deliver(context.Background(), *again)
	if after, _ := db.GetDelivery(again.ID); after.Status != "sent" && after.Status != "delivered" {
		t.Fatalf("without a gate the delivery is %s", after.Status)
	}
}

// An SOS leg is queued as its sender asks: its own reference, priority 0,
// no deadline, and max_retries 0, which keeps it trying past any number of
// failures (MeshSat Android retries an SOS leg until it goes). An ordinary
// row still gives up after its retries. [MESHSAT-1446]
func TestRetryForever_KeepsTheLegTrying(t *testing.T) {
	db := testDB(t)
	mesh := &mockMeshTransport{down: true}
	d := NewDispatcher(db, channel.NewRegistry(), nil, mesh)
	w := &DeliveryWorker{db: db, channelID: "mesh_0", mesh: mesh, gate: &d.gate}
	old := time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05") // past the down-bearer grace

	leg, _, err := d.QueueDirectSendTo("mesh_0", "SOS: Anna needs help.", DirectSendOptions{
		MsgRef: "sos-7-mesh", Class: database.DeliveryClassSOS, Critical: true, RetryForever: true, TTLSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	plain, _, err := d.QueueDirectSendTo("mesh_0", "hello", DirectSendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{leg, plain} {
		if _, err := db.Exec(`UPDATE message_deliveries SET retries = 2, created_at = ? WHERE id = ?`, old, id); err != nil {
			t.Fatal(err)
		}
	}
	row, _ := db.GetDelivery(leg)
	if row.MaxRetries != 0 || row.Priority != 0 || row.ExpiresAt != nil || row.MsgRef != "sos-7-mesh" {
		t.Fatalf("the SOS leg: max_retries %d, priority %d, expires %v, ref %s", row.MaxRetries, row.Priority, row.ExpiresAt, row.MsgRef)
	}
	for _, id := range []int64{leg, plain} {
		del, _ := db.GetDelivery(id)
		w.deliver(context.Background(), *del)
	}
	if after, _ := db.GetDelivery(leg); after.Status != "retry" || after.Retries != 3 {
		t.Fatalf("the SOS leg after its third failure: %s, %d retries", after.Status, after.Retries)
	}
	if after, _ := db.GetDelivery(plain); after.Status != "dead" {
		t.Fatalf("the ordinary row after its third failure: %s", after.Status)
	}
}
