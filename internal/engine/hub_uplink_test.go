package engine

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/hubreporter"
)

// A hub_uplink direct send keeps the binary payload and uses the text as the
// preview only; the class bypasses egress policy. [MESHSAT-963]
func TestQueueDirectSendTo_HubUplinkPayload(t *testing.T) {
	d, db := setupTestDispatcher(t)
	frame := []byte{0x4D, 0x53, 1, 3, 0xff, 0x00, 0x10}
	id, _, err := d.QueueDirectSendTo("iridium_0", "hub uplink frame, 7 B",
		DirectSendOptions{Class: database.DeliveryClassHubUplink, Payload: frame})
	if err != nil {
		t.Fatal(err)
	}
	del, err := db.GetDelivery(id)
	if err != nil {
		t.Fatal(err)
	}
	if string(del.Payload) != string(frame) {
		t.Fatalf("payload not kept: %x", del.Payload)
	}
	if del.TextPreview != "hub uplink frame, 7 B" || del.Class != database.DeliveryClassHubUplink {
		t.Fatalf("preview/class: %q %q", del.TextPreview, del.Class)
	}
	if !database.DeliveryClassBypassesPolicy(del.Class) || database.DeliveryClassBypassesPolicy(database.DeliveryClassMessage) {
		t.Fatal("bypass policy wrong")
	}
}

// Critical queues a direct send at priority 0, as MeshSat Android queues an
// SOS leg (the Bridge: the SOS's Hub frame); without it the row keeps
// priority 1. [MESHSAT-1430]
func TestQueueDirectSendTo_CriticalIsPriorityZero(t *testing.T) {
	d, db := setupTestDispatcher(t)
	for _, tc := range []struct {
		critical bool
		want     int
	}{{false, 1}, {true, 0}} {
		id, _, err := d.QueueDirectSendTo("iridium_0", "frame", DirectSendOptions{
			Class: database.DeliveryClassHubUplink, Payload: []byte{0x4D, 0x53, 1, 1}, Critical: tc.critical,
		})
		if err != nil {
			t.Fatal(err)
		}
		del, err := db.GetDelivery(id)
		if err != nil {
			t.Fatal(err)
		}
		if del.Priority != tc.want {
			t.Fatalf("critical %v: priority %d, want %d", tc.critical, del.Priority, tc.want)
		}
	}
}

// The alarm test's position frame reaches the satellite gateway exactly as
// queued, with a chain on iridium_0 that encrypts and base64s everything
// else: raw bytes on PRIVATE_APP, no text, not marked encrypted, so the Hub's
// uplink decoder finds the frame's own magic. The same bytes queued as an
// ordinary message go through the chain; that is what class hub_uplink
// spares the frame. [MESHSAT-1430]
func TestHubUplinkFrame_SatelliteSkipsTheTransformChain(t *testing.T) {
	h := setupE2E(t)
	t.Cleanup(func() { h.db.Close() })
	h.addInterface(t, "iridium_0", "iridium", true)
	h.setOnline("iridium_0")
	chain := `[{"type":"encrypt","params":{"key":"` + strings.Repeat("c3", 32) + `"}},{"type":"base64"}]`
	if _, err := h.db.Exec(`UPDATE interfaces SET egress_transforms = ? WHERE id = 'iridium_0'`, chain); err != nil {
		t.Fatal(err)
	}
	gw := h.addGateway("iridium_0", "iridium")
	w := newTestWorker(h, "iridium_0", "iridium")
	w.transforms = NewTransformPipeline()

	frame := hubreporter.EncodeSatPosition("msa-flaneur", 52.1601, 4.4970, 3.9, 1, time.Unix(1790000000, 0))
	deliver := func(text string, opts DirectSendOptions) *database.MessageDelivery {
		t.Helper()
		id, _, err := h.dispatch.QueueDirectSendTo("iridium_0", text, opts)
		if err != nil {
			t.Fatal(err)
		}
		del, err := h.db.GetDelivery(id)
		if err != nil {
			t.Fatal(err)
		}
		w.deliver(context.Background(), *del)
		after, err := h.db.GetDelivery(id)
		if err != nil {
			t.Fatal(err)
		}
		return after
	}

	// Queued as POST /api/sos/test queues it (Routine, priority 1, 30 minutes).
	sent := deliver("Alarm test: position report to the Hub", DirectSendOptions{
		Precedence: "Routine", Class: database.DeliveryClassHubUplink, Payload: frame, TTLSeconds: 1800,
	})
	if sent.Status != "sent" && sent.Status != "delivered" {
		t.Fatalf("frame delivery: %s %q", sent.Status, sent.LastError)
	}
	msgs := gw.messages()
	if len(msgs) != 1 {
		t.Fatalf("%d messages at the gateway, want 1", len(msgs))
	}
	if m := msgs[0]; !bytes.Equal(m.RawPayload, frame) || m.DecodedText != "" || m.Encrypted || m.PortNum != 256 {
		t.Fatalf("the gateway got raw %x, text %q, encrypted %v, port %d; want the frame %x as it was queued",
			m.RawPayload, m.DecodedText, m.Encrypted, m.PortNum, frame)
	}

	// The control: the same bytes as an ordinary message are encrypted.
	deliver("control", DirectSendOptions{Payload: frame})
	msgs = gw.messages()
	if len(msgs) != 2 {
		t.Fatalf("%d messages at the gateway, want 2", len(msgs))
	}
	if m := msgs[1]; !m.Encrypted || len(m.RawPayload) != 0 || strings.Contains(m.DecodedText, string(frame)) {
		t.Fatalf("the ordinary message was not run through the chain: encrypted %v, raw %x, text %q", m.Encrypted, m.RawPayload, m.DecodedText)
	}
}
