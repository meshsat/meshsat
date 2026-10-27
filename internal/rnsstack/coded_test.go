package rnsstack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/reticulum"
	"meshsat/internal/rns"
	"meshsat/internal/routing"
)

// A kit joins its workspace's private Reticulum network on the Hub with an
// access-coded tcp_rns row [MESHSAT-1722]. These tests build the bridge the
// way main.go does (interface manager, coded set, registry, stack) and put
// a node with the same access code on the far end of a real TCP connection,
// where the Hub's private node sits.

const (
	testNetname = "meshsat-0a1b2c3d"
	testPass    = "QX7ZK2M4PAN6EHV3RJW5TB8YCDGFLS2N"
)

// privateNet is the far end: one connection, one node with the code.
type privateNet struct {
	ln     net.Listener
	node   *rns.Node
	member *rns.Destination
	got    chan string

	mu       sync.Mutex
	conn     net.Conn
	frames   [][]byte
	accepted atomic.Int32
}

func (p *privateNet) Transmit(_ string, raw []byte) error {
	p.mu.Lock()
	c := p.conn
	p.mu.Unlock()
	if c == nil {
		return errors.New("no connection")
	}
	_, err := c.Write(reticulum.HDLCFrame(raw))
	return err
}
func (p *privateNet) Floodable() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == nil {
		return nil
	}
	return []string{"srv"}
}
func (p *privateNet) HWMTU(string) int   { return routing.TCPRNSHWMTU }
func (p *privateNet) Bitrate(string) int { return 0 }

// writeRaw puts a frame on the wire as it is, without the node's code.
func (p *privateNet) writeRaw(raw []byte) {
	p.mu.Lock()
	c := p.conn
	p.mu.Unlock()
	if c != nil {
		_, _ = c.Write(reticulum.HDLCFrame(raw))
	}
}

func (p *privateNet) allCoded() (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, f := range p.frames {
		if !reticulum.HasIFACFlag(f) {
			return len(p.frames), false
		}
	}
	return len(p.frames), true
}

func startPrivateNet(t *testing.T, ctx context.Context) *privateNet {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	id, err := reticulum.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	p := &privateNet{ln: ln, got: make(chan string, 16)}
	p.node, err = rns.New(rns.Config{Identity: id}, p)
	if err != nil {
		t.Fatal(err)
	}
	f, err := reticulum.NewIFAC(testNetname, testPass, reticulum.DefaultIFACSize)
	if err != nil {
		t.Fatal(err)
	}
	p.node.SetIFAC("srv", f)
	p.member = p.node.AddDestination(&rns.Destination{Name: "test.member", ProveAll: true,
		OnPacket: func(plain []byte, _ *rns.Packet) { p.got <- string(plain) }})
	p.node.Start(ctx)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.conn = c
			p.mu.Unlock()
			p.accepted.Add(1)
			go func(c net.Conn) {
				defer c.Close()
				r := reticulum.NewHDLCFrameReader()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					for _, fr := range r.Feed(buf[:n]) {
						p.mu.Lock()
						p.frames = append(p.frames, append([]byte(nil), fr...))
						p.mu.Unlock()
						p.node.Inbound(append([]byte(nil), fr...), "srv")
					}
				}
			}(c)
		}
	}()
	return p
}

type recSink struct {
	mu      sync.Mutex
	senders map[string]bool
	packets map[string]int
}

func (s *recSink) InjectReticulumPacket(_ []byte, iface string) {
	s.mu.Lock()
	s.packets[iface]++
	s.mu.Unlock()
}
func (s *recSink) RegisterPacketSender(id string, _ func(ctx context.Context, data []byte) error) {
	s.mu.Lock()
	s.senders[id] = true
	s.mu.Unlock()
}
func (s *recSink) UnregisterPacketSender(id string) {
	s.mu.Lock()
	delete(s.senders, id)
	s.mu.Unlock()
}

// meshTap is mesh_0 in the registry: it records what the node puts on it.
type meshTap struct {
	mu     sync.Mutex
	frames [][]byte
}

func (m *meshTap) send(_ context.Context, p []byte) error {
	m.mu.Lock()
	m.frames = append(m.frames, append([]byte(nil), p...))
	m.mu.Unlock()
	return nil
}

func (m *meshTap) carried(dest [rns.HashLen]byte) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.frames {
		if bytes.Contains(f, dest[:]) {
			return true
		}
	}
	return false
}

type kit struct {
	st        *Stack
	mgr       *routing.IfaceManager
	coded     *routing.CodedIfaces
	reg       *routing.InterfaceRegistry
	sink      *recSink
	mesh      *meshTap
	db        *database.DB
	id        string
	mirrored  atomic.Int32 // announces handed to the legacy tables
	bridgeGot chan string
}

// newKit boots like main.go: the interfaces load before the node exists.
func newKit(t *testing.T, ctx context.Context, port int) *kit {
	t.Helper()
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	k := &kit{db: db, reg: routing.NewInterfaceRegistry(), coded: routing.NewCodedIfaces(),
		sink: &recSink{senders: map[string]bool{}, packets: map[string]int{}}, mesh: &meshTap{}, bridgeGot: make(chan string, 16)}
	k.reg.Register(routing.NewReticulumInterface("mesh_0", reticulum.IfaceMesh, 230, k.mesh.send))
	k.mgr = routing.NewIfaceManager(routing.IfaceManagerConfig{DB: db, Registry: k.reg, Coded: k.coded, Sink: k.sink})
	if err := k.mgr.Load(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k.mgr.StopAll)
	cfg := fmt.Sprintf(`{"host":"127.0.0.1","port":%d,"ifac_netname":%q,"ifac_passphrase":%q}`, port, testNetname, testPass)
	if k.id, err = k.mgr.Create(routing.DynTypeTCPRNS, json.RawMessage(cfg), true); err != nil {
		t.Fatal(err)
	}
	ident, err := routing.NewIdentity(db)
	if err != nil {
		t.Fatal(err)
	}
	k.st, err = Build(ctx, Config{Identity: ident, Registry: k.reg, Coded: k.coded, DB: db, AcceptLinks: true,
		OnAnnounce: func(*reticulum.Announce, []byte, string) { k.mirrored.Add(1) },
		OnPacket:   func(plain []byte, _ *rns.Packet) { k.bridgeGot <- string(plain) },
		LXMF:       LXMFConfig{Enabled: true, DisplayName: "kit"}})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestCodedRow_JoinsThePrivateNetworkAsALeaf(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := startPrivateNet(t, ctx)
	k := newKit(t, ctx, p.ln.Addr().(*net.TCPAddr).Port)

	// The row connects and announces the kit's own destinations in the code.
	waitUntil(t, "the private network learns the kit's LXMF address", func() bool {
		return p.node.Paths().Has(k.st.LXMF.Dest.Hash) && p.node.Paths().Has(k.st.Bridge.Hash)
	})
	if n, ok := p.allCoded(); !ok {
		t.Fatalf("a frame without the access code left on the coded row (%d frames)", n)
	}
	if !k.st.Node.IsLeaf(k.id) {
		t.Fatal("the coded row is not a leaf of the node")
	}
	// Nothing else in the bridge can address the row.
	if k.reg.Get(k.id) != nil {
		t.Fatal("the coded row is in the interface registry")
	}
	k.sink.mu.Lock()
	sender := k.sink.senders[k.id]
	k.sink.mu.Unlock()
	if sender {
		t.Fatal("the coded row has a packet sender in the processor")
	}
	if err := k.reg.Send(k.id, []byte{0x00, 0x00}); err == nil {
		t.Fatal("the registry sent on the coded row")
	}
	if err := k.coded.Send(k.id, []byte{0x00, 0x00, 0x01}); !errors.Is(err, routing.ErrUncodedFrame) {
		t.Fatalf("an uncoded frame on the coded row: %v", err)
	}

	// The member announces: the kit learns it on the row and keeps it there.
	if err := p.node.Announce(p.member, "", false); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the kit learns the member", func() bool { return k.st.Node.NextHopInterface(p.member.Hash) == k.id })
	time.Sleep(1500 * time.Millisecond) // past the node's rebroadcast window
	if k.mesh.carried(p.member.Hash) {
		t.Fatal("the member's announce was rebroadcast onto mesh_0")
	}
	if k.mirrored.Load() != 0 {
		t.Fatal("an announce heard on the coded row reached the legacy tables")
	}
	if rows, err := k.db.GetRNSPaths(); err != nil || len(rows) != 0 {
		t.Fatalf("a path through the coded row was persisted (%d rows, %v)", len(rows), err)
	}
	k.sink.mu.Lock()
	injected := k.sink.packets[k.id]
	k.sink.mu.Unlock()
	if injected != 0 {
		t.Fatalf("%d frames from the coded row reached the processor", injected)
	}

	// A node on the mesh announces: the private network never hears of it.
	meshID, _ := reticulum.GenerateIdentity()
	ann, err := reticulum.NewAnnounce(meshID, "test.mesh", nil)
	if err != nil {
		t.Fatal(err)
	}
	meshDest := meshID.DestHash("test.mesh")
	k.st.Node.Inbound(ann.MarshalPacket(), "mesh_0")
	waitUntil(t, "the kit learns the mesh node", func() bool { return k.st.Node.Paths().Has(meshDest) })
	if p.node.RequestPath(ctx, meshDest, 2*time.Second) || p.node.Paths().Has(meshDest) {
		t.Fatal("the private network got a path to a node on the mesh")
	}

	// The kit's own traffic flows both ways.
	if _, err := k.st.Node.SendData(p.member.Hash, []byte("kit to member"), false); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-p.got:
		if got != "kit to member" {
			t.Fatalf("member got %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the kit's packet did not reach the member")
	}
	if _, err := p.node.SendData(k.st.Bridge.Hash, []byte("member to kit"), false); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-k.bridgeGot:
		if got != "member to kit" {
			t.Fatalf("kit got %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the member's packet did not reach the kit")
	}

	// A frame without the code from the far end is refused by the node.
	before := k.st.Node.Stats().IFACViolations
	other, _ := reticulum.GenerateIdentity()
	stranger, _ := reticulum.NewAnnounce(other, "test.stranger", nil)
	p.writeRaw(stranger.MarshalPacket())
	waitUntil(t, "the uncoded frame is refused", func() bool { return k.st.Node.Stats().IFACViolations > before })
	if k.st.Node.Paths().Has(other.DestHash("test.stranger")) {
		t.Fatal("an uncoded announce was learned on the coded row")
	}
}

func TestCodedRow_StatusUpdateAndRemoval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := startPrivateNet(t, ctx)
	k := newKit(t, ctx, p.ln.Addr().(*net.TCPAddr).Port)
	waitUntil(t, "connected", func() bool { return p.node.Paths().Has(k.st.LXMF.Dest.Hash) })

	st, err := k.mgr.Get(k.id)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Coded || strings.Contains(string(st.Config), testPass) || !strings.Contains(st.Summary, "network "+testNetname) {
		t.Fatalf("status shows %+v", st)
	}

	// A config read back from the API (no passphrase) and saved keeps it.
	if err := k.mgr.Update(k.id, st.Config, true); err != nil {
		t.Fatal(err)
	}
	row, err := k.db.GetRoutingIface(k.id)
	if err != nil || !strings.Contains(row.Config, testPass) {
		t.Fatalf("the passphrase was lost on update: %v", err)
	}
	// The update restarted the row: the far end announces once the new
	// connection is up.
	waitUntil(t, "the row reconnects", func() bool { return p.accepted.Load() == 2 && k.coded.Online(k.id) })
	if err := p.node.Announce(p.member, "", false); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the kit learns the member again", func() bool { return k.st.Node.NextHopInterface(p.member.Hash) == k.id })

	// Removed: the node forgets the row and what it learned there.
	if err := k.mgr.Delete(k.id); err != nil {
		t.Fatal(err)
	}
	if k.coded.Has(k.id) || k.st.Node.IsLeaf(k.id) || k.st.Node.Paths().Has(p.member.Hash) {
		t.Fatal("the node kept the removed row")
	}
}
