package codec

// The MeshSat app SMS wire, version 2 (r40 review R40-R1). Version 1 put
// protocol byte 0x01 in front of an SMS that was compressed or encrypted and
// left the receiver to guess the codec, which no rule can do: the SMAZ2 of
// "sara" is the two bytes of "¡". Version 2 says which:
//
//	SMS text = base64( 0x02 | mode | payload )
//	mode 0x00, clear:    payload = codec | body
//	mode 0x01, AES-GCM:  payload = nonce(12) | AES-256-GCM(codec | body) | tag(16)
//	codec 0x00 UTF-8 text, 0x01 SMAZ2 with the default table, 0x02 MSVQ-SC
//
// With a key the codec is inside the ciphertext, so the tag authenticates
// how the body is read. A plain SMS (no key, no compression) stays the typed
// text. A receiver keeps a frame with a mode or codec it does not know as it
// came, and reads nothing into it. Version 1 and unversioned SMS are still
// read by the old rule, whose ambiguity stays: a two-byte SMAZ2 that is valid
// UTF-8 reads as that text. Shared vectors: internal/engine/testdata/
// sms-wire-v2-vectors.json.
const (
	SMSWireV2      byte = 0x02
	SMSModeClear   byte = 0x00
	SMSModeAESGCM  byte = 0x01
	SMSCodecText   byte = 0x00
	SMSCodecSMAZ2  byte = 0x01
	SMSCodecMSVQSC byte = 0x02
)

// SMSWireV2Header is the version and mode bytes in front of a v2 payload.
func SMSWireV2Header(mode byte) []byte { return []byte{SMSWireV2, mode} }

// ParseSMSWireV2 splits a decoded v2 SMS into its mode and payload. ok is
// false for anything that is not one: another first byte, or too short to
// carry a codec byte.
func ParseSMSWireV2(blob []byte) (mode byte, payload []byte, ok bool) {
	if len(blob) < 3 || blob[0] != SMSWireV2 {
		return 0, nil, false
	}
	return blob[1], blob[2:], true
}
