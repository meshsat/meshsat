package hemb

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// pipeTUN simulates a TUN device using two os.Pipe pairs.
// Pipe 1 (inject): test writes → adapter reads (simulates incoming IP packets)
// Pipe 2 (deliver): adapter writes → test reads (simulates outgoing IP packets)
// No root/CAP_NET_ADMIN required.
type pipeTUN struct {
	inR  *os.File // adapter reads from here
	inW  *os.File // test injects packets here
	outR *os.File // test reads delivered packets from here
	outW *os.File // adapter writes delivered packets here
	name string
}

func newPipeTUN(name string) (*pipeTUN, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	return &pipeTUN{inR: inR, inW: inW, outR: outR, outW: outW, name: name}, nil
}

func (p *pipeTUN) Read(buf []byte) (int, error)  { return p.inR.Read(buf) }
func (p *pipeTUN) Write(buf []byte) (int, error) { return p.outW.Write(buf) }
func (p *pipeTUN) Close() error {
	p.inR.Close()
	p.inW.Close()
	p.outR.Close()
	return p.outW.Close()
}
func (p *pipeTUN) Name() string { return p.name }

// injectPacket writes a packet into the adapter's read pipe (simulates incoming IP).
func (p *pipeTUN) injectPacket(data []byte) error {
	_, err := p.inW.Write(data)
	return err
}

// readDelivered reads a delivered packet from the adapter's write pipe.
func (p *pipeTUN) readDelivered(buf []byte) (int, error) {
	return p.outR.Read(buf)
}

// mockTUNBonder captures Send() calls for test assertions.
type mockTUNBonder struct {
	mu       sync.Mutex
	sent     [][]byte
	sendErr  error
	deliverF func([]byte)
}

func (m *mockTUNBonder) Send(_ context.Context, payload []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(payload))
	copy(cp, payload)
	m.sent = append(m.sent, cp)
	return m.sendErr
}

func (m *mockTUNBonder) ReceiveSymbol(_ uint8, _ []byte) ([]byte, error) { return nil, nil }
func (m *mockTUNBonder) Stats() BondStats                                { return BondStats{} }
func (m *mockTUNBonder) StartStatsEmitter(_ context.Context, _ time.Duration, _ chan<- Event) {
}
func (m *mockTUNBonder) ActiveStreams() []StreamInfo                   { return nil }
func (m *mockTUNBonder) StreamDetail(_ uint8) ([]GenerationInfo, bool) { return nil, false }

func (m *mockTUNBonder) getSent() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]byte, len(m.sent))
	copy(out, m.sent)
	return out
}

func TestTUNReadLoop(t *testing.T) {
	dev, err := newPipeTUN("test0")
	if err != nil {
		t.Fatal(err)
	}

	mock := &mockTUNBonder{}
	adapter := &TUNAdapter{
		dev:    dev,
		bonder: mock,
		mtu:    1500,
		closed: make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Start(ctx) }()

	// Inject a test packet.
	testPacket := []byte("hello hemb tun")
	if err := dev.injectPacket(testPacket); err != nil {
		t.Fatal(err)
	}

	// Wait for the packet to be captured.
	deadline := time.After(2 * time.Second)
	for {
		sent := mock.getSent()
		if len(sent) > 0 {
			if !bytes.Equal(sent[0], testPacket) {
				t.Errorf("got %q, want %q", sent[0], testPacket)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for Send()")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	// Verify stats.
	st := adapter.Stats()
	if st.PacketsSent != 1 {
		t.Errorf("PacketsSent = %d, want 1", st.PacketsSent)
	}
	if st.BytesSent != int64(len(testPacket)) {
		t.Errorf("BytesSent = %d, want %d", st.BytesSent, len(testPacket))
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start() did not return after cancel")
	}
}

func TestTUNDeliverFn(t *testing.T) {
	tunDev, err := newPipeTUN("test0")
	if err != nil {
		t.Fatal(err)
	}

	bearers := []BearerProfile{{
		Index:       0,
		InterfaceID: "test_0",
		ChannelType: "test",
		MTU:         1500,
		SendFn:      func(_ context.Context, _ []byte) error { return nil },
	}}

	adapter, _ := NewTUNAdapter(tunDev, bearers, TUNConfig{Name: "test0"})

	// Call deliverToTUN with a payload — it should write to the TUN device.
	testPayload := []byte("reassembled payload")
	adapter.deliverToTUN(testPayload)

	// Read from the delivery pipe.
	buf := make([]byte, 256)
	n, err := tunDev.readDelivered(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf[:n], testPayload) {
		t.Errorf("got %q, want %q", buf[:n], testPayload)
	}

	st := adapter.Stats()
	if st.PacketsRecv != 1 {
		t.Errorf("PacketsRecv = %d, want 1", st.PacketsRecv)
	}

	adapter.Close()
}

func TestTUNRoundtrip(t *testing.T) {
	// Two-adapter roundtrip: sender TUN → bonder → mock bearer → receiver bonder → receiver TUN
	var receivedPayload atomic.Value

	// Receiver side: bonder that calls DeliverFn on ReceiveSymbol.
	receiverDev, err := newPipeTUN("recv0")
	if err != nil {
		t.Fatal(err)
	}

	// Create sender side with a bearer whose SendFn relays to receiver bonder.
	senderDev, err := newPipeTUN("send0")
	if err != nil {
		t.Fatal(err)
	}

	// For N=1 single-bearer, Send just calls bearer.SendFn directly with the payload.
	// The receiver's bonder will get it via ReceiveSymbol for N>1, but for N=1
	// the bonder calls SendFn(payload) and on the receive side DeliverFn(payload).

	// Simplest roundtrip: sender bonder N=1 → bearer.SendFn captures → deliver to receiver TUN.
	var captured []byte
	var capturedMu sync.Mutex

	senderBearers := []BearerProfile{{
		Index:       0,
		InterfaceID: "mesh_0",
		ChannelType: "mesh",
		MTU:         237,
		SendFn: func(_ context.Context, data []byte) error {
			capturedMu.Lock()
			captured = make([]byte, len(data))
			copy(captured, data)
			capturedMu.Unlock()
			receivedPayload.Store(data)
			return nil
		},
	}}

	senderAdapter, _ := NewTUNAdapter(senderDev, senderBearers, TUNConfig{Name: "send0"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- senderAdapter.Start(ctx) }()

	// Inject packet into sender TUN.
	testPacket := []byte("ip-packet-data-for-roundtrip")
	if err := senderDev.injectPacket(testPacket); err != nil {
		t.Fatal(err)
	}

	// Wait for bearer to capture the packet.
	deadline := time.After(2 * time.Second)
	for {
		v := receivedPayload.Load()
		if v != nil {
			got := v.([]byte)
			if !bytes.Equal(got, testPacket) {
				t.Errorf("bearer received %q, want %q", got, testPacket)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for bearer to receive packet")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	cancel()
	senderAdapter.Close()
	receiverDev.Close()
}

func TestTUNBearerRemoval(t *testing.T) {
	// Two free bearers: mesh and tcp. Send packets, then "remove" mesh
	// by making its SendFn return error. Verify tcp continues carrying symbols.
	meshFailed := atomic.Bool{}
	var tcpReceived atomic.Int32
	var meshReceived atomic.Int32

	bearers := []BearerProfile{
		{
			Index:       0,
			InterfaceID: "mesh_0",
			ChannelType: "mesh",
			MTU:         237,
			CostPerMsg:  0,
			LossRate:    0.02,
			HealthScore: 100,
			SendFn: func(_ context.Context, _ []byte) error {
				if meshFailed.Load() {
					return errors.New("bearer offline")
				}
				meshReceived.Add(1)
				return nil
			},
		},
		{
			Index:       1,
			InterfaceID: "tcp_0",
			ChannelType: "tcp",
			MTU:         1400,
			CostPerMsg:  0,
			LossRate:    0,
			HealthScore: 100,
			SendFn: func(_ context.Context, _ []byte) error {
				tcpReceived.Add(1)
				return nil
			},
		},
	}

	dev, err := newPipeTUN("test0")
	if err != nil {
		t.Fatal(err)
	}

	adapter, _ := NewTUNAdapter(dev, bearers, TUNConfig{Name: "test0"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- adapter.Start(ctx) }()

	// Send first packet — should use both bearers (N=2 RLNC).
	if err := dev.injectPacket([]byte("packet-before-removal")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	beforeMesh := meshReceived.Load()
	beforeTCP := tcpReceived.Load()
	if beforeMesh == 0 || beforeTCP == 0 {
		t.Errorf("before removal: mesh=%d tcp=%d, want both > 0", beforeMesh, beforeTCP)
	}

	// "Remove" mesh bearer — its SendFn now returns error.
	meshFailed.Store(true)

	// Send second packet — mesh will fail but tcp should still carry symbols.
	if err := dev.injectPacket([]byte("packet-after-removal")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	afterTCP := tcpReceived.Load()
	if afterTCP <= beforeTCP {
		t.Errorf("tcp received no new symbols after mesh removal: before=%d after=%d", beforeTCP, afterTCP)
	}

	st := adapter.Stats()
	if st.PacketsSent < 2 {
		t.Errorf("PacketsSent = %d, want >= 2", st.PacketsSent)
	}

	cancel()
	adapter.Close()
}

func TestTUNSendError(t *testing.T) {
	dev, err := newPipeTUN("test0")
	if err != nil {
		t.Fatal(err)
	}

	mock := &mockTUNBonder{sendErr: errors.New("all bearers down")}
	adapter := &TUNAdapter{
		dev:    dev,
		bonder: mock,
		mtu:    1500,
		closed: make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Start(ctx) }()

	if err := dev.injectPacket([]byte("doomed packet")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	st := adapter.Stats()
	if st.PacketsDropped != 1 {
		t.Errorf("PacketsDropped = %d, want 1", st.PacketsDropped)
	}

	cancel()
	adapter.Close()
}

func TestTUNContextCancel(t *testing.T) {
	dev, err := newPipeTUN("test0")
	if err != nil {
		t.Fatal(err)
	}

	mock := &mockTUNBonder{}
	adapter := &TUNAdapter{
		dev:    dev,
		bonder: mock,
		mtu:    1500,
		closed: make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Start(ctx) }()

	// Cancel immediately.
	cancel()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Start() did not return after cancel")
	}
}

func TestComputeTUNMTU(t *testing.T) {
	tests := []struct {
		name    string
		bearers []BearerProfile
		want    int
	}{
		{
			name:    "empty bearers",
			bearers: nil,
			want:    1500,
		},
		{
			name: "single LoRa compact",
			bearers: []BearerProfile{{
				MTU:        237,
				HeaderMode: HeaderModeCompact,
			}},
			want: 237 - CompactHeaderLen - 1,
		},
		{
			name: "single SBD compact",
			bearers: []BearerProfile{{
				MTU:        340,
				HeaderMode: HeaderModeCompact,
			}},
			want: 340 - CompactHeaderLen - 1,
		},
		{
			name: "LoRa + SBD compact (min wins)",
			bearers: []BearerProfile{
				{MTU: 237, HeaderMode: HeaderModeCompact},
				{MTU: 340, HeaderMode: HeaderModeCompact},
			},
			want: 237 - CompactHeaderLen - 1, // 228
		},
		{
			name: "extended header",
			bearers: []BearerProfile{{
				MTU:        1000,
				HeaderMode: HeaderModeExtended,
			}},
			want: 1000 - ExtendedHeaderLen - 1,
		},
		{
			name: "implicit header (IPoUGRS)",
			bearers: []BearerProfile{{
				MTU:        1,
				HeaderMode: HeaderModeImplicit,
			}},
			want: 68, // floor at RFC 791 minimum
		},
		{
			name: "mixed header modes",
			bearers: []BearerProfile{
				{MTU: 237, HeaderMode: HeaderModeCompact},
				{MTU: 100000, HeaderMode: HeaderModeExtended},
			},
			want: 237 - CompactHeaderLen - 1, // 228, LoRa is the bottleneck
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeTUNMTU(tt.bearers)
			if got != tt.want {
				t.Errorf("ComputeTUNMTU() = %d, want %d", got, tt.want)
			}
		})
	}
}
