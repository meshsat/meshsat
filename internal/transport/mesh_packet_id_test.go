package transport

import (
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

// A text sent under our own packet id carries that id, and asks for an
// acknowledgement only as a unicast: never want_ack on a broadcast
// (MESHSAT-1112). [MESHSAT-1581]
func TestBuildTextMessageWithID(t *testing.T) {
	for _, c := range []struct {
		to      uint32
		wantAck bool
	}{{0x0000abcd, true}, {0, false}, {meshBroadcast, false}} {
		raw := buildTextMessageWithID("hello", c.to, 0, 0x00c0ffee)
		var pkt pb.MeshPacket
		if err := proto.Unmarshal(raw, &pkt); err != nil {
			t.Fatal(err)
		}
		if pkt.GetId() != 0x00c0ffee || pkt.GetWantAck() != c.wantAck || string(pkt.GetDecoded().GetPayload()) != "hello" ||
			pkt.GetDecoded().GetPortnum() != pb.PortNum_TEXT_MESSAGE_APP {
			t.Fatalf("to %08x: id %08x want_ack %v payload %q", c.to, pkt.GetId(), pkt.GetWantAck(), pkt.GetDecoded().GetPayload())
		}
	}
	seen := map[uint32]bool{}
	for i := 0; i < 1000; i++ {
		id, err := newMeshPacketID()
		if err != nil || id == 0 {
			t.Fatalf("packet id %d: %v", id, err)
		}
		seen[id] = true
	}
	if len(seen) < 990 {
		t.Fatalf("%d distinct ids in 1000", len(seen))
	}
}
