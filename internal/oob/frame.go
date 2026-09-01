// Package oob implements MeshSat OOB management frames: short authenticated,
// optionally encrypted commands that ride any bearer the kit has (SMS, APRS,
// Meshtastic text, Iridium MT, Hub MQTT) and are executed from a fixed
// allowlist. Specification: docs/OOB_MANAGEMENT_PROTOCOL.md. [MESHSAT-756]
//
// This package never imports the engine, api, gateway or hubreporter
// packages. Everything the executor needs from them arrives as closures or
// small interfaces in Deps, which keeps the import graph acyclic and the
// codec testable in isolation.
package oob

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// Wire constants. See spec section 3.
const (
	Magic       byte = 0x4F // "O"
	Version     byte = 1
	FlagEnc     byte = 0x01
	FlagReply   byte = 0x02
	FlagNoReply byte = 0x04

	HeaderLen   = 9
	TagLen      = 16
	MinFrameLen = HeaderLen + TagLen
	MaxArgs     = 73
	MaxFrameLen = HeaderLen + MaxArgs + TagLen
	KeyLen      = 32
	NonceLen    = 12

	// Sentinel precedes the base32 text form on every bearer.
	Sentinel = "MS:"

	// MinTextLen and MaxTextLen bound the base32 run after the sentinel:
	// 25 bytes encode to 40 characters, 98 bytes to 157.
	MinTextLen = 40
	MaxTextLen = 157
)

// Direction distinguishes a request from a reply in the nonce.
type Direction byte

const (
	DirRequest Direction = 0
	DirReply   Direction = 1
)

// Role is the side of a key relationship a kit sits on. The issuer of a
// bundle-transported key, or the lower destination hash on the ECDH path,
// is RoleIssuer; the other side is RoleImporter. Both sides use the same
// key, so the role bit in the nonce is what keeps their counters from
// colliding.
type Role byte

const (
	RoleIssuer   Role = 0
	RoleImporter Role = 1
)

// Other returns the role of the remote side.
func (r Role) Other() Role {
	if r == RoleIssuer {
		return RoleImporter
	}
	return RoleIssuer
}

// Frame is a decoded management frame.
type Frame struct {
	Enc     bool
	Reply   bool
	NoReply bool
	PeerID  uint16
	Counter uint32
	Cmd     byte
	Args    []byte // plaintext, len <= MaxArgs
}

// Header is the cheap pre-key view used by the classifier and the drop path.
type Header struct {
	Flags   byte
	PeerID  uint16
	Counter uint32
	Cmd     byte
	BodyLen int
}

// Enc reports whether the body is encrypted.
func (h Header) Enc() bool { return h.Flags&FlagEnc != 0 }

// Reply reports whether the frame is a reply.
func (h Header) Reply() bool { return h.Flags&FlagReply != 0 }

// NoReply reports whether the sender asked for no answer.
func (h Header) NoReply() bool { return h.Flags&FlagNoReply != 0 }

// Version returns the version nibble.
func (h Header) Version() byte { return h.Flags >> 4 }

// Errors returned by the codec. Callers treat every one of them as "not a
// frame for us" and stay silent on the wire.
var (
	ErrTooShort   = errors.New("oob: frame too short")
	ErrTooLong    = errors.New("oob: frame too long")
	ErrBadMagic   = errors.New("oob: bad magic")
	ErrBadVersion = errors.New("oob: unsupported version")
	ErrBadKey     = errors.New("oob: key must be 32 bytes")
	ErrBadPeer    = errors.New("oob: peer id must not be zero")
	ErrBadCounter = errors.New("oob: counter must not be zero")
	ErrArgsLen    = errors.New("oob: args exceed 73 bytes")
	ErrAuth       = errors.New("oob: authentication failed")
	ErrBadText    = errors.New("oob: invalid base32 text")
)

// ParseHeader validates magic, version and length bounds without any
// cryptography. It is the first thing the classifier runs.
func ParseHeader(wire []byte) (Header, error) {
	if len(wire) < MinFrameLen {
		return Header{}, ErrTooShort
	}
	if len(wire) > MaxFrameLen {
		return Header{}, ErrTooLong
	}
	if wire[0] != Magic {
		return Header{}, ErrBadMagic
	}
	h := Header{
		Flags:   wire[1],
		PeerID:  binary.BigEndian.Uint16(wire[2:4]),
		Counter: binary.BigEndian.Uint32(wire[4:8]),
		Cmd:     wire[8],
		BodyLen: len(wire) - HeaderLen - TagLen,
	}
	if h.Version() != Version {
		return Header{}, ErrBadVersion
	}
	return h, nil
}

// Nonce builds the deterministic 12-byte GCM nonce. It is never transmitted:
// the receiver derives senderRole from the peer record and dir from the
// REPLY flag. See spec section 3.
func Nonce(peerID uint16, senderRole Role, dir Direction, counter uint32) [NonceLen]byte {
	var n [NonceLen]byte
	binary.BigEndian.PutUint16(n[0:2], peerID)
	n[2] = byte(senderRole)<<1 | byte(dir)
	binary.BigEndian.PutUint32(n[3:7], counter)
	return n
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeyLen {
		return nil, ErrBadKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal builds the wire form of f under key. senderRole is the role of the
// kit that is sending (this side).
func Seal(f Frame, key []byte, senderRole Role) ([]byte, error) {
	if f.PeerID == 0 {
		return nil, ErrBadPeer
	}
	if f.Counter == 0 {
		return nil, ErrBadCounter
	}
	if len(f.Args) > MaxArgs {
		return nil, ErrArgsLen
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}

	hdr := make([]byte, HeaderLen)
	hdr[0] = Magic
	flags := Version << 4
	if f.Enc {
		flags |= FlagEnc
	}
	dir := DirRequest
	if f.Reply {
		flags |= FlagReply
		dir = DirReply
	}
	if f.NoReply {
		flags |= FlagNoReply
	}
	hdr[1] = flags
	binary.BigEndian.PutUint16(hdr[2:4], f.PeerID)
	binary.BigEndian.PutUint32(hdr[4:8], f.Counter)
	hdr[8] = f.Cmd

	nonce := Nonce(f.PeerID, senderRole, dir, f.Counter)
	if f.Enc {
		// ciphertext || tag over the args, header authenticated as AAD.
		return gcm.Seal(hdr, nonce[:], f.Args, hdr), nil
	}
	// Clear body: args ride in the clear and join the AAD; the "ciphertext"
	// is empty so Seal yields the 16-byte tag alone.
	aad := append(append([]byte{}, hdr...), f.Args...)
	tag := gcm.Seal(nil, nonce[:], nil, aad)
	out := append(append([]byte{}, hdr...), f.Args...)
	return append(out, tag...), nil
}

// Open verifies and decodes wire under key. senderRole is the role of the
// REMOTE side that produced the frame.
func Open(wire []byte, key []byte, senderRole Role) (Frame, error) {
	h, err := ParseHeader(wire)
	if err != nil {
		return Frame{}, err
	}
	if h.PeerID == 0 {
		return Frame{}, ErrBadPeer
	}
	if h.Counter == 0 {
		return Frame{}, ErrBadCounter
	}
	gcm, err := newGCM(key)
	if err != nil {
		return Frame{}, err
	}
	dir := DirRequest
	if h.Reply() {
		dir = DirReply
	}
	nonce := Nonce(h.PeerID, senderRole, dir, h.Counter)
	hdr := wire[:HeaderLen]
	f := Frame{
		Enc:     h.Enc(),
		Reply:   h.Reply(),
		NoReply: h.NoReply(),
		PeerID:  h.PeerID,
		Counter: h.Counter,
		Cmd:     h.Cmd,
	}
	if h.Enc() {
		args, err := gcm.Open(nil, nonce[:], wire[HeaderLen:], hdr)
		if err != nil {
			return Frame{}, ErrAuth
		}
		f.Args = args
		return f, nil
	}
	body := wire[HeaderLen : len(wire)-TagLen]
	tag := wire[len(wire)-TagLen:]
	aad := append(append([]byte{}, hdr...), body...)
	if _, err := gcm.Open(nil, nonce[:], tag, aad); err != nil {
		return Frame{}, ErrAuth
	}
	f.Args = append([]byte{}, body...)
	return f, nil
}

// crockford is Douglas Crockford's base32 alphabet: no I, L, O or U, so a
// frame survives being read over a voice channel and typed at the other end.
var crockford = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// Encode returns the text form: sentinel plus Crockford base32.
func Encode(wire []byte) string {
	return Sentinel + crockford.EncodeToString(wire)
}

// normalize folds case and maps the Crockford aliases (I and L to 1, O to 0),
// drops hyphens, and rejects anything outside the alphabet.
func normalize(s string) (string, error) {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			r -= 'a' - 'A'
		}
		switch r {
		case 'I', 'L':
			r = '1'
		case 'O':
			r = '0'
		case '-':
			continue
		case 'U':
			return "", ErrBadText
		}
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
			continue
		}
		return "", ErrBadText
	}
	return b.String(), nil
}

// Decode parses the text form. A leading sentinel (any case) is optional.
func Decode(text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	if len(text) >= len(Sentinel) && strings.EqualFold(text[:len(Sentinel)], Sentinel) {
		text = text[len(Sentinel):]
	}
	norm, err := normalize(text)
	if err != nil {
		return nil, err
	}
	if len(norm) < MinTextLen || len(norm) > MaxTextLen {
		return nil, ErrBadText
	}
	wire, err := crockford.DecodeString(norm)
	if err != nil {
		return nil, ErrBadText
	}
	return wire, nil
}

// isRunChar reports whether r may be part of the base32 run (letters, digits
// and hyphens; normalization decides validity).
func isRunChar(r byte) bool {
	return (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '-'
}

// boundaryBefore reports whether a sentinel at index i sits at a token
// boundary. "SMS:" therefore never matches (previous byte is 'S'), while
// "[APRS:CALL->ADDR] MS:..." does (previous byte is a space).
func boundaryBefore(text string, i int) bool {
	if i == 0 {
		return true
	}
	switch text[i-1] {
	case ' ', '\t', '\n', '\r', ']', '>', ':':
		return true
	}
	return false
}

// ExtractFrame scans text for a management frame and returns its wire form.
// It never returns a partial result: the sentinel must sit at a token
// boundary, the base32 run must be within bounds, and the header must
// parse. Anything else is "not a frame" and the caller falls through to the
// normal message flow untouched.
func ExtractFrame(text string) ([]byte, bool) {
	from := 0
	for {
		idx := indexFold(text, Sentinel, from)
		if idx < 0 {
			return nil, false
		}
		from = idx + 1
		if !boundaryBefore(text, idx) {
			continue
		}
		start := idx + len(Sentinel)
		end := start
		for end < len(text) && isRunChar(text[end]) {
			end++
		}
		run := text[start:end]
		if len(run) < MinTextLen || len(run) > MaxTextLen+len(run)/2 {
			// Too short to be a frame, or absurdly long even allowing for
			// hyphens; keep scanning for a later sentinel.
			continue
		}
		wire, err := Decode(run)
		if err != nil {
			continue
		}
		if _, err := ParseHeader(wire); err != nil {
			continue
		}
		return wire, true
	}
}

// indexFold is a case-insensitive strings.Index starting at from.
func indexFold(s, sub string, from int) int {
	if from >= len(s) {
		return -1
	}
	upper := strings.ToUpper(s[from:])
	i := strings.Index(upper, strings.ToUpper(sub))
	if i < 0 {
		return -1
	}
	return from + i
}

// String renders a frame for logs without its args.
func (f Frame) String() string {
	kind := "request"
	if f.Reply {
		kind = "reply"
	}
	return fmt.Sprintf("oob %s peer=%d ctr=%d cmd=0x%02x enc=%t args=%d", kind, f.PeerID, f.Counter, f.Cmd, f.Enc, len(f.Args))
}
