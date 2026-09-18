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
