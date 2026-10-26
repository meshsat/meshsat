package gateway

import (
	"net"
	"strconv"
	"testing"
	"time"
)

// listenIPv6Loopback listens on [::1]:0 and counts the connections it
// accepts. It skips the test on a host with no IPv6 loopback.
func listenIPv6Loopback(t *testing.T) (port int, accepted <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback on this host: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ch := make(chan struct{}, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
			ch <- struct{}{}
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, ch
}

func waitAccepted(t *testing.T, accepted <-chan struct{}) {
	t.Helper()
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("the listener on [::1] never saw the connection")
	}
}

// A TAK server given as an IPv6 literal is dialled: the address used to be
// built as "%s:%d", which gives "::1:8087", and the dial failed with "too
// many colons in address". A host already in brackets keeps working.
// [MESHSAT-1676]
func TestTAKDialIPv6LiteralHost(t *testing.T) {
	port, accepted := listenIPv6Loopback(t)
	for _, host := range []string{"::1", "[::1]"} {
		t.Run(host, func(t *testing.T) {
			gw := NewTAKGateway(TAKConfig{Host: host, Port: port}, nil)
			conn, err := gw.dial()
			if err != nil {
				t.Fatalf("dial %s port %d: %v", host, port, err)
			}
			_ = conn.Close()
			waitAccepted(t, accepted)
		})
	}
}

// The same for a Direwolf KISS host given as an IPv6 literal. [MESHSAT-1676]
func TestAPRSKISSDialIPv6LiteralHost(t *testing.T) {
	port, accepted := listenIPv6Loopback(t)
	for _, host := range []string{"::1", "[::1]"} {
		t.Run(host, func(t *testing.T) {
			gw := NewAPRSGateway(APRSConfig{Callsign: "TEST", SSID: 10, KISSHost: host, KISSPort: port}, nil)
			if want := net.JoinHostPort("::1", strconv.Itoa(port)); gw.kiss.Target() != want {
				t.Errorf("KISS target %q, want %q", gw.kiss.Target(), want)
			}
			if err := gw.kiss.Dial(); err != nil {
				t.Fatalf("dial %s port %d: %v", host, port, err)
			}
			_ = gw.kiss.Close()
			waitAccepted(t, accepted)
		})
	}
}

// The helper the TAK, APRS KISS and cell SMPP addresses are built with.
// [MESHSAT-1676]
func TestJoinHostPort(t *testing.T) {
	for _, c := range []struct {
		host string
		port int
		want string
	}{
		{"127.0.0.1", 8001, "127.0.0.1:8001"},
		{"tak.example.org", 8089, "tak.example.org:8089"},
		{"192.0.2.10", 2775, "192.0.2.10:2775"},
		{"::1", 8087, "[::1]:8087"},
		{"fd00::42", 2775, "[fd00::42]:2775"},
		{"[fd00::42]", 2775, "[fd00::42]:2775"},
		{"fe80::1%eth0", 8001, "[fe80::1%eth0]:8001"},
	} {
		if got := joinHostPort(c.host, c.port); got != c.want {
			t.Errorf("joinHostPort(%q, %d) = %q, want %q", c.host, c.port, got, c.want)
		}
	}
}
