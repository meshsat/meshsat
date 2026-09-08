package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/transport"
)

// fakeCellTransport records SMS sends and lets a test inject modem events.
type fakeCellTransport struct {
	mu     sync.Mutex
	sent   [][2]string
	events chan transport.CellEvent
}

func newFakeCellTransport() *fakeCellTransport {
	return &fakeCellTransport{events: make(chan transport.CellEvent, 8)}
}

func (f *fakeCellTransport) Subscribe(ctx context.Context) (<-chan transport.CellEvent, error) {
	return f.events, nil
}
func (f *fakeCellTransport) SendSMS(ctx context.Context, to string, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, [2]string{to, text})
	return nil
}
func (f *fakeCellTransport) GetSignal(ctx context.Context) (*transport.CellSignalInfo, error) {
	return &transport.CellSignalInfo{}, nil
}
func (f *fakeCellTransport) GetSignalFast(ctx context.Context) (*transport.CellSignalInfo, error) {
	return &transport.CellSignalInfo{}, nil
}
func (f *fakeCellTransport) GetStatus(ctx context.Context) (*transport.CellStatus, error) {
	return &transport.CellStatus{}, nil
}
func (f *fakeCellTransport) GetDataStatus(ctx context.Context) (*transport.CellDataStatus, error) {
	return &transport.CellDataStatus{}, nil
}
func (f *fakeCellTransport) ConnectData(ctx context.Context, apn string) error { return nil }
func (f *fakeCellTransport) DisconnectData(ctx context.Context) error          { return nil }
func (f *fakeCellTransport) UnlockPIN(ctx context.Context, pin string) error   { return nil }
func (f *fakeCellTransport) GetCellInfo(ctx context.Context) (*transport.CellInfo, error) {
	return &transport.CellInfo{}, nil
}
func (f *fakeCellTransport) ExecAT(ctx context.Context, command string, timeout time.Duration) (string, error) {
	return "OK", nil
}
func (f *fakeCellTransport) Close() error { return nil }

func (f *fakeCellTransport) sends() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]string{}, f.sent...)
}

// TestCellular_RawTextSentVerbatimToDestination verifies that an OOB frame
// (RawText) goes out without the SMS prefix or attribution and to the
// per-delivery destination rather than the configured numbers. [MESHSAT-756]
func TestCellular_RawTextSentVerbatimToDestination(t *testing.T) {
	cell := newFakeCellTransport()
	gw := NewCellularGateway(CellularConfig{
		SMSPrefix:          "[MeshSat]",
		MaxSMSSegments:     1,
		DestinationNumbers: []string{"+31000000000"},
	}, cell, nil)
	ctx := context.Background()

	const frameText = "MS:9W899JR000002098WTQ26XJ7V4DYYXQ28AEY1BVR"
	if err := gw.sendSMSSync(ctx, &transport.MeshMessage{
		DecodedText:     frameText,
		RawText:         true,
		Destination:     "+31653207829",
		SMSDestinations: []string{"+31653207829"},
	}); err != nil {
		t.Fatalf("send raw: %v", err)
	}
	if err := gw.sendSMSSync(ctx, &transport.MeshMessage{From: 0x1234, DecodedText: "hello"}); err != nil {
		t.Fatalf("send plain: %v", err)
	}

	sends := cell.sends()
	if len(sends) != 2 {
		t.Fatalf("sends %d, want 2", len(sends))
	}
	if sends[0][0] != "+31653207829" || sends[0][1] != frameText {
		t.Fatalf("raw send: %v", sends[0])
	}
	// The plain path keeps its prefix (GSM-sanitised, so the brackets become
	// parentheses) and attribution, and goes to the configured number.
	if sends[1][0] != "+31000000000" || !strings.Contains(sends[1][1], "MeshSat") || !strings.HasSuffix(sends[1][1], ": hello") {
		t.Fatalf("plain send: %v", sends[1])
	}
}

// TestCellular_InboundCarriesSenderAddress verifies that an inbound SMS now
// records the sender number as FromAddr, which is the reply address for an
// OOB management frame. [MESHSAT-756]
func TestCellular_InboundCarriesSenderAddress(t *testing.T) {
	cell := newFakeCellTransport()
	gw := NewCellularGateway(CellularConfig{SMSPrefix: "[MeshSat]", MaxSMSSegments: 1}, cell, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := gw.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop()

	data, _ := json.Marshal(transport.SMSMessage{Sender: "+31653618463", Text: "MS:frame"})
	cell.events <- transport.CellEvent{Type: "sms_received", Message: "MS:frame", Data: data}

	select {
	case in := <-gw.Receive():
		if in.Source != "cellular" || in.Text != "MS:frame" || in.FromAddr != "+31653618463" {
			t.Fatalf("inbound: %+v", in)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no inbound message within 3 s")
	}
}

// A plaintext peer (the Hub) relays "[origin] text": the text is kept, the
// message is flagged Plain, and the Hub's echo of this kit's own SMS (an
// origin that is not an allowed sender) is dropped. [MESHSAT-962]
func TestCellular_PlaintextPeerHubFormatAndEchoGuard(t *testing.T) {
	cell := newFakeCellTransport()
	gw := NewCellularGateway(CellularConfig{SMSPrefix: "[MeshSat]", MaxSMSSegments: 1,
		AllowedSenders: []string{"+31653207829", "+3197010258258"}, PlaintextPeers: []string{"+3197010258258"}}, cell, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := gw.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop()

	send := func(sender, text string) {
		data, _ := json.Marshal(transport.SMSMessage{Sender: sender, Text: text})
		cell.events <- transport.CellEvent{Type: "sms_received", Message: text, Data: data}
	}
	// 1. echo: origin is this kit (+31653618463), not an allowed sender -> dropped
	send("+3197010258258", "[+31653618463] hello from myself")
	// 2. relayed from the peer kit -> body kept, Plain set
	send("+3197010258258", "[+31653207829] hello via hub")
	select {
	case in := <-gw.Receive():
		if in.Text != "hello via hub" || !in.Plain || in.FromAddr != "+3197010258258" {
			t.Fatalf("inbound: %+v", in)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no inbound message within 3 s")
	}
	// 3. direct from the peer (encrypted path): untouched, not Plain
	send("+31653207829", "QUJD")
	select {
	case in := <-gw.Receive():
		if in.Text != "QUJD" || in.Plain {
			t.Fatalf("direct inbound: %+v", in)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no direct inbound within 3 s")
	}
}

func TestParseHubRoutedSMS(t *testing.T) {
	cases := []struct {
		in, origin, body string
		ok               bool
	}{
		{"[+31653207829] hello via hub", "+31653207829", "hello via hub", true},
		{"[300234063904190] pos", "300234063904190", "pos", true},
		{"[+316] ", "+316", "", true},
		{"hello", "", "", false},
		{"[] x", "", "", false},
		{"MS:frame", "", "", false},
	}
	for _, c := range cases {
		o, b, ok := ParseHubRoutedSMS(c.in)
		if ok != c.ok || o != c.origin || b != c.body {
			t.Errorf("%q: got (%q,%q,%v) want (%q,%q,%v)", c.in, o, b, ok, c.origin, c.body, c.ok)
		}
	}
}
