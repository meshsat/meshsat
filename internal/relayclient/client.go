package relayclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Config is what the relay client needs: where the Hub API is, who this
// bridge is, and the credentials the Fleet page issued it.
type Config struct {
	// HubAPIURL is the Hub's HTTPS API base, e.g. https://hub.meshsat.net.
	// This is NOT the MQTT broker URL; see DeriveAPIURL.
	HubAPIURL string
	BridgeID  string
	// Password is the bridge's MQTT password; the relay authenticates with
	// the same credentials (HTTP Basic bridge_id:password).
	Password string
	// CertPEM/KeyPEM is the Hub-issued certificate this bridge presents as
	// the TLS SERVER inside every tunnel. Certificates issued before
	// 2026-09-15 lack ServerAuth and a SAN and are refused by Validate.
	CertPEM, KeyPEM []byte
	// CAPEM is the Hub's bridge CA, the only root either end of a tunnel
	// verifies against. Leave it EMPTY on a kit: the bridge's stored
	// tls_ca_pem is the root store for the MQTT broker (Let's Encrypt) and
	// must not be the bridge CA; Run fetches the bridge CA from the Hub at
	// GET /api/relay/ca over HTTPS with the system roots instead.
	CAPEM []byte
	// Handler answers HTTP inside the tunnels: the bridge's own API router.
	Handler http.Handler
	// IdleTimeout closes a client tunnel with no frames for this long (the
	// Hub does not say when a client went away). Default 5 minutes.
	IdleTimeout time.Duration
}

// DeriveAPIURL guesses the Hub API base from the MQTT broker URL the bridge
// already has: wss://mqtt-hub.example.net:443/mqtt -> https://hub.example.net.
// It returns "" when the broker host does not follow that shape, and the
// relay stays off rather than guessing.
func DeriveAPIURL(mqttURL string) string {
	rest := mqttURL
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	host := rest
	if i := strings.IndexAny(host, ":/"); i >= 0 {
		host = host[:i]
	}
	if !strings.HasPrefix(host, "mqtt-hub.") {
		return ""
	}
	return "https://hub." + strings.TrimPrefix(host, "mqtt-hub.")
}

// Validate checks the configuration and that the certificate can serve.
func (c Config) Validate() error {
	if c.HubAPIURL == "" || c.BridgeID == "" || c.Password == "" {
		return errors.New("relayclient: hub api url, bridge id and password are required")
	}
	if c.Handler == nil {
		return errors.New("relayclient: no handler to serve")
	}
	if _, err := c.serverCert(); err != nil {
		return err
	}
	if len(c.CAPEM) > 0 {
		if _, err := parseCA(c.CAPEM); err != nil {
			return err
		}
	}
	return nil
}

// serverCert loads the bridge certificate and checks it can be a TLS server.
func (c Config) serverCert() (tls.Certificate, error) {
	cert, err := tls.X509KeyPair(c.CertPEM, c.KeyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("relayclient: bridge certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("relayclient: bridge certificate: %w", err)
	}
	serverOK := false
	for _, eku := range leaf.ExtKeyUsage {
		if eku == x509.ExtKeyUsageServerAuth {
			serverOK = true
		}
	}
	if !serverOK || len(leaf.DNSNames) == 0 {
		return tls.Certificate{}, errors.New("relayclient: the bridge certificate cannot serve a relay (no ServerAuth usage or SAN); re-issue it on the Hub's Fleet page")
	}
	return cert, nil
}

func parseCA(pemBytes []byte) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, errors.New("relayclient: hub CA PEM does not parse")
	}
	return pool, nil
}

func (c Config) serverTLS(clientCAs *x509.CertPool) (*tls.Config, error) {
	cert, err := c.serverCert()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
	}, nil
}

// caPool returns the Hub's bridge CA: the configured PEM when there is one,
// otherwise fetched from the Hub, retrying until ctx ends. The fetch goes
// over HTTPS to the Hub's public certificate, so the system roots vouch for
// which CA a bridge will trust its clients against.
func (c *Client) caPool(ctx context.Context) (*x509.CertPool, error) {
	if len(c.cfg.CAPEM) > 0 {
		return parseCA(c.cfg.CAPEM)
	}
	backoff := initialBackoff
	var lastLog time.Time
	for {
		pool, err := fetchCA(ctx, c.cfg.HubAPIURL)
		if err == nil {
			slog.Info("relay: bridge CA fetched from the Hub", "hub", c.cfg.HubAPIURL)
			return pool, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if time.Since(lastLog) > stillDownEvery {
			slog.Warn("relay: cannot fetch the bridge CA from the Hub", "error", err, "retry_in", backoff)
			lastLog = time.Now()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff = min(backoff*2, maxBackoff)
		}
	}
}

// caPath is where the Hub publishes its bridge CA certificate (public).
const caPath = "/api/relay/ca"

func fetchCA(ctx context.Context, hubAPIURL string) (*x509.CertPool, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(hubAPIURL, "/")+caPath, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("relayclient: %s: HTTP %d", caPath, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	return parseCA(body)
}

// Package vars so tests can shorten them.
var (
	initialBackoff = time.Second
	maxBackoff     = 30 * time.Second
	stillDownEvery = time.Minute
)

var errRefused = errors.New("relayclient: the Hub refused the credentials")

// Client keeps one WebSocket to the Hub open and serves the tunnels on it.
type Client struct {
	cfg      Config
	listener *Listener

	mu       sync.Mutex
	conn     *websocket.Conn
	sessions map[string]*session
	served   int
}

// New returns a client; Run starts it.
func New(cfg Config) *Client {
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 5 * time.Minute
	}
	return &Client{cfg: cfg, sessions: map[string]*session{}}
}

// Connected reports whether the relay socket to the Hub is up.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}

// Sessions reports how many client tunnels are open.
func (c *Client) Sessions() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sessions)
}

// Run serves until ctx ends: it dials the Hub, reconnects with backoff, and
// runs the TLS+HTTP server over the tunnels. Validate runs first so a
// configuration fault fails loudly instead of being retried forever.
func (c *Client) Run(ctx context.Context) error {
	if err := c.cfg.Validate(); err != nil {
		return err
	}
	pool, err := c.caPool(ctx)
	if err != nil {
		return nil // ctx ended while waiting for the Hub
	}
	tlsCfg, err := c.cfg.serverTLS(pool)
	if err != nil {
		return err
	}
	c.listener = newListener(c.cfg.BridgeID)
	srv := &http.Server{Handler: c.cfg.Handler, ReadHeaderTimeout: 15 * time.Second}
	go func() { _ = srv.Serve(tls.NewListener(c.listener, tlsCfg)) }()
	defer func() {
		_ = c.listener.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = srv.Shutdown(shutdownCtx)
		cancel()
	}()

	backoff := initialBackoff
	var lastLog time.Time
	for {
		err := c.serveOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errRefused) {
			// 401/403 will not fix themselves; keep trying at the slow rate
			// in case the credentials are rotated on the Hub.
			backoff = maxBackoff
		}
		if time.Since(lastLog) > stillDownEvery {
			slog.Warn("relay: not serving through the Hub", "error", err, "retry_in", backoff)
			lastLog = time.Now()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// serveOnce runs one relay socket to completion.
func (c *Client) serveOnce(ctx context.Context) error {
	url := "ws" + strings.TrimPrefix(strings.TrimSuffix(c.cfg.HubAPIURL, "/"), "http") + "/api/relay/serve"
	hdr := http.Header{}
	hdr.Set("Authorization", "Basic "+basicAuth(c.cfg.BridgeID, c.cfg.Password))
	dialer := websocket.Dialer{HandshakeTimeout: 20 * time.Second}
	conn, resp, err := dialer.DialContext(ctx, url, hdr)
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			return fmt.Errorf("%w (HTTP %d)", errRefused, resp.StatusCode)
		}
		return err
	}
	conn.SetReadLimit(maxFrame)
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	slog.Info("relay: serving through the Hub", "hub", c.cfg.HubAPIURL, "bridge", c.cfg.BridgeID)

	stop := make(chan struct{})
	go c.reapIdle(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()
	defer func() {
		close(stop)
		c.mu.Lock()
		c.conn = nil
		open := c.sessions
		c.sessions = map[string]*session{}
		c.mu.Unlock()
		for _, s := range open {
			s.close()
		}
		_ = conn.Close()
		slog.Info("relay: Hub socket closed", "bridge", c.cfg.BridgeID)
	}()

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		clientID, payload, err := decodeEnvelope(data)
		if err != nil {
			slog.Debug("relay: dropping malformed frame", "error", err)
			continue
		}
		// A tunnel starts with a TLS ClientHello (record type 0x16). Anything
		// else for a client with no open session is a straggler, typically
		// the close_notify a client sends after its request completed and
		// the server side has already closed the pipe; opening a session for
		// it would only produce a handshake error and a phantom tunnel.
		if !c.hasSession(clientID) && (len(payload) == 0 || payload[0] != tlsHandshakeRecord) {
			continue
		}
		// A ClientHello for a client that still has a session is that
		// client's next tunnel. The Hub never says a tunnel ended, so a
		// client that kept its HTTP connection alive left the old one open;
		// writing the new handshake into it failed every other request.
		// The new tunnel replaces the old. [MESHSAT-1297]
		if isClientHello(payload) && c.hasSession(clientID) {
			slog.Debug("relay: new tunnel replaces an open one", "client", clientID)
			c.drop(clientID)
		}
		s := c.sessionFor(clientID)
		if s == nil {
			return errors.New("relayclient: listener closed")
		}
		s.touch()
		// The pipe write blocks until the TLS server reads; a dead session
		// errors out and is dropped.
		_ = s.client.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := s.client.Write(payload); err != nil {
			c.drop(clientID)
		}
	}
}

// tlsHandshakeRecord is the TLS record type of a ClientHello.
const tlsHandshakeRecord = 0x16

// isClientHello reports whether payload starts a TLS handshake: a
// handshake record (0x16) carrying a ClientHello (handshake type 0x01) in
// the clear, with a TLS legacy version (0x03 0x0n) in the record header and
// in the hello. Inside an established TLS 1.3 session every record goes out
// as application data (0x17), and a TLS 1.2 renegotiation, which the bridge's
// server refuses anyway, carries an encrypted body, so this only matches the
// first flight of a new tunnel.
func isClientHello(payload []byte) bool {
	return len(payload) >= 10 && payload[0] == tlsHandshakeRecord && payload[1] == 0x03 &&
		payload[5] == 0x01 && payload[9] == 0x03
}

func (c *Client) hasSession(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.sessions[id]
	return ok
}

// Served reports how many client tunnels have been opened since start.
func (c *Client) Served() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.served
}

// sessionFor returns the tunnel for a client id, creating it and starting
// its outbound pump on first sight.
func (c *Client) sessionFor(id string) *session {
	c.mu.Lock()
	if s, ok := c.sessions[id]; ok {
		c.mu.Unlock()
		return s
	}
	s := newSession(id)
	c.sessions[id] = s
	c.served++
	c.mu.Unlock()
	if !c.listener.offer(s.server) {
		c.drop(id)
		return nil
	}
	go c.pumpOut(s)
	slog.Debug("relay: client tunnel opened", "client", id)
	return s
}

// pumpOut reads what the TLS server writes for one client and sends it to
// the Hub in envelope frames of at most chunkSize.
func (c *Client) pumpOut(s *session) {
	buf := make([]byte, chunkSize)
	for {
		n, err := s.client.Read(buf)
		if n > 0 {
			frame, encErr := encodeEnvelope(s.id, buf[:n])
			if encErr != nil {
				break
			}
			// gorilla permits one concurrent writer; the read loop never
			// writes, so the mutex only orders the pumps.
			c.mu.Lock()
			conn := c.conn
			var werr error
			if conn != nil {
				_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				werr = conn.WriteMessage(websocket.BinaryMessage, frame)
			}
			c.mu.Unlock()
			if conn == nil || werr != nil {
				break
			}
			s.touch()
		}
		if err != nil {
			break
		}
	}
	c.dropSession(s)
}

func (c *Client) drop(id string) {
	c.mu.Lock()
	s, ok := c.sessions[id]
	if ok {
		delete(c.sessions, id)
	}
	c.mu.Unlock()
	if ok {
		s.close()
		slog.Debug("relay: client tunnel closed", "client", id)
	}
}

// dropSession removes s only while it is still the session of its client:
// the pump of a replaced tunnel ends after the new one is in place, and
// dropping by id would take the new one with it.
func (c *Client) dropSession(s *session) {
	c.mu.Lock()
	cur, ok := c.sessions[s.id]
	if ok && cur == s {
		delete(c.sessions, s.id)
	}
	c.mu.Unlock()
	s.close()
	if ok && cur == s {
		slog.Debug("relay: client tunnel closed", "client", s.id)
	}
}

func (c *Client) reapIdle(stop <-chan struct{}) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			c.mu.Lock()
			var idle []string
			for id, s := range c.sessions {
				if s.idleFor(c.cfg.IdleTimeout) {
					idle = append(idle, id)
				}
			}
			c.mu.Unlock()
			for _, id := range idle {
				c.drop(id)
			}
		}
	}
}

// Stop closes the relay socket; Run returns once its context ends.
func (c *Client) Stop() {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "bridge stopping"), time.Now().Add(2*time.Second))
		_ = conn.Close()
	}
}

// DialThroughHub is the CLIENT end in Go: it opens a tunnel to bridgeID
// through the Hub as clientID and returns the raw net.Conn; the caller wraps
// it in tls.Client with its own Hub-issued certificate and ServerName =
// bridgeID. The live test uses it; the phone does the same in Kotlin.
func DialThroughHub(ctx context.Context, hubAPIURL, bridgeID, clientID, password string) (net.Conn, error) {
	url := "ws" + strings.TrimPrefix(strings.TrimSuffix(hubAPIURL, "/"), "http") + "/api/relay/connect/" + bridgeID
	hdr := http.Header{}
	hdr.Set("Authorization", "Basic "+basicAuth(clientID, password))
	dialer := websocket.Dialer{HandshakeTimeout: 20 * time.Second}
	ws, resp, err := dialer.DialContext(ctx, url, hdr)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("relayclient: connect refused: HTTP %d", resp.StatusCode)
		}
		return nil, err
	}
	ws.SetReadLimit(maxFrame)
	return newWSConn(ws, clientID, bridgeID), nil
}
