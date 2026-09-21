package engine

import (
	"encoding/json"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/rules"
	"meshsat/internal/transport"
)

// ownNodeMesh is a MeshTransport stub that only names the local radio.
type ownNodeMesh struct {
	transport.MeshTransport
	id string
}

func (m *ownNodeMesh) LocalNodeID() string { return m.id }

// A kit relays a satellite "yo" onto its mesh; a handheld answers "yo" a few
// minutes later. The answer is a new message and must go up, only the radio's
// own copy of the injected text is an echo. [MESHSAT-1282]
func TestHandleMessage_ReplyRepeatingAnInjectedTextIsForwarded(t *testing.T) {
	const own, handheld = uint32(0xde11f199), uint32(0xa1b3c3a4)
	for _, tc := range []struct {
		name  string
		local string
		from  uint32
		want  int
	}{
		{"reply from another node", "!de11f199", handheld, 1},
		{"echo from the local radio", "!de11f199", own, 0},
		{"sender unknown", "!de11f199", 0, 0},
		{"local node not known yet", "", handheld, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, db := setupTestDispatcher(t)
			defer db.Close()
			for id, ct := range map[string]string{"mesh_0": "mesh", "iridium_imt_0": "iridium_imt"} {
				if _, err := db.Exec(`INSERT OR IGNORE INTO interfaces (id, channel_type, label, enabled, config, ingress_transforms, egress_transforms)
					VALUES (?, ?, ?, 1, '{}', '[]', '[]')`, id, ct, id); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.InsertAccessRule(&database.AccessRule{InterfaceID: "mesh_0", Direction: "ingress", Priority: 1, Name: "ttc:imt",
				Enabled: true, Action: "forward", ForwardTo: "iridium_imt_0", Filters: "{}"}); err != nil {
				t.Fatal(err)
			}
			ae := rules.NewAccessEvaluator(db)
			if err := ae.ReloadFromDB(); err != nil {
				t.Fatal(err)
			}
			d.SetAccessEvaluator(ae)

			p := NewProcessor(db, &ownNodeMesh{id: tc.local})
			p.SetDispatcher(d)
			p.markGatewayInjection("yo")

			data, _ := json.Marshal(transport.MeshMessage{ID: 397785434, From: tc.from, To: 0xffffffff,
				PortNum: int(transport.PortNumTextMessage), PortNumName: "TEXT_MESSAGE_APP", DecodedText: "yo"})
			p.handleMessage(transport.MeshEvent{Type: "message", Data: data})

			dl, err := db.GetPendingDeliveries("iridium_imt_0", 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(dl) != tc.want {
				t.Fatalf("%d deliveries to iridium_imt_0, want %d", len(dl), tc.want)
			}
		})
	}
}
