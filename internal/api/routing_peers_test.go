package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/routing"
)

// peerListener accepts on 127.0.0.1 and counts connections, dropping each at
// once, as a Reticulum node that restarts would.
type peerListener struct {
	ln      net.Listener
	mu      sync.Mutex
	accepts int
}

func startPeerListener(t *testing.T) *peerListener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &peerListener{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.accepts++
			p.mu.Unlock()
			c.Close()
		}
	}()
	return p
}

func (p *peerListener) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.accepts
}

// A peer added through POST /api/routing/peers keeps reconnecting after the
// request that added it has ended: its loop runs under the server's
// long-lived context, not the request's (which net/http cancels when the
// handler returns). Before the fix it connected at most once.
func TestAddPeerOutlivesRequest(t *testing.T) {
	s := newTestServerWithDB(t)
	base, stopBridge := context.WithCancel(context.Background())
	defer stopBridge()
	s.SetBaseContext(base)
	tcp := routing.NewTCPInterface(routing.TCPInterfaceConfig{Name: "tcp_0", ReconnectInterval: 50 * time.Millisecond}, nil)
	if err := tcp.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer tcp.Stop()
	s.SetTCPInterface(tcp)
	peer := startPeerListener(t)

	// A real HTTP server, so the request's context ends with the handler.
	hs := httptest.NewServer(s.Router())
	defer hs.Close()
	resp, err := http.Post(hs.URL+"/api/routing/peers", "application/json", strings.NewReader(`{"address":"`+peer.ln.Addr().String()+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/routing/peers: %d", resp.StatusCode)
	}

	// Each connection is dropped by the peer; the loop dials again every 50 ms.
	deadline := time.Now().Add(5 * time.Second)
	for peer.count() < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := peer.count(); n < 3 {
		t.Fatalf("%d connections after the request ended, want at least 3: the peer's loop died with its request", n)
	}
	if got := s.loadPeers(); len(got) != 1 || got[0] != peer.ln.Addr().String() {
		t.Fatalf("persisted peers %q", got)
	}

	// It is the server's context the loop lives under: when the Bridge stops,
	// the loop ends at its next wait and dials no more.
	stopBridge()
	time.Sleep(300 * time.Millisecond) // a dial already under way may land; then the loop ends
	settled := peer.count()
	time.Sleep(300 * time.Millisecond)
	if n := peer.count(); n != settled {
		t.Fatalf("%d more connections after the server's context ended", n-settled)
	}
}
