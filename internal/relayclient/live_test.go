package relayclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestRelayLive is the acceptance run against the real Hub (MESHSAT-613):
// this process is the bridge end serving a chi router through
// hub.meshsat.net, and the same process is the phone end dialing back in
// through the Hub, so the whole path is exercised: Basic auth on both
// upgrades, the rendezvous across replicas, the inner mutual TLS handshake
// against the Hub CA, and one HTTP round trip. Gated on RELAY_LIVE=1 with
// the credentials of two throwaway bridges of one tenant in the environment
// (PEMs as file paths). Run it twice; the two ends land on different Hub
// replicas.
func TestRelayLive(t *testing.T) {
	if os.Getenv("RELAY_LIVE") != "1" {
		t.Skip("RELAY_LIVE=1 not set")
	}
	need := func(k string) string {
		v := os.Getenv(k)
		if v == "" {
			t.Fatalf("%s not set", k)
		}
		return v
	}
	read := func(k string) []byte {
		b, err := os.ReadFile(need(k))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	hubAPI := need("RELAY_HUB_API")
	bridgeID, bridgePass := need("RELAY_BRIDGE_ID"), os.Getenv("RELAY_BRIDGE_PASSWORD")
	clientID, clientPass := need("RELAY_CLIENT_ID"), need("RELAY_CLIENT_PASSWORD")
	caPEM := read("RELAY_CA")
	clientCert, clientKey := read("RELAY_CLIENT_CERT"), read("RELAY_CLIENT_KEY")

	// RELAY_CLIENT_ONLY=1: the bridge end is a real kit already serving
	// through the Hub (its own relay client, its own API); this process is
	// only the phone. The bridge PEMs are not needed then.
	if os.Getenv("RELAY_CLIENT_ONLY") != "1" {
		bridgeCert, bridgeKey := read("RELAY_BRIDGE_CERT"), read("RELAY_BRIDGE_KEY")
		c := New(Config{HubAPIURL: hubAPI, BridgeID: bridgeID, Password: bridgePass, CertPEM: bridgeCert, KeyPEM: bridgeKey, CAPEM: caPEM, Handler: testRouter()})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { _ = c.Run(ctx); close(done) }()
		defer func() {
			cancel()
			<-done
		}()
		deadline := time.Now().Add(30 * time.Second)
		for !c.Connected() && time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
		}
		if !c.Connected() {
			t.Fatal("bridge end never connected to the Hub")
		}
		t.Logf("bridge end serving through %s as %s", hubAPI, bridgeID)
	} else {
		t.Logf("client only: %s is expected to be serving through %s already", bridgeID, hubAPI)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("CA PEM does not parse")
	}
	cert, err := tls.X509KeyPair(clientCert, clientKey)
	if err != nil {
		t.Fatal(err)
	}
	var peerCN string
	tr := &http.Transport{
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			raw, err := DialThroughHub(ctx, hubAPI, bridgeID, clientID, clientPass)
			if err != nil {
				return nil, err
			}
			tc := tls.Client(raw, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}, RootCAs: pool, ServerName: bridgeID})
			if err := tc.HandshakeContext(ctx); err != nil {
				_ = raw.Close()
				return nil, err
			}
			peerCN = tc.ConnectionState().PeerCertificates[0].Subject.CommonName
			return tc, nil
		},
		DisableKeepAlives: true,
	}
	hc := &http.Client{Transport: tr, Timeout: 20 * time.Second}

	start := time.Now()
	resp, err := hc.Get("https://" + bridgeID + "/health")
	if err != nil {
		t.Fatalf("GET /health through the Hub: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "ok") {
		t.Fatalf("health: %d %s", resp.StatusCode, body)
	}
	t.Logf("GET /health through the Hub: %d %s in %s, server certificate CN=%s", resp.StatusCode, strings.TrimSpace(string(body)), time.Since(start).Round(time.Millisecond), peerCN)
	if peerCN != bridgeID {
		t.Fatalf("server CN %q, want %q", peerCN, bridgeID)
	}

	// A client with no certificate is refused by the bridge's handshake, not
	// by the Hub: the tunnel opens and the TLS handshake fails.
	trNoCert := &http.Transport{
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			raw, err := DialThroughHub(ctx, hubAPI, bridgeID, clientID, clientPass)
			if err != nil {
				return nil, err
			}
			tc := tls.Client(raw, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: bridgeID})
			if err := tc.HandshakeContext(ctx); err != nil {
				_ = raw.Close()
				return nil, err
			}
			return tc, nil
		},
		DisableKeepAlives: true,
	}
	if _, err := (&http.Client{Transport: trNoCert, Timeout: 20 * time.Second}).Get("https://" + bridgeID + "/health"); err == nil {
		t.Fatal("a client without a Hub-issued certificate got through")
	} else {
		t.Logf("client without a certificate refused: %v", err)
	}
}
