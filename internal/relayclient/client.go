package relayclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
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
	// the TLS SERVER inside every tunnel; CAPEM is the Hub CA both ends
	// verify against. Certificates issued before 2026-09-15 lack ServerAuth
	// and a SAN and are refused by Validate.
	CertPEM, KeyPEM, CAPEM []byte
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
	_, err := c.serverTLS()
	return err
}

func (c Config) serverTLS() (*tls.Config, error) {
	cert, err := tls.X509KeyPair(c.CertPEM, c.KeyPEM)
	if err != nil {
		return nil, fmt.Errorf("relayclient: bridge certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("relayclient: bridge certificate: %w", err)
	}
	serverOK := false
	for _, eku := range leaf.ExtKeyUsage {
		if eku == x509.ExtKeyUsageServerAuth {
			serverOK = true
		}
	}
	if !serverOK || len(leaf.DNSNames) == 0 {
		return nil, errors.New("relayclient: the bridge certificate cannot serve a relay (no ServerAuth usage or SAN); re-issue it on the Hub's Fleet page")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(c.CAPEM) {
		return nil, errors.New("relayclient: hub CA PEM does not parse")
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}, nil
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
	tlsCfg, _ := c.cfg.serverTLS()
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
	c.drop(s.id)
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
