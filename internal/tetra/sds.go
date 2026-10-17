// Package tetra speaks a TETRA radio's Peripheral Equipment Interface (PEI,
// ETSI EN 300 392-5): the AT commands that send and receive SDS (Short Data
// Service) messages through a Motorola MTP3550 on USB. Pure Go, no CGO.
//
// The texts are SDS-TL text messages (ETSI TS 100 392-2 clause 29), the kind
// every TETRA handheld shows on its screen, so a MeshSat kit and a plain
// radio read each other. Verified on two MTP3550s in Direct Mode on
// 17 Oct 2026 (MESHSAT-1588). [MESHSAT-1588]
package tetra

import (
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Protocol identifiers of the text services (TS 100 392-2 table 29.21).
const (
	PIDSimpleText          byte = 0x02 // simple text, no SDS-TL header
	PIDSimpleImmediateText byte = 0x09 // simple text shown at once
	PIDText                byte = 0x82 // text messaging over SDS-TL
	PIDImmediateText       byte = 0x89 // text over SDS-TL, shown at once
)

// Text coding schemes (TS 100 392-2 table 29.29) this package reads.
const (
	CodingLatin1  byte = 0x01 // ISO 8859-1
	CodingGreek   byte = 0x07 // ISO 8859-7
	CodingUTF16BE byte = 0x1A // UCS-2 / UTF-16 big endian
)

// SDS-TL message types (high nibble of the byte after the PID).
const (
	sdsTransfer byte = 0x0
	sdsReport   byte = 0x1
	sdsAck      byte = 0x2
)

// headerLen is what an SDS-TL text adds in front of the text: PID, the
// transfer byte, the message reference and the text header.
const headerLen = 4

// EncodeText builds one SDS-TL text transfer (PID 0x82) as the radio sends
// it: no delivery report asked, no timestamp. Latin-1 when every character
// has one, else UTF-16 (two bytes a character). Returns the PDU and its
// length in bits for AT+CMGS.
func EncodeText(ref byte, text string) ([]byte, int) {
	coding, body := encodeBody(text)
	pdu := make([]byte, 0, headerLen+len(body))
	// Transfer byte: message type SDS-TRANSFER, no delivery report, the
	// "service selection / short form report" bit set, as ftl/tetra-pei
	// builds it and as the MTP3550 accepted on 17 Oct 2026.
	pdu = append(pdu, PIDText, sdsTransfer<<4|0x02, ref, coding)
	pdu = append(pdu, body...)
	return pdu, len(pdu) * 8
}

// TextBytes is how many bytes text takes in an SDS-TL text, header included.
func TextBytes(text string) int {
	_, body := encodeBody(text)
	return headerLen + len(body)
}

func encodeBody(text string) (byte, []byte) {
	latin := make([]byte, 0, len(text))
	for _, r := range text {
		if r > 0xFF {
			latin = nil
			break
		}
		latin = append(latin, byte(r))
	}
	if latin != nil {
		return CodingLatin1, latin
	}
	units := utf16.Encode([]rune(text))
	out := make([]byte, 0, len(units)*2)
	for _, u := range units {
		out = append(out, byte(u>>8), byte(u))
	}
	return CodingUTF16BE, out
}

// Received is one SDS the radio handed to the PEI (+CTSDSR plus its PDU).
type Received struct {
	Service  int    // AI service: 12 = SDS type 4
	From     string // calling party identity (ISSI or TSI)
	To       string // called party identity: a group (GSSI) or this radio
	Bits     int    // PDU length in bits as the radio reported it
	PDU      []byte // the user data
	PID      byte
	IsText   bool // a text message (PIDs 0x02, 0x09, 0x82, 0x89)
	IsReport bool // an SDS-TL report or ack, not a message
	Ref      byte // SDS-TL message reference (0 for simple text)
	// DeliveryStatus is a report's or ack's delivery status (TS 100 392-2
	// table 29.16: 0x00 received, 0x02 consumed, ...); 0 for a message.
	DeliveryStatus byte
	Text           string // the words, with the radio's appended identity removed
	SenderID       string // the identity the radio appended to the text, if any
	Coding         byte
}

var ctsdsrRe = regexp.MustCompile(`^\+CTSDSR:\s*(.*)$`)

// ParseCTSDSR reads the header line of an incoming SDS:
// +CTSDSR: <AI service>,<calling party>,<calling type>,<called party>,<called type>,<length>[,...]
func ParseCTSDSR(line string) (Received, error) {
	m := ctsdsrRe.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return Received{}, fmt.Errorf("tetra: not a +CTSDSR line: %q", line)
	}
	f := strings.Split(m[1], ",")
	for i := range f {
		f[i] = strings.TrimSpace(f[i])
	}
	if len(f) < 6 {
		return Received{}, fmt.Errorf("tetra: +CTSDSR with %d fields: %q", len(f), line)
	}
	svc, err := strconv.Atoi(f[0])
	if err != nil {
		return Received{}, fmt.Errorf("tetra: +CTSDSR service %q: %w", f[0], err)
	}
	bits, err := strconv.Atoi(f[5])
	if err != nil {
		return Received{}, fmt.Errorf("tetra: +CTSDSR length %q: %w", f[5], err)
	}
	return Received{Service: svc, From: trimZeros(f[1]), To: trimZeros(f[3]), Bits: bits}, nil
}

func trimZeros(id string) string {
	t := strings.TrimLeft(id, "0")
	if t == "" && id != "" {
		return "0"
	}
	return t
}

// DecodePDU fills r from the hex line that follows +CTSDSR.
func (r *Received) DecodePDU(hexLine string) error {
	pdu, err := hex.DecodeString(strings.TrimSpace(hexLine))
	if err != nil {
		return fmt.Errorf("tetra: SDS user data is not hex: %w", err)
	}
	if r.Bits > 0 && r.Bits/8 < len(pdu) {
		pdu = pdu[:(r.Bits+7)/8]
	}
	r.PDU = pdu
	if len(pdu) == 0 {
		return errors.New("tetra: empty SDS")
	}
	r.PID = pdu[0]
	var body []byte
	switch r.PID {
	case PIDText, PIDImmediateText:
		if len(pdu) < 3 {
			return fmt.Errorf("tetra: SDS-TL text of %d bytes", len(pdu))
		}
		switch pdu[1] >> 4 {
		case sdsTransfer:
		case sdsReport, sdsAck:
			// SDS-REPORT and SDS-ACK: PID, type and flags, delivery
			// status, then the reference of the message they answer.
			r.IsReport = true
			if len(pdu) < 4 {
				return fmt.Errorf("tetra: SDS-TL report of %d bytes", len(pdu))
			}
			r.DeliveryStatus = pdu[2]
			r.Ref = pdu[3]
			return nil
		default:
			return fmt.Errorf("tetra: SDS-TL message type %d", pdu[1]>>4)
		}
		r.Ref = pdu[2]
		if len(pdu) < 4 {
			return errors.New("tetra: SDS-TL text without a text header")
		}
		body = pdu[3:]
	case PIDSimpleText, PIDSimpleImmediateText:
		if len(pdu) < 2 {
			return errors.New("tetra: simple text without a text header")
		}
		body = pdu[1:]
	default:
		return nil // not a text: the caller sees PID and PDU
	}
	r.IsText = true
	hdr := body[0]
	body = body[1:]
	if hdr&0x80 != 0 { // a timestamp follows the header
		if len(body) < 3 {
			return errors.New("tetra: text timestamp cut short")
		}
		body = body[3:]
	}
	r.Coding = hdr & 0x7F
	body, r.SenderID = splitSenderIDBytes(body)
	text, id := splitSenderID(decodeBody(r.Coding, body))
	if id != "" && r.SenderID == "" {
		r.SenderID = id
	}
	r.Text = text
	return nil
}

// The MTP3550 adds CR CR and the sender's ITSI to every text it hands over
// in Direct Mode (17 Oct 2026: 352 bits sent arrived as 496, the extra 18
// bytes "\r\r7530236100000100"). It is the radio's, not the sender's words.
var senderIDRe = regexp.MustCompile(`\r\r([0-9]{1,17})$`)

// splitSenderIDBytes takes the identity off the raw bytes, where the radio
// puts it as plain ASCII whatever the text's coding: on a UTF-16 text an
// ASCII tail would otherwise decode as nine stray characters.
func splitSenderIDBytes(b []byte) ([]byte, string) {
	if m := senderIDBytesRe.FindSubmatchIndex(b); m != nil {
		return b[:m[0]], string(b[m[2]:m[3]])
	}
	return b, ""
}

var senderIDBytesRe = regexp.MustCompile(`\r\r([0-9]{1,17})$`)

func splitSenderID(text string) (string, string) {
	if m := senderIDRe.FindStringSubmatchIndex(text); m != nil {
		return text[:m[0]], text[m[2]:m[3]]
	}
	return text, ""
}

func decodeBody(coding byte, b []byte) string {
	switch coding {
	case CodingUTF16BE:
		// The identity the radio appends may come as plain bytes after
		// the UTF-16: keep an odd tail apart.
		n := len(b) &^ 1
		units := make([]uint16, 0, n/2)
		for i := 0; i < n; i += 2 {
			units = append(units, uint16(b[i])<<8|uint16(b[i+1]))
		}
		s := string(utf16.Decode(units))
		if n < len(b) {
			s += latin1(b[n:])
		}
		return s
	case CodingGreek:
		var sb strings.Builder
		for _, c := range b {
			sb.WriteRune(greekRune(c))
		}
		return sb.String()
	default:
		// Latin-1, and the 8-bit codings read as Latin-1: their ASCII half
		// is the same, which is what a kit relays.
		return latin1(b)
	}
}

func latin1(b []byte) string {
	rs := make([]rune, len(b))
	for i, c := range b {
		rs[i] = rune(c)
	}
	return string(rs)
}

// greekRune maps an ISO 8859-7 byte to Unicode.
func greekRune(c byte) rune {
	switch {
	case c < 0xA0:
		return rune(c)
	case c == 0xA1:
		return '‘'
	case c == 0xA2:
		return '’'
	case c == 0xA4:
		return '€'
	case c == 0xA5:
		return '₯'
	case c == 0xAA:
		return 'ͺ'
	case c == 0xAF:
		return '―'
	case c == 0xB7, c == 0xBB, c == 0xBD:
		return rune(c)
	case c >= 0xB4 && c != 0xD2 && c != 0xFF:
		return rune(c) + 0x2D0
	case c < 0xB4:
		return rune(c)
	}
	return '�'
}

// ParseAddress reads an SDS destination: "tetra:2000" or "2000", an SSI
// (1-16777214) or a full TSI (MCC, MNC and SSI, up to 17 digits).
func ParseAddress(s string) (string, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "tetra:")
	if s == "" || len(s) > 17 {
		return "", false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	if len(s) <= 8 {
		n, _ := strconv.Atoi(s)
		if n < 1 || n > 16777214 {
			return "", false
		}
		return strconv.Itoa(n), true
	}
	return s, true
}

// Address is the delivery ledger's form of an SDS address.
func Address(ssi string) string { return "tetra:" + ssi }

// SSIOf is the SSI part (the last eight digits) of a 15- or 16-digit
// ITSI/GTSI as ATI prints it ("7530236100002000" -> "2000").
func SSIOf(tsi string) string {
	tsi = strings.TrimSpace(tsi)
	if len(tsi) > 8 {
		tsi = tsi[len(tsi)-8:]
	}
	return trimZeros(tsi)
}
