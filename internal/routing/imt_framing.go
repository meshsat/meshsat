package routing

// IMT framing for Reticulum packets, byte-compatible with CrossTalk's
// IridiumIMTInterface (IridiumIMTCodec): every Iridium Messaging Transport
// message carrying a Reticulum packet is "RNSI" + version 0x01 + the raw
// packet. Off by default on the kits (they exchange bare packets); when a
// peer runs CrossTalk on a RockBLOCK 9704 the operator turns it on in
// Settings > Routing. Receive auto-detects either way. [MESHSAT-1351]

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"sync"
	"time"

	"meshsat/internal/reticulum"
)

// IMTFrameMagic and IMTFrameVersion form CrossTalk's 5-byte header.
const (
	IMTFrameMagic   = "RNSI"
	IMTFrameVersion = 0x01
	imtFrameHdrLen  = 5
)

var (
	// IMTFrameHeader is the exact 5 bytes CrossTalk prepends.
	IMTFrameHeader = []byte{'R', 'N', 'S', 'I', IMTFrameVersion}

	ErrIMTFrameVersion = errors.New("imt frame: unsupported version")
	ErrIMTFrameEmpty   = errors.New("imt frame: no packet after the header")
)

// EncodeIMTFrame prepends the header (IridiumIMTCodec.encode).
func EncodeIMTFrame(packet []byte) []byte {
	out := make([]byte, 0, imtFrameHdrLen+len(packet))
	out = append(out, IMTFrameHeader...)
	return append(out, packet...)
}

// DecodeIMTFrame strips the header. With strict set (framing on) a message
// without the magic is an error, as in IridiumIMTCodec.decode; otherwise a
// message without the magic is returned unchanged (framed=false), and a
// message WITH the magic is only unwrapped when the remainder parses as a
// Reticulum packet, so a bare packet that happens to start with "RNSI"
// cannot be misread.
func DecodeIMTFrame(data []byte, strict bool) (packet []byte, framed bool, err error) {
	if len(data) < len(IMTFrameMagic) || !bytes.HasPrefix(data, []byte(IMTFrameMagic)) {
		if strict {
			return nil, false, errors.New("imt frame: missing RNSI header")
		}
		return data, false, nil
	}
	if len(data) < imtFrameHdrLen {
		return nil, true, ErrIMTFrameEmpty
	}
	if data[4] != IMTFrameVersion {
		return nil, true, ErrIMTFrameVersion
	}
	rest := data[imtFrameHdrLen:]
	if len(rest) == 0 {
		return nil, true, ErrIMTFrameEmpty
	}
	if !strict {
		if _, perr := reticulum.UnmarshalHeader(rest); perr != nil {
			return data, false, nil
		}
	}
	return rest, true, nil
}

// imtDedup drops repeats of a message within a window: CrossTalk keeps
// resending an MO every 30 s until the modem acknowledges it, and the
// network can deliver a late acknowledgement's copy again.
type imtDedup struct {
	mu   sync.Mutex
	seen map[[32]byte]time.Time
	ttl  time.Duration
	max  int
}

func newIMTDedup(ttl time.Duration, max int) *imtDedup {
	return &imtDedup{seen: make(map[[32]byte]time.Time), ttl: ttl, max: max}
}

// Seen reports whether data was delivered within the window and records it.
func (d *imtDedup) Seen(data []byte, now time.Time) bool {
	h := sha256.Sum256(data)
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, t := range d.seen {
		if now.Sub(t) > d.ttl {
			delete(d.seen, k)
		}
	}
	if _, ok := d.seen[h]; ok {
		return true
	}
	if len(d.seen) >= d.max {
		var oldestK [32]byte
		var oldestT time.Time
		first := true
		for k, t := range d.seen {
			if first || t.Before(oldestT) {
				oldestK, oldestT, first = k, t, false
			}
		}
		delete(d.seen, oldestK)
	}
	d.seen[h] = now
	return false
}
