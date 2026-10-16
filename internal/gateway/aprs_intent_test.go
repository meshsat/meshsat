package gateway

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// APRS as a delivery-intent route (stage 1b). [MESHSAT-1584]

var intentDest = "PA3ENC-7" // testPeer as an addressee

func intentInfo(t *testing.T, frame []byte) string {
	t.Helper()
	ax, err := DecodeAX25Frame(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return string(ax.Info)
}

// An encrypted intent frame carries the id the engine chose; the addressed
// station's ack comes back as a receipt that completes delivered_device.
func TestAPRSIntent_EncryptedAckFromTheAddressedStation(t *testing.T) {
	tnc := newMockKISSTNC(t)
	defer tnc.close()
	gw := startAckGateway(t, tnc, APRSConfig{AckTimeoutMs: 3000})
	defer gw.Stop()
	res := make(chan IntentOutcome, 1)
	go func() {
		res <- gw.IntentSend(context.Background(), IntentSend{Destination: intentDest, Text: "\x01QUJDREVG", Encrypted: true, CorrValue: "K7Q2Z"})
	}()
	frames := waitTNCFrames(t, tnc, 1, 2*time.Second)
	if info := intentInfo(t, frames[0]); info != "{E1}QUJDREVG{K7Q2Z" {
		t.Fatalf("info %q; want the ciphertext with the prepared id", info)
	}
	tnc.sendRaw(ackFrom(testPeer, "TEST-10", "K7Q2Z"))
	select {
	case o := <-res:
		if o.Class != IntentAccepted || o.Receipt == nil || o.Receipt.Event != "ack" || o.Receipt.Completes != "delivered_device" ||
			o.Receipt.CorrKind != APRSIntentCorrKind || o.Receipt.CorrValue != "K7Q2Z" || o.Receipt.Peer != intentDest {
			t.Fatalf("outcome %+v receipt %+v", o, o.Receipt)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no outcome after the ack")
	}
}

// An ack from another station, or a reject, completes nothing.
func TestAPRSIntent_OtherStationOrRejectCompletesNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame func(id string) []byte
		event string
	}{
		{"other_station", func(id string) []byte { return ackFrom(AX25Address{Call: "PD0XYZ", SSID: 1}, "TEST-10", id) }, "ack"},
		{"reject", func(id string) []byte {
			return EncodeAX25Frame(AX25Address{Call: "APMSHT"}, testPeer, nil, EncodeAPRSMessage("TEST-10", "rej"+id, ""))
		}, "rej"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tnc := newMockKISSTNC(t)
			defer tnc.close()
			gw := startAckGateway(t, tnc, APRSConfig{AckTimeoutMs: 3000})
			defer gw.Stop()
			res := make(chan IntentOutcome, 1)
			go func() {
				res <- gw.IntentSend(context.Background(), IntentSend{Destination: intentDest, Text: "\x01QUJD", Encrypted: true, CorrValue: "ABCDE"})
			}()
			waitTNCFrames(t, tnc, 1, 2*time.Second)
			tnc.sendRaw(tc.frame("ABCDE"))
			select {
			case o := <-res:
				if o.Class != IntentAccepted || o.Receipt == nil || o.Receipt.Event != tc.event || o.Receipt.Completes != "" {
					t.Fatalf("outcome %+v receipt %+v", o, o.Receipt)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no outcome")
			}
		})
	}
}

// No answer: one frame per attempt, then accepted by the radio with no
// receipt; an ack after the call returned reaches the sink.
func TestAPRSIntent_NoAnswerThenLateAck(t *testing.T) {
	tnc := newMockKISSTNC(t)
	defer tnc.close()
	gw := startAckGateway(t, tnc, APRSConfig{TXRepeat: 2, AckAttempts: 2, AckTimeoutMs: 150})
	defer gw.Stop()
	var mu sync.Mutex
	var late []IntentReceipt
	gw.SetIntentReceiptSink(func(r IntentReceipt) { mu.Lock(); late = append(late, r); mu.Unlock() })
	o := gw.IntentSend(context.Background(), IntentSend{Destination: intentDest, Text: "\x01QUJD", Encrypted: true, CorrValue: "LATE1"})
	if o.Class != IntentAccepted || o.AcceptedBy != "radio" || o.Receipt != nil {
		t.Fatalf("outcome %+v", o)
	}
	if n := len(waitTNCFrames(t, tnc, 2, time.Second)); n != 2 {
		t.Fatalf("%d frames; want one per attempt", n)
	}
	tnc.sendRaw(ackFrom(testPeer, "TEST-10", "LATE1"))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(late)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(late) != 1 || late[0].CorrValue != "LATE1" || late[0].Completes != "delivered_device" {
		t.Fatalf("late receipts %+v", late)
	}
}

// A plain intent text is a directed message with the id; a station that
// receives a directed message with an id addressed to it acks it (the
// receive side of the same contract).
func TestAPRSIntent_PlainDirectedMessageAndTheReceiverAcks(t *testing.T) {
	tnc := newMockKISSTNC(t)
	defer tnc.close()
	gw := startAckGateway(t, tnc, APRSConfig{AckAttempts: 1, AckTimeoutMs: 100})
	defer gw.Stop()
	o := gw.IntentSend(context.Background(), IntentSend{Destination: "pa3enc-7", Text: "meet at the gate", CorrValue: "M1"})
	frames := waitTNCFrames(t, tnc, 1, time.Second)
	if info := intentInfo(t, frames[0]); info != ":PA3ENC-7 :meet at the gate{M1" {
		t.Fatalf("info %q", info)
	}
	if o.Class != IntentAccepted {
		t.Fatalf("outcome %+v", o)
	}
	tnc.sendRaw(EncodeAX25Frame(AX25Address{Call: "APMSHT"}, testPeer, nil, EncodeAPRSMessage("TEST-10", "hello kit", "42")))
	frames = waitTNCFrames(t, tnc, 2, 2*time.Second)
	if info := intentInfo(t, frames[1]); info != string(EncodeAPRSMessage("PA3ENC-7", "ack42", "")) {
		t.Fatalf("the receiver answered %q; want an ack for 42", info)
	}
}

// What admission refuses, never cut or changed.
func TestAPRSIntent_Admission(t *testing.T) {
	gw := NewAPRSGateway(APRSConfig{Callsign: "TEST", SSID: 10}, nil)
	cases := []struct {
		dest, text string
		enc        bool
		want       string
	}{
		{intentDest, strings.Repeat("a", 67), false, ""},
		{intentDest, strings.Repeat("a", 68), false, "skipped_oversize"},
		{intentDest, "café", false, "skipped_encoding"},
		{intentDest, "a{b", false, "skipped_encoding"},
		{intentDest, "line\nbreak", false, "skipped_encoding"},
		{"not a call", "hi", false, "skipped_bad_address"},
		{intentDest, "\x01" + strings.Repeat("Q", 245), true, ""}, // 4 + 245 + 1 + 5 = 255
		{intentDest, "\x01" + strings.Repeat("Q", 247), true, "skipped_oversize"},
	}
	for _, c := range cases {
		if got, _ := gw.IntentAdmit(c.dest, []byte(c.text), c.enc); got != c.want {
			t.Errorf("admit(%q, %d chars, enc=%v) = %q; want %q", c.dest, len(c.text), c.enc, got, c.want)
		}
	}
	is := &APRSGateway{is: &aprsISLink{}}
	if got, _ := is.IntentAdmit(intentDest, []byte("\x01QUJD"), true); got != "skipped_unsupported" {
		t.Fatalf("APRS-IS with ciphertext: %q", got)
	}
}

// A gateway that is not running sends nothing: not sent, retryable.
func TestAPRSIntent_NotRunning(t *testing.T) {
	gw := NewAPRSGateway(APRSConfig{Callsign: "TEST", SSID: 10}, nil)
	if o := gw.IntentSend(context.Background(), IntentSend{Destination: intentDest, Text: "hi", CorrValue: "A1"}); o.Class != IntentNotSent || !o.Retryable {
		t.Fatalf("outcome %+v", o)
	}
}
