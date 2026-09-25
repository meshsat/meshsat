// Package hf10m implements the public 10 m HF codec Data Slayer's CrossTalk
// uses for its licensed amateur hop: a plaintext inner frame (radix-40
// callsign, LXMF destination hash, sequence, fragment, UTF-8 text, CRC-16),
// an LDPC(128,64) wrapper with 16-way interleaving, and a 2-CPFSK burst at
// 100 baud with a Costas wake-up, click-track preamble and unique word.
// Everything here is written from the recipe (docs/hf_codec.md) and checked
// against its worked example and a NumPy reference (scripts/hf10m_ref.py).
// Nothing on this hop is a Reticulum packet and nothing is encrypted.
// [MESHSAT-1353]
package hf10m

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Inner-frame constants.
const (
	FrameVersion   = 1
	FrameTypeData  = 0
	FrameHeaderLen = 28 // through LEN
	FrameCRCLen    = 2
	MaxPayload     = 200
	CallsignLen    = 6 // radix-40 packed
	CallsignChars  = 9
	DestHashLen    = 16
)

// Radix40Alphabet is the callsign alphabet, index 0 first (a leading space).
const Radix40Alphabet = " ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/-."

var (
	ErrCallsign   = errors.New("hf10m: callsign has a character outside the radix-40 alphabet or is longer than 9")
	ErrCRC        = errors.New("hf10m: CRC-16 mismatch")
	ErrFrameShort = errors.New("hf10m: frame too short")
	ErrVersion    = errors.New("hf10m: unsupported frame version")
	ErrPayloadLen = errors.New("hf10m: payload longer than 200 bytes or not UTF-8")
	ErrFragment   = errors.New("hf10m: bad fragment nibbles")
)

// PackCallsign encodes a callsign as 6 big-endian radix-40 bytes.
func PackCallsign(call string) ([CallsignLen]byte, error) {
	var out [CallsignLen]byte
	call = strings.ToUpper(strings.TrimSpace(call))
	if len(call) > CallsignChars {
		return out, ErrCallsign
	}
	padded := call + strings.Repeat(" ", CallsignChars-len(call))
	var v uint64
	for _, ch := range padded {
		idx := strings.IndexRune(Radix40Alphabet, ch)
		if idx < 0 {
			return out, ErrCallsign
		}
		v = v*40 + uint64(idx)
	}
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], v)
	copy(out[:], tmp[2:])
	return out, nil
}

// UnpackCallsign decodes 6 radix-40 bytes, trailing spaces trimmed.
func UnpackCallsign(b [CallsignLen]byte) string {
	var tmp [8]byte
	copy(tmp[2:], b[:])
	v := binary.BigEndian.Uint64(tmp[:])
	out := make([]byte, CallsignChars)
	for i := CallsignChars - 1; i >= 0; i-- {
		out[i] = Radix40Alphabet[v%40]
		v /= 40
	}
	return strings.TrimRight(string(out), " ")
}

// CRC16 is CRC-16-CCITT: poly 0x1021, init 0xFFFF, no reflection, xorout 0.
func CRC16(body []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range body {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// Frame is the plaintext inner frame.
type Frame struct {
	Version   uint8
	Type      uint8
	Flags     uint8
	Origin    string            // callsign
	Dest      [DestHashLen]byte // LXMF delivery hash
	MsgID     uint16
	FragIndex uint8 // 0-based
	FragTotal uint8 // 1 = whole message
	Payload   []byte
}

// Marshal packs the frame with its CRC.
func (f *Frame) Marshal() ([]byte, error) {
	if len(f.Payload) > MaxPayload || !utf8.Valid(f.Payload) {
		return nil, ErrPayloadLen
	}
	if f.FragTotal == 0 || f.FragTotal > 15 || f.FragIndex >= f.FragTotal {
		return nil, ErrFragment
	}
	call, err := PackCallsign(f.Origin)
	if err != nil {
		return nil, err
	}
	ver, typ := f.Version, f.Type
	if ver == 0 {
		ver = FrameVersion
	}
	out := make([]byte, 0, FrameHeaderLen+len(f.Payload)+FrameCRCLen)
	out = append(out, ver<<4|typ&0x0F, f.Flags)
	out = append(out, call[:]...)
	out = append(out, f.Dest[:]...)
	out = binary.BigEndian.AppendUint16(out, f.MsgID)
	out = append(out, f.FragIndex<<4|f.FragTotal&0x0F, byte(len(f.Payload)))
	out = append(out, f.Payload...)
	out = binary.BigEndian.AppendUint16(out, CRC16(out))
	return out, nil
}

// Unmarshal parses and verifies a frame (trailing zero padding tolerated,
// because LDPC blocks pad the bit stream to a multiple of 64).
func Unmarshal(b []byte) (*Frame, error) {
	if len(b) < FrameHeaderLen+FrameCRCLen {
		return nil, ErrFrameShort
	}
	n := int(b[27])
	end := FrameHeaderLen + n + FrameCRCLen
	if len(b) < end {
		return nil, ErrFrameShort
	}
	body := b[:end-FrameCRCLen]
	if CRC16(body) != binary.BigEndian.Uint16(b[end-FrameCRCLen:end]) {
		return nil, ErrCRC
	}
	f := &Frame{Version: b[0] >> 4, Type: b[0] & 0x0F, Flags: b[1]}
	if f.Version != FrameVersion {
		return nil, fmt.Errorf("%w: %d", ErrVersion, f.Version)
	}
	var call [CallsignLen]byte
	copy(call[:], b[2:8])
	f.Origin = UnpackCallsign(call)
	copy(f.Dest[:], b[8:24])
	f.MsgID = binary.BigEndian.Uint16(b[24:26])
	f.FragIndex, f.FragTotal = b[26]>>4, b[26]&0x0F
	if f.FragTotal == 0 || f.FragIndex >= f.FragTotal {
		return nil, ErrFragment
	}
	f.Payload = append([]byte(nil), b[FrameHeaderLen:FrameHeaderLen+n]...)
	if !utf8.Valid(f.Payload) {
		return nil, ErrPayloadLen
	}
	return f, nil
}

// Fragment splits text into frames of at most MaxPayload bytes on UTF-8
// boundaries, at most 15 fragments.
func Fragment(origin string, dest [DestHashLen]byte, msgID uint16, text string) ([]*Frame, error) {
	if !utf8.ValidString(text) {
		return nil, ErrPayloadLen
	}
	var parts [][]byte
	rest := []byte(text)
	for len(rest) > 0 {
		n := len(rest)
		if n > MaxPayload {
			n = MaxPayload
			for n > 0 && !utf8.RuneStart(rest[n]) {
				n--
			}
		}
		parts = append(parts, rest[:n])
		rest = rest[n:]
	}
	if len(parts) == 0 {
		parts = [][]byte{{}}
	}
	if len(parts) > 15 {
		return nil, fmt.Errorf("hf10m: text needs %d fragments, at most 15 allowed", len(parts))
	}
	out := make([]*Frame, len(parts))
	for i, p := range parts {
		out[i] = &Frame{Origin: origin, Dest: dest, MsgID: msgID, FragIndex: uint8(i), FragTotal: uint8(len(parts)), Payload: p}
	}
	return out, nil
}

// Reassembler joins fragments of one (origin, msg id) into the full text.
type Reassembler struct {
	parts map[string][][]byte
}

// NewReassembler creates an empty reassembler.
func NewReassembler() *Reassembler { return &Reassembler{parts: make(map[string][][]byte)} }

// Add stores a fragment and returns the complete text once every fragment
// of the message has been seen.
func (r *Reassembler) Add(f *Frame) (text string, complete bool) {
	if f.FragTotal <= 1 {
		return string(f.Payload), true
	}
	key := fmt.Sprintf("%s/%d/%d", f.Origin, f.MsgID, f.FragTotal)
	slots := r.parts[key]
	if slots == nil {
		slots = make([][]byte, f.FragTotal)
		r.parts[key] = slots
	}
	slots[f.FragIndex] = append([]byte(nil), f.Payload...)
	var b []byte
	for _, s := range slots {
		if s == nil {
			return "", false
		}
		b = append(b, s...)
	}
	delete(r.parts, key)
	return string(b), true
}
