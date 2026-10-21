package engine

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"meshsat/internal/codec"
	"meshsat/internal/compress"
)

// The shared H-chain test vectors (HeMB-NG S3-2, MESHSAT-1623/1624): what the
// MeshSat apps must read and write byte for byte as a Bridge does. The file
// is generated from this package's own transforms and checked in;
// MESHSAT_WRITE_VECTORS=1 rewrites it. The apps copy it and record the Bridge
// commit and the file's SHA-256.
const hChainVectorsFile = "testdata/h-chain-vectors-v1.json"

type hChainVectors struct {
	Name        string              `json:"name"`
	Version     int                 `json:"version"`
	GeneratedBy string              `json:"generated_by"`
	Spec        map[string]string   `json:"spec"`
	Dicts       map[string]dictDump `json:"dictionaries"` // duplicates kept: the compressor indexes a word by its LAST entry
	Smaz2       []smaz2Vector       `json:"smaz2"`
	Smaz2g      []smaz2gVector      `json:"smaz2g"`
	Smaz2gBad   []rejectVector      `json:"smaz2g_reject"`
	Envelopes   []envelopeVector    `json:"envelopes"`
	EnvelopeBad []rejectVector      `json:"envelope_reject"`
}

type dictDump struct {
	Words   []string `json:"words"`
	Bigrams string   `json:"bigrams"`
}

type smaz2Vector struct {
	Name, Dict, Text, TextHex, CompressedHex string
}

type smaz2gVector struct {
	Name    string `json:"name"`
	Dict    string `json:"dict"`
	Text    string `json:"text"`
	TextHex string `json:"text_hex"`
	Mode    string `json:"mode"`
	Frame   string `json:"frame_hex"`
}

type envelopeVector struct {
	Name    string `json:"name"`
	Dict    string `json:"dict"`
	Text    string `json:"text"`
	Key     string `json:"key_hex"`
	Nonce   string `json:"nonce_hex"`
	Wire    string `json:"wire_hex"`
	WireLen int    `json:"wire_len"`
}

type rejectVector struct {
	Name  string `json:"name"`
	Dict  string `json:"dict"`
	Key   string `json:"key_hex,omitempty"`
	Bytes string `json:"bytes_hex"`
	Why   string `json:"why"`
}

func (v smaz2Vector) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"name": v.Name, "dict": v.Dict, "text": v.Text, "text_hex": v.TextHex, "compressed_hex": v.CompressedHex})
}

func dictOf(name string) compress.Dictionary {
	if name == "meshtastic" {
		return compress.DictMeshtastic
	}
	return compress.DictDefault
}

func smaz2gChain(dict string) string {
	if dict == "meshtastic" {
		return `[{"type":"smaz2g","params":{"dict":"meshtastic"}}]`
	}
	return `[{"type":"smaz2g"}]`
}

func hChain(dict, key string) string {
	step := `{"type":"smaz2g"}`
	if dict == "meshtastic" {
		step = `{"type":"smaz2g","params":{"dict":"meshtastic"}}`
	}
	return `[` + step + `,{"type":"encrypt","params":{"key":"` + key + `"}}]`
}

// hChainSeal is encryptAESGCM with a fixed nonce, for the fixtures only:
// nonce || ciphertext || tag.
func hChainSeal(t *testing.T, frame []byte, keyHex, nonceHex string) []byte {
	t.Helper()
	key, _ := hex.DecodeString(keyHex)
	nonce, _ := hex.DecodeString(nonceHex)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return gcm.Seal(append([]byte(nil), nonce...), nonce, frame, nil)
}

var hChainTexts = []struct{ name, text string }{
	{"empty", ""},
	{"k", "k"},
	{"ok", "ok"},
	{"en_sos", "SOS need medical assistance"},
	{"en_sentence", "hello there, the satellite is in range"},
	{"en_words_spaces", "the node battery level is low, relay gateway down"},
	{"en_long", "Status report: all nodes online, battery level 80 percent, signal good, position unchanged since the last message. Team at the north gate, all safe, moving to camp before dark. Next check in one hour over the satellite channel. Bridge relay is up, mesh network quiet, no errors."},
	{"el_word", "Καλημέρα"},
	{"el_sentence", "Είμαστε στο βόρειο φυλάκιο, όλοι καλά, κινούμαστε προς το στρατόπεδο"},
	{"mixed", "Node 7: θέση OK, battery 80%, όλα καλά"},
	{"emoji", "SOS 🚨 στο σημείο 4, need water 💧"},
	{"latin1_multibyte", "café naïve façade, Größe, ñandú"},
	{"control_bytes", "a\x01b\x02c\x03d\x04e\x05f\x06g\x07h\x08i"},
	{"nul", "ab\x00cd"},
	{"long_verbatim_run", "αβγδεζηθικλμνξοπρστυφχψω"},
	{"dup_words", "satellite over iridium, total over the iridium satellite total"},
	{"tie_meshtastic", "OK 42"},
	{"tie_default", "on my way!"},
}

// The size ties: SMAZ2 exactly as long as the text, so smaz2g stays raw.
var hChainTies = map[string]string{"meshtastic": "OK 42", "default": "on my way!"}

func buildHChainVectors(t *testing.T) hChainVectors {
	t.Helper()
	tp := NewTransformPipeline()
	words, bigrams := compress.MeshtasticTables()
	v := hChainVectors{
		Name:        "meshsat-h-chain-vectors",
		Version:     1,
		GeneratedBy: "meshsat internal/engine/h_chain_vectors_test.go (MESHSAT_WRITE_VECTORS=1)",
		Spec: map[string]string{
			"envelope":         "protocol version 0x01 || 12-byte nonce || AES-256-GCM ciphertext of the smaz2g frame || 16-byte tag; no AAD, no base64",
			"smaz2g":           "mode byte 0x00 = the text's UTF-8 bytes as they are, 0x01 = its SMAZ2, taken only when strictly shorter (a tie stays raw)",
			"dictionary":       "a configured agreement (step param dict: meshtastic or default); nothing on the wire names it",
			"wire_len":         "30 + min(n, z) for n UTF-8 bytes and SMAZ2 output z",
			"rejects":          "every smaz2g_reject frame and envelope_reject wire must fail without a crash and without showing text",
			"h_chain_egress":   `[{"type":"smaz2g","params":{"dict":"meshtastic"}},{"type":"encrypt","params":{"key_ref":"..."}}]`,
			"rollback_egress":  `[{"type":"smaz2","params":{"dict":"meshtastic"}},{"type":"encrypt"},{"type":"base64"}]`,
			"protocol_version": "01",
		},
		Dicts: map[string]dictDump{"meshtastic": {Words: words, Bigrams: bigrams}},
	}
	for _, dict := range []string{"default", "meshtastic"} {
		for _, c := range hChainTexts {
			z := compress.Compress([]byte(c.text), dictOf(dict))
			v.Smaz2 = append(v.Smaz2, smaz2Vector{Name: c.name, Dict: dict, Text: c.text, TextHex: hex.EncodeToString([]byte(c.text)), CompressedHex: hex.EncodeToString(z)})
			frame, err := tp.ApplyEgress([]byte(c.text), smaz2gChain(dict))
			if err != nil {
				t.Fatal(err)
			}
			mode := "raw"
			if len(frame) > 0 && frame[0] == 0x01 {
				mode = "smaz2"
			}
			if hChainTies[dict] == c.text && (len(z) != len(c.text) || mode != "raw") {
				t.Fatalf("tie %q (%s): SMAZ2 %d bytes for %d, mode %s", c.text, dict, len(z), len(c.text), mode)
			}
			v.Smaz2g = append(v.Smaz2g, smaz2gVector{Name: c.name, Dict: dict, Text: c.text, TextHex: hex.EncodeToString([]byte(c.text)), Mode: mode, Frame: hex.EncodeToString(frame)})
		}
	}
	for _, r := range []struct{ name, dict, frame, why string }{
		{"empty_frame", "meshtastic", "", "no mode byte"},
		{"unknown_mode_02", "meshtastic", "0268656c6c6f", "unknown mode"},
		{"unknown_mode_ff", "default", "ff00", "unknown mode"},
		{"smaz2_mode_no_body", "meshtastic", "01", "smaz2 mode with no data"},
		{"word_escape_at_end", "meshtastic", "0106", "truncated word escape"},
		{"word_space_escape_at_end", "meshtastic", "0107", "truncated word+space escape"},
		{"space_word_escape_at_end", "meshtastic", "0108", "truncated space+word escape"},
		{"verbatim_overflow", "meshtastic", "010341", "verbatim run longer than the data"},
		{"default_smaz2_mode_no_body", "default", "01", "smaz2 mode with no data"},
	} {
		frame, _ := hex.DecodeString(r.frame)
		if _, err := tp.ApplyIngress(frame, smaz2gChain(r.dict)); err == nil {
			t.Fatalf("reject %s: the Bridge decoded it", r.name)
		}
		v.Smaz2gBad = append(v.Smaz2gBad, rejectVector{Name: r.name, Dict: r.dict, Bytes: r.frame, Why: r.why})
	}
	const key = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	for _, e := range []struct{ name, dict, text, nonce string }{
		{"empty", "meshtastic", "", "000000000000000000000001"},
		{"en_sos", "meshtastic", "SOS need medical assistance", "a1a2a3a4a5a6a7a8a9aaabac"},
		{"en_sentence_nonce_starts_01", "meshtastic", "hello there, the satellite is in range", "0102030405060708090a0b0c"},
		{"el_sentence", "meshtastic", "Είμαστε στο βόρειο φυλάκιο, όλοι καλά, κινούμαστε προς το στρατόπεδο", "0f0e0d0c0b0a090807060504"},
		{"mixed", "meshtastic", "Node 7: θέση OK, battery 80%, όλα καλά", "deadbeefcafebabe00112233"},
		{"emoji", "meshtastic", "SOS 🚨 στο σημείο 4, need water 💧", "112233445566778899aabbcc"},
		{"en_long", "meshtastic", hChainTexts[6].text, "ffeeddccbbaa998877665544"},
		{"default_dict", "default", "hello there, the satellite is in range", "010101010101010101010101"},
	} {
		frame, err := tp.ApplyEgress([]byte(e.text), smaz2gChain(e.dict))
		if err != nil {
			t.Fatal(err)
		}
		wire := append([]byte{codec.ProtoVersion1}, hChainSeal(t, frame, key, e.nonce)...)
		// The production read path takes it: the version byte off, then the
		// H chain's ingress (decrypt, then smaz2g).
		ver, body := codec.StripVersionByte(wire)
		if ver != codec.ProtoVersion1 {
			t.Fatalf("envelope %s: version %d", e.name, ver)
		}
		text, err := tp.ApplyIngress(body, hChain(e.dict, key))
		if err != nil || string(text) != e.text {
			t.Fatalf("envelope %s: the Bridge read %q, %v", e.name, text, err)
		}
		n, z := len(e.text), len(compress.Compress([]byte(e.text), dictOf(e.dict)))
		if want := 30 + min(n, z); len(wire) != want && !(n == 0 && len(wire) == 30) {
			t.Fatalf("envelope %s: %d bytes, want 30 + min(%d, %d)", e.name, len(wire), n, z)
		}
		// A random-nonce egress from production is the same length and
		// reads back the same.
		live, err := tp.ApplyEgress([]byte(e.text), hChain(e.dict, key))
		if err != nil || len(live)+1 != len(wire) {
			t.Fatalf("envelope %s: production egress %d bytes, %v", e.name, len(live)+1, err)
		}
		v.Envelopes = append(v.Envelopes, envelopeVector{Name: e.name, Dict: e.dict, Text: e.text, Key: key, Nonce: e.nonce, Wire: hex.EncodeToString(wire), WireLen: len(wire)})
	}
	good, _ := hex.DecodeString(v.Envelopes[1].Wire)
	flip := func(i int) []byte {
		b := append([]byte(nil), good...)
		b[i] ^= 0x01
		return b
	}
	const wrongKey = "1f1e1d1c1b1a191817161514131211100f0e0d0c0b0a09080706050403020100"
	for _, r := range []struct {
		name, key string
		wire      []byte
		why       string
	}{
		{"wrong_key", wrongKey, good, "another key"},
		{"tag_modified", key, flip(len(good) - 1), "last tag byte flipped"},
		{"ciphertext_modified", key, flip(13), "first ciphertext byte flipped"},
		{"nonce_modified", key, flip(1), "first nonce byte flipped"},
		{"truncated_one_byte", key, good[:len(good)-1], "last byte cut"},
		{"nonce_only", key, good[:13], "version byte and nonce, nothing else"},
		{"version_only", key, good[:1], "version byte only"},
	} {
		_, body := codec.StripVersionByte(r.wire)
		if _, err := tp.ApplyIngress(body, hChain("meshtastic", r.key)); err == nil {
			t.Fatalf("envelope reject %s: the Bridge read it", r.name)
		}
		v.EnvelopeBad = append(v.EnvelopeBad, rejectVector{Name: r.name, Dict: "meshtastic", Key: r.key, Bytes: hex.EncodeToString(r.wire), Why: r.why})
	}
	return v
}

// The checked-in vectors are what this package produces, and every one of
// them passes the production chain (checked while building them).
func TestHChainVectors(t *testing.T) {
	v := buildHChainVectors(t)
	want, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	path := filepath.FromSlash(hChainVectorsFile)
	if os.Getenv("MESHSAT_WRITE_VECTORS") == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (generate it with MESHSAT_WRITE_VECTORS=1)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is not what the Bridge produces now; regenerate it with MESHSAT_WRITE_VECTORS=1 and bump the apps' copies", hChainVectorsFile)
	}
}
