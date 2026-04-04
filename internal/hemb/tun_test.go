package hemb

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
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
func (m *mockTUNBonder) InspectGeneration(_ uint8, _ uint16) (*GenerationInspection, bool) {
	return nil, false
}

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
	tunAvailable(t)
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

// ════════════════════════════════════════════════════════════════════════════
// Phase TUN integration: TCP echo over HeMB TUN with mock bearers
// Requires root (CAP_NET_ADMIN) for real TUN device creation.
// ════════════════════════════════════════════════════════════════════════════

func tunAvailable(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Skip("TUN tests require real TUN device — skip in CI")
	}
	if os.Getuid() != 0 {
		t.Skip("requires root for TUN device creation")
	}
}

// setupTUNPair creates two TUN adapters cross-connected through mock bearers.
// Returns adapters, cancel func, and cleanup. The adapters' Start() loops are
// already running. Caller must defer cleanup().
func setupTUNPair(t *testing.T, bearerSetup func(a, b *TUNAdapter) (bearersA, bearersB []BearerProfile)) (
	adapterA, adapterB *TUNAdapter, cancel context.CancelFunc, cleanup func(),
) {
	t.Helper()

	devA, err := OpenTUN("hembt0", 228)
	if err != nil {
		t.Fatalf("OpenTUN A: %v", err)
	}
	devB, err := OpenTUN("hembt1", 228)
	if err != nil {
		devA.Close()
		t.Fatalf("OpenTUN B: %v", err)
	}

	// Create adapters with dummy single bearer first (will Rebind after setup).
	dummyBearer := BearerProfile{
		Index: 0, ChannelType: "tcp", MTU: 1400, HealthScore: 100,
		SendFn: func(_ context.Context, _ []byte) error { return nil },
	}
	adapterA, _ = NewTUNAdapter(devA, []BearerProfile{dummyBearer}, TUNConfig{MTU: 228})
	adapterB, _ = NewTUNAdapter(devB, []BearerProfile{dummyBearer}, TUNConfig{MTU: 228})

	// Let caller define bearers that reference the adapters.
	bearersA, bearersB := bearerSetup(adapterA, adapterB)
	adapterA.Rebind(bearersA)
	adapterB.Rebind(bearersB)

	ctx, cancelFn := context.WithTimeout(context.Background(), 30*time.Second)

	go adapterA.Start(ctx)
	go adapterB.Start(ctx)

	// Configure IP addresses on the TUN interfaces.
	for _, args := range [][]string{
		{"ip", "addr", "add", "10.99.0.1/24", "dev", devA.Name()},
		{"ip", "link", "set", devA.Name(), "up"},
		{"ip", "addr", "add", "10.99.0.2/24", "dev", devB.Name()},
		{"ip", "link", "set", devB.Name(), "up"},
	} {
		out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
		if err != nil {
			cancelFn()
			adapterA.Close()
			adapterB.Close()
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	// Give kernel time to bring interfaces up.
	time.Sleep(100 * time.Millisecond)

	cleanup = func() {
		cancelFn()
		adapterA.Close()
		adapterB.Close()
	}
	return adapterA, adapterB, cancelFn, cleanup
}

// TestTUNTCPEcho creates two TUN devices connected through HeMB bonders
// with a single mock bearer (N=1 passthrough) and verifies a TCP echo
// round-trip through the tunnel.
func TestTUNTCPEcho(t *testing.T) {
	tunAvailable(t)

	_, _, _, cleanup := setupTUNPair(t, func(a, b *TUNAdapter) ([]BearerProfile, []BearerProfile) {
		meshA := BearerProfile{
			Index: 0, InterfaceID: "mesh_0", ChannelType: "mesh",
			MTU: 237, CostPerMsg: 0, HealthScore: 80, HeaderMode: HeaderModeCompact,
			SendFn: func(_ context.Context, data []byte) error {
				_, err := b.Bonder().ReceiveSymbol(0, data)
				return err
			},
		}
		meshB := BearerProfile{
			Index: 0, InterfaceID: "mesh_0", ChannelType: "mesh",
			MTU: 237, CostPerMsg: 0, HealthScore: 80, HeaderMode: HeaderModeCompact,
			SendFn: func(_ context.Context, data []byte) error {
				_, err := a.Bonder().ReceiveSymbol(0, data)
				return err
			},
		}
		return []BearerProfile{meshA}, []BearerProfile{meshB}
	})
	defer cleanup()

	// Start TCP echo server on side B.
	ln, err := net.Listen("tcp", "10.99.0.2:9876")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()

	// Connect from side A.
	dialer := net.Dialer{
		LocalAddr: &net.TCPAddr{IP: net.ParseIP("10.99.0.1")},
		Timeout:   5 * time.Second,
	}
	conn, err := dialer.Dial("tcp", "10.99.0.2:9876")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	testData := []byte("Hello from HeMB TUN — TCP echo over single mock bearer")
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(testData); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf := make([]byte, len(testData))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(buf, testData) {
		t.Fatalf("echo mismatch: got %q, want %q", buf, testData)
	}
}

// TestTUNBearerRemovalTCP is the definitive test (axiom 7):
// Start a TCP session over dual-bearer HeMB TUN (mesh + SBD), remove the
// LoRa/mesh bearer mid-session via Rebind, and verify SBD sustains the TCP
// connection without reset.
func TestTUNBearerRemovalTCP(t *testing.T) {
	tunAvailable(t)

	var adapterA, adapterB *TUNAdapter
	var meshDead atomic.Bool

	adapterA, adapterB, _, cleanup := setupTUNPair(t, func(a, b *TUNAdapter) ([]BearerProfile, []BearerProfile) {
		// Mesh bearer (free) — will be "removed" mid-test.
		meshA := BearerProfile{
			Index: 0, InterfaceID: "mesh_0", ChannelType: "mesh",
			MTU: 237, CostPerMsg: 0, LossRate: 0.10, HealthScore: 80,
			HeaderMode: HeaderModeCompact,
			SendFn: func(_ context.Context, data []byte) error {
				if meshDead.Load() {
					return errors.New("bearer offline — antenna removed")
				}
				b.Bonder().ReceiveSymbol(0, data)
				return nil
			},
		}
		meshB := BearerProfile{
			Index: 0, InterfaceID: "mesh_0", ChannelType: "mesh",
			MTU: 237, CostPerMsg: 0, LossRate: 0.10, HealthScore: 80,
			HeaderMode: HeaderModeCompact,
			SendFn: func(_ context.Context, data []byte) error {
				if meshDead.Load() {
					return errors.New("bearer offline — antenna removed")
				}
				a.Bonder().ReceiveSymbol(0, data)
				return nil
			},
		}
		// SBD bearer (paid) — always available.
		sbdA := BearerProfile{
			Index: 1, InterfaceID: "iridium_0", ChannelType: "iridium_sbd",
			MTU: 340, CostPerMsg: 0.05, LossRate: 0, HealthScore: 90,
			HeaderMode: HeaderModeCompact,
			SendFn: func(_ context.Context, data []byte) error {
				b.Bonder().ReceiveSymbol(1, data)
				return nil
			},
		}
		sbdB := BearerProfile{
			Index: 1, InterfaceID: "iridium_0", ChannelType: "iridium_sbd",
			MTU: 340, CostPerMsg: 0.05, LossRate: 0, HealthScore: 90,
			HeaderMode: HeaderModeCompact,
			SendFn: func(_ context.Context, data []byte) error {
				a.Bonder().ReceiveSymbol(1, data)
				return nil
			},
		}
		return []BearerProfile{meshA, sbdA}, []BearerProfile{meshB, sbdB}
	})
	defer cleanup()

	// Start TCP echo server on side B.
	ln, err := net.Listen("tcp", "10.99.0.2:9876")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()

	// Connect from side A.
	dialer := net.Dialer{
		LocalAddr: &net.TCPAddr{IP: net.ParseIP("10.99.0.1")},
		Timeout:   5 * time.Second,
	}
	conn, err := dialer.Dial("tcp", "10.99.0.2:9876")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Phase 1: dual-bearer active — verify TCP echo works.
	phase1 := []byte("Phase 1: dual-bearer active — mesh + SBD")
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(phase1); err != nil {
		t.Fatalf("phase 1 write: %v", err)
	}
	buf := make([]byte, len(phase1))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("phase 1 read: %v", err)
	}
	if !bytes.Equal(buf, phase1) {
		t.Fatalf("phase 1 echo mismatch")
	}
	t.Log("Phase 1 PASS: dual-bearer TCP echo working")

	// Phase 2: remove LoRa bearer — simulate antenna removal.
	// Mark mesh as dead (SendFn returns error) and Rebind both sides to SBD-only.
	t.Log("Removing LoRa bearer mid-session...")
	meshDead.Store(true)

	// SBD-only bearers for Rebind (N=1 passthrough).
	sbdOnlyA := BearerProfile{
		Index: 0, InterfaceID: "iridium_0", ChannelType: "iridium_sbd",
		MTU: 340, CostPerMsg: 0.05, LossRate: 0, HealthScore: 90,
		HeaderMode: HeaderModeCompact,
		SendFn: func(_ context.Context, data []byte) error {
			adapterB.Bonder().ReceiveSymbol(0, data)
			return nil
		},
	}
	sbdOnlyB := BearerProfile{
		Index: 0, InterfaceID: "iridium_0", ChannelType: "iridium_sbd",
		MTU: 340, CostPerMsg: 0.05, LossRate: 0, HealthScore: 90,
		HeaderMode: HeaderModeCompact,
		SendFn: func(_ context.Context, data []byte) error {
			adapterA.Bonder().ReceiveSymbol(0, data)
			return nil
		},
	}
	adapterA.Rebind([]BearerProfile{sbdOnlyA})
	adapterB.Rebind([]BearerProfile{sbdOnlyB})
	t.Log("Rebind complete: both sides now SBD-only (N=1)")

	// Phase 3: SBD-only — verify SAME TCP connection still works.
	// TCP retransmission handles any in-flight packets lost during Rebind.
	phase3 := []byte("Phase 3: SBD-only — LoRa antenna removed, connection sustained")
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write(phase3); err != nil {
		t.Fatalf("phase 3 write (bearer removal should not break TCP): %v", err)
	}
	buf = make([]byte, len(phase3))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("phase 3 read (bearer removal should not break TCP): %v", err)
	}
	if !bytes.Equal(buf, phase3) {
		t.Fatalf("phase 3 echo mismatch")
	}
	t.Log("Phase 3 PASS: TCP connection survived LoRa bearer removal — SBD sustained session")
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
