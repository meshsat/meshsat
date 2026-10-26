package engine

import (
	"encoding/json"
	"testing"
	"time"

	"meshsat/internal/transport"
)

// A crew's text to the kit's own node reaches the TAK chat's answer path; any
// other text does not. [MESHSAT-1705]
func TestMeshDirectTextHookSeesOnlyTextsToThisNode(t *testing.T) {
	const own, crew = uint32(0xde11f199), uint32(0xa1b3c3a4)
	for _, tc := range []struct {
		name    string
		local   string
		from    uint32
		to      uint32
		portnum int
		want    bool
	}{
		{"a crew's text to this node", "!de11f199", crew, own, transport.PortNumTextMessage, true},
		{"the same text to the channel", "!de11f199", crew, 0xffffffff, transport.PortNumTextMessage, false},
		{"a text to another node", "!de11f199", crew, 0x12345678, transport.PortNumTextMessage, false},
		{"the radio's own text", "!de11f199", own, own, transport.PortNumTextMessage, false},
		{"not a text", "!de11f199", crew, own, transport.PortNumPosition, false},
		{"local node not known yet", "", crew, own, transport.PortNumTextMessage, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, db := setupTestDispatcher(t)
			defer db.Close()
			p := NewProcessor(db, &ownNodeMesh{id: tc.local})
			got := make(chan [2]any, 1)
			p.SetMeshDirectTextHook(func(from uint32, text string) { got <- [2]any{from, text} })

			data, _ := json.Marshal(transport.MeshMessage{ID: 4242, From: tc.from, To: tc.to,
				PortNum: int(tc.portnum), DecodedText: "on my way"})
			p.handleMessage(transport.MeshEvent{Type: "message", Data: data})

			select {
			case g := <-got:
				if !tc.want {
					t.Fatalf("the hook saw %v", g)
				}
				if g[0].(uint32) != tc.from || g[1].(string) != "on my way" {
					t.Errorf("hook got %v", g)
				}
			case <-time.After(300 * time.Millisecond):
				if tc.want {
					t.Fatal("the hook never saw the crew's text")
				}
			}
		})
	}
}
