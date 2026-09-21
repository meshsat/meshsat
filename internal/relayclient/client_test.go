package relayclient

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

// ---- a test CA that issues certificates the way the Hub does ----

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-hub-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// issue mirrors the Hub's IssueBridgeCert after 21f6289: ClientAuth plus
// ServerAuth and a DNS SAN equal to the bridge id. serverOK=false produces
// the pre-2026-09-15 shape (ClientAuth only, no SAN).
func (ca *testCA) issue(t *testing.T, id string, serverOK bool) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: id},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if serverOK {
		tmpl.ExtKeyUsage = append(tmpl.ExtKeyUsage, x509.ExtKeyUsageServerAuth)
		tmpl.DNSNames = []string{id}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
}

// ---- a fake Hub: /api/relay/serve with envelopes, /api/relay/connect/{id} bare ----

type fakeHub struct {
	t        *testing.T
	srv      *httptest.Server
	up       websocket.Upgrader
	mu       sync.Mutex
	bridge   *websocket.Conn
	bridgeMu sync.Mutex
	clients  map[string]*websocket.Conn
	auth     map[string]string // user -> pass
	serves   int
	lastAuth string
	gotServe chan struct{}
	caPEM    []byte // served at /api/relay/ca when set
}

func newFakeHub(t *testing.T) *fakeHub {
	h := &fakeHub{t: t, clients: map[string]*websocket.Conn{}, auth: map[string]string{}, gotServe: make(chan struct{}, 16)}
	r := chi.NewRouter()
	r.Get("/api/relay/serve", h.serve)
	r.Get("/api/relay/connect/{bridge}", h.connect)
	r.Get("/api/relay/ca", func(w http.ResponseWriter, _ *http.Request) {
		h.mu.Lock()
		ca := h.caPEM
		h.mu.Unlock()
		if len(ca) == 0 {
			http.Error(w, "no CA", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/x-pem-file")
		_, _ = w.Write(ca)
	})
	h.srv = httptest.NewServer(r)
	t.Cleanup(h.srv.Close)
	return h
}

func (h *fakeHub) url() string { return h.srv.URL }

func (h *fakeHub) checkAuth(w http.ResponseWriter, r *http.Request) (string, bool) {
	user, pass, ok := r.BasicAuth()
	if !ok || h.auth[user] != pass {
		w.Header().Set("WWW-Authenticate", `Basic realm="meshsat-relay"`)
		w.WriteHeader(http.StatusUnauthorized)
		return "", false
	}
	return user, true
}

func (h *fakeHub) serve(w http.ResponseWriter, r *http.Request) {
	user, ok := h.checkAuth(w, r)
	if !ok {
		return
	}
	h.mu.Lock()
	h.lastAuth = r.Header.Get("Authorization")
	h.serves++
	h.mu.Unlock()
	c, err := h.up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	_ = user
	h.bridgeMu.Lock()
	h.bridge = c
	h.bridgeMu.Unlock()
	h.gotServe <- struct{}{}
	for {
		mt, data, err := c.ReadMessage()
		if err != nil {
			h.bridgeMu.Lock()
			if h.bridge == c {
				h.bridge = nil
			}
			h.bridgeMu.Unlock()
			return
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		id, payload, err := decodeEnvelope(data)
		if err != nil {
			_ = c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1003, "envelope"), time.Now().Add(time.Second))
			return
		}
		h.mu.Lock()
		cl := h.clients[id]
		h.mu.Unlock()
		if cl != nil {
			_ = cl.WriteMessage(websocket.BinaryMessage, payload)
		}
	}
}

func (h *fakeHub) connect(w http.ResponseWriter, r *http.Request) {
	user, ok := h.checkAuth(w, r)
	if !ok {
		return
	}
	c, err := h.up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	h.mu.Lock()
	h.clients[user] = c
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, user)
		h.mu.Unlock()
	}()
	for {
		mt, data, err := c.ReadMessage()
		if err != nil {
			return
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		frame, _ := encodeEnvelope(user, data)
		h.bridgeMu.Lock()
		b := h.bridge
		h.bridgeMu.Unlock()
		if b != nil {
			_ = b.WriteMessage(websocket.BinaryMessage, frame)
		}
	}
}

func (h *fakeHub) dropBridge() {
	h.bridgeMu.Lock()
	b := h.bridge
	h.bridge = nil
	h.bridgeMu.Unlock()
	if b != nil {
		_ = b.Close()
	}
}

// ---- helpers ----

func testRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	r.Post("/echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, _ = w.Write(b)
	})
	return r
}

func shortKnobs(t *testing.T) {
	t.Helper()
	ib, mb, sd := initialBackoff, maxBackoff, stillDownEvery
	initialBackoff, maxBackoff, stillDownEvery = 20*time.Millisecond, 100*time.Millisecond, time.Hour
	t.Cleanup(func() { initialBackoff, maxBackoff, stillDownEvery = ib, mb, sd })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// clientThroughHub is the phone's job in Go: a tunnel to bridgeID, TLS with
// its own Hub-issued certificate, ServerName = bridgeID, and an HTTP client
// on top.
func clientThroughHub(t *testing.T, hub *fakeHub, ca *testCA, bridgeID, clientID, pass string, certPEM, keyPEM []byte) *http.Client {
	t.Helper()
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.pem)
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}, RootCAs: pool, ServerName: bridgeID}
	tr := &http.Transport{
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			raw, err := DialThroughHub(ctx, hub.url(), bridgeID, clientID, pass)
			if err != nil {
				return nil, err
			}
			tc := tls.Client(raw, tlsCfg)
			if err := tc.HandshakeContext(ctx); err != nil {
				_ = raw.Close()
				return nil, err
			}
			return tc, nil
		},
		DisableKeepAlives: true,
	}
	return &http.Client{Transport: tr, Timeout: 5 * time.Second}
}

func startClient(t *testing.T, hub *fakeHub, ca *testCA, bridgeID, pass string) *Client {
	t.Helper()
	certPEM, keyPEM := ca.issue(t, bridgeID, true)
	c := New(Config{
		HubAPIURL: hub.url(), BridgeID: bridgeID, Password: pass,
		CertPEM: certPEM, KeyPEM: keyPEM, CAPEM: ca.pem,
		Handler: testRouter(), IdleTimeout: time.Minute,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = c.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	return c
}

// ---- tests ----

func TestEnvelopeRoundTrip(t *testing.T) {
	f, err := encodeEnvelope("phone-1", []byte("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f[:2], []byte{0x01, 7}) {
		t.Fatalf("header %x", f[:2])
	}
	id, p, err := decodeEnvelope(f)
	if err != nil || id != "phone-1" || string(p) != "hi" {
		t.Fatalf("decode: %q %q %v", id, p, err)
	}
	for _, bad := range [][]byte{nil, {0x01}, {0x02, 1, 'a'}, {0x01, 0}, {0x01, 5, 'a', 'b'}} {
		if _, _, err := decodeEnvelope(bad); err == nil {
			t.Errorf("accepted %x", bad)
		}
	}
	if _, err := encodeEnvelope("", nil); err == nil {
		t.Error("accepted an empty client id")
	}
	if _, err := encodeEnvelope(strings.Repeat("x", 256), nil); err == nil {
		t.Error("accepted a 256-byte client id")
	}
}

func TestDeriveAPIURL(t *testing.T) {
	cases := map[string]string{
		"wss://mqtt-hub.meshsat.net:443/mqtt": "https://hub.meshsat.net",
		"wss://mqtt-hub.meshsat.net/mqtt":     "https://hub.meshsat.net",
		"ssl://mqtt-hub.example.org:8883":     "https://hub.example.org",
		"tcp://nats:1883":                     "",
		"":                                    "",
	}
	for in, want := range cases {
		if got := DeriveAPIURL(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestValidateRefusesACertificateThatCannotServe(t *testing.T) {
	ca := newTestCA(t)
	certPEM, keyPEM := ca.issue(t, "kit-a", false) // the pre-2026-09-15 shape
	cfg := Config{HubAPIURL: "https://hub.example", BridgeID: "kit-a", Password: "p", CertPEM: certPEM, KeyPEM: keyPEM, CAPEM: ca.pem, Handler: testRouter()}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "re-issue") {
		t.Fatalf("want a re-issue error, got %v", err)
	}
	cfg.CertPEM, cfg.KeyPEM = ca.issue(t, "kit-a", true)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a serving certificate refused: %v", err)
	}
	cfg.CAPEM = nil // a kit leaves it empty and Run fetches it from the Hub
	if err := cfg.Validate(); err != nil {
		t.Fatalf("an empty CA refused: %v", err)
	}
	cfg.CAPEM = []byte("not a pem")
	if err := cfg.Validate(); err == nil {
		t.Fatal("a garbage CA accepted")
	}
	if err := (Config{}).Validate(); err == nil {
		t.Fatal("empty config accepted")
	}
}

// A kit must not carry the bridge CA in its Hub connection (that field is
// the MQTT broker's root store and the broker has a public certificate), so
// the relay client fetches the CA from the Hub, and keeps trying until the
// Hub answers.
func TestFetchesTheBridgeCAFromTheHubWhenNotConfigured(t *testing.T) {
	shortKnobs(t)
	ca := newTestCA(t)
	hub := newFakeHub(t)
	hub.auth["kit-a"] = "kit-pass"
	hub.auth["phone-1"] = "pw"
	certPEM, keyPEM := ca.issue(t, "kit-a", true)
	c := New(Config{HubAPIURL: hub.url(), BridgeID: "kit-a", Password: "kit-pass", CertPEM: certPEM, KeyPEM: keyPEM, Handler: testRouter()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = c.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// The Hub has no CA yet (404): the client must wait, not serve.
	select {
	case <-hub.gotServe:
		t.Fatal("served before the CA was available")
	case <-time.After(150 * time.Millisecond):
	}
	hub.mu.Lock()
	hub.caPEM = ca.pem
	hub.mu.Unlock()
	select {
	case <-hub.gotServe:
	case <-time.After(5 * time.Second):
		t.Fatal("never served after the CA became available")
	}
	waitFor(t, "connected", c.Connected)

	// And the fetched CA is the one clients are verified against.
	pc, pk := ca.issue(t, "phone-1", false)
	cl := clientThroughHub(t, hub, ca, "kit-a", "phone-1", "pw", pc, pk)
	resp, err := cl.Get("https://kit-a/health")
	if err != nil {
		t.Fatalf("GET through the tunnel: %v", err)
	}
	_ = resp.Body.Close()
	other := newTestCA(t)
	xc, xk := other.issue(t, "phone-1", false)
	if _, err := clientThroughHub(t, hub, ca, "kit-a", "phone-1", "pw", xc, xk).Get("https://kit-a/health"); err == nil {
		t.Fatal("a foreign certificate got through a fetched-CA tunnel")
	}
}

func TestServesHTTPThroughTheTunnelWithMutualTLS(t *testing.T) {
	shortKnobs(t)
	ca := newTestCA(t)
	hub := newFakeHub(t)
	hub.auth["kit-a"] = "kit-pass"
	hub.auth["phone-1"] = "phone-pass"
	hub.auth["phone-2"] = "phone-pass2"

	c := startClient(t, hub, ca, "kit-a", "kit-pass")
	<-hub.gotServe
	waitFor(t, "connected", c.Connected)

	// The Basic header carries bridge_id:password.
	hub.mu.Lock()
	auth := hub.lastAuth
	hub.mu.Unlock()
	if auth != "Basic "+basicAuth("kit-a", "kit-pass") {
		t.Fatalf("authorization %q", auth)
	}

	p1c, p1k := ca.issue(t, "phone-1", false)
	p2c, p2k := ca.issue(t, "phone-2", false)
	c1 := clientThroughHub(t, hub, ca, "kit-a", "phone-1", "phone-pass", p1c, p1k)
	c2 := clientThroughHub(t, hub, ca, "kit-a", "phone-2", "phone-pass2", p2c, p2k)

	resp, err := c1.Get("https://kit-a/health")
	if err != nil {
		t.Fatalf("GET through the tunnel: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"ok"`) {
		t.Fatalf("health: %d %s", resp.StatusCode, body)
	}

	// Two clients, two tunnels, bytes do not cross: a 100 KiB body (three
	// chunks each way) echoes back intact on the second client.
	big := bytes.Repeat([]byte("phone-2 payload "), 100<<10/16)
	resp2, err := c2.Post("https://kit-a/echo", "application/octet-stream", bytes.NewReader(big))
	if err != nil {
		t.Fatalf("POST through the tunnel: %v", err)
	}
	got, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if !bytes.Equal(got, big) {
		t.Fatalf("echo mismatch: %d bytes back, want %d", len(got), len(big))
	}
	// Keep-alives are off, so each request was its own tunnel and both are
	// closed by now; what must hold is that two were served, not more (a
	// client's late close_notify must not open a phantom third).
	waitFor(t, "tunnels closed", func() bool { return c.Sessions() == 0 })
	if n := c.Served(); n != 2 {
		t.Fatalf("served %d tunnels, want 2", n)
	}
}

func TestBridgeRefusesAClientWithoutAHubIssuedCertificate(t *testing.T) {
	shortKnobs(t)
	ca := newTestCA(t)
	other := newTestCA(t)
	hub := newFakeHub(t)
	hub.auth["kit-a"] = "kit-pass"
	hub.auth["phone-x"] = "pw"
	startClient(t, hub, ca, "kit-a", "kit-pass")
	<-hub.gotServe

	// A certificate from a different CA: the bridge's handshake refuses it.
	xc, xk := other.issue(t, "phone-x", false)
	cl := clientThroughHub(t, hub, ca, "kit-a", "phone-x", "pw", xc, xk)
	if _, err := cl.Get("https://kit-a/health"); err == nil {
		t.Fatal("a client with a foreign certificate got through")
	}

	// And a client that verifies the server against the wrong name fails too.
	pc, pk := ca.issue(t, "phone-x", false)
	cl2 := clientThroughHub(t, hub, ca, "kit-a", "phone-x", "pw", pc, pk)
	cl2.Transport.(*http.Transport).DialTLSContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		raw, err := DialThroughHub(ctx, hub.url(), "kit-a", "phone-x", "pw")
		if err != nil {
			return nil, err
		}
		cert, _ := tls.X509KeyPair(pc, pk)
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(ca.pem)
		tc := tls.Client(raw, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}, RootCAs: pool, ServerName: "kit-b"})
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, err
		}
		return tc, nil
	}
	if _, err := cl2.Get("https://kit-a/health"); err == nil {
		t.Fatal("server name kit-b verified against kit-a's certificate")
	}
}

func TestReconnectsAfterTheHubDropsTheSocket(t *testing.T) {
	shortKnobs(t)
	ca := newTestCA(t)
	hub := newFakeHub(t)
	hub.auth["kit-a"] = "kit-pass"
	hub.auth["phone-1"] = "pw"
	c := startClient(t, hub, ca, "kit-a", "kit-pass")
	<-hub.gotServe
	waitFor(t, "first connect", c.Connected)

	hub.dropBridge()
	waitFor(t, "disconnect noticed", func() bool { return !c.Connected() })
	select {
	case <-hub.gotServe:
	case <-time.After(5 * time.Second):
		t.Fatal("no reconnect")
	}
	waitFor(t, "reconnected", c.Connected)
	hub.mu.Lock()
	n := hub.serves
	hub.mu.Unlock()
	if n < 2 {
		t.Fatalf("serves %d", n)
	}

	// And it serves again on the new socket.
	pc, pk := ca.issue(t, "phone-1", false)
	cl := clientThroughHub(t, hub, ca, "kit-a", "phone-1", "pw", pc, pk)
	resp, err := cl.Get("https://kit-a/health")
	if err != nil {
		t.Fatalf("after reconnect: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRefusedCredentialsAreReportedNotRetriedHot(t *testing.T) {
	shortKnobs(t)
	ca := newTestCA(t)
	hub := newFakeHub(t)
	hub.auth["kit-a"] = "right"
	certPEM, keyPEM := ca.issue(t, "kit-a", true)
	c := New(Config{HubAPIURL: hub.url(), BridgeID: "kit-a", Password: "wrong", CertPEM: certPEM, KeyPEM: keyPEM, CAPEM: ca.pem, Handler: testRouter()})
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	err := c.serveOnce(ctx)
	if !errors.Is(err, errRefused) {
		t.Fatalf("want errRefused, got %v", err)
	}
	// Run keeps retrying at the slow rate and returns cleanly on cancel.
	if err := c.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	hub.mu.Lock()
	n := hub.serves
	hub.mu.Unlock()
	if n != 0 {
		t.Fatalf("a refused credential was upgraded %d times", n)
	}
}

func TestStopClosesTheSocketAndRunReturns(t *testing.T) {
	shortKnobs(t)
	ca := newTestCA(t)
	hub := newFakeHub(t)
	hub.auth["kit-a"] = "kit-pass"
	certPEM, keyPEM := ca.issue(t, "kit-a", true)
	c := New(Config{HubAPIURL: hub.url(), BridgeID: "kit-a", Password: "kit-pass", CertPEM: certPEM, KeyPEM: keyPEM, CAPEM: ca.pem, Handler: testRouter()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	<-hub.gotServe
	waitFor(t, "connected", c.Connected)
	c.Stop()
	waitFor(t, "socket closed", func() bool { return !c.Connected() })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
}

// A client that keeps its HTTP connection alive leaves its tunnel open, and
// the Hub never says a tunnel ended. When the same client opens its next
// tunnel, its ClientHello must start a new session, not be written into the
// stale one: MeshSat Android failed every other request to tesseract
// through the relay on 21 Sep 2026, strictly alternating. [MESHSAT-1297]
func TestANewClientHelloReplacesAStaleTunnelOfTheSameClient(t *testing.T) {
	shortKnobs(t)
	ca := newTestCA(t)
	hub := newFakeHub(t)
	hub.auth["kit-a"] = "kit-pass"
	hub.auth["phone-1"] = "phone-pass"
	c := startClient(t, hub, ca, "kit-a", "kit-pass")
	<-hub.gotServe
	waitFor(t, "connected", c.Connected)

	pc, pk := ca.issue(t, "phone-1", false)
	keepAlive := func() *http.Client {
		hc := clientThroughHub(t, hub, ca, "kit-a", "phone-1", "phone-pass", pc, pk)
		hc.Transport.(*http.Transport).DisableKeepAlives = false
		return hc
	}
	for i := 1; i <= 4; i++ {
		// A fresh transport each time, as a phone app that reconnects does:
		// the previous connection is still open on the bridge side.
		resp, err := keepAlive().Get("https://kit-a/health")
		if err != nil {
			t.Fatalf("request %d through a new tunnel of the same client: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(body), `"ok"`) {
			t.Fatalf("request %d: %d %s", i, resp.StatusCode, body)
		}
	}
	// Each new ClientHello replaced the session before it, so at most one
	// is open for the client at any time.
	if n := c.Sessions(); n > 1 {
		t.Fatalf("%d sessions open for one client", n)
	}
}

func TestIsClientHello(t *testing.T) {
	hello := []byte{0x16, 0x03, 0x01, 0x02, 0x00, 0x01, 0x00, 0x01, 0xfc, 0x03, 0x03}
	if !isClientHello(hello) {
		t.Fatal("a ClientHello record was not recognised")
	}
	for name, p := range map[string][]byte{
		"application data":        {0x17, 0x03, 0x03, 0x00, 0x20, 0x01, 0, 0, 0, 0x03},
		"encrypted handshake":     {0x16, 0x03, 0x03, 0x00, 0x20, 0x9a, 0, 0, 0, 0x55},
		"close_notify alert":      {0x15, 0x03, 0x03, 0x00, 0x02, 0x01, 0x00},
		"truncated record header": {0x16, 0x03, 0x01},
	} {
		if isClientHello(p) {
			t.Errorf("%s taken for a ClientHello", name)
		}
	}
}
