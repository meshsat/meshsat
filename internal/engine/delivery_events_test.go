package engine

import (
	"context"
	"encoding/json"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// decodeDeliveryData asserts the Data payload every delivery_* event
// carries, and returns it for site-specific checks. [MESHSAT-826]
func decodeDeliveryData(t *testing.T, ev transport.MeshEvent, wantID int64, wantChannel, wantRef, wantStatus, wantDest string) map[string]interface{} {
	t.Helper()
	if len(ev.Data) == 0 {
		t.Fatalf("%s: event carries no Data", ev.Type)
	}
	var data map[string]interface{}
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		t.Fatalf("%s: Data is not JSON: %v", ev.Type, err)
	}
	if got := int64(data["id"].(float64)); got != wantID {
		t.Errorf("%s: id = %d, want %d", ev.Type, got, wantID)
	}
	if data["channel"] != wantChannel {
		t.Errorf("%s: channel = %v, want %s", ev.Type, data["channel"], wantChannel)
	}
	if data["msg_ref"] != wantRef {
		t.Errorf("%s: msg_ref = %v, want %s", ev.Type, data["msg_ref"], wantRef)
	}
	if data["status"] != wantStatus {
		t.Errorf("%s: status = %v, want %s", ev.Type, data["status"], wantStatus)
	}
	if data["destination"] != wantDest {
		t.Errorf("%s: destination = %v, want %q", ev.Type, data["destination"], wantDest)
	}
	if ev.Time == "" {
		t.Errorf("%s: Time not set", ev.Type)
	}
	return data
}

func TestDeliveryEvents_SuccessCarriesDataAndLatency(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "mesh_0", "mesh", true)

	var emitted []transport.MeshEvent
	ring := NewPacketRing(10, nil)
	w := newTestWorker(h, "mesh_0", "mesh")
	w.emit = func(e transport.MeshEvent) { emitted = append(emitted, e) }
	w.packets = ring

	id, _ := h.db.InsertDelivery(database.MessageDelivery{
		MsgRef: "evt-mesh", Channel: "mesh_0", Status: "queued", Priority: 1,
		Payload: []byte("to the mesh"), TextPreview: "to the mesh", MaxRetries: 3,
		Visited: "[]", QoSLevel: 1, Destination: "!a1b2c3d4",
	})
	// Hand-built like the worker's own batch would be, without created_at:
	// deliver() must recover it from the row for latency_ms.
	del := database.MessageDelivery{
		ID: id, MsgRef: "evt-mesh", Channel: "mesh_0", Status: "queued", Priority: 1,
		Payload: []byte("to the mesh"), TextPreview: "to the mesh", MaxRetries: 3,
		Visited: "[]", QoSLevel: 1, Destination: "!a1b2c3d4",
	}
	w.deliver(context.Background(), del)

	if len(emitted) != 1 {
		t.Fatalf("expected one event, got %d", len(emitted))
	}
	ev := emitted[0]
	if ev.Type != "delivery_acked" {
		t.Errorf("QoS 1 success must emit delivery_acked, got %s", ev.Type)
	}
	data := decodeDeliveryData(t, ev, id, "mesh_0", "evt-mesh", "delivered", "!a1b2c3d4")
	lat, ok := data["latency_ms"].(float64)
	if !ok || lat < 0 {
		t.Errorf("latency_ms missing or negative: %v", data["latency_ms"])
	}
	if data["ack_status"] != "acked" {
		t.Errorf("ack_status = %v", data["ack_status"])
	}

	// The mesh send also landed in the packet feed as a lora tx record.
	recs := ring.Newest(0, "lora", "tx")
	if len(recs) != 1 {
		t.Fatalf("expected one lora tx record, got %d", len(recs))
	}
	if recs[0].To != "!a1b2c3d4" || recs[0].Text != "to the mesh" || recs[0].MsgRef != "evt-mesh" || recs[0].Iface != "mesh_0" {
		t.Errorf("unexpected tx record %+v", recs[0])
	}
}

func TestDeliveryEvents_RetryAndDeadCarryData(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "sms_0", "cellular", true)
	gw := h.addGateway("sms_0", "cellular")
	gw.failNext = true

	var emitted []transport.MeshEvent
	w := newTestWorker(h, "sms_0", "cellular")
	w.emit = func(e transport.MeshEvent) { emitted = append(emitted, e) }

	// Retry path (retries remain).
	id, _ := h.db.InsertDelivery(database.MessageDelivery{
		MsgRef: "evt-retry", Channel: "sms_0", Status: "queued", Priority: 1,
		Payload: []byte("x"), TextPreview: "x", MaxRetries: 3, Visited: "[]", QoSLevel: 1,
		Destination: "+31600000000",
	})
	del := database.MessageDelivery{
		ID: id, MsgRef: "evt-retry", Channel: "sms_0", Status: "queued", Priority: 1,
		Payload: []byte("x"), TextPreview: "x", MaxRetries: 3, Visited: "[]", QoSLevel: 1,
		Destination: "+31600000000",
	}
	w.deliver(context.Background(), del)
	if len(emitted) != 1 || emitted[0].Type != "delivery_retry" {
		t.Fatalf("expected delivery_retry, got %+v", emitted)
	}
	data := decodeDeliveryData(t, emitted[0], id, "sms_0", "evt-retry", "retry", "+31600000000")
	if data["error"] != errDeliveryFailed.Error() || data["retries"].(float64) != 1 || data["next_retry"] == "" {
		t.Errorf("retry extras: %v", data)
	}

	// Dead path (last retry).
	emitted = nil
	gw.failNext = true
	id2, _ := h.db.InsertDelivery(database.MessageDelivery{
		MsgRef: "evt-dead", Channel: "sms_0", Status: "queued", Priority: 1,
		Payload: []byte("y"), TextPreview: "y", MaxRetries: 2, Retries: 1, Visited: "[]", QoSLevel: 1,
	})
	del2 := database.MessageDelivery{
		ID: id2, MsgRef: "evt-dead", Channel: "sms_0", Status: "queued", Priority: 1,
		Payload: []byte("y"), TextPreview: "y", MaxRetries: 2, Retries: 1, Visited: "[]", QoSLevel: 1,
	}
	w.deliver(context.Background(), del2)
	if len(emitted) != 1 || emitted[0].Type != "delivery_dead" {
		t.Fatalf("expected delivery_dead, got %+v", emitted)
	}
	data = decodeDeliveryData(t, emitted[0], id2, "sms_0", "evt-dead", "dead", "")
	if data["error"] != errDeliveryFailed.Error() {
		t.Errorf("dead extras: %v", data)
	}

	// QoS 0 dead path.
	emitted = nil
	gw.failNext = true
	id3, _ := h.db.InsertDelivery(database.MessageDelivery{
		MsgRef: "evt-qos0", Channel: "sms_0", Status: "queued", Priority: 1,
		Payload: []byte("z"), TextPreview: "z", MaxRetries: 0, Visited: "[]", QoSLevel: 0,
	})
	del3 := database.MessageDelivery{
		ID: id3, MsgRef: "evt-qos0", Channel: "sms_0", Status: "queued", Priority: 1,
		Payload: []byte("z"), TextPreview: "z", MaxRetries: 0, Visited: "[]", QoSLevel: 0,
	}
	w.deliver(context.Background(), del3)
	if len(emitted) != 1 || emitted[0].Type != "delivery_dead" {
		t.Fatalf("expected delivery_dead for QoS 0, got %+v", emitted)
	}
	decodeDeliveryData(t, emitted[0], id3, "sms_0", "evt-qos0", "dead", "")
}

func TestDeliveryEvents_QueuedCarriesData(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "aprs_0", "aprs", true)

	var emitted []transport.MeshEvent
	h.dispatch.SetEmitter(func(e transport.MeshEvent) { emitted = append(emitted, e) })

	id, ref, err := h.dispatch.QueueDirectSendTo("aprs_0", "ping", DirectSendOptions{Destination: "MSPRLX-10"})
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if len(emitted) != 1 || emitted[0].Type != "delivery_queued" {
		t.Fatalf("expected delivery_queued, got %+v", emitted)
	}
	decodeDeliveryData(t, emitted[0], id, "aprs_0", ref, "queued", "MSPRLX-10")
}

func TestParseDeliveryTime(t *testing.T) {
	tests := []struct {
		in   string
		ok   bool
		year int
	}{
		{"", false, 0},
		{"2026-09-06T16:51:48Z", true, 2026},        // modernc DATETIME scan
		{"2026-09-06T16:51:48.123456Z", true, 2026}, // RFC3339Nano
		{"2026-09-06 16:51:48", true, 2026},         // SQLite CURRENT_TIMESTAMP text
		{"yesterday", false, 0},
	}
	for _, tc := range tests {
		got, ok := parseDeliveryTime(tc.in)
		if ok != tc.ok {
			t.Errorf("%q: ok = %v, want %v", tc.in, ok, tc.ok)
			continue
		}
		if ok && got.Year() != tc.year {
			t.Errorf("%q: year = %d", tc.in, got.Year())
		}
	}
}
