package gateway

import (
	"context"
	"errors"
	"net"
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
// station's ack comes back as a receipt that completes acked_station (an
// unauthenticated station ack, never delivered, APRS-R3).
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
		if o.Class != IntentAccepted || o.Receipt == nil || o.Receipt.Event != "ack" || o.Receipt.Completes != "acked_station" ||
			o.Receipt.CorrKind != APRSIntentCorrKind || o.Receipt.CorrValue != "K7Q2Z" || o.Receipt.Peer != intentDest {
			t.Fatalf("outcome %+v receipt %+v", o, o.Receipt)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no outcome after the ack")
	}
}

// A reject from the addressed station ends the send and completes
// nothing; an ack or a reject from another station leaves the send waiting
// for the addressed station (APRS-R4), and goes to the sink, which never
// completes anything for it.
func TestAPRSIntent_RejectAndOtherStations(t *testing.T) {
	other := AX25Address{Call: "PD0XYZ", SSID: 1}
	rej := func(from AX25Address, id string) []byte {
		return EncodeAX25Frame(AX25Address{Call: "APMSHT"}, from, nil, EncodeAPRSMessage("TEST-10", "rej"+id, ""))
	}
	for _, tc := range []struct {
		name    string
		frame   func(id string) []byte
		ends    bool
		event   string
		sinkGot string // the event the sink takes, "" for none
	}{
		{"reject_from_the_addressed_station", func(id string) []byte { return rej(testPeer, id) }, true, "rej", ""},
		{"ack_from_another_station", func(id string) []byte { return ackFrom(other, "TEST-10", id) }, false, "", "ack"},
		{"reject_from_another_station", func(id string) []byte { return rej(other, id) }, false, "", "rej"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tnc := newMockKISSTNC(t)
			defer tnc.close()
			gw := startAckGateway(t, tnc, APRSConfig{AckAttempts: 1, AckTimeoutMs: 800})
			defer gw.Stop()
			var mu sync.Mutex
			var sunk []IntentReceipt
			gw.SetIntentReceiptSink(func(r IntentReceipt) { mu.Lock(); sunk = append(sunk, r); mu.Unlock() })
			res := make(chan IntentOutcome, 1)
			go func() {
				res <- gw.IntentSend(context.Background(), IntentSend{Destination: intentDest, Text: "\x01QUJD", Encrypted: true, CorrValue: "ABCDE"})
			}()
			waitTNCFrames(t, tnc, 1, 2*time.Second)
			tnc.sendRaw(tc.frame("ABCDE"))
			if tc.ends {
				select {
				case o := <-res:
					if o.Class != IntentAccepted || o.Receipt == nil || o.Receipt.Event != tc.event || o.Receipt.Completes != "" {
						t.Fatalf("outcome %+v receipt %+v", o, o.Receipt)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("no outcome")
				}
				return
			}
			select {
			case o := <-res:
				t.Fatalf("another station's answer ended the send: %+v receipt %+v", o, o.Receipt)
			case <-time.After(300 * time.Millisecond):
			}
			tnc.sendRaw(ackFrom(testPeer, "TEST-10", "ABCDE"))
			select {
			case o := <-res:
				if o.Receipt == nil || o.Receipt.Peer != intentDest || o.Receipt.Completes != "acked_station" {
					t.Fatalf("the addressed station's ack after it: %+v receipt %+v", o, o.Receipt)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("the addressed station's ack did not end the send")
			}
			mu.Lock()
			defer mu.Unlock()
			if len(sunk) != 1 || sunk[0].Event != tc.sinkGot || sunk[0].Peer != "PD0XYZ-1" || sunk[0].Completes != "" ||
				(tc.sinkGot == "rej" && sunk[0].CompletesIfPeer != "") {
				t.Fatalf("sink took %+v; want one %s from PD0XYZ-1 that completes nothing by itself", sunk, tc.sinkGot)
			}
		})
	}
}

// No answer: one frame per attempt, then accepted by the radio with no
// receipt; an ack after the call returned reaches the sink as a candidate
// the ledger completes only for a transmission to that station (APRS-R2).
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
	if len(late) != 1 || late[0].CorrValue != "LATE1" || late[0].Peer != intentDest || late[0].Completes != "" ||
		late[0].CompletesIfPeer != "acked_station" {
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

// A gateway that never sent anything and has no sink of its own (a fresh
// gateway after a restart or a replacement) hands an answer to the
// process-wide sink installed before it started (APRS-R2).
func TestAPRSIntent_FreshGatewayUsesTheDefaultSink(t *testing.T) {
	var mu sync.Mutex
	var got []IntentReceipt
	SetDefaultIntentReceiptSink(func(r IntentReceipt) { mu.Lock(); got = append(got, r); mu.Unlock() })
	t.Cleanup(func() { SetDefaultIntentReceiptSink(nil) })
	tnc := newMockKISSTNC(t)
	defer tnc.close()
	gw := startAckGateway(t, tnc, APRSConfig{AckAttempts: 1, AckTimeoutMs: 100})
	defer gw.Stop()
	tnc.sendRaw(ackFrom(testPeer, "TEST-10", "OLD42"))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].CorrKind != APRSIntentCorrKind || got[0].CorrValue != "OLD42" || got[0].Peer != intentDest ||
		got[0].CompletesIfPeer != "acked_station" || got[0].Completes != "" {
		t.Fatalf("default sink took %+v", got)
	}
	// Its own sink, once set, wins.
	var own int
	gw.SetIntentReceiptSink(func(IntentReceipt) { own++ })
	gw.lateIntentReceipt("OLD43", intentDest, false)
	if own != 1 || len(got) != 1 {
		t.Fatalf("own sink %d, default %d; want the gateway's own sink to take it", own, len(got))
	}
}

// isWriteFault is an APRS-IS connection whose write fails: before any byte
// (refused) or after the whole line went to the server.
type isWriteFault struct {
	net.Conn
	before bool
}

func (c *isWriteFault) Write(p []byte) (int, error) {
	if c.before {
		return 0, errors.New("review: refused before write")
	}
	n, err := c.Conn.Write(p)
	if err == nil {
		err = errors.New("review: error after the line reached the server")
	}
	return n, err
}

// APRS-R1 on APRS-IS: a write that failed before any byte is not sent and
// may be retried; one that failed after the line went out is unknown, and
// no second line follows.
func TestAPRSIntent_ISWriteFailureClassification(t *testing.T) {
	setAPRSISKnobs(t, 50*time.Millisecond)
	for _, tc := range []struct {
		name   string
		before bool
		want   string
		lines  int
	}{
		{"zero_write", true, IntentNotSent, 0},
		{"line_then_error", false, IntentUnknown, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPRSIS(t, logrespVerified)
			g := newISGateway(t, f, func(c *APRSConfig) { c.AckAttempts, c.AckTimeoutMs = 2, 200 })
			startGateway(t, g)
			f.waitLogins(t, 1, 5*time.Second)
			waitISState(t, g, APRSStateConnected, 5*time.Second)
			g.is.mu.Lock()
			g.is.conn = &isWriteFault{Conn: g.is.conn, before: tc.before}
			g.is.mu.Unlock()
			o := g.IntentSend(context.Background(), IntentSend{Destination: "PA3ABC-7", Text: "is write", CorrValue: "IS001"})
			if tc.lines > 0 {
				f.waitLine(t, "N0CALL-10>APMSHT,TCPIP*::PA3ABC-7 :is write{IS001", 5*time.Second)
			}
			time.Sleep(300 * time.Millisecond)
			_, lines, _ := f.snapshot()
			n := 0
			for _, l := range lines {
				if strings.Contains(l, "{IS001") {
					n++
				}
			}
			if o.Class != tc.want || n != tc.lines || (tc.want == IntentNotSent) != o.Retryable {
				t.Fatalf("class %s retryable %v, %d lines (%s); want %s and %d", o.Class, o.Retryable, n, o.Detail, tc.want, tc.lines)
			}
		})
	}
}
