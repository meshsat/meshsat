package routing

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/reticulum"
	"meshsat/internal/transport"
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
// Returns an error when there are no peers to send to — previously this
// was a silent success, which hid bond-bearer failures where tcp_0 was
// configured with zero peers. A real error here lets the HeMB bonder's
// per-bearer error log fire so operators see the "no peers" gap.
// [MESHSAT-672]
func (t *TCPInterface) Send(ctx context.Context, packet []byte) error {
	frame := reticulum.HDLCFrame(packet)

	t.mu.Lock()
	defer t.mu.Unlock()

	var lastErr error
	delivered := 0

	// Send to client connection (if connected)
	if t.conn != nil {
		if err := t.writeConn(t.conn, frame); err != nil {
			lastErr = err
		} else {
			delivered++
		}
	}

	// Send to all inbound server peers
	for addr, conn := range t.peers {
		if err := t.writeConn(conn, frame); err != nil {
			log.Debug().Err(err).Str("peer", addr).Msg("tcp: peer write failed, removing")
			conn.Close()
			delete(t.peers, addr)
			lastErr = err
		} else {
			delivered++
		}
	}

	// Send to all dynamic outbound peers
	for addr, ob := range t.outbound {
		if ob.conn != nil {
			if err := t.writeConn(ob.conn, frame); err != nil {
				log.Debug().Err(err).Str("peer", addr).Msg("tcp: outbound peer write failed")
				lastErr = err
			} else {
				delivered++
			}
		}
	}

	if delivered == 0 && lastErr == nil {
		return fmt.Errorf("tcp %s: no connected peers — packet dropped", t.config.Name)
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

		conn, err := dialPeerTCP(ob.addr, 10*time.Second)
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
	_ = readHDLC(conn, t.stopCh, t.callback)
}

// readHDLC reads HDLC frames from conn and hands each packet to onPacket until
// the connection ends, returning the error that ended it, or until stop
// closes (nil). The frame reader drops frames shorter than a Reticulum header.
// tcp_0 and tcp_rns share it.
func readHDLC(conn net.Conn, stop <-chan struct{}, onPacket func(packet []byte)) error {
	reader := reticulum.NewHDLCFrameReader()
	buf := make([]byte, 4096)

	for {
		select {
		case <-stop:
			return nil
		default:
		}

		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		n, err := conn.Read(buf)
		if n > 0 {
			frames := reader.Feed(buf[:n])
			for _, frame := range frames {
				if onPacket != nil {
					onPacket(frame)
				}
			}
		}
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue // read timeout is normal
			}
			return err // real error or EOF
		}
	}
}

func (t *TCPInterface) writeConn(conn net.Conn, data []byte) error {
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := conn.Write(data)
	return err
}

// dialPeerTCP wraps net.DialTimeout with link-local-zone awareness.
// Plain IPv4/IPv6 addresses go through unchanged. For link-local IPv6
// with a zone ID (`[fe80::...%p2p-0]:4242` — the form WiFi-Direct
// peers use after MESHSAT-647 auto-wire) Go's default dialer fails
// with "cannot assign requested address" because it doesn't pick a
// source on the scoped interface. We find our own fe80:: address on
// that iface and pin the outbound socket to it, which lets the
// kernel's routing table resolve the link-local peer unambiguously.
func dialPeerTCP(addr string, timeout time.Duration) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	// Strip the bracket wrapping so SplitHostPort left us with the raw host.
	zoneIdx := strings.Index(host, "%")
	if zoneIdx < 0 || !strings.HasPrefix(host, "fe80:") {
		return net.DialTimeout("tcp", addr, timeout) // not link-local
	}
	zone := host[zoneIdx+1:]
	localIP, err := linkLocalOnIface(zone)
	if err != nil {
		return nil, fmt.Errorf("link-local dial: %w", err)
	}
	d := &net.Dialer{
		Timeout:   timeout,
		LocalAddr: &net.TCPAddr{IP: localIP, Zone: zone},
	}
	return d.Dial("tcp", addr)
}

// linkLocalOnIface returns the first IPv6 link-local address on the
// given interface, for use as a source binding when dialing a scoped
// link-local peer.
func linkLocalOnIface(ifaceName string) (net.IP, error) {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return nil, fmt.Errorf("iface %s: %w", ifaceName, err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("iface %s addrs: %w", ifaceName, err)
	}
	for _, a := range addrs {
		ip, _, err := net.ParseCIDR(a.String())
		if err != nil {
			continue
		}
		if ip.To4() == nil && ip.IsLinkLocalUnicast() {
			return ip, nil
		}
	}
	return nil, fmt.Errorf("iface %s has no IPv6 link-local address", ifaceName)
}

// ============================================================================
// tcp_rns: the RNS TCP client, a dynamic interface
// ============================================================================

const (
	// TCPRNSDefaultPort is Reticulum's TCP port and MeshSat Android's default.
	TCPRNSDefaultPort = 4242
	// TCPRNSHWMTU is the hardware MTU a tcp_rns interface registers, as tcp_0.
	TCPRNSHWMTU = 65535

	tcpRNSConnectTimeout = 10 * time.Second // the TCP connect and the TLS handshake together
	tcpRNSReconnect      = 5 * time.Second
	tcpRNSKeepAlive      = 15 * time.Second // TCP keepalive probe interval
	tcpRNSWriteTimeout   = 10 * time.Second
)

// TCPRNSConfig is the stored config of a tcp_rns interface, MeshSat Android's
// "RNS TCP" settings: the host and port of a stock Reticulum node (Python RNS
// TCPServerInterface, or the Hub's endpoint) and the TLS switch.
type TCPRNSConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	TLS  bool   `json:"tls"`
}

// TCPRNSEffectiveTLS says whether a tcp_rns connection is wrapped in TLS: the
// switch, or port 443 whatever the switch says (Android GatewayService.kt:1713).
func TCPRNSEffectiveTLS(tlsOn bool, port int) bool {
	return tlsOn || port == 443
}

// hostname is the host without the brackets of an IPv6 literal: the name TLS
// sends as SNI and verifies.
func (c TCPRNSConfig) hostname() string {
	h := strings.TrimSpace(c.Host)
	if len(h) > 1 && h[0] == '[' && h[len(h)-1] == ']' {
		h = h[1 : len(h)-1]
	}
	return h
}

func (c TCPRNSConfig) port() int {
	if c.Port == 0 {
		return TCPRNSDefaultPort
	}
	return c.Port
}

// Addr is the address dialled, host:port ([v6]:port for an IPv6 address).
func (c TCPRNSConfig) Addr() string {
	return net.JoinHostPort(c.hostname(), strconv.Itoa(c.port()))
}

// Summary is the status line: host:port, then ", TLS" when the link is TLS.
func (c TCPRNSConfig) Summary() string {
	if TCPRNSEffectiveTLS(c.TLS, c.port()) {
		return c.Addr() + ", TLS"
	}
	return c.Addr()
}

// TCPRNSClient is the tcp_rns interface: one outbound TCP connection to a
// stock Reticulum node, HDLC framed as tcp_0, in TLS when TCPRNSEffectiveTLS
// says so. It follows MeshSat Android's RnsTcpInterface (link id tcp_rns_0):
// connect timeout 10 s, reconnect every 5 s, TCP_NODELAY and keepalive. TLS is
// 1.2 or later against the system roots and, unlike Android, also verifies
// the server's name; when the server asks for a client certificate, the one
// clientCert returns is presented (main.go: the Hub client certificate).
type TCPRNSClient struct {
	name       string
	cfg        TCPRNSConfig
	clientCert func() (*tls.Certificate, error)
	callback   func(packet []byte)

	// Tests only: rootCAs replaces the system roots, dialAddr is where the
	// TCP connection goes while TLS still checks cfg.Host, retry and timeout
	// shorten the reconnect wait and the connect timeout.
	rootCAs  *x509.CertPool
	dialAddr string
	retry    time.Duration
	timeout  time.Duration

	wmu      sync.Mutex // one frame on the wire at a time
	mu       sync.Mutex
	conn     net.Conn
	lastErr  string
	rxb, txb uint64
	cancel   context.CancelFunc
	stopped  bool
	// Runs of failed dials and of connections the server refused after the
	// handshake: the first of a run logs at Warn, the rest at Debug, so a
	// link refused every 5 s does not fill the journal.
	failures, refusals int
}

// NewTCPRNSClient creates the client; Start connects. clientCert may be nil.
func NewTCPRNSClient(name string, cfg TCPRNSConfig, clientCert func() (*tls.Certificate, error), callback func(packet []byte)) *TCPRNSClient {
	return &TCPRNSClient{name: name, cfg: cfg, clientCert: clientCert, callback: callback, retry: tcpRNSReconnect, timeout: tcpRNSConnectTimeout}
}

// Start connects in the background and keeps reconnecting until Stop or ctx ends.
func (c *TCPRNSClient) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return fmt.Errorf("tcp_rns %s: stopped", c.name)
	}
	if c.cancel != nil {
		return nil
	}
	ctx, c.cancel = context.WithCancel(ctx)
	go c.run(ctx)
	return nil
}

func (c *TCPRNSClient) run(ctx context.Context) {
	retry := c.retry
	if retry <= 0 {
		retry = tcpRNSReconnect
	}
	for {
		conn, err := c.dial(ctx)
		if ctx.Err() != nil {
			if conn != nil {
				conn.Close()
			}
			return
		}
		if err != nil {
			c.mu.Lock()
			c.lastErr = err.Error()
			c.failures++
			first := c.failures == 1
			c.mu.Unlock()
			ev := log.Debug()
			if first {
				ev = log.Warn()
			}
			ev.Err(err).Str("iface", c.name).Str("remote", c.cfg.Summary()).Msg("tcp_rns: connect failed")
		} else {
			ok, refusedBefore := c.attach(conn)
			if !ok {
				return // stopped while the dial finished
			}
			ev := log.Info()
			if refusedBefore {
				ev = log.Debug()
			}
			ev.Str("iface", c.name).Str("remote", c.cfg.Summary()).Msg("tcp_rns: connected")
			rerr := readHDLC(conn, ctx.Done(), c.deliver)
			refused, first := c.detach(conn, rerr)
			switch {
			case ctx.Err() != nil: // stopped
			case refused:
				ev := log.Debug()
				if first {
					ev = log.Warn()
				}
				ev.Err(rerr).Str("iface", c.name).Str("remote", c.cfg.Summary()).Msg("tcp_rns: refused by the server after the TLS handshake")
			default:
				log.Info().Err(rerr).Str("iface", c.name).Msg("tcp_rns: disconnected")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
	}
}

// dial opens the connection: TCP with TCP_NODELAY and keepalive, then the TLS
// handshake when the link is TLS, all within the connect timeout.
func (c *TCPRNSClient) dial(ctx context.Context) (net.Conn, error) {
	addr := c.cfg.Addr()
	if c.dialAddr != "" {
		addr = c.dialAddr
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = tcpRNSConnectTimeout
	}
	nd := &net.Dialer{Timeout: timeout, KeepAlive: tcpRNSKeepAlive}
	if !TCPRNSEffectiveTLS(c.cfg.TLS, c.cfg.port()) {
		conn, err := nd.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		setTCPNoDelay(conn)
		return conn, nil
	}
	td := &tls.Dialer{NetDialer: nd, Config: c.tlsConfig()}
	conn, err := td.DialContext(ctx, "tcp", addr)
	if err != nil {
		// The dialer's own timeout during the handshake comes back as a bare
		// context error; say what timed out (a TLS switch on against a plain
		// Reticulum port ends here).
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, fmt.Errorf("tls handshake with %s: no answer within %s", c.cfg.Addr(), timeout)
		}
		return nil, err
	}
	if tc, ok := conn.(*tls.Conn); ok {
		setTCPNoDelay(tc.NetConn())
	}
	return conn, nil
}

func setTCPNoDelay(conn net.Conn) {
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
}

func (c *TCPRNSClient) tlsConfig() *tls.Config {
	return &tls.Config{
		MinVersion:           tls.VersionTLS12,
		ServerName:           c.cfg.hostname(), // SNI, and the name verified
		RootCAs:              c.rootCAs,        // nil: the system roots (SSL_CERT_FILE is honoured)
		GetClientCertificate: c.clientCertificate,
	}
}

// clientCertificate answers a server's certificate request: the certificate
// clientCert returns (read at each handshake, so one saved later is used at
// the next connection), or none. A certificate that cannot be read fails the
// handshake with that reason instead of an anonymous refusal by the server.
func (c *TCPRNSClient) clientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	if c.clientCert != nil {
		cert, err := c.clientCert()
		if err != nil {
			return nil, fmt.Errorf("client certificate: %w", err)
		}
		if cert != nil {
			return cert, nil
		}
	}
	return &tls.Certificate{}, nil
}

// attach makes conn the live connection unless Stop ran meanwhile (false).
// refusedBefore says the previous connection was refused after its handshake.
func (c *TCPRNSClient) attach(conn net.Conn) (ok, refusedBefore bool) {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		conn.Close()
		return false, false
	}
	c.conn = conn
	c.lastErr = ""
	c.failures = 0
	refusedBefore = c.refusals > 0
	c.mu.Unlock()
	return true, refusedBefore
}

// detach drops conn after its read loop ended with err. refused says the
// server ended it with a TLS alert, first that it is the first such in a row.
func (c *TCPRNSClient) detach(conn net.Conn, err error) (refused, first bool) {
	conn.Close()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == conn {
		c.conn = nil
	}
	// A TLS 1.3 server refuses a client certificate after the client's side
	// of the handshake has returned, so the refusal is an alert on the first
	// read. It is a handshake error and is kept as one; a plain close is not.
	var op *net.OpError
	if !c.stopped && errors.As(err, &op) && op.Op == "remote error" {
		c.lastErr = err.Error()
		c.refusals++
		return true, c.refusals == 1
	}
	c.refusals = 0
	return false, false
}

func (c *TCPRNSClient) deliver(packet []byte) {
	c.mu.Lock()
	c.rxb += uint64(len(packet))
	c.mu.Unlock()
	if c.callback != nil {
		c.callback(packet)
	}
}

// Send writes one packet as an HDLC frame. A failed write closes the
// connection (a TLS connection cannot write again after one), and the client
// reconnects.
func (c *TCPRNSClient) Send(ctx context.Context, packet []byte) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("tcp_rns %s: %w", c.name, transport.ErrNotConnected)
	}
	frame := reticulum.HDLCFrame(packet)
	c.wmu.Lock()
	conn.SetWriteDeadline(time.Now().Add(tcpRNSWriteTimeout))
	_, err := conn.Write(frame)
	c.wmu.Unlock()
	if err != nil {
		conn.Close()
		return fmt.Errorf("tcp_rns %s: %w", c.name, err)
	}
	c.mu.Lock()
	c.txb += uint64(len(packet))
	c.mu.Unlock()
	return nil
}

// IsOnline reports whether the connection is up.
func (c *TCPRNSClient) IsOnline() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}

// LastError is the last connect or TLS handshake error, cleared by a
// successful connection.
func (c *TCPRNSClient) LastError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

// Counters returns the packet bytes received and sent.
func (c *TCPRNSClient) Counters() (rx, tx uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rxb, c.txb
}

// Stop closes the connection and ends the loop, a dial in progress included.
func (c *TCPRNSClient) Stop() {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return
	}
	c.stopped = true
	cancel, conn := c.cancel, c.conn
	c.conn = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		conn.Close()
	}
	log.Info().Str("iface", c.name).Msg("tcp_rns: stopped")
}
