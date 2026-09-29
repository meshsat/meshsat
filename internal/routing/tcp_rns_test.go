package routing

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/reticulum"
)

// testCA issues ECDSA certificates for the TLS tests; nothing leaves 127.0.0.1.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

var testSerial atomic.Int64

func newTestCA(t *testing.T, cn string) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(testSerial.Add(1)),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &testCA{cert: cert, key: key, pool: pool}
}

// issue signs a leaf for names (DNS names or IP literals) with one extended usage.
func (ca *testCA) issue(t *testing.T, cn string, usage x509.ExtKeyUsage, names ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(testSerial.Add(1)),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// rnsNode is a stock Reticulum node's TCP server as far as a client can tell:
// on each connection it sends hello as one HDLC frame and records the packets
// it receives. With a TLS config it is the Hub's stunnel in front of one.
type rnsNode struct {
	ln    net.Listener
	hello []byte

	mu   sync.Mutex
	v    nodeView
	live []net.Conn
}

// nodeView is what the node has seen: packets received, SNI names, client
// certificate names, connections accepted and connections ended.
type nodeView struct {
	got, sni, peerCNs []string
	accepts, ends     int
}

func startRNSNode(t *testing.T, cfg *tls.Config, hello []byte) *rnsNode {
	t.Helper()
	n := &rnsNode{hello: hello}
	var err error
	if cfg != nil {
		cfg = cfg.Clone()
		cfg.GetConfigForClient = func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			n.mu.Lock()
			n.v.sni = append(n.v.sni, h.ServerName)
			n.mu.Unlock()
			return nil, nil
		}
		n.ln, err = tls.Listen("tcp", "127.0.0.1:0", cfg)
	} else {
		n.ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.close)
	go n.serve()
	return n
}

func (n *rnsNode) port() int { return n.ln.Addr().(*net.TCPAddr).Port }

func (n *rnsNode) serve() {
	for {
		c, err := n.ln.Accept()
		if err != nil {
			return
		}
		n.mu.Lock()
		n.v.accepts++
		n.live = append(n.live, c)
		n.mu.Unlock()
		go n.handle(c)
	}
}

func (n *rnsNode) handle(c net.Conn) {
	defer func() {
		c.Close()
		n.mu.Lock()
		n.v.ends++
		n.mu.Unlock()
	}()
	if tc, ok := c.(*tls.Conn); ok {
		if err := tc.Handshake(); err != nil {
			return
		}
		if pcs := tc.ConnectionState().PeerCertificates; len(pcs) > 0 {
			n.mu.Lock()
			n.v.peerCNs = append(n.v.peerCNs, pcs[0].Subject.CommonName)
			n.mu.Unlock()
		}
	}
	if n.hello != nil {
		if _, err := c.Write(reticulum.HDLCFrame(n.hello)); err != nil {
			return
		}
	}
	r := reticulum.NewHDLCFrameReader()
	buf := make([]byte, 4096)
	for {
		k, err := c.Read(buf)
		for _, f := range r.Feed(buf[:k]) {
			n.mu.Lock()
			n.v.got = append(n.v.got, string(f))
			n.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// dropAll closes every connection the node holds, as a node restart would.
func (n *rnsNode) dropAll() {
	n.mu.Lock()
	live := n.live
	n.live = nil
	n.mu.Unlock()
	for _, c := range live {
		c.Close()
	}
}

func (n *rnsNode) close() {
	n.ln.Close()
	n.dropAll()
}

func (n *rnsNode) view() nodeView {
	n.mu.Lock()
	defer n.mu.Unlock()
	v := n.v
	v.got = append([]string(nil), v.got...)
	v.sni = append([]string(nil), v.sni...)
	v.peerCNs = append([]string(nil), v.peerCNs...)
	return v
}

// rxLog collects what a client hands to its callback.
type rxLog struct {
	mu  sync.Mutex
	got []string
}

func (r *rxLog) add(p []byte) {
	r.mu.Lock()
	r.got = append(r.got, string(p))
	r.mu.Unlock()
}

func (r *rxLog) has(s string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, g := range r.got {
		if g == s {
			return true
		}
	}
	return false
}

// Packets of at least the Reticulum header size (19 bytes) with both HDLC
// special bytes in them, so the escaping is exercised on the wire.
var (
	helloFromNode = "\x7e\x7dhello from a stock rns node"
	helloToNode   = "\x7d\x7ehello from the bridge's tcp_rns"
)

func TestTCPRNSEffectiveTLS443(t *testing.T) {
	for _, c := range []struct {
		tls  bool
		port int
		want bool
	}{
		{false, 4242, false},
		{true, 4242, true},
		{false, 443, true}, // Android GatewayService.kt:1713
		{true, 443, true},
		{false, 4243, false},
		{true, 4243, true},
	} {
		if got := TCPRNSEffectiveTLS(c.tls, c.port); got != c.want {
			t.Errorf("TCPRNSEffectiveTLS(%v, %d) = %v, want %v", c.tls, c.port, got, c.want)
		}
	}
	for _, c := range []struct {
		cfg  TCPRNSConfig
		want string
	}{
		{TCPRNSConfig{Host: "10.0.0.5", Port: 4242}, "10.0.0.5:4242"},
		{TCPRNSConfig{Host: "rns.example.org", Port: 443}, "rns.example.org:443, TLS"},
		{TCPRNSConfig{Host: "reticulum.meshsat.net", Port: 4243, TLS: true}, "reticulum.meshsat.net:4243, TLS"},
		{TCPRNSConfig{Host: "fd00::5"}, "[fd00::5]:4242"},
		{TCPRNSConfig{Host: "[fd00::5]", Port: 443}, "[fd00::5]:443, TLS"},
	} {
		if got := c.cfg.Summary(); got != c.want {
			t.Errorf("Summary(%+v) = %q, want %q", c.cfg, got, c.want)
		}
	}
	// Port 443 alone dials TLS: a plain listener sees a TLS ClientHello.
	c := NewTCPRNSClient("tcp_rns_0", TCPRNSConfig{Host: "rns.example.org", Port: 443}, nil, nil)
	if cfg := c.tlsConfig(); cfg.MinVersion != tls.VersionTLS12 || cfg.ServerName != "rns.example.org" || cfg.RootCAs != nil || cfg.InsecureSkipVerify {
		t.Fatalf("tls config %+v", cfg)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c.dialAddr = ln.Addr().String()
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	srv, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	srv.SetReadDeadline(time.Now().Add(5 * time.Second))
	hdr := make([]byte, 3)
	if _, err := io.ReadFull(srv, hdr); err != nil {
		t.Fatal(err)
	}
	if hdr[0] != 0x16 || hdr[1] != 0x03 { // TLS handshake record
		t.Fatalf("port 443 did not start TLS: % x", hdr)
	}
}

func TestTCPRNSClientPlainReconnect(t *testing.T) {
	node := startRNSNode(t, nil, []byte(helloFromNode))
	var rx rxLog
	c := NewTCPRNSClient("tcp_rns_0", TCPRNSConfig{Host: "127.0.0.1", Port: node.port()}, nil, rx.add)
	c.retry = 50 * time.Millisecond
	if c.IsOnline() {
		t.Fatal("online before Start")
	}
	if err := c.Send(context.Background(), []byte(helloToNode)); err == nil {
		t.Fatal("send without a connection succeeded")
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	waitCond(t, "connected", 5*time.Second, c.IsOnline)
	waitCond(t, "the node's packet", 5*time.Second, func() bool { return rx.has(helloFromNode) })
	if err := c.Send(context.Background(), []byte(helloToNode)); err != nil {
		t.Fatal(err)
	}
	waitCond(t, "the bridge's packet at the node", 5*time.Second, func() bool {
		got := node.view().got
		return len(got) == 1 && got[0] == helloToNode
	})
	if rxb, txb := c.Counters(); rxb != uint64(len(helloFromNode)) || txb != uint64(len(helloToNode)) {
		t.Fatalf("counters rx %d tx %d", rxb, txb)
	}
	// The node goes away and comes back: the client reconnects by itself.
	node.dropAll()
	waitCond(t, "a second connection", 5*time.Second, func() bool {
		return node.view().accepts >= 2 && c.IsOnline()
	})
	if c.LastError() != "" {
		t.Fatalf("a clean close is not a connect error: %q", c.LastError())
	}
	// Stop closes the connection and nothing reconnects.
	c.Stop()
	if c.IsOnline() {
		t.Fatal("online after Stop")
	}
	time.Sleep(200 * time.Millisecond) // four retry periods
	if n := node.view().accepts; n != 2 {
		t.Fatalf("%d connections, want 2 (reconnected after Stop?)", n)
	}
	if err := c.Start(context.Background()); err == nil {
		t.Fatal("a stopped client started again")
	}
}

func TestTCPRNSClientTLS(t *testing.T) {
	ca := newTestCA(t, "MeshSat test CA")
	other := newTestCA(t, "Some other CA")
	srvCert := ca.issue(t, "rns.meshsat.test", x509.ExtKeyUsageServerAuth, "rns.meshsat.test", "127.0.0.1")
	node := startRNSNode(t, &tls.Config{Certificates: []tls.Certificate{srvCert}}, []byte(helloFromNode))

	var certCalls atomic.Int32
	clientCert := func() (*tls.Certificate, error) { certCalls.Add(1); return nil, nil }

	// The test CA through the root override: verified by IP, and packets flow
	// both ways inside TLS.
	var rx rxLog
	c := NewTCPRNSClient("tcp_rns_0", TCPRNSConfig{Host: "127.0.0.1", Port: node.port(), TLS: true}, clientCert, rx.add)
	c.rootCAs = ca.pool
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitCond(t, "TLS connection", 5*time.Second, c.IsOnline)
	waitCond(t, "the node's packet over TLS", 5*time.Second, func() bool { return rx.has(helloFromNode) })
	if err := c.Send(context.Background(), []byte(helloToNode)); err != nil {
		t.Fatal(err)
	}
	waitCond(t, "the bridge's packet at the node", 5*time.Second, func() bool {
		got := node.view().got
		return len(got) == 1 && got[0] == helloToNode
	})
	if c.LastError() != "" {
		t.Fatalf("last error %q", c.LastError())
	}
	c.Stop()
	if n := certCalls.Load(); n != 0 {
		t.Fatalf("client certificate read %d times for a server that asked for none", n)
	}

	// A name: sent as SNI and verified, while the TCP connection goes to 127.0.0.1.
	named := NewTCPRNSClient("tcp_rns_0", TCPRNSConfig{Host: "rns.meshsat.test", Port: node.port(), TLS: true}, nil, nil)
	named.rootCAs, named.dialAddr = ca.pool, node.ln.Addr().String()
	if err := named.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitCond(t, "TLS connection by name", 5*time.Second, named.IsOnline)
	named.Stop()
	if sni := node.view().sni; len(sni) != 2 || sni[0] != "" || sni[1] != "rns.meshsat.test" {
		t.Fatalf("SNI seen by the node %q, want no SNI for the IP and the name for the name", sni)
	}

	// Handshake failures reach LastError and the client stays offline.
	for _, fc := range []struct {
		name, host string
		roots      *x509.CertPool
		want       string
	}{
		{"unknown authority", "127.0.0.1", other.pool, "certificate signed by unknown authority"},
		{"hostname mismatch (Q9: Android skips this check)", "rns.other.test", ca.pool, "not rns.other.test"},
	} {
		bad := NewTCPRNSClient("tcp_rns_0", TCPRNSConfig{Host: fc.host, Port: node.port(), TLS: true}, nil, nil)
		bad.rootCAs, bad.dialAddr = fc.roots, node.ln.Addr().String()
		if err := bad.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		waitCond(t, fc.name+" in LastError", 5*time.Second, func() bool { return bad.LastError() != "" })
		if le := bad.LastError(); !strings.Contains(le, fc.want) || bad.IsOnline() {
			t.Errorf("%s: last error %q (want %q), online %v", fc.name, le, fc.want, bad.IsOnline())
		} else {
			t.Logf("%s: %s", fc.name, le)
		}
		bad.Stop()
	}

	// TLS switched on against a plain Reticulum port: the node never answers
	// the ClientHello, and last_error says so instead of a bare context error.
	plainNode := startRNSNode(t, nil, nil)
	silent := NewTCPRNSClient("tcp_rns_0", TCPRNSConfig{Host: "127.0.0.1", Port: plainNode.port(), TLS: true}, nil, nil)
	silent.rootCAs, silent.timeout = ca.pool, 200*time.Millisecond
	if err := silent.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitCond(t, "handshake timeout in LastError", 5*time.Second, func() bool { return silent.LastError() != "" })
	if le, want := silent.LastError(), "tls handshake with 127.0.0.1:"+strconv.Itoa(plainNode.port())+": no answer within 200ms"; le != want || silent.IsOnline() {
		t.Errorf("last error %q, want %q", le, want)
	}
	silent.Stop()

	// Through the manager, with the system roots, which do not hold the test
	// CA: the refusal is the status's last_error, and the switch stays on.
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := NewIfaceManager(IfaceManagerConfig{DB: db, Registry: NewInterfaceRegistry()})
	if err := m.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer m.StopAll()
	id, err := m.Create(DynTypeTCPRNS, json.RawMessage(`{"host":"127.0.0.1","port":`+strconv.Itoa(node.port())+`,"tls":true}`), true)
	if err != nil || id != "tcp_rns_0" {
		t.Fatalf("create: %q %v", id, err)
	}
	waitCond(t, "handshake failure in last_error", 5*time.Second, func() bool { s, _ := m.Get(id); return s.LastError != "" })
	st, _ := m.Get(id)
	if !st.Enabled || !st.Running || st.Online || !strings.Contains(st.LastError, "certificate") {
		t.Fatalf("status %+v", st)
	}
	if st.Summary != "127.0.0.1:"+strconv.Itoa(node.port())+", TLS" {
		t.Fatalf("summary %q", st.Summary)
	}
}

func TestTCPRNSClientMTLS(t *testing.T) {
	ca := newTestCA(t, "MeshSat test Hub CA")
	srvCert := ca.issue(t, "reticulum.meshsat.test", x509.ExtKeyUsageServerAuth, "127.0.0.1")
	hubCert := ca.issue(t, "bridge-e2e", x509.ExtKeyUsageClientAuth)
	strangerCA := newTestCA(t, "Stranger CA")
	strangerCert := strangerCA.issue(t, "stranger", x509.ExtKeyUsageClientAuth)

	for _, ver := range []struct {
		name string
		max  uint16
	}{
		{"TLS 1.3 (the refusal arrives after the client's handshake)", tls.VersionTLS13},
		{"TLS 1.2 (the refusal ends the handshake)", tls.VersionTLS12},
	} {
		t.Run(ver.name, func(t *testing.T) {
			node := startRNSNode(t, &tls.Config{
				Certificates: []tls.Certificate{srvCert},
				ClientAuth:   tls.RequireAndVerifyClientCert,
				ClientCAs:    ca.pool,
				MaxVersion:   ver.max,
			}, []byte(helloFromNode))

			// The Hub client certificate is presented and accepted.
			var rx rxLog
			c := NewTCPRNSClient("tcp_rns_0", TCPRNSConfig{Host: "127.0.0.1", Port: node.port(), TLS: true},
				func() (*tls.Certificate, error) { return &hubCert, nil }, rx.add)
			c.rootCAs = ca.pool
			if err := c.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			waitCond(t, "mTLS connection", 5*time.Second, func() bool { return c.IsOnline() && rx.has(helloFromNode) })
			if err := c.Send(context.Background(), []byte(helloToNode)); err != nil {
				t.Fatal(err)
			}
			waitCond(t, "the bridge's packet at the node", 5*time.Second, func() bool {
				got := node.view().got
				return len(got) == 1 && got[0] == helloToNode
			})
			c.Stop()
			if cns := node.view().peerCNs; len(cns) != 1 || cns[0] != "bridge-e2e" {
				t.Fatalf("client certificates seen by the node: %q", cns)
			}

			// Refusals: no certificate stored, a certificate from another CA, and
			// a stored certificate that cannot be read.
			for _, rc := range []struct {
				name string
				cert func() (*tls.Certificate, error)
				want string
			}{
				{"no Hub certificate", func() (*tls.Certificate, error) { return nil, nil }, "certificate"},
				{"no certificate function", nil, "certificate"},
				{"certificate from another CA", func() (*tls.Certificate, error) { return &strangerCert, nil }, "certificate"},
				{"unreadable Hub certificate", func() (*tls.Certificate, error) { return nil, errors.New("tls: failed to find any PEM data") },
					"client certificate: tls: failed to find any PEM data"},
			} {
				bad := NewTCPRNSClient("tcp_rns_0", TCPRNSConfig{Host: "127.0.0.1", Port: node.port(), TLS: true}, rc.cert, nil)
				bad.rootCAs = ca.pool
				if err := bad.Start(context.Background()); err != nil {
					t.Fatal(err)
				}
				waitCond(t, rc.name+" in LastError", 5*time.Second, func() bool { return bad.LastError() != "" })
				if le := bad.LastError(); !strings.Contains(le, rc.want) || bad.IsOnline() {
					t.Errorf("%s: last error %q (want %q), online %v", rc.name, le, rc.want, bad.IsOnline())
				} else {
					t.Logf("%s: %s", rc.name, le)
				}
				bad.Stop()
			}
			if cns := node.view().peerCNs; len(cns) != 1 {
				t.Fatalf("a refused client got through: %q", cns)
			}
		})
	}
}
