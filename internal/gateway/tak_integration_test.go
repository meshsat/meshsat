package gateway

import (
	"bufio"
	"context"
	"encoding/xml"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/transport"
)

// mockTAKServer is a simple TCP server that accepts CoT XML events.
type mockTAKServer struct {
	listener net.Listener
	received []CotEvent
	mu       sync.Mutex
	wg       sync.WaitGroup
}

func newMockTAKServer(t *testing.T) *mockTAKServer {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mock tak: listen: %v", err)
	}
	s := &mockTAKServer{listener: l}
	s.wg.Add(1)
	go s.accept()
	return s
}

func (s *mockTAKServer) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go s.handleConn(conn)
	}
}

func (s *mockTAKServer) handleConn(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 256*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev CotEvent
		if err := xml.Unmarshal(line, &ev); err != nil {
			continue
		}
		s.mu.Lock()
		s.received = append(s.received, ev)
		s.mu.Unlock()
	}
}

func (s *mockTAKServer) addr() string {
	return s.listener.Addr().String()
}

func (s *mockTAKServer) close() {
	s.listener.Close()
	s.wg.Wait()
}

func (s *mockTAKServer) events() []CotEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CotEvent, len(s.received))
	copy(out, s.received)
	return out
}

func TestTAKIntegration_ForwardPosition(t *testing.T) {
	srv := newMockTAKServer(t)
	defer srv.close()

	host, port := splitHostPort(t, srv.addr())
	cfg := TAKConfig{
		Host:           host,
		Port:           port,
		CallsignPrefix: "TEST",
		CotStaleSec:    300,
	}

	gw := NewTAKGateway(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := gw.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop()

	// Send a mesh position message
	msg := &transport.MeshMessage{
		From:        0xAABBCCDD,
		To:          0xFFFFFFFF,
		PortNum:     3, // POSITION_APP
		DecodedText: "Position report from field",
	}
	if err := gw.Forward(ctx, msg); err != nil {
		t.Fatalf("forward: %v", err)
	}

	// Wait for delivery
	time.Sleep(200 * time.Millisecond)

	events := srv.events()
	if len(events) == 0 {
		t.Fatal("expected at least 1 CoT event, got 0")
	}

	ev := events[0]
	if ev.Type != CotEventTypePosition {
		t.Errorf("type: got %q, want %q", ev.Type, CotEventTypePosition)
	}
	if !strings.HasPrefix(ev.UID, "meshsat-") {
		t.Errorf("uid: got %q, expected meshsat- prefix", ev.UID)
	}
	if ev.Detail == nil || ev.Detail.Contact == nil {
		t.Fatal("detail/contact is nil")
	}
	if !strings.HasPrefix(ev.Detail.Contact.Callsign, "TEST-") {
		t.Errorf("callsign: got %q, expected TEST- prefix", ev.Detail.Contact.Callsign)
	}

	// Check status
	status := gw.Status()
	if !status.Connected {
		t.Error("expected connected=true")
	}
	if status.MessagesOut < 1 {
		t.Errorf("expected msgsOut >= 1, got %d", status.MessagesOut)
	}
}

func TestTAKIntegration_ForwardChat(t *testing.T) {
	srv := newMockTAKServer(t)
	defer srv.close()

	host, port := splitHostPort(t, srv.addr())
	cfg := TAKConfig{
		Host:           host,
		Port:           port,
		CallsignPrefix: "TEST",
		CotStaleSec:    300,
	}

	gw := NewTAKGateway(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := gw.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop()

	msg := &transport.MeshMessage{
		From:        0x12345678,
		PortNum:     1, // TEXT_MESSAGE_APP
		DecodedText: "Hello from mesh",
	}
	if err := gw.Forward(ctx, msg); err != nil {
		t.Fatalf("forward: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	events := srv.events()
	if len(events) == 0 {
		t.Fatal("expected at least 1 CoT event")
	}
	if events[0].Type != CotEventTypeChat {
		t.Errorf("type: got %q, want %q", events[0].Type, CotEventTypeChat)
	}
}

func TestTAKIntegration_ForwardTelemetry(t *testing.T) {
	srv := newMockTAKServer(t)
	defer srv.close()

	host, port := splitHostPort(t, srv.addr())
	cfg := TAKConfig{
		Host:           host,
		Port:           port,
		CallsignPrefix: "TEST",
		CotStaleSec:    300,
	}

	gw := NewTAKGateway(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := gw.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop()

	msg := &transport.MeshMessage{
		From:        0xDEADBEEF,
		PortNum:     67, // TELEMETRY_APP
		DecodedText: "temperature=22.5C battery=85%",
	}
	if err := gw.Forward(ctx, msg); err != nil {
		t.Fatalf("forward: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	events := srv.events()
	if len(events) == 0 {
		t.Fatal("expected at least 1 CoT event")
	}
	if events[0].Type != CotEventTypeSensor {
		t.Errorf("type: got %q, want %q", events[0].Type, CotEventTypeSensor)
	}
}

func TestTAKIntegration_ReceiveInbound(t *testing.T) {
	// Start a mock TAK server that sends a CoT event to the client
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	// Accept connection and send a CoT event
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		ev := BuildPositionEvent("TAK-OPERATOR-1", "ALPHA-1", 52.3676, 4.9041, 10.0, 300)
		data, _ := MarshalCotEvent(ev)
		conn.Write(append(data, '\n'))
		// Keep connection alive
		time.Sleep(2 * time.Second)
	}()

	host, port := splitHostPort(t, listener.Addr().String())
	cfg := TAKConfig{
		Host:           host,
		Port:           port,
		CallsignPrefix: "TEST",
		CotStaleSec:    300,
	}

	gw := NewTAKGateway(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := gw.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop()

	// Read from receive channel
	select {
	case msg := <-gw.Receive():
		if msg.Source != "tak" {
			t.Errorf("source: got %q, want tak", msg.Source)
		}
		if !strings.Contains(msg.Text, "ALPHA-1") {
			t.Errorf("text should contain callsign: %q", msg.Text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for inbound message")
	}
}

func TestTAKIntegration_ConnectionRefused(t *testing.T) {
	cfg := TAKConfig{
		Host:           "127.0.0.1",
		Port:           19999, // nothing listening
		CallsignPrefix: "TEST",
		CotStaleSec:    300,
	}

	gw := NewTAKGateway(cfg, nil)
	ctx := context.Background()

	err := gw.Start(ctx)
	if err == nil {
		gw.Stop()
		t.Fatal("expected connection error, got nil")
	}
}

func TestTAKIntegration_MultipleMessages(t *testing.T) {
	srv := newMockTAKServer(t)
	defer srv.close()

	host, port := splitHostPort(t, srv.addr())
	cfg := TAKConfig{
		Host:           host,
		Port:           port,
		CallsignPrefix: "TEST",
		CotStaleSec:    300,
	}

	gw := NewTAKGateway(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := gw.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop()

	// Send 5 messages rapidly
	for i := 0; i < 5; i++ {
		msg := &transport.MeshMessage{
			From:        uint32(0x1000 + i),
			PortNum:     1,
			DecodedText: "msg",
		}
		gw.Forward(ctx, msg)
	}

	time.Sleep(500 * time.Millisecond)

	events := srv.events()
	if len(events) != 5 {
		t.Errorf("expected 5 events, got %d", len(events))
	}

	status := gw.Status()
	if status.MessagesOut != 5 {
		t.Errorf("msgsOut: got %d, want 5", status.MessagesOut)
	}
}

// splitHostPort splits "host:port" for test config.
func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split host port %q: %v", addr, err)
	}
	port := 0
	for _, c := range portStr {
		port = port*10 + int(c-'0')
	}
	return host, port
}
