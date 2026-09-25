// Package kiss is the KISS framing shared by the RNode driver, the generic
// KISS TNC interface and (eventually) the APRS gateway: FEND-delimited
// frames whose first byte is a command, with FESC escaping. [MESHSAT-1349]
package kiss

import "time"

// Framing bytes (TNC-2 KISS).
const (
	FEND  = 0xC0
	FESC  = 0xDB
	TFEND = 0xDC
	TFESC = 0xDD

	// CmdData is the data frame command byte (port 0).
	CmdData = 0x00
)

// Escape replaces FESC and FEND inside a payload.
func Escape(data []byte) []byte {
	out := make([]byte, 0, len(data)+len(data)/16+4)
	for _, b := range data {
		switch b {
		case FESC:
			out = append(out, FESC, TFESC)
		case FEND:
			out = append(out, FESC, TFEND)
		default:
			out = append(out, b)
		}
	}
	return out
}

// Unescape reverses Escape. An invalid escape pair keeps the escaped byte.
func Unescape(data []byte) []byte {
	out := make([]byte, 0, len(data))
	esc := false
	for _, b := range data {
		if esc {
			switch b {
			case TFEND:
				out = append(out, FEND)
			case TFESC:
				out = append(out, FESC)
			default:
				out = append(out, b)
			}
			esc = false
			continue
		}
		if b == FESC {
			esc = true
			continue
		}
		out = append(out, b)
	}
	return out
}

// Encode builds one frame: FEND cmd escaped(payload) FEND.
func Encode(cmd byte, payload []byte) []byte {
	out := make([]byte, 0, len(payload)+4)
	out = append(out, FEND, cmd)
	out = append(out, Escape(payload)...)
	return append(out, FEND)
}

// Frame is one delimited frame with its command byte and unescaped payload.
type Frame struct {
	Cmd     byte
	Payload []byte
}

// Splitter turns a byte stream into frames. Feed returns every complete
// frame in the bytes so far; a partial frame older than Timeout is dropped
// (RNode's device read timeout), and one longer than MaxLen is discarded.
type Splitter struct {
	MaxLen  int
	Timeout time.Duration

	buf      []byte
	inFrame  bool
	lastByte time.Time
}

// Feed appends data and returns the complete frames.
func (s *Splitter) Feed(data []byte, now time.Time) []Frame {
	if s.Timeout > 0 && s.inFrame && len(s.buf) > 0 && now.Sub(s.lastByte) > s.Timeout {
		s.buf = s.buf[:0]
		s.inFrame = false
	}
	if len(data) > 0 {
		s.lastByte = now
	}
	var frames []Frame
	for _, b := range data {
		if b == FEND {
			if s.inFrame && len(s.buf) > 0 {
				frames = append(frames, Frame{Cmd: s.buf[0], Payload: Unescape(s.buf[1:])})
			}
			s.buf = s.buf[:0]
			s.inFrame = true
			continue
		}
		if !s.inFrame {
			continue
		}
		if s.MaxLen > 0 && len(s.buf) > s.MaxLen {
			s.buf = s.buf[:0]
			s.inFrame = false
			continue
		}
		s.buf = append(s.buf, b)
	}
	return frames
}

// Reset drops any partial frame.
func (s *Splitter) Reset() {
	s.buf = s.buf[:0]
	s.inFrame = false
}
