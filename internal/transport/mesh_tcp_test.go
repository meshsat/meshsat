package transport

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

// fakeDaemon speaks meshtasticd's TCP API on 127.0.0.1: the serial framing
// over a socket. It answers the config handshake and keeps every ToRadio
// packet the bridge writes. [MESHSAT-1384]
type fakeDaemon struct {
	t       *testing.T
	ln      net.Listener
	nodeNum uint32
	conns   chan net.Conn
	packets chan *pb.MeshPacket
}

func newFakeDaemon(t *testing.T, nodeNum uint32) *fakeDaemon {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDaemon{t: t, ln: ln, nodeNum: nodeNum, conns: make(chan net.Conn, 4), packets: make(chan *pb.MeshPacket, 16)}
	go d.accept()
	t.Cleanup(func() { ln.Close() })
	return d
}

func (d *fakeDaemon) addr() string { return "tcp://" + d.ln.Addr().String() }

func (d *fakeDaemon) accept() {
	for {
		conn, err := d.ln.Accept()
		if err != nil {
			return
		}
		d.conns <- conn
		go d.serve(conn)
	}
}

// serve reads frames until the socket goes away. The wake burst in front of
// the first frame is plain bytes the reader discards, as the radio would.
func (d *fakeDaemon) serve(conn net.Conn) {
	reader := &meshFrameReader{port: conn}
	for {
		payload, err := reader.readFrame(context.Background())
		if err != nil {
			return
		}
		toRadio := &pb.ToRadio{}
		if err := proto.Unmarshal(payload, toRadio); err != nil {
			continue
		}
		if id := toRadio.GetWantConfigId(); id != 0 {
			_ = sendFrame(conn, myInfoFrame(d.t, d.nodeNum))
			_ = sendFrame(conn, fromRadioBytes(d.t, &pb.FromRadio{PayloadVariant: &pb.FromRadio_ConfigCompleteId{ConfigCompleteId: id}}))
			continue
		}
		if pkt := toRadio.GetPacket(); pkt != nil {
			select {
			case d.packets <- pkt:
			default:
			}
		}
	}
}

// deliver puts a text from another node on the bridge's link, as the daemon
// would after hearing it on the air.
func (d *fakeDaemon) deliver(conn net.Conn, from uint32, text string) {
	fr := &pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: &pb.MeshPacket{
		From: from, To: 0xffffffff, Id: 0x5700fa99,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte(text)}},
	}}}
	if err := sendFrame(conn, fromRadioBytes(d.t, fr)); err != nil {
		d.t.Errorf("deliver: %v", err)
	}
}

func waitFor(t *testing.T, what string, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestMeshTCPAddr(t *testing.T) {
	for in, want := range map[string]string{
		"tcp://127.0.0.1":      "127.0.0.1:4403",
		"tcp://127.0.0.1:5000": "127.0.0.1:5000",
		"TCP://phone.local":    "phone.local:4403",
		"tcp://[::1]":          "[::1]:4403",
		"tcp://[::1]:4403":     "[::1]:4403",
	} {
		if !IsMeshTCP(in) {
			t.Errorf("%s: not seen as tcp", in)
		}
		if got := meshTCPAddr(in); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
	for _, in := range []string{"/dev/ttyACM0", "auto", "supervisor", ""} {
		if IsMeshTCP(in) {
			t.Errorf("%q taken for tcp", in)
		}
	}
	if meshTransportName("tcp://127.0.0.1") != "tcp" || meshTransportName("/dev/ttyACM0") != "serial" {
		t.Error("transport name")
	}
}

// The frame reader treats (0, nil) as "nothing yet" and an error as a lost
// link; a socket must give it the first, not a timeout error.
func TestTCPMeshStream_ReadTimeoutIsNoData(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			time.Sleep(2 * time.Second)
			conn.Close()
		}
	}()
	link, err := dialMeshTCP("tcp://" + ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer link.Close()
	start := time.Now()
	n, err := link.Read(make([]byte, 64))
	if n != 0 || err != nil {
		t.Fatalf("silent link: got (%d, %v), want (0, nil)", n, err)
	}
	if waited := time.Since(start); waited < meshReadTimeout/2 || waited > 3*meshReadTimeout {
		t.Fatalf("read waited %v, want about %v", waited, meshReadTimeout)
	}
}

// Handshake, a text in, a text out, over a socket instead of a serial port.
func TestDirectMesh_TCP_HandshakeReceiveSend(t *testing.T) {
	noMeshPacing(t)
	daemon := newFakeDaemon(t, 0x52cb81e7)
	tr := NewDirectMeshTransport(daemon.addr())
	tr.SetConfigTimeout(5 * time.Second)
	defer tr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	events, err := tr.Subscribe(ctx)
	if err != nil {
		t.Fatalf("connect over tcp: %v", err)
	}
	if !tr.IsConnected() {
		t.Fatal("not connected after the handshake")
	}
	if got := tr.MyNodeNum(); got != 0x52cb81e7 {
		t.Fatalf("node number %08x, want 52cb81e7", got)
	}
	if got := tr.GetPort(); got != daemon.addr() {
		t.Fatalf("port %q, want the tcp address", got)
	}

	var conn net.Conn
	select {
	case conn = <-daemon.conns:
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon saw no connection")
	}

	daemon.deliver(conn, 0xa1b3c2ec, "yyoio")
	waitFor(t, "the text event", 5*time.Second, func() bool {
		for {
			select {
			case ev := <-events:
				if ev.Type == "message" && bytes.Contains(ev.Data, []byte("yyoio")) {
					return true
				}
			default:
				return false
			}
		}
	})

	if err := tr.SendMessage(ctx, SendRequest{Text: "hello from the phone"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case pkt := <-daemon.packets:
			if d := pkt.GetDecoded(); d != nil && d.Portnum == pb.PortNum_TEXT_MESSAGE_APP {
				if string(d.Payload) != "hello from the phone" {
					t.Fatalf("daemon got %q", d.Payload)
				}
				return
			}
		case <-deadline:
			t.Fatal("the daemon never got the text")
		}
	}
}

// A daemon that closes the socket (it restarts on every settings change) is
// a disconnect the processor's retry loop can see, and a reconnect finds it
// again on the same address.
func TestDirectMesh_TCP_LostSocketIsADisconnect(t *testing.T) {
	noMeshPacing(t)
	daemon := newFakeDaemon(t, 0x52cb81e7)
	tr := NewDirectMeshTransport(daemon.addr())
	tr.SetConfigTimeout(5 * time.Second)
	defer tr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := tr.Subscribe(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	conn := <-daemon.conns
	conn.Close()

	select {
	case <-tr.DisconnectedCh():
	case <-time.After(5 * time.Second):
		t.Fatal("no disconnect signalled after the daemon closed the socket")
	}
	waitFor(t, "connected to drop", 5*time.Second, func() bool { return !tr.IsConnected() })

	if err := tr.Reconnect(ctx); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if !tr.IsConnected() {
		t.Fatal("not connected after the reconnect")
	}
}

// The serial heal rung has no meaning on a socket, and must not tear the
// session down while saying so.
func TestRebootViaLines_RefusesATCPLink(t *testing.T) {
	noMeshPacing(t)
	daemon := newFakeDaemon(t, 0x52cb81e7)
	tr := NewDirectMeshTransport(daemon.addr())
	tr.SetConfigTimeout(5 * time.Second)
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := tr.Subscribe(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	err := tr.RebootViaLines(ctx)
	if err == nil || !strings.Contains(err.Error(), "tcp") {
		t.Fatalf("got %v, want a refusal that names the tcp link", err)
	}
	if !tr.IsConnected() {
		t.Fatal("the refusal closed the session")
	}
}
