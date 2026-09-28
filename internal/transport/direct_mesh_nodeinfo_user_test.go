package transport

import (
	"context"
	"errors"
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

// A NodeInfo request is a NODEINFO_APP packet that carries our own User, as
// the firmware's sendOurNodeInfo and the Android app's request do: the peer
// writes the User it receives over its NodeDB row for us, so a request with
// an empty payload blanked our name, MAC, role and public key on every node
// the bridge asked. Until the config download has reported the radio's own
// row, nothing is asked. [MESHSAT-1388]
func TestBuildRequestNodeInfo_CarriesOwnUser(t *testing.T) {
	user := &pb.User{Id: "!52cb81e7", LongName: "meshsat-pinephone-pro", ShortName: "MSPP", HwModel: pb.HardwareModel_PORTDUINO, PublicKey: []byte{1, 2, 3}}
	data := buildRequestNodeInfo(0x52cb81e7, 0xa1b3c2ec, user)
	if data == nil {
		t.Fatal("no frame built")
	}
	var toRadio pb.ToRadio
	if err := proto.Unmarshal(data, &toRadio); err != nil {
		t.Fatal(err)
	}
	pkt := toRadio.GetPacket()
	if pkt.GetFrom() != 0x52cb81e7 || pkt.GetTo() != 0xa1b3c2ec {
		t.Fatalf("addressed !%08x -> !%08x", pkt.GetFrom(), pkt.GetTo())
	}
	if pkt.GetDecoded().GetPortnum() != pb.PortNum_NODEINFO_APP || !pkt.GetDecoded().GetWantResponse() {
		t.Fatalf("port %v, want_response %v", pkt.GetDecoded().GetPortnum(), pkt.GetDecoded().GetWantResponse())
	}
	var got pb.User
	if err := proto.Unmarshal(pkt.GetDecoded().GetPayload(), &got); err != nil {
		t.Fatal(err)
	}
	if got.GetId() != "!52cb81e7" || got.GetLongName() != "meshsat-pinephone-pro" || got.GetShortName() != "MSPP" ||
		got.GetHwModel() != pb.HardwareModel_PORTDUINO || string(got.GetPublicKey()) != "\x01\x02\x03" {
		t.Fatalf("the request carries %v", &got)
	}
	if buildRequestNodeInfo(1, 2, nil) != nil {
		t.Fatal("a request without our User must not be built")
	}
}

func TestRequestNodeInfo_WaitsForOwnRow(t *testing.T) {
	tr := NewDirectMeshTransport("/dev/null")
	port := &fakeWritePort{}
	tr.file = port
	tr.connected = true
	tr.myNodeNum = 5
	ctx := context.Background()

	if err := tr.RequestNodeInfo(ctx, 6); !errors.Is(err, ErrOwnUserUnknown) {
		t.Fatalf("before the own row: err %v, want ErrOwnUserUnknown", err)
	}
	if n := port.count(); n != 0 {
		t.Fatalf("%d frames written before the own row, want 0", n)
	}

	// A zeroed own row (MESHSAT-1102) is not a User to send.
	tr.handleFromRadio(ownRowFrame(t, 5, "", ""))
	if err := tr.RequestNodeInfo(ctx, 6); !errors.Is(err, ErrOwnUserUnknown) {
		t.Fatalf("zeroed own row: err %v, want ErrOwnUserUnknown", err)
	}

	tr.handleFromRadio(ownRowFrame(t, 5, "kit-a", "KITA"))
	if err := tr.RequestNodeInfo(ctx, 6); err != nil {
		t.Fatalf("after the own row: %v", err)
	}
	if n := port.count(); n != 1 {
		t.Fatalf("%d frames for the request, want 1", n)
	}
	user := requestedUser(t, port, 0)
	if user.GetId() != "!00000005" || user.GetLongName() != "kit-a" || user.GetShortName() != "KITA" || string(user.GetPublicKey()) != "pk" {
		t.Fatalf("the request carries %v", user)
	}
}

// The automatic request on the first decodable packet of an unnamed node
// carries the User too, and waits for the own row rather than asking without.
func TestHandlePacket_AutoRequestCarriesOwnUser(t *testing.T) {
	tr := NewDirectMeshTransport("/dev/null")
	port := &fakeWritePort{}
	tr.file = port
	tr.connected = true
	tr.myNodeNum = 1
	telemetry := func() *ProtoMeshPacket {
		return &ProtoMeshPacket{From: 3, To: 0xffffffff, Decoded: &ProtoData{PortNum: PortNumTelemetry, Payload: []byte{1}}}
	}

	tr.handlePacket(telemetry())
	if n := port.count(); n != 0 {
		t.Fatalf("%d requests before the own row is known, want 0", n)
	}
	tr.handleFromRadio(ownRowFrame(t, 1, "kit-a", "KITA"))
	tr.handlePacket(telemetry())
	if n := port.count(); n != 1 {
		t.Fatalf("%d requests once the own row is known, want 1", n)
	}
	if user := requestedUser(t, port, 0); user.GetLongName() != "kit-a" || user.GetId() != "!00000001" {
		t.Fatalf("the request carries %v", user)
	}
}

// ownRowFrame is the config download's NodeInfo for the radio's own row.
func ownRowFrame(t *testing.T, num uint32, longName, shortName string) []byte {
	t.Helper()
	user := &pb.User{LongName: longName, ShortName: shortName}
	if longName != "" {
		user.Id = "!" + hex8(num)
		user.Macaddr = []byte{1, 2, 3, 4, 5, 6}
		user.PublicKey = []byte("pk")
	}
	return fromRadioBytes(t, &pb.FromRadio{PayloadVariant: &pb.FromRadio_NodeInfo{NodeInfo: &pb.NodeInfo{Num: num, User: user}}})
}

func hex8(num uint32) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		out[i] = digits[num&0xf]
		num >>= 4
	}
	return string(out)
}

// requestedUser decodes the User carried by the i-th frame the transport wrote.
func requestedUser(t *testing.T, port *fakeWritePort, i int) *pb.User {
	t.Helper()
	port.mu.Lock()
	frame := append([]byte(nil), port.writes[i]...)
	port.mu.Unlock()
	if len(frame) < 4 || frame[0] != meshStart1 || frame[1] != meshStart2 {
		t.Fatalf("frame %d is not a serial frame: %x", i, frame)
	}
	var toRadio pb.ToRadio
	if err := proto.Unmarshal(frame[4:], &toRadio); err != nil {
		t.Fatalf("frame %d: %v", i, err)
	}
	decoded := toRadio.GetPacket().GetDecoded()
	if decoded.GetPortnum() != pb.PortNum_NODEINFO_APP {
		t.Fatalf("frame %d is on port %v, want NODEINFO_APP", i, decoded.GetPortnum())
	}
	var user pb.User
	if err := proto.Unmarshal(decoded.GetPayload(), &user); err != nil {
		t.Fatalf("frame %d payload: %v", i, err)
	}
	return &user
}
