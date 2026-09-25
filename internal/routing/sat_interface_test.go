package routing

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"meshsat/internal/reticulum"
	"meshsat/internal/transport"
)

// CrossTalk's IridiumIMTCodec test vector: bytes(range(256)) + bytes(range(244)).
func crossTalkVector() []byte {
	v := make([]byte, 0, 500)
	for i := 0; i < 256; i++ {
		v = append(v, byte(i))
	}
	for i := 0; i < 244; i++ {
		v = append(v, byte(i))
	}
	return v
}

func validRNSPacket(t *testing.T) []byte {
	t.Helper()
	id, _ := reticulum.GenerateIdentity()
	a, err := reticulum.NewAnnounce(id, "imt.test", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	return a.MarshalPacket()
}

func TestIMTFrameCodec(t *testing.T) {
	packet := crossTalkVector()
	msg := EncodeIMTFrame(packet)
	if !bytes.Equal(msg[:5], []byte("RNSI\x01")) || len(msg) != 505 {
		t.Fatalf("encode: %x len %d", msg[:5], len(msg))
	}
	got, framed, err := DecodeIMTFrame(msg, true)
	if err != nil || !framed || !bytes.Equal(got, packet) {
		t.Fatalf("strict round trip: %v %v", err, framed)
	}
	// IridiumIMTCodec.decode raises on a foreign message and on a bare header.
	if _, _, err := DecodeIMTFrame([]byte("not-reticulum"), true); err == nil {
		t.Fatal("strict accepted a foreign message")
	}
	if _, _, err := DecodeIMTFrame(IMTFrameHeader, true); !errors.Is(err, ErrIMTFrameEmpty) {
		t.Fatalf("bare header: %v", err)
	}
	if _, _, err := DecodeIMTFrame([]byte("RNSI\x02abc"), false); !errors.Is(err, ErrIMTFrameVersion) {
		t.Fatalf("version: %v", err)
	}
	// Framing off: a bare packet passes through untouched...
	rns := validRNSPacket(t)
	got, framed, err = DecodeIMTFrame(rns, false)
	if err != nil || framed || !bytes.Equal(got, rns) {
		t.Fatalf("pass-through: %v %v", err, framed)
	}
	// ...a framed valid packet is unwrapped anyway (auto-detect)...
	got, framed, err = DecodeIMTFrame(EncodeIMTFrame(rns), false)
	if err != nil || !framed || !bytes.Equal(got, rns) {
		t.Fatalf("auto-detect: %v %v", err, framed)
	}
	// ...and a bare message that merely starts with "RNSI" but does not
	// parse as a packet after the header stays as it is.
	odd := append([]byte("RNSI\x01"), 0xFF, 0x00)
	got, framed, err = DecodeIMTFrame(odd, false)
	if err != nil || framed || !bytes.Equal(got, odd) {
		t.Fatalf("odd message: %v %v %x", err, framed, got)
	}
}

func TestIMTDedup(t *testing.T) {
	d := newIMTDedup(time.Minute, 2)
	now := time.Now()
	if d.Seen([]byte("a"), now) || !d.Seen([]byte("a"), now.Add(time.Second)) {
		t.Fatal("repeat not caught")
	}
	if d.Seen([]byte("a"), now.Add(2*time.Minute)) {
		t.Fatal("expired entry still caught")
	}
	d.Seen([]byte("b"), now)
	d.Seen([]byte("c"), now.Add(time.Second)) // evicts the oldest
	if len(d.seen) > 2 {
		t.Fatalf("cap not enforced: %d", len(d.seen))
	}
}

// mockSat is a SatTransport whose MT mailbox the test fills.
type mockSat struct {
	mu     sync.Mutex
	sent   [][]byte
	mt     [][]byte
	events chan transport.SatEvent
}

func newMockSat() *mockSat { return &mockSat{events: make(chan transport.SatEvent, 8)} }
func (m *mockSat) Subscribe(ctx context.Context) (<-chan transport.SatEvent, error) {
	return m.events, nil
}
func (m *mockSat) Send(ctx context.Context, data []byte) (*transport.SatResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, append([]byte(nil), data...))
	return &transport.SatResult{MOStatus: 0}, nil
}
func (m *mockSat) SendText(ctx context.Context, text string) (*transport.SatResult, error) {
	return m.Send(ctx, []byte(text))
}
func (m *mockSat) Receive(ctx context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.mt) == 0 {
		return nil, nil
	}
	d := m.mt[0]
	m.mt = m.mt[1:]
	return d, nil
}
func (m *mockSat) MailboxCheck(ctx context.Context) (*transport.SatResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.mt) == 0 {
		return &transport.SatResult{}, nil
	}
	return &transport.SatResult{MTStatus: 1, MTLength: len(m.mt[0])}, nil
}
func (m *mockSat) GetSignal(ctx context.Context) (*transport.SignalInfo, error) {
	return &transport.SignalInfo{}, nil
}
func (m *mockSat) GetSignalFast(ctx context.Context) (*transport.SignalInfo, error) {
	return &transport.SignalInfo{}, nil
}
func (m *mockSat) GetStatus(ctx context.Context) (*transport.SatStatus, error) {
	return &transport.SatStatus{Connected: true, Type: "imt"}, nil
}
func (m *mockSat) GetFirmwareVersion(ctx context.Context) (string, error) { return "mock", nil }
func (m *mockSat) Close() error                                           { return nil }
func (m *mockSat) push(mt []byte) {
	m.mu.Lock()
	m.mt = append(m.mt, mt)
	m.mu.Unlock()
	m.events <- transport.SatEvent{Type: "mt_received"}
}
func (m *mockSat) lastSent() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		return nil
	}
	return m.sent[len(m.sent)-1]
}

func TestSatInterfaceIMTFraming(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sat := newMockSat()
	var mu sync.Mutex
	var got [][]byte
	iface := NewSatInterface(SatInterfaceConfig{Name: "iridium_imt_0", Type: "iridium", MTU: 600}, sat,
		func(p []byte) { mu.Lock(); got = append(got, p); mu.Unlock() })
	if err := iface.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer iface.Stop()
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(got) }
	pkt := validRNSPacket(t)

	// Off: bare on the wire, bare and framed both accepted on receive.
	if err := iface.Send(ctx, pkt); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sat.lastSent(), pkt) {
		t.Fatalf("framed while off")
	}
	sat.push(pkt)
	waitCond(t, "bare MT delivered", 3*time.Second, func() bool { return count() == 1 })
	sat.push(EncodeIMTFrame(pkt))
	time.Sleep(200 * time.Millisecond)
	if count() != 1 {
		t.Fatalf("duplicate (framed copy of the same packet) not dropped: %d", count())
	}
	pkt2 := validRNSPacket(t)
	sat.push(EncodeIMTFrame(pkt2))
	waitCond(t, "framed MT auto-detected", 3*time.Second, func() bool { return count() == 2 })
	mu.Lock()
	if !bytes.Equal(got[1], pkt2) {
		t.Fatalf("framed MT not unwrapped")
	}
	mu.Unlock()

	// On: framed on the wire, MTU counts the header, bare rejected on receive.
	iface.SetRNSFraming(true)
	if err := iface.Send(ctx, pkt); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sat.lastSent(), EncodeIMTFrame(pkt)) {
		t.Fatalf("not framed while on")
	}
	if err := iface.Send(ctx, make([]byte, 596)); err == nil {
		t.Fatal("MTU did not count the header")
	}
	pkt3 := validRNSPacket(t)
	sat.push(pkt3) // bare: CrossTalk's rule, rejected
	sat.push(EncodeIMTFrame(pkt3))
	waitCond(t, "framed MT delivered while on", 3*time.Second, func() bool { return count() == 3 })
	mu.Lock()
	if !bytes.Equal(got[2], pkt3) {
		t.Fatalf("wrong packet delivered")
	}
	mu.Unlock()
	sat.push([]byte("RNSI\x02zz"))
	time.Sleep(200 * time.Millisecond)
	if count() != 3 {
		t.Fatalf("bad version accepted")
	}
}
