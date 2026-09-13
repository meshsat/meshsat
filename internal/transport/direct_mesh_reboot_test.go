package transport

import (
	"context"
	"strings"
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
)

// parallax's radio reset itself twice on 13 Sep 2026 with no host action
// (17:18:15 and 20:22:14 CEST, USB back within the second). reboot_count
// in MyNodeInfo makes such a reset visible at the next handshake.
// [MESHSAT-1102]
func TestHandleFromRadio_RebootCountRiseEmitsEvent(t *testing.T) {
	tr := NewDirectMeshTransport("/dev/null")
	events := make(chan MeshEvent, 8)
	tr.eventSubs[1] = events
	myInfo := func(reboots uint32) []byte {
		return fromRadioBytes(t, &pb.FromRadio{PayloadVariant: &pb.FromRadio_MyInfo{MyInfo: &pb.MyNodeInfo{MyNodeNum: 0x402d9e7b, RebootCount: reboots}}})
	}

	tr.handleFromRadio(myInfo(41)) // first handshake: baseline only
	tr.handleFromRadio(myInfo(41)) // reconnect without a reboot
	tr.handleFromRadio(myInfo(43)) // two reboots since

	var rebooted []MeshEvent
	for len(events) > 0 {
		if ev := <-events; ev.Type == "radio_rebooted" {
			rebooted = append(rebooted, ev)
		}
	}
	if len(rebooted) != 1 || !strings.Contains(rebooted[0].Message, "2 time(s)") || !strings.Contains(rebooted[0].Message, "41 -> 43") {
		t.Fatalf("radio_rebooted events %+v, want one for 41 -> 43", rebooted)
	}
	if s, _ := tr.GetStatus(context.Background()); s.RebootCount != 43 {
		t.Fatalf("status reboot_count %d, want 43", s.RebootCount)
	}
}
