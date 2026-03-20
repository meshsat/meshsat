package reticulum

// HDLC-like framing used by Reticulum's TCPInterface and SerialInterface.
// Reference: RNS/Interfaces/TCPInterface.py class HDLC

const (
	// HDLCFlag is the frame delimiter byte.
	HDLCFlag byte = 0x7E
	// HDLCEsc is the escape byte (PPP-style, same as RFC 1662).
	HDLCEsc byte = 0x7D
	// HDLCEscMask is XORed with escaped bytes.
	HDLCEscMask byte = 0x20
)

// HDLCEscape escapes a raw payload for HDLC framing.
// Replaces ESC → ESC,ESC^MASK and FLAG → ESC,FLAG^MASK.
func HDLCEscape(data []byte) []byte {
	// Worst case: every byte needs escaping → 2x size
	out := make([]byte, 0, len(data)+len(data)/4)
	for _, b := range data {
		switch b {
		case HDLCEsc:
			out = append(out, HDLCEsc, HDLCEsc^HDLCEscMask)
		case HDLCFlag:
			out = append(out, HDLCEsc, HDLCFlag^HDLCEscMask)
		default:
			out = append(out, b)
		}
	}
	return out
}

// HDLCUnescape reverses HDLC escaping.
func HDLCUnescape(data []byte) []byte {
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if data[i] == HDLCEsc && i+1 < len(data) {
			out = append(out, data[i+1]^HDLCEscMask)
			i++ // skip next byte
		} else {
			out = append(out, data[i])
		}
	}
	return out
}

// HDLCFrame wraps a raw Reticulum packet in HDLC framing: [FLAG] [escaped_data] [FLAG]
func HDLCFrame(data []byte) []byte {
	escaped := HDLCEscape(data)
	frame := make([]byte, 0, 2+len(escaped))
	frame = append(frame, HDLCFlag)
	frame = append(frame, escaped...)
	frame = append(frame, HDLCFlag)
	return frame
}

// HDLCFrameReader extracts complete HDLC frames from a byte stream.
// It accumulates data and returns complete packets when available.
type HDLCFrameReader struct {
	buf []byte
}

// NewHDLCFrameReader creates a new HDLC frame reader.
func NewHDLCFrameReader() *HDLCFrameReader {
	return &HDLCFrameReader{}
}

// Feed adds data to the buffer and returns any complete frames extracted.
func (r *HDLCFrameReader) Feed(data []byte) [][]byte {
	r.buf = append(r.buf, data...)

	var frames [][]byte
	for {
		// Find first FLAG
		start := -1
		for i, b := range r.buf {
			if b == HDLCFlag {
				start = i
				break
			}
		}
		if start < 0 {
			// No flag found — discard everything
			r.buf = r.buf[:0]
			break
		}

		// Find second FLAG after start
		end := -1
		for i := start + 1; i < len(r.buf); i++ {
			if r.buf[i] == HDLCFlag {
				end = i
				break
			}
		}
		if end < 0 {
			// Only one flag — trim before it and wait for more data
			r.buf = r.buf[start:]
			break
		}

		// Extract frame between the two flags
		escaped := r.buf[start+1 : end]
		if len(escaped) >= HeaderMinSize {
			frame := HDLCUnescape(escaped)
			frames = append(frames, frame)
		}

		// Move past the end flag (it can also be the start of the next frame)
		r.buf = r.buf[end:]
	}

	return frames
}
