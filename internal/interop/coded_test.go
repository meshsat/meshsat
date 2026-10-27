package interop

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/lxmf"
	"meshsat/internal/rns"
	"meshsat/internal/rnsstack"
	"meshsat/internal/routing"
)

// nodeSink is the processor's part for the uncoded rows: every frame to the
// node first, as Processor.handleRoutingPacket does.
type nodeSink struct{ node atomic.Pointer[rns.Node] }

func (s *nodeSink) InjectReticulumPacket(p []byte, iface string) {
	if n := s.node.Load(); n != nil {
		n.Inbound(append([]byte(nil), p...), iface)
	}
}
func (s *nodeSink) RegisterPacketSender(string, func(context.Context, []byte) error) {}
func (s *nodeSink) UnregisterPacketSender(string)                                    {}

// A kit on its workspace's private network [MESHSAT-1722], against stock
// Python RNS: "island" is a transport node that requires a network name and
// passphrase on its TCP server (where the Hub's private node sits for a
// workspace), "mesh" is a node on one of the kit's other networks. The kit
// is built as main.go builds it and reaches island with an access-coded
// tcp_rns row and mesh with an ordinary one. The kit's own traffic crosses
// both; nothing crosses between them through the kit.
func TestInteropCodedRowIsALeafOfThePrivateNetwork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const netname, pass = "meshsat-0a1b2c3d", "QX7ZK2M4PAN6EHV3RJW5TB8YCDGFLS2N"
	islandPort, meshPort := freePort(t), freePort(t)
	island := startPeer(t, peerOpts{name: "island", transport: true, listen: islandPort, ifacNetname: netname, ifacPassphrase: pass})
	mesh := startPeer(t, peerOpts{name: "mesh", listen: meshPort})

	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reg := routing.NewInterfaceRegistry()
	coded := routing.NewCodedIfaces()
	sink := &nodeSink{}
	mgr := routing.NewIfaceManager(routing.IfaceManagerConfig{DB: db, Registry: reg, Coded: coded, Sink: sink})
	if err := mgr.Load(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.StopAll()
	codedID, err := mgr.Create(routing.DynTypeTCPRNS, json.RawMessage(fmt.Sprintf(
		`{"host":"127.0.0.1","port":%d,"ifac_netname":%q,"ifac_passphrase":%q}`, islandPort, netname, pass)), true)
	if err != nil {
		t.Fatal(err)
	}
	meshID, err := mgr.Create(routing.DynTypeTCPRNS, json.RawMessage(fmt.Sprintf(`{"host":"127.0.0.1","port":%d}`, meshPort)), true)
	if err != nil {
		t.Fatal(err)
	}
	ident, err := routing.NewIdentity(db)
	if err != nil {
		t.Fatal(err)
	}
	st, err := rnsstack.Build(ctx, rnsstack.Config{Identity: ident, Registry: reg, Coded: coded, DB: db, AcceptLinks: true,
		LXMF: rnsstack.LXMFConfig{Enabled: true, DisplayName: "kit"}})
	if err != nil {
		t.Fatal(err)
	}
	sink.node.Store(st.Node)
	var gotMu sync.Mutex
	var got []string
	st.SetInboundHook(func(source, text string) {
		gotMu.Lock()
		got = append(got, source+"|"+text)
		gotMu.Unlock()
	})
	kitLXMF := st.LXMF.HashHex()

	// The coded row announces the kit on connect, in the code island needs.
	island.wait("announce", func(e event) bool { return e["dest"] == kitLXMF }, 20*time.Second)
	waitFor(t, "mesh row up", 10*time.Second, func() bool { return reg.Get(meshID) != nil && reg.Get(meshID).IsOnline() })
	if err := st.LXMF.Announce(); err != nil {
		t.Fatal(err)
	}
	mesh.wait("announce", func(e event) bool { return e["dest"] == kitLXMF }, 20*time.Second)

	// Both sides announce; the kit learns each on its own row.
	island.send(event{"cmd": "announce"})
	mesh.send(event{"cmd": "announce"})
	waitFor(t, "kit learns island", 15*time.Second, func() bool { return st.Node.NextHopInterface(h16(island.Delivery)) == codedID })
	waitFor(t, "kit learns mesh", 15*time.Second, func() bool { return st.Node.NextHopInterface(h16(mesh.Delivery)) == meshID })

	// Neither side hears of the other through the kit, by announce or by
	// asking.
	time.Sleep(3 * time.Second)
	island.send(event{"cmd": "request_path", "dest": mesh.Delivery})
	mesh.send(event{"cmd": "request_path", "dest": island.Delivery})
	time.Sleep(4 * time.Second)
	island.send(event{"cmd": "has_path", "dest": mesh.Delivery})
	if ev := island.wait("path", func(e event) bool { return e["dest"] == mesh.Delivery }, 10*time.Second); ev["known"] == true {
		t.Fatalf("the private network got a path to the mesh through the kit: %v", ev)
	}
	mesh.send(event{"cmd": "has_path", "dest": island.Delivery})
	if ev := mesh.wait("path", func(e event) bool { return e["dest"] == island.Delivery }, 10*time.Second); ev["known"] == true {
		t.Fatalf("the mesh got a path to the private network through the kit: %v", ev)
	}
	if st.Node.Stats().LeafRefused == 0 {
		t.Fatal("the kit counted no refusal")
	}

	// The kit's own LXMF traffic crosses the coded row both ways.
	island.send(event{"cmd": "send_lxm", "dest": kitLXMF, "content": "hello kit", "title": "island", "method": "opportunistic"})
	island.wait("lxm_delivered", nil, 20*time.Second)
	waitFor(t, "kit files the island's message", 10*time.Second, func() bool {
		gotMu.Lock()
		defer gotMu.Unlock()
		for _, g := range got {
			if strings.HasPrefix(g, island.Delivery+"|") && strings.HasSuffix(g, "hello kit") {
				return true
			}
		}
		return false
	})
	m, _, err := st.LXMF.Send(ctx, h16(island.Delivery), []byte("hello island"), []byte("kit"), lxmf.MapOf(), 0)
	if err != nil {
		t.Fatal(err)
	}
	rcv := island.wait("lxm_received", nil, 20*time.Second)
	if rcv["content"] != "hello island" || rcv["hash"] != hex.EncodeToString(m.Hash[:]) || rcv["signature_validated"] != true {
		t.Fatalf("island got %v", rcv)
	}
}
