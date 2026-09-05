package transport

import (
	"context"
	"sync"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"go.bug.st/serial"
	"google.golang.org/protobuf/proto"
)

// fakeWritePort records frames written to it; only Write and Close are
// used by the code under test, every other serial.Port method would panic.
type fakeWritePort struct {
	serial.Port
	mu     sync.Mutex
	writes [][]byte
}

func (f *fakeWritePort) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.writes = append(f.writes, append([]byte(nil), p...))
	f.mu.Unlock()
	return len(p), nil
}

func (f *fakeWritePort) Close() error { return nil }

func (f *fakeWritePort) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

// An admin reply from the local radio (the device health probe's
// get_device_metadata response) stamps LastLocalReply and the firmware
// version, and never reaches the message ring buffer or the event stream.
// [MESHSAT-817]
func TestHandlePacket_LocalAdminReplyIsLivenessNotTraffic(t *testing.T) {
	tr := NewDirectMeshTransport("/dev/null")
	tr.myNodeNum = 0x11223344
	events := make(chan MeshEvent, 8)
	tr.eventSubs[1] = events

	admin := &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetDeviceMetadataResponse{
		GetDeviceMetadataResponse: &pb.DeviceMetadata{FirmwareVersion: "2.5.20.test"},
	}}
	payload, err := proto.Marshal(admin)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	tr.handlePacket(&ProtoMeshPacket{
		From: tr.myNodeNum, To: tr.myNodeNum,
		Decoded: &ProtoData{PortNum: PortNumAdminApp, Payload: payload},
	})
	if got := tr.LastLocalReply(); got.Before(before) {
		t.Fatalf("LastLocalReply %v not stamped", got)
	}
	tr.mu.RLock()
	fw := tr.firmwareVer
	tr.mu.RUnlock()
	if fw != "2.5.20.test" {
		t.Fatalf("firmware %q, want 2.5.20.test", fw)
	}
	if msgs, _ := tr.GetMessages(context.Background(), 10); len(msgs) != 0 {
		t.Fatalf("admin reply stored as a message: %+v", msgs)
	}
	select {
	case ev := <-events:
		t.Fatalf("admin reply emitted event %+v", ev)
	default:
	}

	// The same admin port from a remote node is ordinary traffic.
	tr.handlePacket(&ProtoMeshPacket{
		From: 0x55667788, To: tr.myNodeNum,
		Decoded: &ProtoData{PortNum: PortNumAdminApp, Payload: payload},
	})
	if msgs, _ := tr.GetMessages(context.Background(), 10); len(msgs) != 1 {
		t.Fatalf("remote admin packet not stored: %d messages", len(msgs))
	}
}

// After a handshake the radio's own clock is set; the set-time to every
// remote NodeDB entry (42 LoRa transmissions on parallax) only runs when
// explicitly enabled. [MESHSAT-783]
func TestSendTimeSync_LocalOnlyUnlessRemoteEnabled(t *testing.T) {
	tr := NewDirectMeshTransport("/dev/null")
	port := &fakeWritePort{}
	tr.file = port
	tr.connected = true
	tr.myNodeNum = 1
	for _, n := range []uint32{1, 2, 3, 4} {
		tr.nodes[n] = &MeshNode{Num: n}
	}

	tr.sendTimeSync()
	if n := port.count(); n != 1 {
		t.Fatalf("%d frames written, want 1 (local radio only)", n)
	}

	tr.SetTimeSyncRemote(true)
	tr.sendTimeSync()
	if n := port.count(); n != 1+4 {
		t.Fatalf("%d frames written, want 5 with remote enabled", n)
	}
}
