package routing

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"meshsat/internal/reticulum"
)

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestTCPInterface_ServerClientExchange(t *testing.T) {
	addr := freePort(t)

	var serverReceived [][]byte
	var mu sync.Mutex

	// Start server
	server := NewTCPInterface(TCPInterfaceConfig{
		Name:       "tcp_srv",
		ListenAddr: addr,
	}, func(packet []byte) {
		mu.Lock()
		cp := make([]byte, len(packet))
		copy(cp, packet)
		serverReceived = append(serverReceived, cp)
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := server.Start(ctx); err != nil {
		t.Fatalf("server start: %v", err)
	}
	defer server.Stop()

	// Wait for server to be ready
	time.Sleep(50 * time.Millisecond)

	var clientReceived [][]byte
	var clientMu sync.Mutex

	// Start client
	client := NewTCPInterface(TCPInterfaceConfig{
		Name:        "tcp_cli",
		ConnectAddr: addr,
		Reconnect:   false,
	}, func(packet []byte) {
		clientMu.Lock()
		cp := make([]byte, len(packet))
		copy(cp, packet)
		clientReceived = append(clientReceived, cp)
		clientMu.Unlock()
	})

	if err := client.Start(ctx); err != nil {
		t.Fatalf("client start: %v", err)
	}
	defer client.Stop()

	// Wait for connection
	time.Sleep(100 * time.Millisecond)

	// Client sends to server
	packet := make([]byte, reticulum.HeaderMinSize+5)
	packet[0] = 0xAA
	if err := client.Send(ctx, packet); err != nil {
		t.Fatalf("client send: %v", err)
	}

	// Server sends to client
	packet2 := make([]byte, reticulum.HeaderMinSize+3)
	packet2[0] = 0xBB
	time.Sleep(50 * time.Millisecond) // wait for server to see connection
	if err := server.Send(ctx, packet2); err != nil {
		t.Fatalf("server send: %v", err)
	}

	// Wait for delivery
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	if len(serverReceived) != 1 {
		t.Fatalf("server received %d packets, want 1", len(serverReceived))
	}
	if serverReceived[0][0] != 0xAA {
		t.Fatalf("server packet[0] = 0x%02x, want 0xAA", serverReceived[0][0])
	}
	mu.Unlock()

	clientMu.Lock()
	if len(clientReceived) != 1 {
		t.Fatalf("client received %d packets, want 1", len(clientReceived))
	}
	if clientReceived[0][0] != 0xBB {
		t.Fatalf("client packet[0] = 0x%02x, want 0xBB", clientReceived[0][0])
	}
	clientMu.Unlock()
}

func TestTCPInterface_PeerCount(t *testing.T) {
	addr := freePort(t)

	server := NewTCPInterface(TCPInterfaceConfig{
		Name:       "tcp_srv",
		ListenAddr: addr,
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer server.Stop()

	if server.PeerCount() != 0 {
		t.Fatalf("initial peer count = %d, want 0", server.PeerCount())
	}

	// Connect a client
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	time.Sleep(100 * time.Millisecond)

	if server.PeerCount() != 1 {
		t.Fatalf("peer count after connect = %d, want 1", server.PeerCount())
	}

	conn.Close()
	time.Sleep(200 * time.Millisecond)

	// After disconnect, peer should be removed once a send/read fails
}

func TestTCPInterface_IsOnline(t *testing.T) {
	addr := freePort(t)

	// Server-only interface
	server := NewTCPInterface(TCPInterfaceConfig{
		Name:       "tcp_srv",
		ListenAddr: addr,
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server.Start(ctx)
	defer server.Stop()

	if server.IsOnline() {
		t.Fatal("should not be online without peers")
	}

	// Connect
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	if !server.IsOnline() {
		t.Fatal("should be online with peer")
	}

	conn.Close()
}

func TestTCPInterface_MultipleClients(t *testing.T) {
	addr := freePort(t)
	var received int
	var mu sync.Mutex

	server := NewTCPInterface(TCPInterfaceConfig{
		Name:       "tcp_srv",
		ListenAddr: addr,
	}, func(packet []byte) {
		mu.Lock()
		received++
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server.Start(ctx)
	defer server.Stop()
	time.Sleep(50 * time.Millisecond)

	// Connect 3 clients and send from each
	for i := 0; i < 3; i++ {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()

		packet := make([]byte, reticulum.HeaderMinSize)
		packet[0] = byte(i)
		frame := reticulum.HDLCFrame(packet)
		conn.Write(frame)
	}

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	if received != 3 {
		t.Fatalf("received %d packets, want 3", received)
	}
	mu.Unlock()

	if server.PeerCount() != 3 {
		t.Fatalf("peer count = %d, want 3", server.PeerCount())
	}
}

func TestTCPInterface_Stop(t *testing.T) {
	addr := freePort(t)

	iface := NewTCPInterface(TCPInterfaceConfig{
		Name:       "tcp_test",
		ListenAddr: addr,
	}, nil)

	ctx := context.Background()
	iface.Start(ctx)

	// Connect a peer
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	iface.Stop()
	conn.Close()

	// Verify stopped
	if iface.IsOnline() {
		t.Fatal("should not be online after stop")
	}

	// Double stop should not panic
	iface.Stop()
}

func TestTCPInterface_HDLCFramingCompat(t *testing.T) {
	// Verify our HDLC framing is compatible by manually constructing
	// a frame the way Python RNS does it and parsing it
	addr := freePort(t)

	var received []byte
	var mu sync.Mutex

	server := NewTCPInterface(TCPInterfaceConfig{
		Name:       "tcp_srv",
		ListenAddr: addr,
	}, func(packet []byte) {
		mu.Lock()
		received = make([]byte, len(packet))
		copy(received, packet)
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server.Start(ctx)
	defer server.Stop()
	time.Sleep(50 * time.Millisecond)

	// Connect and send a manually-constructed HDLC frame
	// This simulates what a Python RNS node would send
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Raw Reticulum packet (announce-like)
	rawPacket := make([]byte, 50)
	rawPacket[0] = 0x01 // flags: announce
	rawPacket[1] = 0x00 // hops
	for i := 2; i < 18; i++ {
		rawPacket[i] = byte(i) // dest hash
	}
	rawPacket[18] = 0x01 // context

	// HDLC frame it manually (Python-style)
	escaped := make([]byte, 0, len(rawPacket)*2)
	for _, b := range rawPacket {
		switch b {
		case 0x1B: // ESC
			escaped = append(escaped, 0x1B, 0x1B^0x20)
		case 0x7E: // FLAG
			escaped = append(escaped, 0x1B, 0x7E^0x20)
		default:
			escaped = append(escaped, b)
		}
	}
	frame := append([]byte{0x7E}, escaped...)
	frame = append(frame, 0x7E)

	conn.Write(frame)
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if received == nil {
		t.Fatal("no packet received")
	}
	if len(received) != len(rawPacket) {
		t.Fatalf("received %d bytes, want %d", len(received), len(rawPacket))
	}
	if received[0] != 0x01 {
		t.Fatalf("first byte = 0x%02x, want 0x01", received[0])
	}

	fmt.Printf("HDLC compat test: sent %d bytes, received %d bytes OK\n", len(rawPacket), len(received))
}
