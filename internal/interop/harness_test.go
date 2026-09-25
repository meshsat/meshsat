package interop

// Interoperability harness: an in-process RNS node (internal/rns) on a real
// TCP interface, talking to upstream Python RNS peers (testdata/rnsnode.py)
// from the pinned venv. See internal/interop/rnsenv.

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/interop/rnsenv"
	"meshsat/internal/reticulum"
	"meshsat/internal/rns"
	"meshsat/internal/routing"
)

// ---------------------------------------------------------------------------
// Python peer
// ---------------------------------------------------------------------------

type event map[string]any

type peer struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	events chan event
	mu     sync.Mutex
	log    []event
	// from the ready event
	Identity, Delivery, Raw, TransportID string
}

type peerOpts struct {
	transport bool
	connect   string
	listen    int
	name      string
	stampCost int
	enforce   bool
	loglevel  int
}

func scriptPath(t *testing.T) string {
	_, file, _, _ := runtime.Caller(0)
	p := filepath.Join(filepath.Dir(file), "testdata", "rnsnode.py")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("rnsnode.py not found: %v", err)
	}
	return p
}

// startPeerWithConfig runs rnsnode.py with a caller-written RNS config.
func startPeerWithConfig(t *testing.T, py, cfgDir, config, name string) *peer {
	t.Helper()
	if err := os.WriteFile(filepath.Join(cfgDir, "config"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	return startPeerArgs(t, py, cfgDir, []string{"--config-dir", cfgDir, "--keep-config", "--name", name}, name)
}

func startPeer(t *testing.T, o peerOpts) *peer {
	t.Helper()
	py := rnsenv.Python(t)
	cfgDir := t.TempDir()
	args := []string{scriptPath(t), "--config-dir", cfgDir}
	if o.transport {
		args = append(args, "--transport", "yes")
	}
	if o.connect != "" {
		args = append(args, "--connect", o.connect)
	}
	if o.listen != 0 {
		args = append(args, "--listen", fmt.Sprint(o.listen))
	}
	if o.name != "" {
		args = append(args, "--name", o.name)
	}
	if o.stampCost > 0 {
		args = append(args, "--stamp-cost", fmt.Sprint(o.stampCost))
	}
	if o.enforce {
		args = append(args, "--enforce-stamps")
	}
	if o.loglevel > 0 {
		args = append(args, "--loglevel", fmt.Sprint(o.loglevel))
	}
	return startPeerArgs(t, py, cfgDir, args[1:], o.name)
}

func startPeerArgs(t *testing.T, py, cfgDir string, args []string, name string) *peer {
	t.Helper()
	args = append([]string{scriptPath(t)}, args...)
	o := peerOpts{name: name}
	cmd := exec.Command(py, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start rnsnode.py: %v", err)
	}
	p := &peer{t: t, cmd: cmd, stdin: stdin, events: make(chan event, 256)}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var ev event
			if json.Unmarshal(sc.Bytes(), &ev) == nil {
				p.mu.Lock()
				p.log = append(p.log, ev)
				p.mu.Unlock()
				p.events <- ev
			}
		}
		close(p.events)
	}()
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			t.Logf("[py %s] %s", o.name, sc.Text())
		}
	}()
	t.Cleanup(func() {
		if t.Failed() {
			if data, err := os.ReadFile(filepath.Join(cfgDir, "rns.log")); err == nil {
				lines := strings.Split(strings.TrimSpace(string(data)), "\n")
				if len(lines) > 60 {
					lines = lines[len(lines)-60:]
				}
				t.Logf("[py %s rns.log tail]\n%s", o.name, strings.Join(lines, "\n"))
			}
		}
		p.send(event{"cmd": "quit"})
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
		}
	})
	ready := p.wait("ready", nil, 20*time.Second)
	p.Identity = ready["identity"].(string)
	p.Delivery = ready["delivery"].(string)
	p.Raw = ready["raw"].(string)
	p.TransportID = ready["transport_id"].(string)
	if ready["rns"] != rnsenv.RNSVersion {
		t.Fatalf("peer runs RNS %v, want %s", ready["rns"], rnsenv.RNSVersion)
	}
	return p
}

func (p *peer) send(cmd event) {
	b, _ := json.Marshal(cmd)
	p.stdin.Write(append(b, '\n'))
}

// wait returns the first event named name matching pred (nil = any). Events
// already received are searched first, so a fast peer never races the test.
func (p *peer) wait(name string, pred func(event) bool, timeout time.Duration) event {
	p.t.Helper()
	p.mu.Lock()
	for i, ev := range p.log {
		if ev["event"] == name && (pred == nil || pred(ev)) {
			p.log = append(p.log[:i:i], p.log[i+1:]...)
			p.mu.Unlock()
			return ev
		}
	}
	p.mu.Unlock()
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-p.events:
			if !ok {
				p.t.Fatalf("peer exited while waiting for %s", name)
			}
			if ev["event"] == name && (pred == nil || pred(ev)) {
				// remove from log (it was appended by the reader)
				p.mu.Lock()
				for i := len(p.log) - 1; i >= 0; i-- {
					if &p.log[i] == &ev || fmt.Sprint(p.log[i]) == fmt.Sprint(ev) {
						p.log = append(p.log[:i:i], p.log[i+1:]...)
						break
					}
				}
				p.mu.Unlock()
				return ev
			}
			if ev["event"] == "error" {
				p.t.Logf("peer error event: %v", ev["error"])
			}
		case <-deadline:
			p.t.Fatalf("timeout waiting for %s from peer", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Bridge-side node on a TCP interface
// ---------------------------------------------------------------------------

type tcpTx struct {
	iface *routing.TCPInterface
	id    string
}

func (x *tcpTx) Transmit(ifaceID string, raw []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return x.iface.Send(ctx, raw)
}
func (x *tcpTx) Floodable() []string {
	if x.iface.IsOnline() {
		return []string{x.id}
	}
	return nil
}
func (x *tcpTx) HWMTU(string) int   { return 262144 }
func (x *tcpTx) Bitrate(string) int { return 0 }

type bridge struct {
	node  *rns.Node
	tcp   *routing.TCPInterface
	port  int
	ident *reticulum.Identity
	dest  *rns.Destination // "interop.raw" style destination, accepts links, proves all
	got   chan string
	links chan *rns.Link
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}

func newBridge(t *testing.T, ctx context.Context, connect string) *bridge {
	t.Helper()
	id, _ := reticulum.GenerateIdentity()
	port := freePort(t)
	var node *rns.Node
	cfg := routing.TCPInterfaceConfig{Name: "tcp_0", ListenAddr: fmt.Sprintf("127.0.0.1:%d", port), ConnectAddr: connect, Reconnect: true, ReconnectInterval: time.Second}
	tcp := routing.NewTCPInterface(cfg, func(packet []byte) { node.Inbound(packet, "tcp_0") })
	n, err := rns.New(rns.Config{Identity: id}, &tcpTx{iface: tcp, id: "tcp_0"})
	if err != nil {
		t.Fatal(err)
	}
	node = n
	b := &bridge{node: n, tcp: tcp, port: port, ident: id, got: make(chan string, 16), links: make(chan *rns.Link, 16)}
	b.dest = n.AddDestination(&rns.Destination{Name: "interop.raw", AcceptLinks: true, ProveAll: true,
		OnPacket:     func(plain []byte, pkt *rns.Packet) { b.got <- string(plain) },
		OnLink:       func(l *rns.Link) { b.links <- l },
		OnLinkPacket: func(l *rns.Link, plain []byte, pkt *rns.Packet) { b.got <- string(plain) },
	})
	if err := tcp.Start(ctx); err != nil {
		t.Fatal(err)
	}
	n.Start(ctx)
	t.Cleanup(tcp.Stop)
	return b
}

func h16(s string) [16]byte {
	b, _ := hex.DecodeString(s)
	var out [16]byte
	copy(out[:], b)
	return out
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestInteropAnnounceBothWays(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := newBridge(t, ctx, "")
	p := startPeer(t, peerOpts{name: "A", connect: fmt.Sprintf("127.0.0.1:%d", b.port)})
	waitFor(t, "tcp peer", 10*time.Second, func() bool { return b.tcp.PeerCount() > 0 })

	// Python -> bridge
	p.send(event{"cmd": "announce"})
	waitFor(t, "bridge learns python raw dest", 10*time.Second, func() bool { return b.node.Paths().Hops(h16(p.Raw)) == 1 })
	waitFor(t, "bridge learns python lxmf dest", 10*time.Second, func() bool { return b.node.Paths().Hops(h16(p.Delivery)) == 1 })
	if k := b.node.Recall(h16(p.Delivery)); k == nil || len(k.AppData) == 0 {
		t.Fatalf("LXMF announce app data not stored")
	}
	// Bridge -> Python, twice: the second (newer) announce must be accepted too.
	if err := b.node.Announce(b.dest, "", false); err != nil {
		t.Fatal(err)
	}
	dh := hex.EncodeToString(b.dest.Hash[:])
	ev := p.wait("announce", func(e event) bool { return e["dest"] == dh }, 10*time.Second)
	if ev["hops"].(float64) != 1 {
		t.Fatalf("python sees bridge at %v hops, want 1", ev["hops"])
	}
	time.Sleep(1100 * time.Millisecond) // a newer emission timestamp
	if err := b.node.Announce(b.dest, "", false); err != nil {
		t.Fatal(err)
	}
	p.wait("announce", func(e event) bool { return e["dest"] == dh }, 10*time.Second)
	p.send(event{"cmd": "has_path", "dest": dh})
	pe := p.wait("path", nil, 5*time.Second)
	if pe["known"] != true || pe["hops"].(float64) != 1 {
		t.Fatalf("python path to bridge: %v", pe)
	}
}

func TestInteropPacketAndProofBothWays(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := newBridge(t, ctx, "")
	p := startPeer(t, peerOpts{name: "A", connect: fmt.Sprintf("127.0.0.1:%d", b.port)})
	waitFor(t, "tcp peer", 10*time.Second, func() bool { return b.tcp.PeerCount() > 0 })
	p.send(event{"cmd": "announce"})
	waitFor(t, "bridge learns python", 10*time.Second, func() bool { return b.node.Paths().Hops(h16(p.Raw)) == 1 })
	b.node.Announce(b.dest, "", false)
	dh := hex.EncodeToString(b.dest.Hash[:])
	p.wait("announce", func(e event) bool { return e["dest"] == dh }, 10*time.Second)

	// Python -> bridge single packet, bridge proves it.
	p.send(event{"cmd": "send_packet", "dest": dh, "data": "ping from python"})
	select {
	case got := <-b.got:
		if got != "ping from python" {
			t.Fatalf("got %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("bridge did not receive the packet")
	}
	p.wait("packet_proved", nil, 10*time.Second)

	// Bridge -> Python single packet, python proves it.
	r, err := b.node.SendData(h16(p.Raw), []byte("ping from bridge"), true)
	if err != nil {
		t.Fatal(err)
	}
	ev := p.wait("packet_received", nil, 10*time.Second)
	if ev["data"] != "ping from bridge" {
		t.Fatalf("python got %v", ev)
	}
	select {
	case res := <-r.Done():
		if !res.Delivered {
			t.Fatalf("bridge receipt not delivered")
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("no proof from python")
	}
}

func TestInteropLinkPythonToBridge(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := newBridge(t, ctx, "")
	p := startPeer(t, peerOpts{name: "A", connect: fmt.Sprintf("127.0.0.1:%d", b.port)})
	waitFor(t, "tcp peer", 10*time.Second, func() bool { return b.tcp.PeerCount() > 0 })
	b.node.Announce(b.dest, "", false)
	dh := hex.EncodeToString(b.dest.Hash[:])
	p.wait("announce", func(e event) bool { return e["dest"] == dh }, 10*time.Second)

	p.send(event{"cmd": "link", "dest": dh})
	ev := p.wait("link_established", nil, 15*time.Second)
	linkID := ev["link"].(string)
	var l *rns.Link
	select {
	case l = <-b.links:
	case <-time.After(10 * time.Second):
		t.Fatalf("bridge link not active")
	}
	if hex.EncodeToString(l.ID[:]) != linkID {
		t.Fatalf("link id mismatch")
	}
	// Data on the link, proved by the bridge (PROVE_ALL).
	p.send(event{"cmd": "link_send", "link": linkID, "data": "link data from python"})
	select {
	case got := <-b.got:
		if got != "link data from python" {
			t.Fatalf("got %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("bridge did not receive link data")
	}
	p.wait("link_proof", nil, 10*time.Second)
	// Identify, then keepalive round trip at a short interval, then close.
	p.send(event{"cmd": "link_identify", "link": linkID})
	waitFor(t, "identified", 10*time.Second, func() bool { return l.RemoteIdentity() != nil })
	p.send(event{"cmd": "set_keepalive", "link": linkID, "seconds": 5})
	p.wait("keepalive_set", nil, 5*time.Second)
	// One keepalive interval plus margin: the bridge must answer python's
	// 0xFF with 0xFE and the link must still be active on both ends.
	for i := 0; i < 8; i++ {
		time.Sleep(time.Second)
		t.Logf("t+%ds bridge link state %s", i+1, l.State())
	}
	if l.State() != rns.LinkActive {
		t.Fatalf("link state after keepalive cycle: %s", l.State())
	}
	p.send(event{"cmd": "link_close", "link": linkID})
	waitFor(t, "bridge sees close", 10*time.Second, func() bool { return b.node.Links().Get(l.ID) == nil })
}

func TestInteropLinkBridgeToPython(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := newBridge(t, ctx, "")
	p := startPeer(t, peerOpts{name: "A", connect: fmt.Sprintf("127.0.0.1:%d", b.port)})
	waitFor(t, "tcp peer", 10*time.Second, func() bool { return b.tcp.PeerCount() > 0 })
	p.send(event{"cmd": "announce"})
	waitFor(t, "bridge learns python", 10*time.Second, func() bool { return b.node.Paths().Hops(h16(p.Raw)) == 1 })

	l, err := b.node.Links().Initiate(h16(p.Raw))
	if err != nil {
		t.Fatal(err)
	}
	est := make(chan struct{}, 1)
	l.OnEstablished = func(*rns.Link) { est <- struct{}{} }
	select {
	case <-est:
	case <-time.After(15 * time.Second):
		t.Fatalf("link not established: %s", l.State())
	}
	ev := p.wait("link_established", nil, 10*time.Second)
	if ev["link"] != hex.EncodeToString(l.ID[:]) {
		t.Fatalf("python link id %v", ev["link"])
	}
	r, err := b.node.Links().Send(l, []byte("hello over link"))
	if err != nil {
		t.Fatal(err)
	}
	pe := p.wait("link_packet", nil, 10*time.Second)
	if pe["data"] != "hello over link" {
		t.Fatalf("python got %v", pe)
	}
	select {
	case res := <-r.Done():
		if !res.Delivered {
			t.Fatalf("link packet not proved by python")
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("no link proof")
	}
	if err := b.node.Links().Identify(l, b.ident); err != nil {
		t.Fatal(err)
	}
	ie := p.wait("link_identified", nil, 10*time.Second)
	ih := b.ident.IdentityHash()
	if ie["identity"] != hex.EncodeToString(ih[:]) {
		t.Fatalf("python identified %v", ie["identity"])
	}
	b.node.Links().Teardown(l)
	p.wait("link_closed", nil, 10*time.Second)
}

// Python A <-> bridge (transport) <-> Python B. A and B never connect to each other.
func TestInteropThreeNodeTransport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := newBridge(t, ctx, "")
	pa := startPeer(t, peerOpts{name: "A", connect: fmt.Sprintf("127.0.0.1:%d", b.port)})
	pb := startPeer(t, peerOpts{name: "B", connect: fmt.Sprintf("127.0.0.1:%d", b.port)})
	waitFor(t, "two tcp peers", 10*time.Second, func() bool { return b.tcp.PeerCount() >= 2 })

	pa.send(event{"cmd": "announce"})
	// B hears A's raw destination through the bridge's HEADER_2 rebroadcast at 2 hops.
	ev := pb.wait("announce", func(e event) bool { return e["dest"] == pa.Raw }, 15*time.Second)
	if ev["hops"].(float64) != 2 {
		t.Fatalf("B sees A at %v hops, want 2", ev["hops"])
	}
	pb.send(event{"cmd": "has_path", "dest": pa.Raw})
	pe := pb.wait("path", nil, 5*time.Second)
	bid := b.node.IdentityHash()
	if pe["next_hop"] != hex.EncodeToString(bid[:]) {
		t.Fatalf("B's next hop is %v, want the bridge identity %x", pe["next_hop"], bid)
	}

	// B -> A single packet through the bridge, proof back via the reverse table.
	pb.send(event{"cmd": "send_packet", "dest": pa.Raw, "data": "B to A through the bridge"})
	got := pa.wait("packet_received", nil, 15*time.Second)
	if got["data"] != "B to A through the bridge" || got["hops"].(float64) != 2 {
		t.Fatalf("A got %v", got)
	}
	pb.wait("packet_proved", nil, 15*time.Second)

	// B -> A link through the bridge: request in transit, proof validated in transit, data repeated both ways.
	pb.send(event{"cmd": "link", "dest": pa.Raw})
	le := pb.wait("link_established", nil, 20*time.Second)
	linkID := le["link"].(string)
	pa.wait("link_established", func(e event) bool { return e["link"] == linkID }, 20*time.Second)
	pb.send(event{"cmd": "link_send", "link": linkID, "data": "on the transit link"})
	lp := pa.wait("link_packet", nil, 15*time.Second)
	if lp["data"] != "on the transit link" {
		t.Fatalf("A got %v", lp)
	}
	pb.wait("link_proof", nil, 15*time.Second)
	pb.send(event{"cmd": "link_close", "link": linkID})
	pa.wait("link_closed", func(e event) bool { return e["link"] == linkID }, 15*time.Second)

	// Path request: B asks for the bridge's own destination it has not heard.
	// (The bridge announced nothing yet.)
	dh := hex.EncodeToString(b.dest.Hash[:])
	pb.send(event{"cmd": "request_path", "dest": dh})
	pb.wait("path_requested", nil, 5*time.Second)
	waitFor(t, "bridge counts the path request", 10*time.Second, func() bool { return b.node.Stats().PathRequestsRx >= 1 })
	pb.wait("announce", func(e event) bool { return e["dest"] == dh }, 15*time.Second) // the path response
	pb.send(event{"cmd": "has_path", "dest": dh})
	pe2 := pb.wait("path", nil, 5*time.Second)
	if pe2["known"] != true || pe2["hops"].(float64) != 1 {
		t.Fatalf("B path to bridge after path request: %v", pe2)
	}
}

// The bridge behind a stock rnsd transport: it learns a peer at 2 hops and
// inserts the rnsd identity as next hop.
func TestInteropBehindStockRNSD(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	port := freePort(t)
	tr := startPeer(t, peerOpts{name: "T", transport: true, listen: port})
	pa := startPeer(t, peerOpts{name: "A", connect: fmt.Sprintf("127.0.0.1:%d", port)})
	b := newBridge(t, ctx, fmt.Sprintf("127.0.0.1:%d", port))
	waitFor(t, "bridge connected", 15*time.Second, func() bool { return b.tcp.IsOnline() })
	time.Sleep(500 * time.Millisecond)

	pa.send(event{"cmd": "announce"})
	waitFor(t, "bridge learns A via T", 20*time.Second, func() bool { return b.node.Paths().Hops(h16(pa.Raw)) == 2 })
	if e := b.node.Paths().Get(h16(pa.Raw)); hex.EncodeToString(e.NextHop[:]) != tr.TransportID {
		t.Fatalf("next hop %x, want rnsd transport id %s", e.NextHop, tr.TransportID)
	}
	// Bridge -> A through rnsd with proof back.
	r, err := b.node.SendData(h16(pa.Raw), []byte("bridge to A via rnsd"), true)
	if err != nil {
		t.Fatal(err)
	}
	got := pa.wait("packet_received", nil, 15*time.Second)
	if got["data"] != "bridge to A via rnsd" {
		t.Fatalf("A got %v", got)
	}
	select {
	case res := <-r.Done():
		if !res.Delivered {
			t.Fatalf("no proof through rnsd")
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("proof timeout")
	}
	// Bridge announces; A must see it at 2 hops via rnsd.
	b.node.Announce(b.dest, "", false)
	dh := hex.EncodeToString(b.dest.Hash[:])
	ev := pa.wait("announce", func(e event) bool { return e["dest"] == dh }, 15*time.Second)
	if ev["hops"].(float64) != 2 {
		t.Fatalf("A sees bridge at %v hops via rnsd, want 2", ev["hops"])
	}
	// A -> bridge link through rnsd.
	pa.send(event{"cmd": "link", "dest": dh})
	pa.wait("link_established", nil, 20*time.Second)
	select {
	case <-b.links:
	case <-time.After(15 * time.Second):
		t.Fatalf("bridge link via rnsd not active")
	}
}
