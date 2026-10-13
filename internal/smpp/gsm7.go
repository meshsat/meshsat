package smpp

import (
	"fmt"
	"unicode/utf16"
)

// GSM 03.38 default alphabet: septet value to rune. osmo-msc hands the
// ESME these values one per byte (gsm_7bit_expand does no character
// conversion) and packs what the ESME sends the same way, so '@' is 0x00
// on the wire, 'è' is 0x04 and '[' is the escape 0x1B followed by 0x3C.
var gsm7Basic = [128]rune{
	'@', '£', '$', '¥', 'è', 'é', 'ù', 'ì', 'ò', 'Ç', '\n', 'Ø', 'ø', '\r', 'Å', 'å',
	'Δ', '_', 'Φ', 'Γ', 'Λ', 'Ω', 'Π', 'Ψ', 'Σ', 'Θ', 'Ξ', gsm7Escape, 'Æ', 'æ', 'ß', 'É',
	' ', '!', '"', '#', '¤', '%', '&', '\'', '(', ')', '*', '+', ',', '-', '.', '/',
	'0', '1', '2', '3', '4', '5', '6', '7', '8', '9', ':', ';', '<', '=', '>', '?',
	'¡', 'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'I', 'J', 'K', 'L', 'M', 'N', 'O',
	'P', 'Q', 'R', 'S', 'T', 'U', 'V', 'W', 'X', 'Y', 'Z', 'Ä', 'Ö', 'Ñ', 'Ü', '§',
	'¿', 'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j', 'k', 'l', 'm', 'n', 'o',
	'p', 'q', 'r', 's', 't', 'u', 'v', 'w', 'x', 'y', 'z', 'ä', 'ö', 'ñ', 'ü', 'à',
}

// gsm7Escape marks the extension table in the basic table (0x1B); the rune
// is a private-use code point that no text contains.
const gsm7Escape rune = 0xE01B

// Extension table (after 0x1B).
var gsm7Ext = map[byte]rune{
	0x0A: '\f', 0x14: '^', 0x28: '{', 0x29: '}', 0x2F: '\\',
	0x3C: '[', 0x3D: '~', 0x3E: ']', 0x40: '|', 0x65: '€',
}

var (
	gsm7BasicIndex = map[rune]byte{}
	gsm7ExtIndex   = map[rune]byte{}
)

func init() {
	for i, r := range gsm7Basic {
		if r != gsm7Escape {
			gsm7BasicIndex[r] = byte(i)
		}
	}
	for b, r := range gsm7Ext {
		gsm7ExtIndex[r] = b
	}
}

// GSM7Septets counts the septets text takes in the GSM 03.38 alphabet
// (an extension character costs two) and reports whether every character
// is in the alphabet.
func GSM7Septets(text string) (int, bool) {
	n := 0
	for _, r := range text {
		if _, ok := gsm7BasicIndex[r]; ok {
			n++
			continue
		}
		if _, ok := gsm7ExtIndex[r]; ok {
			n += 2
			continue
		}
		return n, false
	}
	return n, true
}

// IsGSM7 reports whether every character of text is in GSM 03.38.
func IsGSM7(text string) bool {
	_, ok := GSM7Septets(text)
	return ok
}

// EncodeGSM7 turns text into GSM 03.38 septet values, one per byte, the
// form osmo-msc packs on submit_sm with data_coding 0x00.
func EncodeGSM7(text string) ([]byte, error) {
	out := make([]byte, 0, len(text))
	for _, r := range text {
		if b, ok := gsm7BasicIndex[r]; ok {
			out = append(out, b)
			continue
		}
		if b, ok := gsm7ExtIndex[r]; ok {
			out = append(out, 0x1B, b)
			continue
		}
		return nil, fmt.Errorf("smpp: %q is not in the GSM 03.38 alphabet", r)
	}
	return out, nil
}

// DecodeGSM7 turns septet values (one per byte, as osmo-msc delivers
// them with data_coding 0x01) into text. An escape with no known
// extension character after it decodes as a space, as GSM 03.38 says.
func DecodeGSM7(septets []byte) string {
	out := make([]rune, 0, len(septets))
	for i := 0; i < len(septets); i++ {
		b := septets[i] & 0x7F
		if b == 0x1B {
			if i+1 < len(septets) {
				if r, ok := gsm7Ext[septets[i+1]&0x7F]; ok {
					out = append(out, r)
					i++
					continue
				}
			}
			out = append(out, ' ')
			continue
		}
		out = append(out, gsm7Basic[b])
	}
	return string(out)
}

// EncodeUCS2 turns text into UCS-2 (UTF-16 big-endian) bytes.
func EncodeUCS2(text string) []byte {
	units := utf16.Encode([]rune(text))
	out := make([]byte, 0, 2*len(units))
	for _, u := range units {
		out = append(out, byte(u>>8), byte(u))
	}
	return out
}

// DecodeUCS2 turns UCS-2 big-endian bytes into text; a trailing odd byte
// is dropped.
func DecodeUCS2(b []byte) string {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		units = append(units, uint16(b[i])<<8|uint16(b[i+1]))
	}
	return string(utf16.Decode(units))
}

// UCS2Units counts the UTF-16 code units text takes (an emoji is two).
func UCS2Units(text string) int {
	return len(utf16.Encode([]rune(text)))
}

// DecodeText reads a short_message body (after any UDH) by data_coding:
// GSM 03.38 septets for 0x00/0x01 and the 0xF0 class-coded family with
// bit 2 clear, UCS-2 for 0x08, and bytes as they are (Latin-1) for 8-bit
// data, which a report's text is.
func DecodeText(dataCoding byte, body []byte) string {
	switch {
	case dataCoding == DCSDefault || dataCoding == DCSGSM7:
		return DecodeGSM7(body)
	case dataCoding&0xFC == 0xF0 && dataCoding&0x04 == 0: // GSM 03.38 DCS passed through, 7-bit
		return DecodeGSM7(body)
	case dataCoding == DCSUCS2:
		return DecodeUCS2(body)
	default:
		return latin1(body)
	}
}

func latin1(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}
