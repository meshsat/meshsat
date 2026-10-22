package engine

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"meshsat/internal/codec"
)

// The app SMS wire v2 vectors (r40 review R40-R1): made here, by the frame
// MeshSat Android, iOS and the Bridge send (smsFrame) and the encrypt step's
// AES-256-GCM with a fixed nonce; the Hub and both apps check their reader
// and sender against the same file. MESHSAT_WRITE_VECTORS=1 writes it.
const smsWireVectorsFile = "testdata/sms-wire-v2-vectors.json"

const smsVectorKey = "101112131415161718191a1b1c1d1e1f202122232425262728292a2b2c2d2e2f"

type smsWireExpect struct {
	Status string `json:"status"` // decoded, kept, legacy
	Text   string `json:"text,omitempty"`
}

type smsWireVector struct {
	Name     string        `json:"name"`
	Text     string        `json:"text,omitempty"`
	Mode     *int          `json:"mode,omitempty"`
	Codec    *int          `json:"codec,omitempty"`
	FrameHex string        `json:"frame_hex,omitempty"`
	NonceHex string        `json:"nonce_hex,omitempty"`
	WireHex  string        `json:"wire_hex"`
	SMS      string        `json:"sms"`
	Expect   smsWireExpect `json:"expect"`
	Note     string        `json:"note,omitempty"`
}

type smsWireVectors struct {
	Version  int             `json:"version"`
	Contract string          `json:"contract"`
	Kept     string          `json:"kept"`
	KeyHex   string          `json:"key_hex"`
	Vectors  []smsWireVector `json:"vectors"`
	Negative []smsWireVector `json:"negative"`
	Legacy   []smsWireVector `json:"legacy"`
}

var smsVectorTexts = []struct{ name, text string }{
	{"sara", "sara"},
	{"sata", "sata"},
	{"inverted_exclamation", "¡"},
	{"copyright", "©"},
	{"greek_word", "Καλημέρα"},
	{"greek_sentence", "Γεια σου κόσμε, φτάσαμε στη γέφυρα"},
	{"accents_cafe", "café crème"},
	{"accents_facade", "naïve façade"},
	{"emoji", "😀👍"},
	{"mixed", "hello Καλημέρα 😀"},
	{"english", "the weather is getting worse, heading down to the hut"},
	{"short_ascii", "ok"},
	{"one_letter", "a"},
	{"control_byte", "x\x01y and the rest"},
}

func ip(v int) *int { return &v }

func vectorNonce(i int) []byte {
	n := make([]byte, 12)
	for j := range n {
		n[j] = byte(0xA0 + i)
	}
	n[11] = byte(i)
	return n
}

func buildSMSWireVectors(t *testing.T) smsWireVectors {
	t.Helper()
	tp := NewTransformPipeline()
	v := smsWireVectors{
		Version: 1,
		Contract: "SMS text = base64(0x02 | mode | payload). mode 0x00 clear: payload = codec | body. " +
			"mode 0x01 AES-256-GCM: payload = nonce(12) | ciphertext(codec | body) | tag(16), no AAD. " +
			"codec 0x00 UTF-8 text, 0x01 SMAZ2 with the default table, 0x02 MSVQ-SC. Senders: MSVQ-SC when set and it encodes, " +
			"else SMAZ2 when shorter, else the text. A plain SMS (no key, no compression) is the typed text, unframed.",
		Kept: "status kept: the receiver shows the SMS text as it came (the base64), reads nothing into it, and logs the unknown mode or codec. " +
			"status legacy: version 1 or unversioned, read by the old rule; its ambiguity is the contract's reason to exist.",
		KeyHex: smsVectorKey,
	}
	seal := func(frame []byte, i int) ([]byte, string) {
		nonce := vectorNonce(i)
		return sealWithNonce(t, frame, smsVectorKey, nonce), hex.EncodeToString(nonce)
	}
	n := 0
	for _, c := range smsVectorTexts {
		frame := tp.smsFrame([]byte(c.text), "[]")
		clear := append(codec.SMSWireV2Header(codec.SMSModeClear), frame...)
		v.Vectors = append(v.Vectors, smsWireVector{
			Name: c.name + "_clear", Text: c.text, Mode: ip(0), Codec: ip(int(frame[0])), FrameHex: hex.EncodeToString(frame),
			WireHex: hex.EncodeToString(clear), SMS: base64.StdEncoding.EncodeToString(clear),
			Expect: smsWireExpect{Status: "decoded", Text: c.text},
		})
		n++
		sealed, nonce := seal(frame, n)
		wire := append(codec.SMSWireV2Header(codec.SMSModeAESGCM), sealed...)
		v.Vectors = append(v.Vectors, smsWireVector{
			Name: c.name + "_aesgcm", Text: c.text, Mode: ip(1), Codec: ip(int(frame[0])), FrameHex: hex.EncodeToString(frame), NonceHex: nonce,
			WireHex: hex.EncodeToString(wire), SMS: base64.StdEncoding.EncodeToString(wire),
			Expect: smsWireExpect{Status: "decoded", Text: c.text},
		})
	}
	neg := func(name string, wire []byte, note string) {
		v.Negative = append(v.Negative, smsWireVector{Name: name, WireHex: hex.EncodeToString(wire),
			SMS: base64.StdEncoding.EncodeToString(wire), Expect: smsWireExpect{Status: "kept"}, Note: note})
	}
	n++
	unknownSealed, _ := seal([]byte{0x07, 'h', 'i'}, n)
	neg("unknown_codec_aesgcm", append(codec.SMSWireV2Header(codec.SMSModeAESGCM), unknownSealed...), "codec 0x07 inside a frame the key opens")
	neg("unknown_codec_clear", append(codec.SMSWireV2Header(codec.SMSModeClear), 0x07, 'h', 'i'), "codec 0x07")
	neg("unknown_mode", []byte{codec.SMSWireV2, 0x05, codec.SMSCodecText, 'h', 'i'}, "mode 0x05")
	neg("text_codec_bad_utf8", append(codec.SMSWireV2Header(codec.SMSModeClear), codec.SMSCodecText, 0xC3, 0x28), "codec 0x00 with bytes that are not UTF-8")

	legacy := func(name, text string, wire []byte, note string) {
		v.Legacy = append(v.Legacy, smsWireVector{Name: name, WireHex: hex.EncodeToString(wire),
			SMS: base64.StdEncoding.EncodeToString(wire), Expect: smsWireExpect{Status: "legacy", Text: text}, Note: note})
	}
	n++
	v1Sara, _ := seal([]byte{0xC2, 0xA1}, n) // what an old sender sends for "sara"
	legacy("v1_sara_aesgcm", "¡", codec.PrependVersionByte(v1Sara),
		"MeshSat Android up to 2.22.3 sends \"sara\" as SMAZ2 c2 a1 behind 0x01; nothing tells it from \"¡\", and the old rule reads \"¡\"")
	legacy("v1_sara_clear", "¡", []byte{codec.ProtoVersion1, 0xC2, 0xA1}, "the same without a key")
	n++
	en := []byte("the weather is getting worse, heading down to the hut")
	enFrame := tp.smsFrame(en, "[]")
	v1En, _ := seal(enFrame[1:], n)
	legacy("v1_english_aesgcm", string(en), codec.PrependVersionByte(v1En), "English through SMAZ2 reads right under the old rule")
	return v
}

func TestSMSWireV2Vectors(t *testing.T) {
	got, err := json.MarshalIndent(buildSMSWireVectors(t), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if os.Getenv("MESHSAT_WRITE_VECTORS") == "1" {
		if err := os.WriteFile(smsWireVectorsFile, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(smsWireVectorsFile)
	if err != nil {
		t.Fatalf("%v (MESHSAT_WRITE_VECTORS=1 writes it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from what the Bridge makes now; regenerate with MESHSAT_WRITE_VECTORS=1 and take the new file into the apps' locks", smsWireVectorsFile)
	}
}

// The Bridge reads every vector the way the contract says, and its own
// sender makes the frames in the file.
func TestSMSWireV2BridgeReadsAndSends(t *testing.T) {
	raw, err := os.ReadFile(smsWireVectorsFile)
	if err != nil {
		t.Fatal(err)
	}
	var v smsWireVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	tp := NewTransformPipeline()
	tp.SetChatKeyResolver(newFakeChatKeys("sms:"+chatPeer, v.KeyHex))
	for _, c := range v.Vectors {
		blob, _ := base64.StdEncoding.DecodeString(c.SMS)
		switch *c.Mode {
		case int(codec.SMSModeAESGCM):
			got, _, ok := tp.OpenChatSMS(chatPeer, c.SMS)
			if !ok || string(got) != c.Expect.Text {
				t.Errorf("%s: read %q (%v), want %q", c.Name, got, ok, c.Expect.Text)
			}
			wire, err := tp.SealChatSMS([]byte(c.Text), v.KeyHex, "[]")
			if err != nil {
				t.Fatal(err)
			}
			sent, _ := base64.StdEncoding.DecodeString(wire)
			frame, err := decryptAESGCM(sent[2:], v.KeyHex)
			if err != nil || hex.EncodeToString(frame) != c.FrameHex || !bytes.Equal(sent[:2], blob[:2]) {
				t.Errorf("%s: the Bridge sends % x with frame %x, want frame %s", c.Name, sent[:2], frame, c.FrameHex)
			}
		case int(codec.SMSModeClear):
			mode, payload, ok := codec.ParseSMSWireV2(blob)
			words, wok := tp.smsFrameWords(payload)
			if !ok || mode != codec.SMSModeClear || !wok || string(words) != c.Expect.Text {
				t.Errorf("%s: read %q (%v %v)", c.Name, words, ok, wok)
			}
		}
	}
	for _, c := range v.Negative {
		got, _, ok := tp.OpenChatSMS(chatPeer, c.SMS)
		blob, _ := base64.StdEncoding.DecodeString(c.SMS)
		_, payload, _ := codec.ParseSMSWireV2(blob)
		if _, wok := tp.smsFrameWords(payload); wok && blob[1] == codec.SMSModeClear {
			t.Errorf("%s: a clear frame the contract keeps was read", c.Name)
		}
		if ok && string(got) != c.SMS {
			t.Errorf("%s: opened to %q, want it kept as it came", c.Name, got)
		}
	}
	for _, c := range v.Legacy {
		blob, _ := base64.StdEncoding.DecodeString(c.SMS)
		if len(blob) > 1 && blob[0] == codec.ProtoVersion1 && len(blob) > 3 {
			got, _, ok := tp.OpenChatSMS(chatPeer, c.SMS)
			if !ok || string(got) != c.Expect.Text {
				t.Errorf("%s: read %q (%v), want %q", c.Name, got, ok, c.Expect.Text)
			}
		}
	}
	if len(v.Vectors) != 2*len(smsVectorTexts) || len(v.Negative) != 4 || len(v.Legacy) != 3 {
		t.Fatal(fmt.Sprintf("%d/%d/%d vectors", len(v.Vectors), len(v.Negative), len(v.Legacy)))
	}
}
