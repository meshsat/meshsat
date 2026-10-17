package engine

import (
	"strings"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/rules"
)

// B12 (r3 F8, F13): a relay over a bearer's size limit is queued whole,
// never as DTN fragments nothing reassembles; a custody payload this node
// takes is queued where a worker sends it, or not taken. [MESHSAT-1584]

func TestDispatchAccess_ALongRelayIsOneWholeRowNotFragments(t *testing.T) {
	d, db := setupTestDispatcher(t)
	for _, i := range []database.Interface{
		{ID: "mesh_0", ChannelType: "mesh"}, {ID: "cellular_0", ChannelType: "cellular"},
		{ID: "iridium_0", ChannelType: "iridium"}, {ID: "zigbee_0", ChannelType: "zigbee"},
	} {
		i.Enabled = true
		db.InsertInterface(&i)
	}
	dests := []string{"cellular_0", "iridium_0", "zigbee_0"}
	for i, dest := range dests {
		db.InsertAccessRule(&database.AccessRule{InterfaceID: "mesh_0", Direction: "ingress", Name: dest, Enabled: true,
			Priority: i + 1, Action: "forward", ForwardTo: dest, Filters: "{}"})
	}
	ae := rules.NewAccessEvaluator(db)
	if err := ae.ReloadFromDB(); err != nil {
		t.Fatal(err)
	}
	d.SetAccessEvaluator(ae)
	text := strings.Repeat("long relay ", 20)[:200]                                                      // over SMS (160) and ZigBee (100)
	envelope := []byte(`{"from":1,"to":4294967295,"text":"x","pad":"` + strings.Repeat("y", 400) + `"}`) // over SBD (340) and ZigBee (100)
	if n := d.DispatchAccess("mesh_0", rules.RouteMessage{Text: text, From: "!00000001", PortNum: 1}, envelope); n != len(dests) {
		t.Fatalf("%d deliveries; want one per destination (%d)", n, len(dests))
	}
	for _, dest := range dests {
		dl, _ := db.GetPendingDeliveries(dest, 10)
		if len(dl) != 1 {
			t.Fatalf("%s: %d rows; want one whole row", dest, len(dl))
		}
		if strings.HasPrefix(dl[0].TextPreview, "[frag") {
			t.Fatalf("%s: a fragment row %q", dest, dl[0].TextPreview[:12])
		}
		want := text
		if !isTextBearer(strings.TrimSuffix(dest, "_0")) {
			want = string(envelope) // a byte bearer (SBD, ZigBee) keeps the envelope
		}
		if string(dl[0].Payload) != want {
			t.Fatalf("%s: payload %d B; want the whole %d B", dest, len(dl[0].Payload), len(want))
		}
	}
}

func TestAcceptCustody_QueuesOnTheMeshInterfaceOrTakesNothing(t *testing.T) {
	d, db := setupTestDispatcher(t)
	db.InsertInterface(&database.Interface{ID: "mesh_0", ChannelType: "mesh", Enabled: true})
	id, err := d.AcceptCustody([]byte("custody payload"))
	if err != nil || id == 0 {
		t.Fatalf("accept: %d %v", id, err)
	}
	dl, _ := db.GetPendingDeliveries("mesh_0", 10)
	if len(dl) != 1 || dl[0].ID != id || dl[0].TextPreview != "custody payload" {
		t.Fatalf("mesh_0 queue: %+v; want the payload where mesh_0's worker sends it", dl)
	}
	if stray, _ := db.GetPendingDeliveries("mesh", 10); len(stray) != 0 {
		t.Fatalf("%d rows on channel mesh, which no worker serves", len(stray))
	}

	// No enabled mesh interface: nothing queued, an error (no ACK goes).
	d2, db2 := setupTestDispatcher(t)
	if _, err := db2.Exec(`DELETE FROM interfaces`); err != nil {
		t.Fatal(err)
	}
	if err := db2.InsertInterface(&database.Interface{ID: "mesh_0", ChannelType: "mesh", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if id, err := d2.AcceptCustody([]byte("nowhere")); err == nil || id != 0 {
		t.Fatalf("accept with the mesh off: %d %v; want an error", id, err)
	}
	var n int
	_ = db2.QueryRow(`SELECT COUNT(*) FROM message_deliveries`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows queued with no mesh to send them", n)
	}
}
