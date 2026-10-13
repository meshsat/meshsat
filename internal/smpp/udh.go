package smpp

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Concatenation limits (3GPP TS 23.040 9.2.3.24.1 and 9.2.3.24.8).
const (
	// GSM7Single is the septets a single GSM 03.38 part carries.
	GSM7Single = 160
	// GSM7Part is the septets a concatenated part carries after the 6-byte
	// 8-bit-reference header (48 bits round up to 7 septets).
	GSM7Part = 153
	// UCS2Single is the UCS-2 code units a single part carries.
	UCS2Single = 70
	// UCS2Part is the UCS-2 code units a concatenated part carries.
	UCS2Part = 67
	// OctetSingle is the bytes a single 8-bit part carries.
	OctetSingle = 140
)

// Concat describes a concatenation header found in a short_message.
type Concat struct {
	Ref   uint16
	Total byte
	Seq   byte
}

// SplitUDH separates the user data header from the body of a short_message
// whose esm_class has the UDHI bit. The body follows the header byte for
// byte: for data_coding 0x00/0x01 osmo-msc has already expanded the
// septets past the header's padding, so they start right after it.
func SplitUDH(sm []byte) (udh, body []byte, err error) {
	if len(sm) == 0 {
		return nil, nil, errors.New("smpp: UDHI set on an empty short_message")
	}
	n := int(sm[0])
	if 1+n > len(sm) {
		return nil, nil, fmt.Errorf("smpp: UDH length %d exceeds the short_message (%d bytes)", n, len(sm))
	}
	return sm[1 : 1+n], sm[1+n:], nil
}

// ParseConcat finds the concatenation element (IE 0x00 with an 8-bit
// reference, IE 0x08 with a 16-bit one) in a user data header. ok is
// false when the header has none.
func ParseConcat(udh []byte) (c Concat, ok bool) {
	for i := 0; i+1 < len(udh); {
		id, n := udh[i], int(udh[i+1])
		if i+2+n > len(udh) {
			return Concat{}, false
		}
		v := udh[i+2 : i+2+n]
		switch {
		case id == 0x00 && n == 3:
			return Concat{Ref: uint16(v[0]), Total: v[1], Seq: v[2]}, v[1] > 0 && v[2] > 0 && v[2] <= v[1]
		case id == 0x08 && n == 4:
			return Concat{Ref: uint16(v[0])<<8 | uint16(v[1]), Total: v[2], Seq: v[3]}, v[2] > 0 && v[3] > 0 && v[3] <= v[2]
		}
		i += 2 + n
	}
	return Concat{}, false
}

// ConcatUDH builds the 6-byte header (with its length byte) for part seq
// of total under an 8-bit reference.
func ConcatUDH(ref byte, total, seq byte) []byte {
	return []byte{0x05, 0x00, 0x03, ref, total, seq}
}

// Part is one submit_sm-sized piece of a text.
type Part struct {
	DataCoding   byte
	ShortMessage []byte // UDH (if any) followed by the body
	UDH          bool
}

// SplitText turns a text into the parts a submit_sm can carry: one part
// in GSM 03.38 (160 septets) or UCS-2 (70 units) when it fits, else a
// concatenated series (153 septets or 67 units per part) under the
// 8-bit reference ref. An extension character never straddles two parts.
func SplitText(text string, ref byte) ([]Part, error) {
	if text == "" {
		return nil, errors.New("smpp: empty text")
	}
	if septets, ok := GSM7Septets(text); ok {
		enc, err := EncodeGSM7(text)
		if err != nil {
			return nil, err
		}
		if septets <= GSM7Single {
			return []Part{{DataCoding: DCSDefault, ShortMessage: enc}}, nil
		}
		return concat(splitSeptets(enc, GSM7Part), DCSDefault, ref), nil
	}
	units := UCS2Units(text)
	enc := EncodeUCS2(text)
	if units <= UCS2Single {
		return []Part{{DataCoding: DCSUCS2, ShortMessage: enc}}, nil
	}
	return concat(splitUnits(enc, UCS2Part), DCSUCS2, ref), nil
}

// splitSeptets cuts septet values into pieces of at most n septets without
// separating an escape from the character it introduces.
func splitSeptets(enc []byte, n int) [][]byte {
	var out [][]byte
	for len(enc) > 0 {
		end := n
		if end >= len(enc) {
			out = append(out, enc)
			break
		}
		if enc[end-1] == 0x1B {
			end--
		}
		out = append(out, enc[:end])
		enc = enc[end:]
	}
	return out
}

// splitUnits cuts UCS-2 bytes into pieces of at most n code units without
// separating a surrogate pair.
func splitUnits(enc []byte, n int) [][]byte {
	var out [][]byte
	for len(enc) > 0 {
		end := 2 * n
		if end >= len(enc) {
			out = append(out, enc)
			break
		}
		if hi := uint16(enc[end-2])<<8 | uint16(enc[end-1]); hi >= 0xD800 && hi < 0xDC00 {
			end -= 2
		}
		out = append(out, enc[:end])
		enc = enc[end:]
	}
	return out
}

func concat(pieces [][]byte, dcs byte, ref byte) []Part {
	parts := make([]Part, 0, len(pieces))
	for i, p := range pieces {
		sm := append(ConcatUDH(ref, byte(len(pieces)), byte(i+1)), p...)
		parts = append(parts, Part{DataCoding: dcs, ShortMessage: sm, UDH: true})
	}
	return parts
}

// Reassembler collects the parts of concatenated deliveries per sender and
// reference, and forgets a series that does not complete in time.
type Reassembler struct {
	mu      sync.Mutex
	timeout time.Duration
	series  map[string]*partial
	now     func() time.Time
}

type partial struct {
	total   int
	parts   map[int][]byte
	dcs     byte
	started time.Time
}

// NewReassembler makes a reassembler that drops an incomplete series
// after timeout.
func NewReassembler(timeout time.Duration) *Reassembler {
	return &Reassembler{timeout: timeout, series: map[string]*partial{}, now: time.Now}
}

// Add files one part. It returns the whole body once every part is in,
// with done true; before that done is false and body is nil.
func (r *Reassembler) Add(sender string, c Concat, dcs byte, body []byte) (whole []byte, done bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for k, p := range r.series {
		if now.Sub(p.started) > r.timeout {
			delete(r.series, k)
		}
	}
	key := fmt.Sprintf("%s/%d/%d", sender, c.Ref, c.Total)
	p, ok := r.series[key]
	if !ok {
		p = &partial{total: int(c.Total), parts: map[int][]byte{}, dcs: dcs, started: now}
		r.series[key] = p
	}
	p.parts[int(c.Seq)] = append([]byte(nil), body...)
	if len(p.parts) < p.total {
		return nil, false
	}
	for i := 1; i <= p.total; i++ {
		whole = append(whole, p.parts[i]...)
	}
	delete(r.series, key)
	return whole, true
}

// Pending reports how many series wait for parts.
func (r *Reassembler) Pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.series)
}
