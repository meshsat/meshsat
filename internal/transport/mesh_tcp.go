package transport

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// meshStream is what the Meshtastic frame reader and writer need of the link
// to a radio: bytes in, bytes out, and a way to close it. A USB serial port
// satisfies it, and so does a TCP connection to a meshtasticd daemon, which
// carries the same framing (0x94 0xC3, length, protobuf). [MESHSAT-1384]
type meshStream interface {
	io.Reader
	io.Writer
	io.Closer
}

const (
	// meshTCPPort is meshtasticd's default API port.
	meshTCPPort = "4403"
	// meshTCPWriteTimeout bounds a write to a daemon that stopped reading, so
	// a stalled socket cannot hold the transport's lock forever.
	meshTCPWriteTimeout = 5 * time.Second
)

// IsMeshTCP reports whether a mesh port setting names a meshtasticd daemon
// over TCP (`tcp://host[:port]`) rather than a serial device.
func IsMeshTCP(port string) bool {
	return strings.HasPrefix(strings.ToLower(port), "tcp://")
}

// meshTCPAddr turns `tcp://host[:port]` into host:port, with meshtasticd's
// default port when none is given.
func meshTCPAddr(port string) string {
	addr := port[len("tcp://"):]
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(strings.Trim(addr, "[]"), meshTCPPort)
	}
	return addr
}

// meshTransportName is what the status page shows for the link in use.
func meshTransportName(port string) string {
	switch {
	case IsMeshTCP(port):
		return "tcp"
	case IsMeshBLE(port):
		return "ble"
	}
	return "serial"
}

// tcpMeshStream gives a TCP connection the read contract the frame reader was
// written against: a serial port with a read timeout returns (0, nil) when
// nothing arrived, and the reader uses that pause to check its context. A
// net.Conn returns a timeout error instead, which the reader would take for a
// lost link. Writes get a deadline of their own.
type tcpMeshStream struct {
	conn net.Conn
}

func (s *tcpMeshStream) Read(p []byte) (int, error) {
	_ = s.conn.SetReadDeadline(time.Now().Add(meshReadTimeout))
	n, err := s.conn.Read(p)
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return n, nil
		}
		return n, err
	}
	return n, nil
}

func (s *tcpMeshStream) Write(p []byte) (int, error) {
	_ = s.conn.SetWriteDeadline(time.Now().Add(meshTCPWriteTimeout))
	return s.conn.Write(p)
}

func (s *tcpMeshStream) Close() error {
	return s.conn.Close()
}

// dialMeshTCP connects to a meshtasticd daemon, the way the RNode and KISS
// interfaces dial their TCP peers.
func dialMeshTCP(port string) (meshStream, error) {
	addr := meshTCPAddr(port)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(2 * time.Second)
	}
	return &tcpMeshStream{conn: conn}, nil
}
