package engine

import (
	"testing"

	"meshsat/internal/database"
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
