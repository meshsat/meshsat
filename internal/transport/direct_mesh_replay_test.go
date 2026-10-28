package transport

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

// After each config download Meshtastic 2.8 replays every node in the radio's
// NodeDB as a broadcast POSITION_APP packet: from the node, rx_time the node's
// last_heard, a stable id, the payload's time the fix's own and often 0
// (PhoneAPI::makeReplayPositionPacket in firmware 47db0e3, the kits' T-Echos).
// The transport cannot tell it from a live packet, and does not try: the
// message keeps the radio's rx_time and the node table keeps that last-heard,
// so the TAK gateway can tell a node heard on 19 Sep from one heard now
// (gateway.TestAStalePositionReplayIsNotExportedOnAnyOutput). Stamping the
// delivery time instead is what put such nodes on the Hub's map as crews seen
// this minute at every Bridge restart. [MESHSAT-1750]
func TestHandlePacket_ReplayedPositionKeepsTheRadiosRxTime(t *testing.T) {
	tr := NewDirectMeshTransport("/dev/null")
	events := make(chan MeshEvent, 16)
	tr.eventSubs[1] = events
	tr.configID = 42

	// The handshake of a reconnect, then the replay that trails it.
	tr.handleFromRadio(myInfoFrame(t, 0x0a0b0c0d))
	tr.handleFromRadio(fromRadioBytes(t, &pb.FromRadio{PayloadVariant: &pb.FromRadio_ConfigCompleteId{ConfigCompleteId: 42}}))
	heard := uint32(time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC).Unix())
	lat, lon := int32(521000000), int32(45000000)
	payload, err := proto.Marshal(&pb.Position{LatitudeI: &lat, LongitudeI: &lon}) // a T-Deck without a fix stamps no time
	if err != nil {
		t.Fatal(err)
	}
	tr.handleFromRadio(fromRadioBytes(t, &pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: &pb.MeshPacket{
		From: 0x0a0bc2ec, To: 0xffffffff, Id: 0x5eed0001, RxTime: heard, Priority: pb.MeshPacket_BACKGROUND,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_POSITION_APP, Payload: payload}},
	}}}))

	var msgs []MeshMessage
	for len(events) > 0 {
		if ev := <-events; ev.Type == "message" {
			var m MeshMessage
			if err := json.Unmarshal(ev.Data, &m); err != nil {
				t.Fatal(err)
			}
			msgs = append(msgs, m)
		}
	}
	if len(msgs) != 1 {
		t.Fatalf("%d message events, want the replayed position alone", len(msgs))
	}
	m := msgs[0]
	if m.From != 0x0a0bc2ec || m.PortNum != PortNumPosition || len(m.RawPayload) == 0 {
		t.Fatalf("message %+v", m)
	}
	if m.RxTime != int64(heard) {
		t.Fatalf("rx_time %d (%s), want the radio's %d: the delivery time is not when the kit heard the node", m.RxTime, m.Timestamp, heard)
	}
	if pos, err := ParsePositionPayload(m.RawPayload); err != nil || pos.Time != 0 || pos.LatitudeI != lat {
		t.Fatalf("payload %+v (%v)", pos, err)
	}
	nodes, err := tr.GetNodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.Num == 0x0a0bc2ec && n.LastHeard != int64(heard) {
			t.Fatalf("node table last-heard %d, want %d", n.LastHeard, heard)
		}
	}
}
