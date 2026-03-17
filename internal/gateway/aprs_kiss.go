package gateway

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"time"
)

// KISS protocol constants (TNC-2 spec).
const (
	kissFEND  = 0xC0 // Frame End
	kissFESC  = 0xDB // Frame Escape
	kissTFEND = 0xDC // Transposed Frame End
	kissTFESC = 0xDD // Transposed Frame Escape
	kissData  = 0x00 // Data frame command byte
)

// KISSConn manages a KISS TCP connection to a Direwolf TNC.
type KISSConn struct {
	addr string
	conn net.Conn
}

// NewKISSConn creates a new KISS TCP connection manager.
func NewKISSConn(addr string) *KISSConn {
	return &KISSConn{addr: addr}
}

// Dial connects to the Direwolf KISS TCP port.
func (k *KISSConn) Dial() error {
	conn, err := net.DialTimeout("tcp", k.addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("kiss: dial %s: %w", k.addr, err)
	}
	k.conn = conn
	return nil
}

// Close closes the connection.
func (k *KISSConn) Close() error {
	if k.conn != nil {
		return k.conn.Close()
	}
	return nil
}

// SendFrame encodes and sends a KISS frame containing an AX.25 payload.
func (k *KISSConn) SendFrame(payload []byte) error {
	frame := KISSEncode(payload)
	if k.conn == nil {
		return fmt.Errorf("kiss: not connected")
	}
	if err := k.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	_, err := k.conn.Write(frame)
	return err
}

// ReadFrame reads and decodes a single KISS frame from the connection.
// Returns the decoded AX.25 payload.
func (k *KISSConn) ReadFrame() ([]byte, error) {
	if k.conn == nil {
		return nil, fmt.Errorf("kiss: not connected")
	}

	buf := make([]byte, 1)
	var frame bytes.Buffer

	// Wait for start FEND
	for {
		if err := k.conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
			return nil, err
		}
		_, err := io.ReadFull(k.conn, buf)
		if err != nil {
			return nil, err
		}
		if buf[0] == kissFEND {
			break
		}
	}

	// Read until end FEND
	for {
		if err := k.conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return nil, err
		}
		_, err := io.ReadFull(k.conn, buf)
		if err != nil {
			return nil, err
		}
		if buf[0] == kissFEND {
			break
		}
		frame.WriteByte(buf[0])
	}

	if frame.Len() == 0 {
		return nil, fmt.Errorf("kiss: empty frame")
	}

	return KISSDecode(frame.Bytes())
}

// KISSEncode wraps an AX.25 payload in a KISS frame.
// Format: FEND + command(0x00) + escaped_data + FEND
func KISSEncode(payload []byte) []byte {
	var buf bytes.Buffer
	buf.WriteByte(kissFEND)
	buf.WriteByte(kissData) // port 0, data frame
	for _, b := range payload {
		switch b {
		case kissFEND:
			buf.WriteByte(kissFESC)
			buf.WriteByte(kissTFEND)
		case kissFESC:
			buf.WriteByte(kissFESC)
			buf.WriteByte(kissTFESC)
		default:
			buf.WriteByte(b)
		}
	}
	buf.WriteByte(kissFEND)
	return buf.Bytes()
}

// KISSDecode removes KISS framing and unescapes a KISS frame payload.
// Input should NOT include the outer FEND delimiters.
func KISSDecode(frame []byte) ([]byte, error) {
	if len(frame) < 2 {
		return nil, fmt.Errorf("kiss: frame too short")
	}

	// First byte is command byte — 0x00 for data frames
	cmd := frame[0]
	if cmd&0x0F != kissData {
		return nil, fmt.Errorf("kiss: non-data frame (cmd=0x%02x)", cmd)
	}

	var buf bytes.Buffer
	escaped := false
	for _, b := range frame[1:] {
		if escaped {
			switch b {
			case kissTFEND:
				buf.WriteByte(kissFEND)
			case kissTFESC:
				buf.WriteByte(kissFESC)
			default:
				return nil, fmt.Errorf("kiss: invalid escape sequence 0xDB 0x%02x", b)
			}
			escaped = false
		} else if b == kissFESC {
			escaped = true
		} else {
			buf.WriteByte(b)
		}
	}

	if escaped {
		return nil, fmt.Errorf("kiss: trailing escape")
	}

	return buf.Bytes(), nil
}
