// Package crewchat is the wire form of a dispatcher's chat to a crew when
// the kit is off MQTT (MESHSAT-1744): the Hub sends one compact frame as a
// satellite MT or as base64 in one SMS from its own number, and the kit
// answers the same way. The layouts are the Hub's contract
// (meshsat-hub docs/bridge-hub-protocol.md, "Crew chat over satellite and
// SMS"); the Hub's internal/takchat/frame.go writes and reads the same
// bytes, and the two test suites hold the same vectors.
//
//	C1, Hub to kit:
//	  'C' | flags | message id (16) | crew node (4, BE) | operator hash (8) |
//	  callsign (1 + up to 16) | text (SMAZ2 when flag 0x01, else UTF-8)
//	C2, kit to Hub, the crew's answer (an uplink frame, type 0x43):
//	  4D 53 01 43 | message id (16) | crew node (4) | unix seconds (4) | flags | text
//	C3, kit to Hub, what became of the chat (an uplink frame, type 0x44):
//	  4D 53 01 44 | message id (16) | crew node (4) | unix seconds (4) |
//	  status (1) | reason (1) | detail (1 + up to 80)
package crewchat

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"meshsat/internal/compress"
)

const (
	// MTTypeChat is the first byte of a C1 frame: 'C', outside the compact
	// binary flag values and the warning digest's 'W'.
	MTTypeChat byte = 0x43
	// UplinkTypeAnswer and UplinkTypeStatus are the uplink frame types of
	// C2 and C3, after the uplink header (hubreporter.SatUplinkMagic, 1).
	UplinkTypeAnswer byte = 0x43
	UplinkTypeStatus byte = 0x44

	// C1 flags.
	FlagCompressed byte = 0x01 // the text is SMAZ2 (the Meshtastic table)
	FlagToKit      byte = 0x02 // the crew is the kit itself (uid MESHSAT-<node hex>)
	FlagReplyHere  byte = 0x04 // answer and status on this bearer even with the Hub link up

	// C2 flags.
	AnswerFlagCompressed byte = 0x01

	maxCallsign = 16
	maxDetail   = 80

	// OperatorUIDPrefix is the Hub's prefix for an operator's TAK uid;
	// OperatorHashMarker follows it when the kit knows the operator by hash
	// alone, and the Hub resolves such a uid.
	OperatorUIDPrefix  = "meshsat-hub-op-"
	OperatorHashMarker = "~"
)

// The uplink header's magic and version (hubreporter.SatUplinkMagic,
// satUplinkVersion), written here so the package stands on its own.
var uplinkHeader = [3]byte{0x4D, 0x53, 0x01}

// Status codes and reason codes, the kit's words as one byte each.
const (
	StatusCodeQueued      byte = 1
	StatusCodeDelivered   byte = 2
	StatusCodeFailed      byte = 3
	StatusCodeUndelivered byte = 4
)

var statusCodes = map[string]byte{"queued": StatusCodeQueued, "delivered": StatusCodeDelivered,
	"failed": StatusCodeFailed, "undelivered": StatusCodeUndelivered}

var reasonCodes = map[string]byte{"not_heard": 1, "ambiguous_callsign": 2, "no_radio": 3, "no_text": 4,
	"no_sender": 5, "own_chat": 6, "send_failed": 7}

// Frame errors.
var (
	ErrShort = errors.New("crewchat: frame too short")
	ErrType  = errors.New("crewchat: not a crew chat frame")
	ErrText  = errors.New("crewchat: the frame's text does not decompress")
	ErrID    = errors.New("crewchat: the message id is not a UUID")
)

// Chat is a C1 frame's content.
type Chat struct {
	MessageID    string // the GeoChat's message id, canonical UUID text
	Node         uint32 // the crew's mesh node id, or the kit's own
	ToKit        bool   // the crew is the kit itself
	ReplyHere    bool   // answer and status on the bearer the chat came on
	OperatorHash [8]byte
	Callsign     string // the operator's name as the crew sees it
	Text         string
}

// OperatorUID is the uid the kit writes for the operator a frame names: the
// Hub's prefix, the marker, the hash as 16 hex digits.
func (c Chat) OperatorUID() string {
	return OperatorUIDPrefix + OperatorHashMarker + hex.EncodeToString(c.OperatorHash[:])
}

// OperatorHash is the eight bytes a frame carries for an operator's uid.
func OperatorHash(uid string) [8]byte {
	sum := sha256.Sum256([]byte(uid))
	var h [8]byte
	copy(h[:], sum[:8])
	return h
}

// IsChat reports whether data starts like a C1 frame.
func IsChat(data []byte) bool {
	return len(data) >= 31 && data[0] == MTTypeChat
}

// EncodeChat writes a C1 frame (the Hub's job; here for the tests and the
// vectors). The text goes SMAZ2 when that is shorter.
func EncodeChat(c Chat) ([]byte, error) {
	id, ok := UUIDBytes(c.MessageID)
	if !ok {
		return nil, ErrID
	}
	flags := byte(0)
	if c.ToKit {
		flags |= FlagToKit
	}
	if c.ReplyHere {
		flags |= FlagReplyHere
	}
	text, packed := pack(c.Text)
	if packed {
		flags |= FlagCompressed
	}
	callsign := cutRunes(c.Callsign, maxCallsign)
	out := make([]byte, 0, 31+len(callsign)+len(text))
	out = append(out, MTTypeChat, flags)
	out = append(out, id[:]...)
	out = binary.BigEndian.AppendUint32(out, c.Node)
	out = append(out, c.OperatorHash[:]...)
	out = append(out, byte(len(callsign))) // #nosec G115 -- cut to maxCallsign above
	out = append(out, callsign...)
	out = append(out, text...)
	return out, nil
}

// DecodeChat reads a C1 frame.
func DecodeChat(data []byte) (Chat, error) {
	var c Chat
	if len(data) < 31 {
		return c, ErrShort
	}
	if data[0] != MTTypeChat {
		return c, ErrType
	}
	flags := data[1]
	var id [16]byte
	copy(id[:], data[2:18])
	c.MessageID = UUIDText(id)
	c.Node = binary.BigEndian.Uint32(data[18:22])
	copy(c.OperatorHash[:], data[22:30])
	c.ToKit = flags&FlagToKit != 0
	c.ReplyHere = flags&FlagReplyHere != 0
	n := int(data[30])
	if n > maxCallsign || len(data) < 31+n {
		return c, ErrShort
	}
	c.Callsign = string(data[31 : 31+n])
	text, err := unpack(data[31+n:], flags&FlagCompressed != 0)
	if err != nil {
		return c, err
	}
	c.Text = text
	return c, nil
}

// Answer is a C2 frame's content: the crew's words on one question.
type Answer struct {
	MessageID string // the question's
	Node      uint32
	At        time.Time
	Text      string
}

// EncodeAnswer writes a whole C2 uplink frame, header included.
func EncodeAnswer(a Answer) ([]byte, error) {
	id, ok := UUIDBytes(a.MessageID)
	if !ok {
		return nil, ErrID
	}
	text, packed := pack(a.Text)
	flags := byte(0)
	if packed {
		flags = AnswerFlagCompressed
	}
	out := make([]byte, 0, 4+16+4+4+1+len(text))
	out = append(out, uplinkHeader[:]...)
	out = append(out, UplinkTypeAnswer)
	out = append(out, id[:]...)
	out = binary.BigEndian.AppendUint32(out, a.Node)
	out = binary.BigEndian.AppendUint32(out, uint32(a.At.Unix())) // #nosec G115 -- unix seconds until 2106
	out = append(out, flags)
	out = append(out, text...)
	return out, nil
}

// DecodeAnswer reads a C2 frame's payload, after the uplink header.
func DecodeAnswer(payload []byte) (Answer, error) {
	var a Answer
	if len(payload) < 25 {
		return a, ErrShort
	}
	var id [16]byte
	copy(id[:], payload[:16])
	a.MessageID = UUIDText(id)
	a.Node = binary.BigEndian.Uint32(payload[16:20])
	a.At = time.Unix(int64(binary.BigEndian.Uint32(payload[20:24])), 0).UTC()
	text, err := unpack(payload[25:], payload[24]&AnswerFlagCompressed != 0)
	if err != nil {
		return a, err
	}
	a.Text = text
	return a, nil
}

// Status is a C3 frame's content: the kit's word on one chat.
type Status struct {
	MessageID string
	Node      uint32
	At        time.Time
	Status    string // queued, delivered, failed, undelivered
	Reason    string // one machine word, "" for none
	Detail    string
}

// EncodeStatus writes a whole C3 uplink frame, header included.
func EncodeStatus(s Status) ([]byte, error) {
	id, ok := UUIDBytes(s.MessageID)
	if !ok {
		return nil, ErrID
	}
	code, ok := statusCodes[s.Status]
	if !ok {
		return nil, fmt.Errorf("crewchat: status %q is not one of the four words", s.Status)
	}
	detail := cutRunes(s.Detail, maxDetail)
	out := make([]byte, 0, 4+16+4+4+3+len(detail))
	out = append(out, uplinkHeader[:]...)
	out = append(out, UplinkTypeStatus)
	out = append(out, id[:]...)
	out = binary.BigEndian.AppendUint32(out, s.Node)
	out = binary.BigEndian.AppendUint32(out, uint32(s.At.Unix()))     // #nosec G115 -- unix seconds until 2106
	out = append(out, code, reasonCodes[s.Reason], byte(len(detail))) // #nosec G115 -- cut to maxDetail above
	out = append(out, detail...)
	return out, nil
}

// DecodeStatus reads a C3 frame's payload, after the uplink header.
func DecodeStatus(payload []byte) (Status, error) {
	var s Status
	if len(payload) < 27 {
		return s, ErrShort
	}
	var id [16]byte
	copy(id[:], payload[:16])
	s.MessageID = UUIDText(id)
	s.Node = binary.BigEndian.Uint32(payload[16:20])
	s.At = time.Unix(int64(binary.BigEndian.Uint32(payload[20:24])), 0).UTC()
	for word, code := range statusCodes {
		if code == payload[24] {
			s.Status = word
		}
	}
	if s.Status == "" {
		return s, fmt.Errorf("crewchat: status code %d is not one of the four", payload[24])
	}
	for word, code := range reasonCodes {
		if code == payload[25] {
			s.Reason = word
		}
	}
	n := int(payload[26])
	if n > maxDetail || len(payload) < 27+n {
		return s, ErrShort
	}
	if !utf8.Valid(payload[27 : 27+n]) {
		return s, ErrText
	}
	s.Detail = string(payload[27 : 27+n])
	return s, nil
}

// pack compresses text with the Meshtastic SMAZ2 table when that is shorter
// and reads back exactly.
func pack(text string) ([]byte, bool) {
	raw := []byte(text)
	packed := compress.Compress(raw, compress.DictMeshtastic)
	if len(packed) == 0 || len(packed) >= len(raw) {
		return raw, false
	}
	if back, err := compress.Decompress(packed, compress.DictMeshtastic); err != nil || string(back) != text {
		return raw, false
	}
	return packed, true
}

func unpack(data []byte, packed bool) (string, error) {
	if packed {
		plain, err := compress.Decompress(data, compress.DictMeshtastic)
		if err != nil {
			return "", ErrText
		}
		data = plain
	}
	if !utf8.Valid(data) {
		return "", ErrText
	}
	return string(data), nil
}

func cutRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// UUIDBytes reads a canonical UUID; ok is false for anything else.
func UUIDBytes(s string) (b [16]byte, ok bool) {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return b, false
	}
	h := strings.ReplaceAll(s, "-", "")
	raw, err := hex.DecodeString(h)
	if err != nil || len(raw) != 16 {
		return b, false
	}
	copy(b[:], raw)
	return b, true
}

// UUIDText writes a UUID in its canonical form.
func UUIDText(b [16]byte) string {
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
