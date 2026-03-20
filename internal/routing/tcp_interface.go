package routing

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/reticulum"
)

// TCPInterfaceConfig configures a TCP Reticulum interface.
type TCPInterfaceConfig struct {
	// Name is the interface identifier (e.g. "tcp_0").
	Name string
	// ListenAddr is the address to listen on (server mode). Empty = client only.
	ListenAddr string
	// ConnectAddr is the address to connect to (client mode). Empty = server only.
	ConnectAddr string
	// Reconnect enables automatic reconnection for client mode.
	Reconnect bool
	// ReconnectInterval is the delay between reconnection attempts.
	ReconnectInterval time.Duration
}

// TCPInterface is a bidirectional Reticulum interface over TCP using HDLC framing.
// It can operate as client (connects to a remote RNS node), server (accepts
// connections from RNS nodes), or both.
type TCPInterface struct {
	config   TCPInterfaceConfig
	callback func(packet []byte) // called when a packet is received

	mu       sync.Mutex
	conn     net.Conn
	listener net.Listener
	peers    map[string]net.Conn // server mode: addr → conn
	online   bool
	stopCh   chan struct{}
	stopped  bool
}

// NewTCPInterface creates a new TCP Reticulum interface.
// The callback is invoked for each received Reticulum packet (already unframed).
func NewTCPInterface(config TCPInterfaceConfig, callback func(packet []byte)) *TCPInterface {
	if config.ReconnectInterval <= 0 {
		config.ReconnectInterval = 10 * time.Second
	}
	return &TCPInterface{
		config:   config,
		callback: callback,
		peers:    make(map[string]net.Conn),
		stopCh:   make(chan struct{}),
	}
}

// Start initiates the TCP interface. In client mode, it connects to the remote
// host. In server mode, it starts listening for connections. Can do both.
func (t *TCPInterface) Start(ctx context.Context) error {
	if t.config.ListenAddr != "" {
		if err := t.startServer(ctx); err != nil {
			return fmt.Errorf("tcp server: %w", err)
		}
	}

	if t.config.ConnectAddr != "" {
		go t.clientLoop(ctx)
	}

	return nil
}

// Send transmits a Reticulum packet to all connected peers (HDLC framed).
func (t *TCPInterface) Send(ctx context.Context, packet []byte) error {
	frame := reticulum.HDLCFrame(packet)

	t.mu.Lock()
	defer t.mu.Unlock()

	var lastErr error

	// Send to client connection (if connected)
	if t.conn != nil {
		if err := t.writeConn(t.conn, frame); err != nil {
			lastErr = err
		}
	}

	// Send to all server peers
	for addr, conn := range t.peers {
		if err := t.writeConn(conn, frame); err != nil {
			log.Debug().Err(err).Str("peer", addr).Msg("tcp: peer write failed, removing")
			conn.Close()
			delete(t.peers, addr)
			lastErr = err
		}
	}

	return lastErr
}

// Stop closes all connections and stops the interface.
func (t *TCPInterface) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.stopped {
		return
	}
	t.stopped = true
	close(t.stopCh)
	t.online = false

	if t.conn != nil {
		t.conn.Close()
		t.conn = nil
	}
	if t.listener != nil {
		t.listener.Close()
		t.listener = nil
	}
	for addr, conn := range t.peers {
		conn.Close()
		delete(t.peers, addr)
	}

	log.Info().Str("name", t.config.Name).Msg("tcp interface stopped")
}

// IsOnline returns whether the interface has at least one active connection.
func (t *TCPInterface) IsOnline() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.conn != nil || len(t.peers) > 0
}

// PeerCount returns the number of connected peers (server mode).
func (t *TCPInterface) PeerCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	count := len(t.peers)
	if t.conn != nil {
		count++
	}
	return count
}

// ============================================================================
// Client mode
// ============================================================================

func (t *TCPInterface) clientLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.stopCh:
			return
		default:
		}

		conn, err := net.DialTimeout("tcp", t.config.ConnectAddr, 10*time.Second)
		if err != nil {
			log.Debug().Err(err).
				Str("addr", t.config.ConnectAddr).
				Msg("tcp: connect failed")

			if !t.config.Reconnect {
				return
			}
			select {
			case <-time.After(t.config.ReconnectInterval):
				continue
			case <-ctx.Done():
				return
			case <-t.stopCh:
				return
			}
		}

		log.Info().
			Str("name", t.config.Name).
			Str("remote", t.config.ConnectAddr).
			Msg("tcp interface connected")

		t.mu.Lock()
		t.conn = conn
		t.online = true
		t.mu.Unlock()

		// Read loop — blocks until connection drops
		t.readLoop(conn)

		t.mu.Lock()
		t.conn = nil
		t.online = false
		t.mu.Unlock()

		log.Info().
			Str("name", t.config.Name).
			Msg("tcp interface disconnected")

		if !t.config.Reconnect {
			return
		}

		select {
		case <-time.After(t.config.ReconnectInterval):
		case <-ctx.Done():
			return
		case <-t.stopCh:
			return
		}
	}
}

// ============================================================================
// Server mode
// ============================================================================

func (t *TCPInterface) startServer(ctx context.Context) error {
	ln, err := net.Listen("tcp", t.config.ListenAddr)
	if err != nil {
		return err
	}

	t.mu.Lock()
	t.listener = ln
	t.mu.Unlock()

	log.Info().
		Str("name", t.config.Name).
		Str("addr", t.config.ListenAddr).
		Msg("tcp interface listening")

	go t.acceptLoop(ctx, ln)
	return nil
}

func (t *TCPInterface) acceptLoop(ctx context.Context, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-t.stopCh:
				return
			case <-ctx.Done():
				return
			default:
				log.Debug().Err(err).Msg("tcp: accept error")
				continue
			}
		}

		addr := conn.RemoteAddr().String()
		log.Info().
			Str("name", t.config.Name).
			Str("peer", addr).
			Msg("tcp interface: peer connected")

		t.mu.Lock()
		t.peers[addr] = conn
		t.mu.Unlock()

		go func() {
			t.readLoop(conn)

			t.mu.Lock()
			delete(t.peers, addr)
			t.mu.Unlock()

			log.Info().
				Str("name", t.config.Name).
				Str("peer", addr).
				Msg("tcp interface: peer disconnected")
		}()
	}
}

// ============================================================================
// Shared read loop
// ============================================================================

func (t *TCPInterface) readLoop(conn net.Conn) {
	reader := reticulum.NewHDLCFrameReader()
	buf := make([]byte, 4096)

	for {
		select {
		case <-t.stopCh:
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		n, err := conn.Read(buf)
		if n > 0 {
			frames := reader.Feed(buf[:n])
			for _, frame := range frames {
				if t.callback != nil {
					t.callback(frame)
				}
			}
		}
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue // read timeout is normal
			}
			return // real error or EOF
		}
	}
}

func (t *TCPInterface) writeConn(conn net.Conn, data []byte) error {
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := conn.Write(data)
	return err
}
