package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"meshsat/internal/transport"
)

// Per-message acks for encrypted APRS frames. [MESHSAT-1021]

func TestAPRSAckID_StableFiveAlphanumerics(t *testing.T) {
	a := aprsAckID([]byte("dGVzdENpcGhlcg=="))
	if b := aprsAckID([]byte("dGVzdENpcGhlcg==")); a != b {
		t.Fatalf("id not stable: %q then %q", a, b)
	}
	if len(a) != aprsAckIDLen || !validAckID(a) {
		t.Fatalf("id %q is not %d alphanumerics", a, aprsAckIDLen)
	}
	if c := aprsAckID([]byte("b3RoZXI=")); c == a {
		t.Fatalf("two ciphertexts share the id %q", a)
	}
}

func TestSplitAckRequest(t *testing.T) {
	cases := []struct{ in, body, id string }{
		{"QUJD{7K2Q9", "QUJD", "7K2Q9"},
		{"QUJD", "QUJD", ""},
		{"QUJD{", "QUJD{", ""},
		{"QUJD{TOOLONG", "QUJD{TOOLONG", ""},
		{"QUJD{ab-1", "QUJD{ab-1", ""},
	}
	for _, c := range cases {
		body, id := splitAckRequest(c.in)
		if body != c.body || id != c.id {
			t.Errorf("splitAckRequest(%q) = %q, %q; want %q, %q", c.in, body, id, c.body, c.id)
		}
	}
}

func TestAPRSAckReply(t *testing.T) {
	cases := []struct {
		msg    string
		id     string
		reject bool
		ok     bool
	}{
		{"ack7K2Q9", "7K2Q9", false, true},
		{"rej7K2Q9", "7K2Q9", true, true},
		{"ackAB}CD", "AB", false, true},
		{"acknowledged", "", false, false},
		{"ack", "", false, false},
		{"hello there", "", false, false},
	}
	for _, c := range cases {
		id, reject, ok := aprsAckReply(&APRSPacket{DataType: ':', Message: c.msg})
		if id != c.id || reject != c.reject || ok != c.ok {
			t.Errorf("aprsAckReply(%q) = %q, %v, %v; want %q, %v, %v", c.msg, id, reject, ok, c.id, c.reject, c.ok)
		}
	}
	if _, _, ok := aprsAckReply(&APRSPacket{DataType: '>', Message: "ack12345"}); ok {
		t.Fatal("a status frame was read as an ack")
	}
}

func startAckGateway(t *testing.T, tnc *mockKISSTNC, cfg APRSConfig) *APRSGateway {
	t.Helper()
	host, port := splitHostPort(t, tnc.addr())
	cfg.KISSHost, cfg.KISSPort = host, port
	cfg.Callsign, cfg.SSID = "TEST", 10
	cfg.FrequencyMHz = 868.0
	cfg.ExternalDirewolf = true
	gw := NewAPRSGateway(cfg, nil)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	return gw
}

func waitTNCFrames(t *testing.T, tnc *mockKISSTNC, n int, within time.Duration) [][]byte {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if f := tnc.frames(); len(f) >= n {
			return f
		}
		time.Sleep(10 * time.Millisecond)
	}
	f := tnc.frames()
	t.Fatalf("the TNC got %d frames, want %d", len(f), n)
	return nil
}

// encryptedMsg is what the DeliveryWorker hands the gateway: the version byte
// and the base64 ciphertext.
func encryptedMsg(cipher string) *transport.MeshMessage {
	return &transport.MeshMessage{From: 0x12345678, PortNum: 1, DecodedText: "\x01" + cipher, Encrypted: true, MsgRef: "ack-test"}
}

func ackFrom(src AX25Address, to, id string) []byte {
	return EncodeAX25Frame(AX25Address{Call: "APMSHT"}, src, nil, EncodeAPRSMessage(to, "ack"+id, ""))
}

var testPeer = AX25Address{Call: "PA3ENC", SSID: 7}

// The frame carries its id, the peer's ack releases the sender, and no blind
// repeat copy follows it.
func TestAPRSAck_AckReleasesSenderWithoutABlindRepeat(t *testing.T) {
	tnc := newMockKISSTNC(t)
	defer tnc.close()
	gw := startAckGateway(t, tnc, APRSConfig{TXRepeat: 2, TXRepeatGapMs: 1200, AckTimeoutMs: 3000})
	defer gw.Stop()

	cipher := "dGVzdENpcGhlcg=="
	id := aprsAckID([]byte(cipher))
	res := make(chan error, 1)
	go func() { res <- gw.Forward(context.Background(), encryptedMsg(cipher)) }()

	frames := waitTNCFrames(t, tnc, 1, 2*time.Second)
	ax, err := DecodeAX25Frame(frames[0])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if want := "{E1}" + cipher + "{" + id; string(ax.Info) != want {
		t.Fatalf("info %q, want %q", ax.Info, want)
	}

	tnc.sendRaw(ackFrom(testPeer, "TEST-10", id))
	select {
	case err := <-res:
		if err != nil {
			t.Fatalf("forward: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Forward did not return after the ack")
	}

	time.Sleep(1800 * time.Millisecond) // past the latest repeat (1200 ms + 25 %)
	if n := len(tnc.frames()); n != 1 {
		t.Fatalf("%d frames on the air, want 1: an ack-requested frame goes out once per attempt", n)
	}
	st := gw.GetAPRSStatus()
	if st["acks_received"] != int64(1) || st["ack_failures"] != int64(0) {
		t.Fatalf("acks_received %v ack_failures %v", st["acks_received"], st["ack_failures"])
	}
}

// No ack: the identical frame goes out again, one copy per attempt even with
// TXRepeat set, then Forward fails with ErrNoAck.
func TestAPRSAck_UnackedMessageIsSentAgainThenFails(t *testing.T) {
	tnc := newMockKISSTNC(t)
	defer tnc.close()
	gw := startAckGateway(t, tnc, APRSConfig{TXRepeat: 2, TXRepeatGapMs: 50, AckAttempts: 3, AckTimeoutMs: 150})
	defer gw.Stop()

	err := gw.Forward(context.Background(), encryptedMsg("QUFBQUFBQUE="))
	if !errors.Is(err, transport.ErrNoAck) {
		t.Fatalf("err %v, want ErrNoAck", err)
	}
	time.Sleep(200 * time.Millisecond)
	frames := waitTNCFrames(t, tnc, 3, time.Second)
	if len(frames) != 3 {
		t.Fatalf("%d frames, want 3: one per attempt, no repeat copies", len(frames))
	}
	if string(frames[0]) != string(frames[1]) || string(frames[1]) != string(frames[2]) {
		t.Fatal("a retransmission differs from the first frame")
	}
	st := gw.GetAPRSStatus()
	if st["ack_retries"] != int64(2) || st["ack_failures"] != int64(1) {
		t.Fatalf("ack_retries %v ack_failures %v, want 2 and 1", st["ack_retries"], st["ack_failures"])
	}
}

// The receiver answers the id with a standard APRS ack to the sender and hands
// on the ciphertext without the id.
func TestAPRSAck_ReceiverAcksAndStripsTheID(t *testing.T) {
	tnc := newMockKISSTNC(t)
	defer tnc.close()
	gw := startAckGateway(t, tnc, APRSConfig{})
	defer gw.Stop()
	time.Sleep(100 * time.Millisecond)

	cipher := "QUFBQUFBQUFBQUE="
	tnc.sendRaw(EncodeAX25Frame(AX25Address{Call: "APMSHT"}, testPeer, nil, []byte("{E1}"+cipher+"{7K2Q9")))

	select {
	case msg := <-gw.Receive():
		if msg.Text != cipher {
			t.Fatalf("text %q, want the ciphertext %q without the id", msg.Text, cipher)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("encrypted message not delivered")
	}

	frames := waitTNCFrames(t, tnc, 1, 2*time.Second)
	ax, err := DecodeAX25Frame(frames[0])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if want := string(EncodeAPRSMessage("PA3ENC-7", "ack7K2Q9", "")); string(ax.Info) != want {
		t.Fatalf("ack info %q, want %q", ax.Info, want)
	}
	if FormatCallsign(ax.Src) != "TEST-10" {
		t.Fatalf("ack from %s, want TEST-10", FormatCallsign(ax.Src))
	}
}

// A TNC echo of our own frame is never acked.
func TestAPRSAck_OwnEchoIsNotAcked(t *testing.T) {
	tnc := newMockKISSTNC(t)
	defer tnc.close()
	gw := startAckGateway(t, tnc, APRSConfig{})
	defer gw.Stop()
	time.Sleep(100 * time.Millisecond)

	tnc.sendRaw(EncodeAX25Frame(AX25Address{Call: "APMSHT"}, AX25Address{Call: "TEST", SSID: 10}, nil, []byte("{E1}QUFB{7K2Q9")))
	select {
	case <-gw.Receive():
	case <-time.After(3 * time.Second):
		t.Fatal("echo not delivered")
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(tnc.frames()); n != 0 {
		t.Fatalf("%d frames sent in answer to our own echo", n)
	}
}

// An ack is protocol traffic: it never reaches Receive, so it is never relayed
// to the mesh as a text.
func TestAPRSAck_AckIsNeverForwarded(t *testing.T) {
	tnc := newMockKISSTNC(t)
	defer tnc.close()
	gw := startAckGateway(t, tnc, APRSConfig{})
	defer gw.Stop()
	time.Sleep(100 * time.Millisecond)

	tnc.sendRaw(ackFrom(testPeer, "TEST-10", "7K2Q9"))
	tnc.sendRaw(ackFrom(testPeer, "OTHER-1", "7K2Q9"))
	tnc.sendAPRSPosition(AX25Address{Call: "PA3XYZ"}, 52.1, 4.5, "after the acks")
	select {
	case msg := <-gw.Receive():
		if !strings.Contains(msg.Text, "PA3XYZ") {
			t.Fatalf("an ack reached Receive: %q", msg.Text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("position after the acks not delivered")
	}
}

// Plaintext is not waited on and carries no id.
func TestAPRSAck_PlaintextDoesNotWait(t *testing.T) {
	tnc := newMockKISSTNC(t)
	defer tnc.close()
	gw := startAckGateway(t, tnc, APRSConfig{AckTimeoutMs: 5000})
	defer gw.Stop()

	start := time.Now()
	if err := gw.Forward(context.Background(), &transport.MeshMessage{From: 1, PortNum: 1, DecodedText: "plain"}); err != nil {
		t.Fatalf("forward: %v", err)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("plaintext Forward blocked for %s", d)
	}
	ax, err := DecodeAX25Frame(waitTNCFrames(t, tnc, 1, time.Second)[0])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if strings.Contains(string(ax.Info), "{") {
		t.Fatalf("plaintext frame carries an id: %q", ax.Info)
	}
}

// Stopping the gateway releases a Forward waiting for an ack as a down bearer,
// so the delivery is deferred rather than spending a retry.
func TestAPRSAck_StopReleasesWaitingForward(t *testing.T) {
	tnc := newMockKISSTNC(t)
	defer tnc.close()
	gw := startAckGateway(t, tnc, APRSConfig{AckTimeoutMs: 5000})

	res := make(chan error, 1)
	go func() { res <- gw.Forward(context.Background(), encryptedMsg("QUJDREVG")) }()
	waitTNCFrames(t, tnc, 1, 2*time.Second)
	gw.Stop()
	select {
	case err := <-res:
		if !errors.Is(err, transport.ErrNotConnected) {
			t.Fatalf("err %v, want ErrNotConnected", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Forward still waiting after Stop")
	}
}

func TestAPRSAck_ForwardBeforeStartIsNotConnected(t *testing.T) {
	gw := NewAPRSGateway(APRSConfig{Callsign: "TEST", SSID: 10, KISSHost: "127.0.0.1", KISSPort: 1}, nil)
	if err := gw.Forward(context.Background(), encryptedMsg("QUJD")); !errors.Is(err, transport.ErrNotConnected) {
		t.Fatalf("err %v, want ErrNotConnected", err)
	}
}
