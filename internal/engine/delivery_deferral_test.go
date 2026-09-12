package engine

import (
	"context"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// A bearer that is down at the moment of the attempt must not kill the message.
//
// On 12 Sep 2026 a T-Deck text was relayed kit to kit over APRS, reached the far
// kit, and was queued to that kit's own mesh radio while the radio was
// re-enumerating on the USB bus. The delivery was marked dead on its first
// attempt with "not connected", retries 0, max_retries 0 — the rule's QoS 0 made
// the first failure final. The radio came back 78 seconds later. [MESHSAT-1061]
func TestHandleFailure_BearerDownDefersWithoutConsumingARetry(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "mesh_0", "mesh", true)
	h.addGateway("mesh_0", "mesh")
	h.meshTx.setDown(true)

	var emitted []transport.MeshEvent
	w := newTestWorker(h, "mesh_0", "mesh")
	w.emit = func(e transport.MeshEvent) { emitted = append(emitted, e) }

	// QoS 0 with no retries left is exactly the shape of the row that died.
	row := database.MessageDelivery{
		MsgRef: "relayed-text", Channel: "mesh_0", Status: "queued", Priority: 10,
		Payload: []byte("oo"), TextPreview: "oo", MaxRetries: 0, Visited: "[\"aprs_0\"]", QoSLevel: 0,
	}
	id, err := h.db.InsertDelivery(row)
	if err != nil {
		t.Fatal(err)
	}
	row.ID = id

	w.deliver(context.Background(), row)

	got, err := h.db.GetDelivery(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == "dead" {
		t.Fatalf("a delivery to a down bearer was marked dead; that is the bug (last_error=%q)", got.LastError)
	}
	if got.Status != "retry" {
		t.Errorf("status = %q, want retry", got.Status)
	}
	if got.Retries != 0 {
		t.Errorf("retries = %d, want 0: a bearer being down says nothing about the message", got.Retries)
	}
	if got.NextRetry == nil {
		t.Error("next_retry not set, so nothing will pick the delivery up again")
	}
	if len(emitted) != 1 || emitted[0].Type != "delivery_deferred" {
		t.Fatalf("expected one delivery_deferred event, got %+v", emitted)
	}
}

// The deferral is bounded: a bearer that never comes back must not hold a
// message for ever. Past the window the delivery is treated as an ordinary
// failure and follows the usual QoS rules.
func TestHandleFailure_BearerDownPastWindowFallsThrough(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "mesh_0", "mesh", true)
	h.addGateway("mesh_0", "mesh")
	h.meshTx.setDown(true)

	orig := bearerDownDeferWindow
	bearerDownDeferWindow = -time.Second // everything is already past the window
	defer func() { bearerDownDeferWindow = orig }()

	row := database.MessageDelivery{
		MsgRef: "stale", Channel: "mesh_0", Status: "queued", Priority: 10,
		Payload: []byte("x"), TextPreview: "x", MaxRetries: 0, Visited: "[]", QoSLevel: 0,
	}
	id, err := h.db.InsertDelivery(row)
	if err != nil {
		t.Fatal(err)
	}
	row.ID = id

	w := newTestWorker(h, "mesh_0", "mesh")
	w.deliver(context.Background(), row)

	got, err := h.db.GetDelivery(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "dead" {
		t.Errorf("status = %q, want dead once the defer window is exhausted", got.Status)
	}
}

// End to end: queue while the bearer is down, bring it up, and the message must
// arrive without anyone re-sending it. This is the booth path — a relayed text
// landing on a kit whose radio is mid-heal.
func TestDeliveryWorker_BearerDownThenUp_Delivers(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "mesh_0", "mesh", true)
	h.addGateway("mesh_0", "mesh")
	h.meshTx.setDown(true)

	// Retry immediately once deferred, so the test does not sleep for the real
	// ten seconds.
	orig := bearerDownRetryWait
	bearerDownRetryWait = 0
	defer func() { bearerDownRetryWait = orig }()

	row := database.MessageDelivery{
		MsgRef: "booth-relay", Channel: "mesh_0", Status: "queued", Priority: 10,
		Payload: []byte("oo"), TextPreview: "oo", MaxRetries: 0, Visited: "[\"aprs_0\"]", QoSLevel: 0,
	}
	id, err := h.db.InsertDelivery(row)
	if err != nil {
		t.Fatal(err)
	}

	w := newTestWorker(h, "mesh_0", "mesh")

	// First pass: the radio is down. The delivery survives as a retry.
	w.processBatch(context.Background())
	got, err := h.db.GetDelivery(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == "dead" {
		t.Fatal("delivery died while the bearer was down")
	}
	if len(h.meshTx.messages()) != 0 {
		t.Fatal("nothing should have been forwarded while the bearer was down")
	}

	// The radio comes back.
	h.meshTx.setDown(false)

	w.processBatch(context.Background())

	got, err = h.db.GetDelivery(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "sent" && got.Status != "delivered" {
		t.Errorf("status = %q, want the message to go out once the bearer returned (last_error=%q)", got.Status, got.LastError)
	}
	if got := h.meshTx.messages(); len(got) != 1 {
		t.Errorf("forwarded %d messages, want 1", len(got))
	}
}
