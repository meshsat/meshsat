package transport

import (
	"context"
	"errors"
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

func fromRadioBytes(t *testing.T, fr *pb.FromRadio) []byte {
	t.Helper()
	b, err := proto.Marshal(fr)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func myInfoFrame(t *testing.T, num uint32) []byte {
	return fromRadioBytes(t, &pb.FromRadio{PayloadVariant: &pb.FromRadio_MyInfo{MyInfo: &pb.MyNodeInfo{MyNodeNum: num}}})
}

// A packet from the local node can arrive before MyNodeInfo in the config
// handshake. Asking that "unnamed node" for its NodeInfo zeroed the radio's
// own NodeDB row on both kits on 13 Sep 2026, and parallax's radio came back
// from its next boot with a new node number. [MESHSAT-1102]
func TestHandlePacket_NeverRequestsNodeInfoFromOwnRadio(t *testing.T) {
	tr := NewDirectMeshTransport("/dev/null")
	port := &fakeWritePort{}
	tr.file = port
	tr.connected = true

	telemetry := func(from uint32) *ProtoMeshPacket {
		return &ProtoMeshPacket{From: from, To: 0xffffffff, Decoded: &ProtoData{PortNum: PortNumTelemetry, Payload: []byte{1}}}
	}

	tr.handlePacket(telemetry(0x235779ff))
	if n := port.count(); n != 0 {
		t.Fatalf("%d NodeInfo requests before MyNodeInfo, want 0", n)
	}

	tr.handleFromRadio(myInfoFrame(t, 0x235779ff))
	tr.handlePacket(telemetry(0x235779ff))
	if n := port.count(); n != 0 {
		t.Fatalf("%d NodeInfo requests to the local radio, want 0", n)
	}

	tr.handlePacket(telemetry(0x698690dd))
	if n := port.count(); n != 1 {
		t.Fatalf("%d NodeInfo requests to a remote unnamed node, want 1", n)
	}
}

func TestRequestNodeInfo_RefusesLocalRadio(t *testing.T) {
	tr := NewDirectMeshTransport("/dev/null")
	port := &fakeWritePort{}
	tr.file = port
	tr.connected = true
	ctx := context.Background()

	if err := tr.RequestNodeInfo(ctx, 5); !errors.Is(err, ErrNodeNumUnknown) {
		t.Fatalf("before MyNodeInfo: err %v, want ErrNodeNumUnknown", err)
	}
	tr.myNodeNum = 5
	if err := tr.RequestNodeInfo(ctx, 5); !errors.Is(err, ErrNodeInfoSelf) {
		t.Fatalf("own node: err %v, want ErrNodeInfoSelf", err)
	}
	if n := port.count(); n != 0 {
		t.Fatalf("%d frames written for refused requests, want 0", n)
	}
	if err := tr.RequestNodeInfo(ctx, 6); err != nil {
		t.Fatalf("remote node: %v", err)
	}
	if n := port.count(); n != 1 {
		t.Fatalf("%d frames for a remote request, want 1", n)
	}
}

func TestHandleFromRadio_FlagsZeroedOwnRow(t *testing.T) {
	status := func(tr *DirectMeshTransport) *MeshStatus {
		s, _ := tr.GetStatus(context.Background())
		return s
	}
	nodeInfo := func(num uint32, mac []byte, long string) []byte {
		return fromRadioBytes(t, &pb.FromRadio{PayloadVariant: &pb.FromRadio_NodeInfo{NodeInfo: &pb.NodeInfo{
			Num: num, User: &pb.User{LongName: long, Macaddr: mac},
		}}})
	}

	tr := NewDirectMeshTransport("/dev/null")
	tr.handleFromRadio(myInfoFrame(t, 0x698690dd))
	tr.handleFromRadio(nodeInfo(0x698690dd, []byte{0xe0, 0x72, 0xa1, 0xd8, 0x9d, 0x70}, "MeshSat tesseract"))
	if status(tr).OwnRowZeroed {
		t.Fatal("healthy own row flagged as zeroed")
	}
	tr.handleFromRadio(nodeInfo(0x698690dd, make([]byte, 6), ""))
	if !status(tr).OwnRowZeroed {
		t.Fatal("zeroed own row not flagged")
	}

	other := NewDirectMeshTransport("/dev/null")
	other.handleFromRadio(myInfoFrame(t, 0x698690dd))
	other.handleFromRadio(nodeInfo(0x235779ff, make([]byte, 6), ""))
	if status(other).OwnRowZeroed {
		t.Fatal("a remote node's empty row flagged as our own")
	}
}

func TestHandleFromRadio_NodeNumberChangeEmitsEvent(t *testing.T) {
	tr := NewDirectMeshTransport("/dev/null")
	events := make(chan MeshEvent, 8)
	tr.eventSubs[1] = events

	tr.handleFromRadio(myInfoFrame(t, 0x235779ff))
	tr.handleFromRadio(myInfoFrame(t, 0x235779ff))
	tr.handleFromRadio(myInfoFrame(t, 0x402d9e7b))

	changed := 0
	for len(events) > 0 {
		if ev := <-events; ev.Type == "node_num_changed" {
			changed++
		}
	}
	if changed != 1 {
		t.Fatalf("%d node_num_changed events, want 1", changed)
	}
	if tr.MyNodeNum() != 0x402d9e7b {
		t.Fatalf("my node num %x", tr.MyNodeNum())
	}
}

func TestHandleFromRadio_MetadataSetsFirmwareVersion(t *testing.T) {
	tr := NewDirectMeshTransport("/dev/null")
	tr.handleFromRadio(fromRadioBytes(t, &pb.FromRadio{PayloadVariant: &pb.FromRadio_Metadata{
		Metadata: &pb.DeviceMetadata{FirmwareVersion: "2.6.10.9ce4455"},
	}}))
	s, _ := tr.GetStatus(context.Background())
	if s.FirmwareVersion != "2.6.10.9ce4455" {
		t.Fatalf("firmware version %q", s.FirmwareVersion)
	}
}

// A heal rung can reconnect while an old handshake is still waiting with the
// lock released. When that old handshake gives up it must not close the
// newer session's port. [MESHSAT-850]
func TestAbandonSilentHandshake_LeavesNewerSessionAlone(t *testing.T) {
	old := &fakeWritePort{}
	newer := &fakeWritePort{}
	tr := NewDirectMeshTransport("/dev/null")
	tr.file = newer
	tr.connected = true
	done := make(chan struct{})
	close(done)
	cancelled := false

	err := tr.abandonSilentHandshake(old, func() { cancelled = true }, done)
	if !errors.Is(err, ErrMeshHandshakeSilent) {
		t.Fatalf("err %v, want ErrMeshHandshakeSilent", err)
	}
	if tr.file != newer || !tr.connected || tr.handshakeFails != 0 {
		t.Fatalf("newer session disturbed: file replaced %v connected %v fails %d", tr.file != newer, tr.connected, tr.handshakeFails)
	}
	if !cancelled {
		t.Fatal("the old session's reader was not cancelled")
	}
}

func TestAbandonSilentHandshake_ClosesItsOwnSession(t *testing.T) {
	sp := &fakeWritePort{}
	tr := NewDirectMeshTransport("/dev/null")
	tr.file = sp
	tr.connected = true
	done := make(chan struct{})
	close(done)

	if err := tr.abandonSilentHandshake(sp, func() {}, done); !errors.Is(err, ErrMeshHandshakeSilent) {
		t.Fatalf("err %v, want ErrMeshHandshakeSilent", err)
	}
	if tr.file != nil || tr.connected || tr.handshakeFails != 1 {
		t.Fatalf("own session not torn down: file %v connected %v fails %d", tr.file, tr.connected, tr.handshakeFails)
	}
}
