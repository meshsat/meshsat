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

// outboundPeer tracks a dynamically added outbound connection with its own
// reconnect loop and cancel channel.
type outboundPeer struct {
	addr   string
	conn   net.Conn
	cancel context.CancelFunc
}

// TCPInterface is a bidirectional Reticulum interface over TCP using HDLC framing.
// It can operate as client (connects to a remote RNS node), server (accepts
// connections from RNS nodes), or both. Supports dynamic peer management via
// AddPeer/RemovePeer for UI-driven configuration.
type TCPInterface struct {
	config   TCPInterfaceConfig
	callback func(packet []byte) // called when a packet is received

	mu       sync.Mutex
	conn     net.Conn                 // legacy single outbound (from ConnectAddr)
	listener net.Listener             // server accept socket
	peers    map[string]net.Conn      // inbound peers (server mode: remoteAddr → conn)
	outbound map[string]*outboundPeer // dynamic outbound peers (configured addr → peer)
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
		outbound: make(map[string]*outboundPeer),
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

	// Send to all inbound server peers
	for addr, conn := range t.peers {
		if err := t.writeConn(conn, frame); err != nil {
			log.Debug().Err(err).Str("peer", addr).Msg("tcp: peer write failed, removing")
			conn.Close()
			delete(t.peers, addr)
			lastErr = err
		}
	}

	// Send to all dynamic outbound peers
	for addr, ob := range t.outbound {
		if ob.conn != nil {
			if err := t.writeConn(ob.conn, frame); err != nil {
				log.Debug().Err(err).Str("peer", addr).Msg("tcp: outbound peer write failed")
				lastErr = err
			}
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
	for addr, ob := range t.outbound {
		ob.cancel()
		if ob.conn != nil {
			ob.conn.Close()
		}
		delete(t.outbound, addr)
	}

	log.Info().Str("name", t.config.Name).Msg("tcp interface stopped")
}

// IsOnline returns whether the interface has at least one active connection.
func (t *TCPInterface) IsOnline() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conn != nil || len(t.peers) > 0 {
		return true
	}
	for _, ob := range t.outbound {
		if ob.conn != nil {
			return true
		}
	}
	return false
}

// PeerCount returns the number of connected peers (inbound + outbound).
func (t *TCPInterface) PeerCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	count := len(t.peers)
	if t.conn != nil {
		count++
	}
	for _, ob := range t.outbound {
		if ob.conn != nil {
			count++
		}
	}
	return count
}

// AddPeer starts a persistent outbound connection to the given address with
// automatic reconnection. Safe to call while the interface is running.
func (t *TCPInterface) AddPeer(ctx context.Context, addr string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.stopped {
		return fmt.Errorf("interface stopped")
	}
	if _, exists := t.outbound[addr]; exists {
		return fmt.Errorf("peer already exists: %s", addr)
	}

	peerCtx, cancel := context.WithCancel(ctx)
	ob := &outboundPeer{addr: addr, cancel: cancel}
	t.outbound[addr] = ob

	go t.outboundLoop(peerCtx, ob)

	log.Info().Str("name", t.config.Name).Str("peer", addr).Msg("tcp: outbound peer added")
	return nil
}

// RemovePeer stops and removes a dynamic outbound peer connection.
func (t *TCPInterface) RemovePeer(addr string) {
	t.mu.Lock()
	ob, exists := t.outbound[addr]
	if !exists {
		t.mu.Unlock()
		return
	}
	delete(t.outbound, addr)
	t.mu.Unlock()

	ob.cancel()
	if ob.conn != nil {
		ob.conn.Close()
	}
	log.Info().Str("name", t.config.Name).Str("peer", addr).Msg("tcp: outbound peer removed")
}

// ListPeers returns info about all connected peers (inbound + outbound).
func (t *TCPInterface) ListPeers() []PeerInfo {
	t.mu.Lock()
	defer t.mu.Unlock()

	var result []PeerInfo
	if t.conn != nil {
		result = append(result, PeerInfo{
			Address:   t.config.ConnectAddr,
			Direction: "outbound",
			Connected: true,
			Dynamic:   false,
		})
	}
	for _, ob := range t.outbound {
		result = append(result, PeerInfo{
			Address:   ob.addr,
			Direction: "outbound",
			Connected: ob.conn != nil,
			Dynamic:   true,
		})
	}
	for addr := range t.peers {
		result = append(result, PeerInfo{
			Address:   addr,
			Direction: "inbound",
			Connected: true,
			Dynamic:   false,
		})
	}
	return result
}

// PeerInfo describes a connected or configured peer.
type PeerInfo struct {
	Address   string `json:"address"`
	Direction string `json:"direction"` // "inbound" or "outbound"
	Connected bool   `json:"connected"`
	Dynamic   bool   `json:"dynamic"` // true if added via AddPeer (UI-managed)
}

// outboundLoop maintains a persistent connection to a dynamic outbound peer.
func (t *TCPInterface) outboundLoop(ctx context.Context, ob *outboundPeer) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.stopCh:
			return
		default:
		}

		conn, err := net.DialTimeout("tcp", ob.addr, 10*time.Second)
		if err != nil {
			log.Debug().Err(err).Str("peer", ob.addr).Msg("tcp: outbound peer connect failed")
			select {
			case <-time.After(t.config.ReconnectInterval):
				continue
			case <-ctx.Done():
				return
			case <-t.stopCh:
				return
			}
		}

		log.Info().Str("name", t.config.Name).Str("peer", ob.addr).Msg("tcp: outbound peer connected")

		t.mu.Lock()
		ob.conn = conn
		t.mu.Unlock()

		t.readLoop(conn)

		t.mu.Lock()
		ob.conn = nil
		t.mu.Unlock()

		log.Info().Str("name", t.config.Name).Str("peer", ob.addr).Msg("tcp: outbound peer disconnected")

		select {
		case <-time.After(t.config.ReconnectInterval):
		case <-ctx.Done():
			return
		case <-t.stopCh:
			return
		}
	}
}

// ListenAddr returns the configured listen address (may be empty).
func (t *TCPInterface) ListenAddr() string { return t.config.ListenAddr }

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
