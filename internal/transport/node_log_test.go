package transport

import (
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

// The log of a node over Bluetooth: LogRadio values become radio log lines
// with sequence numbers, read after the last one a page has. [MESHSAT-1406]

func TestRecordLogRadio_LinesWithSequence(t *testing.T) {
	tr := NewDirectMeshTransport("/dev/null")
	events := make(chan MeshEvent, 8)
	tr.eventSubs[1] = events
	value := func(msg string, level pb.LogRecord_Level) []byte {
		data, err := proto.Marshal(&pb.LogRecord{Message: msg, Time: 1790000000, Source: "IridiumPipe", Level: level})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	tr.recordLogRadio(value("modem answered\n", pb.LogRecord_WARNING))
	tr.recordLogRadio(value("Reboot in 5 seconds", pb.LogRecord_UNSET))
	tr.recordLogRadio([]byte{0xff, 0xff, 0xff}) // not a LogRecord
	tr.recordLogRadio(value("", pb.LogRecord_INFO))

	all := tr.RadioLogAfter(0)
	if len(all) != 2 {
		t.Fatalf("%d lines, want 2", len(all))
	}
	if all[0].Seq != 1 || all[1].Seq != 2 {
		t.Errorf("seq = %d, %d", all[0].Seq, all[1].Seq)
	}
	if all[0].Message != "modem answered" || all[0].Level != "WARNING" || all[0].Source != "IridiumPipe" || all[0].RadioTime != 1790000000 {
		t.Errorf("line = %+v", all[0])
	}
	if all[1].Level != "" {
		t.Errorf("an unset level reads %q, want empty", all[1].Level)
	}
	if got := tr.RadioLogAfter(1); len(got) != 1 || got[0].Seq != 2 {
		t.Errorf("after 1: %+v", got)
	}
	if got := tr.RadioLogAfter(2); len(got) != 0 {
		t.Errorf("after 2: %+v", got)
	}
	// A person reads this stream on a page: it raises no events.
	select {
	case ev := <-events:
		t.Errorf("a LogRadio line raised event %+v", ev)
	default:
	}
}

func TestRadioLog_KeepsTheNewestWithTheirSequence(t *testing.T) {
	tr := NewDirectMeshTransport("/dev/null")
	for i := 0; i < radioLogKeep+5; i++ {
		tr.addRadioLog(&ProtoLogRecord{Message: "line"}, false)
	}
	all := tr.RadioLogAfter(0)
	if len(all) != radioLogKeep || all[0].Seq != 6 || all[len(all)-1].Seq != radioLogKeep+5 {
		t.Errorf("%d lines from %d to %d", len(all), all[0].Seq, all[len(all)-1].Seq)
	}
	if got := tr.RadioLogAfter(uint64(radioLogKeep)); len(got) != 5 {
		t.Errorf("after %d: %d lines, want 5", radioLogKeep, len(got))
	}
}

func TestNodeLog_NothingToFollowWithoutBluetooth(t *testing.T) {
	tr := NewDirectMeshTransport("/dev/null")
	if available, following := tr.FollowNodeLog(); available || following {
		t.Errorf("FollowNodeLog on a serial node = %v %v", available, following)
	}
	if _, known := tr.DebugLogSetting(); known {
		t.Error("DebugLogSetting known before the node sent its security settings")
	}
	link := newBLELink(tr, "ble")
	tr.ble = link
	if available, following := tr.FollowNodeLog(); available || following {
		t.Errorf("FollowNodeLog with no session = %v %v", available, following)
	}
	if link.logUntil.IsZero() {
		t.Error("a request to follow did not start the lease")
	}
	link.session = &bleGattSession{lost: make(chan struct{})}
	if available, following := tr.NodeLogStatus(); available || following {
		t.Errorf("NodeLogStatus on a node without LogRadio = %v %v", available, following)
	}
}
