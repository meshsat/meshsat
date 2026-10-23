package engine

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"meshsat/internal/codec"
	"meshsat/internal/database"
	"meshsat/internal/gateway"
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
	// Encrypted: a key opened the SMS (the lock), apart from whether it
	// reads (r41 review V1).
	Encrypted bool `json:"encrypted"`
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
			"status legacy: version 1 or unversioned, read by the old rule; its ambiguity is the contract's reason to exist. " +
			"encrypted: a key opened the SMS, whether or not it then reads. A receiver takes what a key opens (v2 sealed, then v1 or unversioned) " +
			"before it reads a clear v2 frame, which nothing authenticates.",
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
			Expect: smsWireExpect{Status: "decoded", Text: c.text, Encrypted: true},
		})
	}
	neg := func(name string, encrypted bool, wire []byte, note string) {
		v.Negative = append(v.Negative, smsWireVector{Name: name, WireHex: hex.EncodeToString(wire),
			SMS: base64.StdEncoding.EncodeToString(wire), Expect: smsWireExpect{Status: "kept", Encrypted: encrypted}, Note: note})
	}
	n++
	unknownSealed, _ := seal([]byte{0x07, 'h', 'i'}, n)
	neg("unknown_codec_aesgcm", true, append(codec.SMSWireV2Header(codec.SMSModeAESGCM), unknownSealed...), "codec 0x07 inside a frame the key opens")
	neg("unknown_codec_clear", false, append(codec.SMSWireV2Header(codec.SMSModeClear), 0x07, 'h', 'i'), "codec 0x07")
	neg("unknown_mode", false, []byte{codec.SMSWireV2, 0x05, codec.SMSCodecText, 'h', 'i'}, "mode 0x05")
	neg("text_codec_bad_utf8", false, append(codec.SMSWireV2Header(codec.SMSModeClear), codec.SMSCodecText, 0xC3, 0x28), "codec 0x00 with bytes that are not UTF-8")

	legacy := func(name, text string, encrypted bool, wire []byte, note string) {
		v.Legacy = append(v.Legacy, smsWireVector{Name: name, WireHex: hex.EncodeToString(wire),
			SMS: base64.StdEncoding.EncodeToString(wire), Expect: smsWireExpect{Status: "legacy", Text: text, Encrypted: encrypted}, Note: note})
	}
	n++
	v1Sara, _ := seal([]byte{0xC2, 0xA1}, n) // what an old sender sends for "sara"
	legacy("v1_sara_aesgcm", "¡", true, codec.PrependVersionByte(v1Sara),
		"MeshSat Android up to 2.22.3 sends \"sara\" as SMAZ2 c2 a1 behind 0x01; nothing tells it from \"¡\", and the old rule reads \"¡\"")
	legacy("v1_sara_clear", "¡", false, []byte{codec.ProtoVersion1, 0xC2, 0xA1}, "the same without a key")
	n++
	en := []byte("the weather is getting worse, heading down to the hut")
	enFrame := tp.smsFrame(en, "[]")
	v1En, _ := seal(enFrame[1:], n)
	legacy("v1_english_aesgcm", string(en), true, codec.PrependVersionByte(v1En), "English through SMAZ2 reads right under the old rule")
	legacy("legacy_encrypted_clear_header_overlap", "ok", true, sealWithNonce(t, []byte("ok"), smsVectorKey, smsOverlapNonce),
		"unversioned, sealed by the key, whose nonce starts 02 00 00 and whose rest is valid UTF-8, so it is also a clear v2 frame; "+
			"the key opens it and that wins: \"ok\", encrypted, never the clear frame's text")
	return v
}

// smsOverlapNonce makes an unversioned sealed SMS that also parses as a
// clear v2 frame with the text codec (r41 review V2): an old sender's random
// nonce, or a Bridge chain's (whose version byte sits outside the base64),
// can start 02 00 00. The nine digits were searched once so that the rest
// of the nonce, the ciphertext and the tag of "ok" under smsVectorKey are
// valid UTF-8; TestSMSWireV2OverlapVectorIsAClearFrame keeps it so.
var smsOverlapNonce = append([]byte{codec.SMSWireV2, codec.SMSModeClear, codec.SMSCodecText}, "000028374"...)

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

func loadSMSWireVectors(t *testing.T) smsWireVectors {
	t.Helper()
	raw, err := os.ReadFile(smsWireVectorsFile)
	if err != nil {
		t.Fatal(err)
	}
	var v smsWireVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Vectors) != 2*len(smsVectorTexts) || len(v.Negative) != 4 || len(v.Legacy) != 4 {
		t.Fatalf("%d/%d/%d vectors", len(v.Vectors), len(v.Negative), len(v.Legacy))
	}
	return v
}

// smsInbox is the Bridge's SMS history: the cellular event recorder, the
// processor's DecodeSMS, the chat keys (none when keyHex is empty) and
// cellular_0's ingress chain. It returns what the history keeps.
func smsInbox(t *testing.T, ingress, keyHex string) func(sms string) database.SMSMessageRecord {
	t.Helper()
	rec, db, tp := newSMSRecorderHarness(t, ingress)
	if keyHex != "" {
		tp.SetChatKeyResolver(newFakeChatKeys("sms:"+chatPeer, keyHex))
	}
	return func(sms string) database.SMSMessageRecord {
		t.Helper()
		if _, err := db.Exec(`DELETE FROM sms_messages`); err != nil {
			t.Fatal(err)
		}
		rec.handleSMSReceived(smsEvent(t, chatPeer, sms))
		return lastSMS(t, db)
	}
}

// smsRelay is the Bridge's receive and forward path for one SMS: the gateway
// receiver (its chat keys, cellular_0's chain and the chain's gate), the
// message table, then the access rule that relays every SMS to mesh_0. A
// fresh processor each time, so no loop or duplicate check sees two.
func smsRelay(t *testing.T, ingress, keyHex, sms string) (stored, relayed string, dropped uint64) {
	t.Helper()
	p, db, tp, gw := newInboundAuthHarness(t, "cellular_0", "cellular", ingress)
	if keyHex != "" {
		tp.SetChatKeyResolver(newFakeChatKeys("sms:"+chatPeer, keyHex))
	}
	msgs, dropped := deliver(t, p, db, gw, gateway.InboundMessage{Text: sms, Source: "cellular", FromAddr: chatPeer})
	if dropped > 0 {
		return "", "", dropped
	}
	dl, err := db.GetPendingDeliveries("mesh_0", 10)
	if err != nil || len(dl) != 1 {
		t.Fatalf("relay to mesh_0: %+v %v", dl, err)
	}
	return msgs[0].DecodedText, dl[0].TextPreview, 0
}

// The Bridge's own sender makes the frames in the file.
func TestSMSWireV2BridgeSends(t *testing.T) {
	v := loadSMSWireVectors(t)
	tp := NewTransformPipeline()
	for _, c := range v.Vectors {
		blob, _ := base64.StdEncoding.DecodeString(c.SMS)
		if *c.Mode != int(codec.SMSModeAESGCM) {
			if frame := tp.smsFrame([]byte(c.Text), "[]"); hex.EncodeToString(frame) != c.FrameHex {
				t.Errorf("%s: the Bridge frames %x, want %s", c.Name, frame, c.FrameHex)
			}
			continue
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
	}
}

// Every vector through the Bridge's real receive paths, the SMS history and
// the relay, not its helpers (r41 review V1, V2): what each keeps and
// relays, and whether a key opened it.
func TestSMSWireV2ThroughTheRealReceivePaths(t *testing.T) {
	v := loadSMSWireVectors(t)
	var clear, sealed []smsWireVector
	for _, c := range v.Vectors {
		if *c.Mode == int(codec.SMSModeClear) {
			clear = append(clear, c)
		} else {
			sealed = append(sealed, c)
		}
	}
	history := func(t *testing.T, inbox func(string) database.SMSMessageRecord, c smsWireVector, want string) {
		t.Helper()
		if h := inbox(c.SMS); h.Text != want || h.Encrypted != c.Expect.Encrypted {
			t.Errorf("%s: the history keeps %q encrypted=%v, want %q encrypted=%v", c.Name, h.Text, h.Encrypted, want, c.Expect.Encrypted)
		}
	}
	relay := func(t *testing.T, ingress, keyHex string, c smsWireVector, want string) {
		t.Helper()
		stored, relayed, dropped := smsRelay(t, ingress, keyHex, c.SMS)
		if dropped != 0 || stored != want || relayed != want {
			t.Errorf("%s: stored %q, relayed %q, dropped %d, want %q", c.Name, stored, relayed, dropped, want)
		}
	}

	t.Run("clear_v2_real_inbox_without_key", func(t *testing.T) {
		inbox := smsInbox(t, "[]", "")
		for _, c := range clear {
			history(t, inbox, c, c.Expect.Text)
		}
	})
	t.Run("clear_v2_real_inbox_with_key", func(t *testing.T) {
		inbox := smsInbox(t, "[]", v.KeyHex)
		for _, c := range append(clear, sealed...) {
			history(t, inbox, c, c.Expect.Text)
		}
	})
	t.Run("clear_v2_real_forwarding", func(t *testing.T) {
		for _, c := range clear {
			relay(t, "[]", "", c, c.Expect.Text)
		}
		for _, c := range sealed {
			relay(t, "[]", v.KeyHex, c, c.Expect.Text)
		}
		// The Linux app's chain (an optional decrypt with the link's key,
		// base64) and a chain that only takes base64 off read nothing into
		// a clear frame either.
		for _, c := range clear {
			relay(t, linuxAppIngress, "", c, c.Expect.Text)
			relay(t, base64Ingress, "", c, c.Expect.Text)
		}
	})
	t.Run("unreadable_v2_keeps_original", func(t *testing.T) {
		inbox := smsInbox(t, "[]", v.KeyHex)
		for _, c := range v.Negative {
			history(t, inbox, c, c.SMS)
			relay(t, "[]", v.KeyHex, c, c.SMS)
			relay(t, linuxAppIngress, v.KeyHex, c, c.SMS)
			relay(t, base64Ingress, v.KeyHex, c, c.SMS)
		}
		// Sealed under a key this Bridge does not have: kept as it came.
		for _, c := range sealed[:2] {
			stranger := c
			stranger.Expect.Encrypted = false
			history(t, smsInbox(t, "[]", ""), stranger, c.SMS)
			relay(t, "[]", "", stranger, c.SMS)
		}
	})
	t.Run("legacy_encrypted_clear_header_overlap", func(t *testing.T) {
		c := v.Legacy[len(v.Legacy)-1]
		if c.Name != "legacy_encrypted_clear_header_overlap" {
			t.Fatalf("the last legacy vector is %s", c.Name)
		}
		history(t, smsInbox(t, "[]", v.KeyHex), c, c.Expect.Text)
		relay(t, "[]", v.KeyHex, c, c.Expect.Text)
		relay(t, linuxAppIngress, v.KeyHex, c, c.Expect.Text)
		// Sealed with the link's own key and no chat key: the link's
		// optional decrypt opens it, and that outranks the clear frame too.
		linkKeyed := `[{"type":"decrypt","params":{"key":"` + v.KeyHex + `","optional":"true"}},{"type":"base64"}]`
		history(t, smsInbox(t, linkKeyed, ""), c, c.Expect.Text)
		relay(t, linkKeyed, "", c, c.Expect.Text)
	})
	t.Run("legacy_keyed", func(t *testing.T) {
		inbox := smsInbox(t, "[]", v.KeyHex)
		for _, c := range v.Legacy {
			if c.Expect.Encrypted {
				history(t, inbox, c, c.Expect.Text)
			}
		}
	})
	// On a link whose chain authenticates, a frame nothing authenticated is
	// still the gate's to drop: a clear v2 frame does not get past it.
	t.Run("clear_v2_authenticating_link_still_gated", func(t *testing.T) {
		chain := `[{"type":"encrypt","params":{"key":"` + chatKeyChain + `"}},{"type":"base64"}]`
		for _, c := range append(clear[:2], v.Negative...) {
			if _, _, dropped := smsRelay(t, chain, "", c.SMS); dropped != 1 {
				t.Errorf("%s: not dropped on an authenticating link", c.Name)
			}
		}
	})
}

// linuxAppIngress is the Linux app's SMS receive chain: an optional decrypt
// with the link's key, then base64.
var linuxAppIngress = `[{"type":"decrypt","params":{"key":"` + chatKeyChain + `","optional":"true"}},{"type":"base64"}]`

// base64Ingress takes base64 off and nothing else: it authenticates nothing.
const base64Ingress = `[{"type":"base64"}]`

// The overlap vector is what it says: its bytes parse as a clear v2 frame
// with the text codec and a body that reads, and the key opens them.
func TestSMSWireV2OverlapVectorIsAClearFrame(t *testing.T) {
	v := loadSMSWireVectors(t)
	c := v.Legacy[len(v.Legacy)-1]
	blob, _ := base64.StdEncoding.DecodeString(c.SMS)
	mode, payload, ok := codec.ParseSMSWireV2(blob)
	words, wok := NewTransformPipeline().smsFrameWords(payload)
	if !ok || mode != codec.SMSModeClear || payload[0] != codec.SMSCodecText || !wok || len(words) == 0 {
		t.Fatalf("%s does not parse as a clear text frame: % x", c.Name, blob[:3])
	}
	if p, err := decryptAESGCM(blob, v.KeyHex); err != nil || string(p) != c.Expect.Text {
		t.Fatalf("%s: the key opens it to %q (%v)", c.Name, p, err)
	}
}
