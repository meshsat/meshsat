package interop

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meshsat/internal/lxmf"
)

func newLXMFBridge(t *testing.T, ctx context.Context, stampCost int) (*bridge, *lxmf.Router, chan *lxmf.Message) {
	b := newBridge(t, ctx, "")
	got := make(chan *lxmf.Message, 16)
	r := lxmf.New(b.node, lxmf.Config{Identity: b.ident, DisplayName: "MeshSat test kit", StampCost: stampCost,
		EnforceStamps: stampCost > 0, OnMessage: func(m *lxmf.Message) { got <- m }})
	return b, r, got
}

func TestInteropLXMFSinglePacketBothWays(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b, r, got := newLXMFBridge(t, ctx, 0)
	p := startPeer(t, peerOpts{name: "A", connect: fmt.Sprintf("127.0.0.1:%d", b.port)})
	waitFor(t, "tcp peer", 10*time.Second, func() bool { return b.tcp.PeerCount() > 0 })
	p.send(event{"cmd": "announce"})
	waitFor(t, "bridge learns python delivery dest", 10*time.Second, func() bool { return b.node.Paths().Hops(h16(p.Delivery)) == 1 })
	if err := r.Announce(); err != nil {
		t.Fatal(err)
	}
	dh := r.HashHex()
	ev := p.wait("announce", func(e event) bool { return e["dest"] == dh }, 10*time.Second)
	app, _ := hex.DecodeString(ev["app_data"].(string))
	if info := lxmf.ParseAnnounceAppData(app); info.DisplayName != "MeshSat test kit" {
		t.Fatalf("python saw app data %+v", info)
	}

	// Python -> bridge, opportunistic single packet, bridge proves it.
	p.send(event{"cmd": "send_lxm", "dest": dh, "content": "hello kit", "title": "t1", "method": "opportunistic"})
	sent := p.wait("lxm_sent", nil, 10*time.Second)
	select {
	case m := <-got:
		if string(m.Content) != "hello kit" || string(m.Title) != "t1" || !m.SignatureValid || m.Method != lxmf.Opportunistic {
			t.Fatalf("bridge got %+v", m)
		}
		if hex.EncodeToString(m.Hash[:]) != sent["hash"] {
			t.Fatalf("hash mismatch")
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("bridge did not receive the LXMF message")
	}
	p.wait("lxm_delivered", nil, 15*time.Second)

	// Bridge -> Python, opportunistic, python proves it.
	m, res, err := r.Send(ctx, h16(p.Delivery), []byte("hello python"), []byte("t2"), lxmf.MapOf(), 0)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if res.Method != lxmf.Opportunistic {
		t.Fatalf("method %s", res.Method)
	}
	rcv := p.wait("lxm_received", nil, 15*time.Second)
	if rcv["content"] != "hello python" || rcv["title"] != "t2" || rcv["signature_validated"] != true || rcv["hash"] != hex.EncodeToString(m.Hash[:]) {
		t.Fatalf("python got %v", rcv)
	}
	if peers := r.Peers(); len(peers) != 1 || peers[0].DestHash != p.Delivery || peers[0].DisplayName != "A" {
		t.Fatalf("peers: %+v", peers)
	}
}

func TestInteropLXMFDirectLinkBothWays(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b, r, got := newLXMFBridge(t, ctx, 0)
	p := startPeer(t, peerOpts{name: "A", connect: fmt.Sprintf("127.0.0.1:%d", b.port)})
	waitFor(t, "tcp peer", 10*time.Second, func() bool { return b.tcp.PeerCount() > 0 })
	p.send(event{"cmd": "announce"})
	waitFor(t, "bridge learns python", 10*time.Second, func() bool { return b.node.Paths().Hops(h16(p.Delivery)) == 1 })
	r.Announce()
	dh := r.HashHex()
	p.wait("announce", func(e event) bool { return e["dest"] == dh }, 10*time.Second)

	// Python -> bridge over a link (DIRECT), 310 bytes of content: a link packet.
	content := strings.Repeat("d", 310)
	p.send(event{"cmd": "send_lxm", "dest": dh, "content": content, "method": "direct"})
	select {
	case m := <-got:
		if string(m.Content) != content || m.Method != lxmf.Direct || !m.SignatureValid {
			t.Fatalf("bridge got method %s valid %v len %d", m.Method, m.SignatureValid, len(m.Content))
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("bridge did not receive the direct LXMF message")
	}
	p.wait("lxm_delivered", nil, 15*time.Second)

	// Bridge -> Python over a link the bridge opens, then a second one that
	// must reuse it.
	m, res, err := r.Send(ctx, h16(p.Delivery), []byte(strings.Repeat("b", 300)), nil, lxmf.MapOf(), 0)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if res.Method != lxmf.Direct {
		t.Fatalf("method %s", res.Method)
	}
	rcv := p.wait("lxm_received", nil, 20*time.Second)
	if rcv["hash"] != hex.EncodeToString(m.Hash[:]) || rcv["signature_validated"] != true {
		t.Fatalf("python got %v", rcv)
	}
	before := len(b.node.Links().All())
	if _, _, err := r.Send(ctx, h16(p.Delivery), []byte(strings.Repeat("c", 300)), nil, lxmf.MapOf(), 0); err != nil {
		t.Fatalf("second send: %v", err)
	}
	p.wait("lxm_received", nil, 20*time.Second)
	if after := len(b.node.Links().All()); after != before {
		t.Fatalf("second direct send opened another link (%d -> %d)", before, after)
	}
}

func TestInteropLXMFStamps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Bridge requires cost 6 and enforces; Python requires cost 6 and enforces.
	b, r, got := newLXMFBridge(t, ctx, 6)
	p := startPeer(t, peerOpts{name: "S", connect: fmt.Sprintf("127.0.0.1:%d", b.port), stampCost: 6, enforce: true, loglevel: 6})
	waitFor(t, "tcp peer", 10*time.Second, func() bool { return b.tcp.PeerCount() > 0 })
	p.send(event{"cmd": "announce"})
	waitFor(t, "bridge learns python", 10*time.Second, func() bool { return b.node.Paths().Hops(h16(p.Delivery)) == 1 })
	r.Announce()
	dh := r.HashHex()
	p.wait("announce", func(e event) bool { return e["dest"] == dh }, 10*time.Second)
	if k := b.node.Recall(h16(p.Delivery)); lxmf.ParseAnnounceAppData(k.AppData).StampCost != 6 {
		t.Fatalf("bridge did not read python's stamp cost")
	}

	p.send(event{"cmd": "send_lxm", "dest": dh, "content": "stamped from python", "method": "opportunistic"})
	select {
	case m := <-got:
		if !m.StampValid || m.StampValue < 6 {
			t.Fatalf("stamp not valid: %+v", m)
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("bridge did not receive the stamped message")
	}
	p.wait("lxm_delivered", nil, 15*time.Second)

	m, res, err := r.Send(ctx, h16(p.Delivery), []byte("stamped from bridge"), nil, lxmf.MapOf(), 0)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if res.StampCost != 6 || m.Stamp == nil {
		t.Fatalf("no stamp generated: %+v", res)
	}
	rcv := p.wait("lxm_received", nil, 30*time.Second)
	if rcv["stamp_valid"] != true {
		t.Fatalf("python did not accept the bridge's stamp: %v", rcv)
	}
}

func TestInteropLXMFResourceBothWays(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b, r, got := newLXMFBridge(t, ctx, 0)
	p := startPeer(t, peerOpts{name: "A", connect: fmt.Sprintf("127.0.0.1:%d", b.port), loglevel: 5})
	waitFor(t, "tcp peer", 10*time.Second, func() bool { return b.tcp.PeerCount() > 0 })
	p.send(event{"cmd": "announce"})
	waitFor(t, "bridge learns python", 10*time.Second, func() bool { return b.node.Paths().Hops(h16(p.Delivery)) == 1 })
	r.Announce()
	dh := r.HashHex()
	p.wait("announce", func(e event) bool { return e["dest"] == dh }, 10*time.Second)

	// Python -> bridge: 2 KB of content forces an RNS resource on a link.
	big := strings.Repeat("resource payload from python. ", 70)
	p.send(event{"cmd": "send_lxm", "dest": dh, "content": big, "method": "direct"})
	select {
	case m := <-got:
		if string(m.Content) != big || m.Method != lxmf.Direct || !m.SignatureValid {
			t.Fatalf("bridge got method %s valid %v len %d", m.Method, m.SignatureValid, len(m.Content))
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("bridge did not receive the resource message")
	}
	p.wait("lxm_delivered", nil, 30*time.Second)

	// Bridge -> Python: 3 KB of content as a resource on the bridge's link.
	big2 := strings.Repeat("resource payload from the bridge. ", 90)
	m, res, err := r.Send(ctx, h16(p.Delivery), []byte(big2), []byte("big"), lxmf.MapOf(), 0)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if res.Method != lxmf.Direct {
		t.Fatalf("method %s", res.Method)
	}
	rcv := p.wait("lxm_received", nil, 30*time.Second)
	if rcv["hash"] != hex.EncodeToString(m.Hash[:]) || rcv["signature_validated"] != true || rcv["content"] != big2 {
		t.Fatalf("python got hash %v valid %v len %d", rcv["hash"], rcv["signature_validated"], len(rcv["content"].(string)))
	}
}

// The mid field (contract C2) crosses upstream LXMF both ways byte for byte:
// stock LXMF keeps the fixture's fields map as sent and the bridge reads
// the mid from what Python sent. [MESHSAT-1641]
func TestInteropLXMFMidFieldBothWays(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "hembfixtures", "testdata", "v1", "lxmf_mid_field.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		FieldsHex string `json:"fields_msgpack_hex"`
		Mid       string `json:"mid"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	mid, err := lxmf.ParseMid(fx.Mid)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b, r, got := newLXMFBridge(t, ctx, 0)
	p := startPeer(t, peerOpts{name: "A", connect: fmt.Sprintf("127.0.0.1:%d", b.port)})
	waitFor(t, "tcp peer", 10*time.Second, func() bool { return b.tcp.PeerCount() > 0 })
	p.send(event{"cmd": "announce"})
	waitFor(t, "bridge learns python delivery dest", 10*time.Second, func() bool { return b.node.Paths().Hops(h16(p.Delivery)) == 1 })
	if err := r.Announce(); err != nil {
		t.Fatal(err)
	}
	dh := r.HashHex()
	p.wait("announce", func(e event) bool { return e["dest"] == dh }, 10*time.Second)

	p.send(event{"cmd": "send_lxm", "dest": dh, "content": "with an id", "method": "opportunistic", "fields_hex": fx.FieldsHex})
	p.wait("lxm_sent", nil, 10*time.Second)
	select {
	case m := <-got:
		if got, ok := lxmf.MidOf(m.Fields); !ok || got != mid || !m.SignatureValid {
			t.Fatalf("bridge read mid %s %v from %s", lxmf.FormatMid(got), ok, hex.EncodeToString(lxmf.Pack(m.Fields)))
		}
	case <-time.After(15 * time.Second):
		t.Fatal("bridge did not receive the LXMF message")
	}

	if _, _, err := r.Send(ctx, h16(p.Delivery), []byte("with an id"), nil, lxmf.MidFields(mid), 0); err != nil {
		t.Fatalf("send: %v", err)
	}
	rcv := p.wait("lxm_received", nil, 15*time.Second)
	if rcv["fields_hex"] != fx.FieldsHex || rcv["signature_validated"] != true {
		t.Fatalf("python got fields %v, want %s", rcv["fields_hex"], fx.FieldsHex)
	}

	// The boundary with the mid (r40 review, decision 4): 259 bytes still go
	// as one packet, 260 over a link, and stock LXMF takes both.
	for _, c := range []struct {
		n    int
		want lxmf.Method
	}{{259, lxmf.Opportunistic}, {260, lxmf.Direct}} {
		body := strings.Repeat("b", c.n)
		_, res, err := r.Send(ctx, h16(p.Delivery), []byte(body), nil, lxmf.MidFields(mid), 0)
		if err != nil {
			t.Fatalf("%d bytes: %v", c.n, err)
		}
		if res.Method != c.want {
			t.Fatalf("%d bytes went %v, want %v", c.n, res.Method, c.want)
		}
		got := p.wait("lxm_received", func(e event) bool { return e["content"] == body }, 20*time.Second)
		if got["fields_hex"] != fx.FieldsHex {
			t.Fatalf("%d bytes: fields %v", c.n, got["fields_hex"])
		}
	}
}
