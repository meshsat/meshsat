package engine

import (
	"context"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/rules"
	"meshsat/internal/transport"
)

// Tests for the per-delivery destination and delivery class that OOB
// management replies rely on. [MESHSAT-756]

func TestQueueDirectSendTo_SetsColumns(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "cellular_0", "cellular", true)

	id, ref, err := h.dispatch.QueueDirectSendTo("cellular_0", "MS:frame", DirectSendOptions{
		Precedence: "Priority", Destination: "+31653207829", Class: database.DeliveryClassOOB, MaxRetries: 5,
	})
	if err != nil || ref == "" {
		t.Fatalf("queue: %v", err)
	}
	del, err := h.db.GetDelivery(id)
	if err != nil {
		t.Fatal(err)
	}
	if del.Destination != "+31653207829" || del.Class != database.DeliveryClassOOB || del.MaxRetries != 5 || del.Precedence != "Priority" {
		t.Fatalf("row: %+v", del)
	}

	// The legacy entry point keeps its defaults.
	id2, _, err := h.dispatch.QueueDirectSend("cellular_0", "hello", "")
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := h.db.GetDelivery(id2)
	if plain.Destination != "" || plain.Class != database.DeliveryClassMessage || plain.MaxRetries != 3 {
		t.Fatalf("legacy row: %+v", plain)
	}
}

func TestDeliver_OOBClassBypassesEgressRules(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "cellular_0", "cellular", true)
	h.setOnline("cellular_0")
	gw := h.addGateway("cellular_0", "cellular")

	// An explicit drop rule with no filters denies every ordinary egress.
	if _, err := h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "cellular_0", Direction: "egress", Name: "block all SMS", Enabled: true,
		Priority: 10, Action: "drop", Filters: "{}",
	}); err != nil {
		t.Fatal(err)
	}
	ae := rules.NewAccessEvaluator(h.db)
	if err := ae.ReloadFromDB(); err != nil {
		t.Fatal(err)
	}
	if !ae.HasEgressRules("cellular_0") {
		t.Fatal("fixture: no egress rules loaded")
	}
	w := newTestWorker(h, "cellular_0", "cellular")
	w.access = ae
	ctx := context.Background()

	// Message class: denied, nothing forwarded.
	mid, _, _ := h.dispatch.QueueDirectSend("cellular_0", "hello", "")
	mdel, _ := h.db.GetDelivery(mid)
	w.deliver(ctx, *mdel)
	if after, _ := h.db.GetDelivery(mid); after.Status != "denied" {
		t.Fatalf("message class status %q, want denied", after.Status)
	}
	if n := len(gw.messages()); n != 0 {
		t.Fatalf("message class forwarded %d times", n)
	}

	// OOB class: bypasses the gate, forwarded verbatim to the sender address.
	oid, _, _ := h.dispatch.QueueDirectSendTo("cellular_0", "MS:frame", DirectSendOptions{
		Destination: "+31653207829", Class: database.DeliveryClassOOB, MaxRetries: 5,
	})
	odel, _ := h.db.GetDelivery(oid)
	w.deliver(ctx, *odel)
	// QoS 1 marks a successful gateway forward as delivered straight away.
	if after, _ := h.db.GetDelivery(oid); after.Status != "sent" && after.Status != "delivered" {
		t.Fatalf("oob class status %q, want sent or delivered", after.Status)
	}
	msgs := gw.messages()
	if len(msgs) != 1 {
		t.Fatalf("oob class forwarded %d times, want 1", len(msgs))
	}
	got := msgs[0]
	if !got.RawText || got.Destination != "+31653207829" || got.DecodedText != "MS:frame" {
		t.Fatalf("forwarded message: raw=%v dest=%q text=%q", got.RawText, got.Destination, got.DecodedText)
	}
	if len(got.SMSDestinations) != 1 || got.SMSDestinations[0] != "+31653207829" {
		t.Fatalf("sms destinations %v", got.SMSDestinations)
	}
}

func TestDeliver_OOBClassSkipsEgressTransforms(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "cellular_0", "cellular", true)
	h.setOnline("cellular_0")
	gw := h.addGateway("cellular_0", "cellular")
	if _, err := h.db.Exec(`UPDATE interfaces SET egress_transforms = ? WHERE id = 'cellular_0'`, `[{"type":"base64"}]`); err != nil {
		t.Fatal(err)
	}
	w := newTestWorker(h, "cellular_0", "cellular")
	w.transforms = h.dispatch.TransformPipeline()
	ctx := context.Background()

	mid, _, _ := h.dispatch.QueueDirectSend("cellular_0", "hello", "")
	mdel, _ := h.db.GetDelivery(mid)
	w.deliver(ctx, *mdel)

	oid, _, _ := h.dispatch.QueueDirectSendTo("cellular_0", "MS:frame", DirectSendOptions{Class: database.DeliveryClassOOB})
	odel, _ := h.db.GetDelivery(oid)
	w.deliver(ctx, *odel)

	msgs := gw.messages()
	if len(msgs) != 2 {
		t.Fatalf("forwarded %d, want 2", len(msgs))
	}
	if msgs[0].DecodedText == "hello" {
		t.Fatal("message class was not transformed")
	}
	if msgs[1].DecodedText != "MS:frame" || msgs[1].Encrypted {
		t.Fatalf("oob class altered: %q encrypted=%v", msgs[1].DecodedText, msgs[1].Encrypted)
	}
}

func TestDeliver_MeshDestinationSetsTo(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "mesh_0", "mesh", true)
	h.setOnline("mesh_0")
	w := newTestWorker(h, "mesh_0", "mesh")
	ctx := context.Background()

	oid, _, _ := h.dispatch.QueueDirectSendTo("mesh_0", "MS:frame", DirectSendOptions{Destination: "!aabbccdd", Class: database.DeliveryClassOOB})
	odel, _ := h.db.GetDelivery(oid)
	w.deliver(ctx, *odel)

	mid, _, _ := h.dispatch.QueueDirectSend("mesh_0", "hello", "")
	mdel, _ := h.db.GetDelivery(mid)
	w.deliver(ctx, *mdel)

	h.meshTx.mu.Lock()
	sent := append([]transport.SendRequest{}, h.meshTx.sent...)
	h.meshTx.mu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("mesh sends %d, want 2", len(sent))
	}
	if sent[0].To != "!aabbccdd" || sent[0].Text != "MS:frame" {
		t.Fatalf("directed mesh send: %+v", sent[0])
	}
	if sent[1].To != "" {
		t.Fatalf("plain mesh send should broadcast, got To=%q", sent[1].To)
	}
}

func TestSendToRecipient_RawAddressReachesRow(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "aprs_0", "aprs", true)

	res, err := h.dispatch.SendToRecipient(context.Background(),
		RecipientRef{Raw: &RawRecipient{InterfaceID: "aprs_0", Address: "PD0XYZ-7"}},
		[]byte("MS:frame"),
		SendOptions{Class: database.DeliveryClassOOB, MaxRetries: 5})
	if err != nil {
		t.Fatal(err)
	}
	ids := res.DeliveryIDs[directory.Kind("")]
	if len(ids) != 1 {
		t.Fatalf("delivery ids: %v", res.DeliveryIDs)
	}
	del, err := h.db.GetDelivery(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if del.Channel != "aprs_0" || del.Destination != "PD0XYZ-7" || del.Class != database.DeliveryClassOOB || del.MaxRetries != 5 {
		t.Fatalf("row: %+v", del)
	}
}
