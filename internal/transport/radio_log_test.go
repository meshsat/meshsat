package transport

import (
	"strings"
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

// The radio's own account of itself used to be discarded by parseFromRadio:
// its log lines, its "I rebooted" flag and its client notifications. These
// tests pin that all three now come through. [MESHSAT-1112]

func TestParseFromRadio_LogRecord(t *testing.T) {
	data, err := proto.Marshal(&pb.FromRadio{PayloadVariant: &pb.FromRadio_LogRecord{LogRecord: &pb.LogRecord{
		Message: "Reset reason: 0x2\n",
		Time:    1789700000,
		Source:  "main",
		Level:   pb.LogRecord_INFO,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	fr, err := parseFromRadio(data)
	if err != nil {
		t.Fatalf("parseFromRadio: %v", err)
	}
	if fr.LogRecord == nil {
		t.Fatal("LogRecord not parsed")
	}
	if fr.LogRecord.Message != "Reset reason: 0x2\n" || fr.LogRecord.Source != "main" || fr.LogRecord.Level != "INFO" || fr.LogRecord.Time != 1789700000 {
		t.Errorf("LogRecord = %+v", *fr.LogRecord)
	}
}

func TestParseFromRadio_Rebooted(t *testing.T) {
	data, err := proto.Marshal(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Rebooted{Rebooted: true}})
	if err != nil {
		t.Fatal(err)
	}
	fr, err := parseFromRadio(data)
	if err != nil {
		t.Fatalf("parseFromRadio: %v", err)
	}
	if !fr.Rebooted {
		t.Error("Rebooted flag lost")
	}
}

func TestParseFromRadio_ClientNotification(t *testing.T) {
	reply := uint32(7)
	data, err := proto.Marshal(&pb.FromRadio{PayloadVariant: &pb.FromRadio_ClientNotification{ClientNotification: &pb.ClientNotification{
		ReplyId: &reply,
		Time:    1789700001,
		Level:   pb.LogRecord_ERROR,
		Message: "Rebooting due to critical error",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	fr, err := parseFromRadio(data)
	if err != nil {
		t.Fatalf("parseFromRadio: %v", err)
	}
	if fr.Notification == nil {
		t.Fatal("Notification not parsed")
	}
	if fr.Notification.Message != "Rebooting due to critical error" || fr.Notification.Level != "ERROR" || fr.Notification.ReplyID != 7 {
		t.Errorf("Notification = %+v", *fr.Notification)
	}
}

// A log line that names a reset cause lands in the ring, becomes the last
// reset reason on the status, and goes out as a radio_log event; an
// ordinary line is kept but stays quiet. The ring never grows past
// radioLogKeep.
func TestHandleFromRadio_RadioLogRingAndResetReason(t *testing.T) {
	tr := NewDirectMeshTransport("/dev/null")
	events := make(chan MeshEvent, 8)
	tr.eventSubs[1] = events

	frame := func(msg string) []byte {
		data, err := proto.Marshal(&pb.FromRadio{PayloadVariant: &pb.FromRadio_LogRecord{LogRecord: &pb.LogRecord{Message: msg, Level: pb.LogRecord_INFO}}})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	tr.handleFromRadio(frame("NodeDB loaded 2 nodes\n"))
	tr.handleFromRadio(frame("Reset reason: 0x2 (RESET_REASON_PIN)\n"))

	got := tr.RadioLog(0)
	if len(got) != 2 {
		t.Fatalf("ring holds %d lines, want 2", len(got))
	}
	if got[1].Message != "Reset reason: 0x2 (RESET_REASON_PIN)" {
		t.Errorf("newline not trimmed: %q", got[1].Message)
	}
	st, _ := tr.GetStatus(nil)
	if !strings.Contains(st.RadioLastResetReason, "Reset reason: 0x2") {
		t.Errorf("RadioLastResetReason = %q", st.RadioLastResetReason)
	}
	if st.RadioLogLines != 2 {
		t.Errorf("RadioLogLines = %d, want 2", st.RadioLogLines)
	}

	select {
	case ev := <-events:
		if ev.Type != "radio_log" || !strings.Contains(ev.Message, "Reset reason") {
			t.Errorf("event = %+v", ev)
		}
	default:
		t.Fatal("no radio_log event for the reset line")
	}
	select {
	case ev := <-events:
		t.Errorf("unexpected second event %+v (the plain line must stay quiet)", ev)
	default:
	}

	for i := 0; i < radioLogKeep+25; i++ {
		tr.handleFromRadio(frame("tick\n"))
	}
	if n := len(tr.RadioLog(0)); n != radioLogKeep {
		t.Errorf("ring grew to %d, want %d", n, radioLogKeep)
	}
	if n := len(tr.RadioLog(5)); n != 5 {
		t.Errorf("RadioLog(5) returned %d", n)
	}
}

func TestHandleFromRadio_RebootedAndNotificationEvents(t *testing.T) {
	tr := NewDirectMeshTransport("/dev/null")
	events := make(chan MeshEvent, 8)
	tr.eventSubs[1] = events

	data, _ := proto.Marshal(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Rebooted{Rebooted: true}})
	tr.handleFromRadio(data)
	data, _ = proto.Marshal(&pb.FromRadio{PayloadVariant: &pb.FromRadio_ClientNotification{ClientNotification: &pb.ClientNotification{Level: pb.LogRecord_CRITICAL, Message: "Rebooting: watchdog"}}})
	tr.handleFromRadio(data)

	types := []string{}
	for len(events) > 0 {
		types = append(types, (<-events).Type)
	}
	if strings.Join(types, ",") != "radio_rebooted,radio_notification" {
		t.Errorf("events = %v", types)
	}
	st, _ := tr.GetStatus(nil)
	if !strings.Contains(st.RadioLastResetReason, "Rebooting: watchdog") {
		t.Errorf("notification not kept as the last reset reason: %q", st.RadioLastResetReason)
	}
}

// Plain-text console output in front of a protobuf frame (what the firmware
// prints before a client attaches, including the boot banner) reaches the
// radio log as CONSOLE lines instead of being dropped. [MESHSAT-1112]
func TestFrameReader_ConsoleTextReachesRadioLog(t *testing.T) {
	tr := NewDirectMeshTransport("/dev/null")
	r := &meshFrameReader{onText: tr.consoleText}
	frame, _ := proto.Marshal(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Rebooted{Rebooted: true}})
	r.accum = append([]byte("\x1b[32mINFO  \x1b[0m| ??:??:?? 0 \x1b[32mReset reason: 0x4 (RESET_REASON_SOFT)\r\n\x1b[34mDEBUG \x1b[0m| Battery: usbPower=1\r\n"), 0x94, 0xC3, 0x00, byte(len(frame)))
	r.accum = append(r.accum, frame...)
	if got := r.extractFrame(); got == nil {
		t.Fatal("frame after console text not extracted")
	}
	lines := tr.RadioLog(0)
	if len(lines) != 2 || lines[0].Level != "CONSOLE" || lines[0].Message != "INFO  | ??:??:?? 0 Reset reason: 0x4 (RESET_REASON_SOFT)" {
		t.Fatalf("console lines = %+v", lines)
	}
	st, _ := tr.GetStatus(nil)
	if !strings.Contains(st.RadioLastResetReason, "Reset reason: 0x4") {
		t.Errorf("reset reason from the console not kept: %q", st.RadioLastResetReason)
	}
}

// A broadcast never asks the firmware for an ack, a unicast does, and an
// admin message to the radio's own node does not. [MESHSAT-1112]
func TestWantAck_BroadcastAndSelfNeverAsk(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"text broadcast", buildTextMessage("hi", 0, 0), false},
		{"text broadcast explicit", buildTextMessage("hi", meshBroadcast, 0), false},
		{"text unicast", buildTextMessage("hi", 0x4370c1d8, 0), true},
		{"raw broadcast asks anyway", buildRawPacket([]byte{1}, 256, 0, 0, true), false},
		{"raw unicast", buildRawPacket([]byte{1}, 256, 0x4370c1d8, 0, true), true},
		{"admin to self", buildAdminGetDeviceMetadata(0xde11f199, 0xde11f199), false},
		{"admin to peer", buildAdminGetDeviceMetadata(0xde11f199, 0x4370c1d8), true},
	}
	for _, c := range cases {
		// The text and raw builders return a bare MeshPacket (SendMessage
		// wraps it), the admin builder a ToRadio.
		pkt := &pb.MeshPacket{}
		if err := proto.Unmarshal(c.data, pkt); err != nil || pkt.GetTo() == 0 {
			msg := &pb.ToRadio{}
			if err := proto.Unmarshal(c.data, msg); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			pkt = msg.GetPacket()
		}
		if pkt == nil {
			t.Fatalf("%s: no packet", c.name)
		}
		if pkt.GetWantAck() != c.want {
			t.Errorf("%s: want_ack = %v, want %v", c.name, pkt.GetWantAck(), c.want)
		}
	}
}
