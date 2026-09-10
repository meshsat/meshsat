package gateway

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"meshsat/internal/transport"
)

// KISS protocol constants (TNC-2 spec).
const (
	kissFEND  = 0xC0 // Frame End
	kissFESC  = 0xDB // Frame Escape
	kissTFEND = 0xDC // Transposed Frame End
	kissTFESC = 0xDD // Transposed Frame Escape
	kissData  = 0x00 // Data frame command byte
)

// kissDeadliner is what a TCP connection offers and a serial port does not:
// per-call deadlines. The serial opener enforces its timeout in the port.
type kissDeadliner interface {
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
}

// kissTimeoutError is what a serial read returns when the port's read
// timeout passes with no byte; the read worker treats it like a TCP read
// deadline and simply reads again. [MESHSAT-821]
type kissTimeoutError struct{}

func (kissTimeoutError) Error() string   { return "kiss: read timeout" }
func (kissTimeoutError) Timeout() bool   { return true }
func (kissTimeoutError) Temporary() bool { return true }

// kissFrameError is a KISS frame the TNC delimited but that does not decode
// (a trailing FESC, an invalid escape pair, a non-data command byte). It is
// one corrupted frame, not a dead link: the read worker drops it and keeps
// reading, and logs the raw bytes so the next occurrence tells us what the
// TNC actually sent. Before this, every decode error closed and reopened
// the serial port, which discarded whatever the TNC had buffered and, on
// the PicoAPRS, disturbed the radio through the CP2102's modem lines; both
// kits lost the peer's repeat copy on each such event (10 Sep 2026).
type kissFrameError struct {
	raw []byte // the frame between the FENDs, command byte included
	err error
}

func (e *kissFrameError) Error() string { return e.err.Error() }
func (e *kissFrameError) Unwrap() error { return e.err }

// KISSConn manages one KISS link to a TNC: Direwolf over TCP (the bundled
// sound-card modem) or a hardware TNC over a serial port (PicoAPRS V4 over
// USB-C, 115200 baud). RX/TX counters track frames at the KISS level, the
// single source of truth for all traffic through this link. [MESHSAT-403,
// MESHSAT-821]
type KISSConn struct {
	addr   string // TCP host:port, when device is empty
	device string // serial device path, e.g. /dev/serial/by-id/usb-Silicon_Labs_...
	baud   int

	mu sync.Mutex
	rw io.ReadWriteCloser

	RX atomic.Int64 // frames read from the TNC
	TX atomic.Int64 // frames sent to the TNC
	// Repaired counts frames the TNC closed with a stray FESC right before
	// the FEND. The PicoAPRS V4 does this on a few percent of its frames
	// (both copies of the same message, so a strict decoder loses the
	// message): the AX.25 frame in front of the stray byte is complete, so
	// the byte is dropped and the frame kept. [MESHSAT-1020]
	Repaired atomic.Int64
}

// NewKISSConn creates a KISS TCP connection manager.
func NewKISSConn(addr string) *KISSConn {
	return &KISSConn{addr: addr}
}

// NewKISSSerialConn creates a KISS serial connection manager for a hardware
// TNC. baud 0 means 115200.
func NewKISSSerialConn(device string, baud int) *KISSConn {
	if baud <= 0 {
		baud = 115200
	}
	return &KISSConn{device: device, baud: baud}
}

// newKISSConnRW wraps an already-open stream (tests).
func newKISSConnRW(rw io.ReadWriteCloser) *KISSConn {
	return &KISSConn{rw: rw}
}

// Serial reports whether this link is a serial TNC rather than TCP.
func (k *KISSConn) Serial() bool { return k.device != "" }

// Target is the human-readable address of the link for logs and status.
func (k *KISSConn) Target() string {
	if k.device != "" {
		return fmt.Sprintf("%s@%d", k.device, k.baud)
	}
	return k.addr
}

// Dial opens the link: a TCP connect to Direwolf, or the serial port of a
// hardware TNC. The serial port is opened with DTR and RTS low and never
// toggled: on the PicoAPRS the CP2102's modem lines reach the ESP32, and a
// pulse there is a reboot. [MESHSAT-821]
func (k *KISSConn) Dial() error {
	var rw io.ReadWriteCloser
	if k.device != "" {
		port, err := transport.OpenKISSSerial(k.device, k.baud)
		if err != nil {
			return fmt.Errorf("kiss: open %s: %w", k.device, err)
		}
		rw = port
	} else {
		conn, err := net.DialTimeout("tcp", k.addr, 10*time.Second)
		if err != nil {
			return fmt.Errorf("kiss: dial %s: %w", k.addr, err)
		}
		rw = conn
	}
	k.mu.Lock()
	old := k.rw
	k.rw = rw
	k.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return nil
}

// Close closes the link.
func (k *KISSConn) Close() error {
	k.mu.Lock()
	rw := k.rw
	k.rw = nil
	k.mu.Unlock()
	if rw != nil {
		return rw.Close()
	}
	return nil
}

func (k *KISSConn) stream() io.ReadWriteCloser {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.rw
}

// SendFrame encodes and sends a KISS frame containing an AX.25 payload.
func (k *KISSConn) SendFrame(payload []byte) error {
	frame := KISSEncode(payload)
	rw := k.stream()
	if rw == nil {
		return fmt.Errorf("kiss: not connected")
	}
	if d, ok := rw.(kissDeadliner); ok {
		if err := d.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return err
		}
	}
	_, err := rw.Write(frame)
	if err == nil {
		k.TX.Add(1)
	}
	return err
}

// ReadFrame reads and decodes a single KISS frame from the link and returns
// the decoded AX.25 payload. A timeout with no byte is returned as an error
// whose Timeout() is true; the caller reads again.
func (k *KISSConn) ReadFrame() ([]byte, error) {
	rw := k.stream()
	if rw == nil {
		return nil, fmt.Errorf("kiss: not connected")
	}
	d, hasDeadline := rw.(kissDeadliner)

	buf := make([]byte, 1)
	var frame bytes.Buffer

	// Wait for start FEND
	for {
		if hasDeadline {
			if err := d.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
				return nil, err
			}
		}
		if err := readByte(rw, buf); err != nil {
			return nil, err
		}
		if buf[0] == kissFEND {
			break
		}
	}

	// Read until end FEND
	for {
		if hasDeadline {
			if err := d.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				return nil, err
			}
		}
		if err := readByte(rw, buf); err != nil {
			return nil, err
		}
		if buf[0] == kissFEND {
			if frame.Len() == 0 {
				// Back-to-back FENDs between frames (TNC idle fill): keep waiting.
				continue
			}
			break
		}
		frame.WriteByte(buf[0])
	}

	raw := frame.Bytes()
	decoded, err := KISSDecode(raw)
	if err != nil {
		// A lone FESC as the last byte cannot be part of any valid frame
		// and is what the PicoAPRS appends now and then: strip it and try
		// once more. Anything else is a corrupted frame.
		if errors.Is(err, errKISSTrailingEscape) && len(raw) > 2 {
			if repaired, rerr := KISSDecode(raw[:len(raw)-1]); rerr == nil {
				k.RX.Add(1)
				k.Repaired.Add(1)
				return repaired, nil
			}
		}
		return nil, &kissFrameError{raw: append([]byte(nil), raw...), err: err}
	}
	k.RX.Add(1)
	return decoded, nil
}

// errKISSTrailingEscape is a frame whose last byte is a bare FESC.
var errKISSTrailingEscape = errors.New("kiss: trailing escape")

// readByte reads exactly one byte. A serial port with a read timeout returns
// (0, nil) when nothing arrived; that becomes kissTimeoutError so the worker
// can distinguish silence from a dead link.
func readByte(r io.Reader, buf []byte) error {
	for {
		n, err := r.Read(buf[:1])
		if n == 1 {
			return nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return io.EOF
			}
			return err
		}
		return kissTimeoutError{}
	}
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
		return nil, errKISSTrailingEscape
	}

	return buf.Bytes(), nil
}
