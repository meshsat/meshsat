package relayclient

import (
	"errors"
	"net"
	"sync"
	"time"
)

// relayAddr is what a tunnel connection reports as its addresses: the
// network is "hub-relay" and the string is the peer's bridge id.
type relayAddr struct{ id string }

func (a relayAddr) Network() string { return "hub-relay" }
func (a relayAddr) String() string  { return a.id }

// Listener hands one net.Conn per relay client to whatever serves on it
// (tls.NewListener + http.Server.Serve in practice). It implements net.Listener.
type Listener struct {
	accept chan net.Conn
	done   chan struct{}
	once   sync.Once
	addr   relayAddr
}

func newListener(bridgeID string) *Listener {
	return &Listener{accept: make(chan net.Conn, 16), done: make(chan struct{}), addr: relayAddr{bridgeID}}
}

// Accept implements net.Listener.
func (l *Listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.accept:
		return c, nil
	case <-l.done:
		return nil, errors.New("relayclient: listener closed")
	}
}

// Close implements net.Listener.
func (l *Listener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

// Addr implements net.Listener.
func (l *Listener) Addr() net.Addr { return l.addr }

func (l *Listener) offer(c net.Conn) bool {
	select {
	case l.accept <- c:
		return true
	case <-l.done:
		return false
	}
}

// session is one client's tunnel: a net.Pipe whose server side is served
// by the API and whose client side is pumped to and from relay frames.
type session struct {
	id     string
	server net.Conn // handed to Accept
	client net.Conn // pumped by the relay client
	mu     sync.Mutex
	seen   time.Time
	closed bool
}

func newSession(id string) *session {
	srv, cli := net.Pipe()
	return &session{id: id, server: srv, client: cli, seen: time.Now()}
}

func (s *session) touch() {
	s.mu.Lock()
	s.seen = time.Now()
	s.mu.Unlock()
}

func (s *session) idleFor(d time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.seen) > d
}

func (s *session) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.client.Close()
	_ = s.server.Close()
}
