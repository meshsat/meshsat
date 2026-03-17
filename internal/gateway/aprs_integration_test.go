package gateway

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/transport"
)

// mockKISSTNC is a TCP server that simulates a Direwolf KISS TNC.
type mockKISSTNC struct {
	listener net.Listener
	received [][]byte // raw AX.25 payloads received (KISS-decoded)
	mu       sync.Mutex
	wg       sync.WaitGroup
	sendCh   chan []byte // frames to send to the client
}

func newMockKISSTNC(t *testing.T) *mockKISSTNC {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mock kiss: listen: %v", err)
	}
	s := &mockKISSTNC{
		listener: l,
		sendCh:   make(chan []byte, 10),
	}
	s.wg.Add(1)
	go s.accept()
	return s
}

func (s *mockKISSTNC) accept() {
	defer s.wg.Done()
	conn, err := s.listener.Accept()
	if err != nil {
		return
	}
	s.wg.Add(2)
	go s.readFromClient(conn)
	go s.writeToClient(conn)
}

func (s *mockKISSTNC) readFromClient(conn net.Conn) {
	defer s.wg.Done()
	buf := make([]byte, 4096)
	var frame []byte
	inFrame := false

	for {
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		for _, b := range buf[:n] {
			if b == kissFEND {
				if inFrame && len(frame) > 0 {
					decoded, err := KISSDecode(frame)
					if err == nil {
						s.mu.Lock()
						s.received = append(s.received, decoded)
						s.mu.Unlock()
					}
					frame = nil
				}
				inFrame = true
				continue
			}
			if inFrame {
				frame = append(frame, b)
			}
		}
	}
}

func (s *mockKISSTNC) writeToClient(conn net.Conn) {
	defer s.wg.Done()
	for data := range s.sendCh {
		kissFrame := KISSEncode(data)
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		conn.Write(kissFrame)
	}
}

func (s *mockKISSTNC) addr() string {
	return s.listener.Addr().String()
}

func (s *mockKISSTNC) close() {
	close(s.sendCh)
	s.listener.Close()
	s.wg.Wait()
}

func (s *mockKISSTNC) frames() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.received))
	for i, f := range s.received {
		c := make([]byte, len(f))
		copy(c, f)
		out[i] = c
	}
	return out
}

// sendAPRSPosition sends a mock APRS position frame to connected client.
func (s *mockKISSTNC) sendAPRSPosition(src AX25Address, lat, lon float64, comment string) {
	dst := AX25Address{Call: "APRS", SSID: 0}
	info := EncodeAPRSPosition(lat, lon, '/', '-', comment)
	frame := EncodeAX25Frame(dst, src, nil, info)
	s.sendCh <- frame
}

func TestAPRSIntegration_ForwardMessage(t *testing.T) {
	tnc := newMockKISSTNC(t)
	defer tnc.close()

	host, port := splitHostPort(t, tnc.addr())
	cfg := APRSConfig{
		KISSHost:     host,
		KISSPort:     port,
		Callsign:     "TEST",
		SSID:         10,
		FrequencyMHz: 144.800,
	}

	gw := NewAPRSGateway(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := gw.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop()

	// Forward a mesh message to APRS
	msg := &transport.MeshMessage{
		From:        0xAABBCCDD,
		PortNum:     1,
		DecodedText: "Hello from mesh via APRS",
	}
	if err := gw.Forward(ctx, msg); err != nil {
		t.Fatalf("forward: %v", err)
	}

	time.Sleep(300 * time.Millisecond)

	frames := tnc.frames()
	if len(frames) == 0 {
		t.Fatal("expected at least 1 KISS frame sent to TNC, got 0")
	}

	// Decode the AX.25 frame
	ax25, err := DecodeAX25Frame(frames[0])
	if err != nil {
		t.Fatalf("decode AX.25: %v", err)
	}
	if ax25.Src.Call != "TEST" {
		t.Errorf("src callsign: got %q, want TEST", ax25.Src.Call)
	}
	if ax25.Src.SSID != 10 {
		t.Errorf("src SSID: got %d, want 10", ax25.Src.SSID)
	}
	if !strings.Contains(string(ax25.Info), "Hello from mesh via APRS") {
		t.Errorf("info field should contain message: %q", string(ax25.Info))
	}

	status := gw.Status()
	if !status.Connected {
		t.Error("expected connected=true")
	}
	if status.MessagesOut < 1 {
		t.Errorf("expected msgsOut >= 1, got %d", status.MessagesOut)
	}
}

func TestAPRSIntegration_ReceivePosition(t *testing.T) {
	tnc := newMockKISSTNC(t)
	defer tnc.close()

	host, port := splitHostPort(t, tnc.addr())
	cfg := APRSConfig{
		KISSHost:     host,
		KISSPort:     port,
		Callsign:     "TEST",
		SSID:         10,
		FrequencyMHz: 144.800,
	}

	gw := NewAPRSGateway(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := gw.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop()

	// Give the gateway a moment to start reading
	time.Sleep(100 * time.Millisecond)

	// Send an APRS position from the mock TNC
	tnc.sendAPRSPosition(
		AX25Address{Call: "PA3XYZ", SSID: 7},
		52.3676, 4.9041,
		"Mobile station",
	)

	// Read from receive channel
	select {
	case msg := <-gw.Receive():
		if msg.Source != "aprs" {
			t.Errorf("source: got %q, want aprs", msg.Source)
		}
		if !strings.Contains(msg.Text, "PA3XYZ") {
			t.Errorf("text should contain callsign: %q", msg.Text)
		}
		if !strings.Contains(msg.Text, "52.") {
			t.Errorf("text should contain latitude: %q", msg.Text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for APRS inbound message")
	}

	status := gw.Status()
	if status.MessagesIn < 1 {
		t.Errorf("expected msgsIn >= 1, got %d", status.MessagesIn)
	}
}

func TestAPRSIntegration_ConnectionRefused(t *testing.T) {
	cfg := APRSConfig{
		KISSHost:     "127.0.0.1",
		KISSPort:     19998,
		Callsign:     "TEST",
		SSID:         10,
		FrequencyMHz: 144.800,
	}

	gw := NewAPRSGateway(cfg, nil)
	ctx := context.Background()

	err := gw.Start(ctx)
	if err == nil {
		gw.Stop()
		t.Fatal("expected connection error, got nil")
	}
}

func TestAPRSIntegration_GatewayType(t *testing.T) {
	cfg := DefaultAPRSConfig()
	cfg.Callsign = "TEST"
	gw := NewAPRSGateway(cfg, nil)
	if gw.Type() != "aprs" {
		t.Errorf("type: got %q, want aprs", gw.Type())
	}
}
