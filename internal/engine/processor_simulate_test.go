package engine

import (
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/rules"
	"meshsat/internal/transport"
)

// A simulated inbound mesh text takes the radio path: it is persisted and the
// access rules queue its relay with the text as payload. [MESHSAT-857]
func TestSimulateInbound_RelaysThroughRules(t *testing.T) {
	d, db := setupTestDispatcher(t)
	db.InsertInterface(&database.Interface{ID: "mesh_0", ChannelType: "mesh", Enabled: true})
	db.InsertInterface(&database.Interface{ID: "cellular_0", ChannelType: "cellular", Enabled: true})
	db.InsertAccessRule(&database.AccessRule{InterfaceID: "mesh_0", Direction: "ingress", Name: "relay", Enabled: true,
		Priority: 10, Action: "forward", ForwardTo: "cellular_0", Filters: `{"portnums":"[1]"}`})
	ae := rules.NewAccessEvaluator(db)
	if err := ae.ReloadFromDB(); err != nil {
		t.Fatal(err)
	}
	d.SetAccessEvaluator(ae)
	p := NewProcessor(db, nil)
	p.SetDispatcher(d)
	if err := p.SimulateInbound(transport.MeshMessage{From: 0x00c0ffee, DecodedText: "booth check 1"}); err != nil {
		t.Fatal(err)
	}
	dl, err := db.GetPendingDeliveries("cellular_0", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(dl) != 1 || string(dl[0].Payload) != "booth check 1" {
		t.Fatalf("want one relay delivery carrying the text, got %d: %q", len(dl), payloadOf(dl))
	}
	msgs, _, err := db.GetMessages(database.MessageFilter{Limit: 5})
	if err != nil || len(msgs) == 0 {
		t.Fatalf("simulated message not persisted: %v", err)
	}
}
