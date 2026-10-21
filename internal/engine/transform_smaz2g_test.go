package engine

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math/rand"
	"strings"
	"testing"

	"meshsat/internal/codec"
	"meshsat/internal/compress"
	"meshsat/internal/database"
	"meshsat/internal/gateway"
	"meshsat/internal/rules"
)

const smaz2gTestKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func TestSmaz2gModeByte(t *testing.T) {
	cases := []struct {
		name, chain, text, wire string
	}{
		{"default dict compresses", `[{"type":"smaz2g"}]`, "hello there, the satellite is in range",
			"0184b1026f200615022c2081026520c289b193026520960120800120a1980165"},
		{"default dict would grow it", `[{"type":"smaz2g"}]`, "OK 42", "004f4b203432"},
		{"same length stays raw", `[{"type":"smaz2g"}]`, "on my way!", "006f6e206d79207761792" + "1"},
		{"meshtastic dict compresses", `[{"type":"smaz2g","params":{"dict":"meshtastic"}}]`, "hello there, the satellite is in range",
			"0184b16f088f2c208165087c209620800809"},
		{"meshtastic dict words", `[{"type":"smaz2g","params":{"dict":"meshtastic"}}]`, "SOS need medical assistance",
			"01534f5308ad08420843"},
		{"greek stays as typed", `[{"type":"smaz2g","params":{"dict":"meshtastic"}}]`, "Καλημέρα",
			"00ce9aceb1cebbceb7cebcceadcf81ceb1"},
	}
	tp := NewTransformPipeline()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wire, err := tp.ApplyEgress([]byte(c.text), c.chain)
			if err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(wire); got != c.wire {
				t.Fatalf("wire %s, want %s", got, c.wire)
			}
			text, err := tp.ApplyIngress(wire, c.chain)
			if err != nil {
				t.Fatal(err)
			}
			if string(text) != c.text {
				t.Fatalf("read back %q, want %q", text, c.text)
			}
		})
	}
}

func TestSmaz2gEmptyInput(t *testing.T) {
	tp := NewTransformPipeline()
	wire, err := tp.ApplyEgress(nil, `[{"type":"smaz2g"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wire, []byte{0x00}) {
		t.Fatalf("empty text is % x, want the raw mode byte alone", wire)
	}
	text, err := tp.ApplyIngress(wire, `[{"type":"smaz2g"}]`)
	if err != nil || len(text) != 0 {
		t.Fatalf("raw mode byte alone reads %q, %v; want the empty text", text, err)
	}
	if _, err := decodeSmaz2g(nil, compress.DictDefault); err == nil || !strings.Contains(err.Error(), "no mode byte") {
		t.Fatalf("a frame without its mode byte: %v", err)
	}
}

func TestSmaz2gRefusesUnknownMode(t *testing.T) {
	for _, mode := range []byte{0x02, 0x09, 0x7f, 0x80, 0xff} {
		_, err := decodeSmaz2g([]byte{mode, 'h', 'i'}, compress.DictDefault)
		if err == nil || !strings.Contains(err.Error(), "unknown mode") {
			t.Fatalf("mode 0x%02x: %v", mode, err)
		}
	}
}

func TestSmaz2gRefusesMalformedBody(t *testing.T) {
	for _, dict := range []compress.Dictionary{compress.DictDefault, compress.DictMeshtastic} {
		for _, frame := range [][]byte{
			{0x01},             // smaz2 mode with nothing after it
			{0x01, 0x06},       // a word escape cut off
			{0x01, 0x05, 'a'},  // a verbatim run longer than what is left
			{0x01, 0x41, 0x08}, // space+word cut off after a literal
		} {
			out, err := decodeSmaz2g(frame, dict)
			if err == nil {
				t.Fatalf("dict %d frame % x read as %q", dict, frame, out)
			}
		}
	}
}

// Every frame is the mode byte plus the shorter of the text and its SMAZ2,
// so it is at most one byte longer than the text, and reads back exactly.
func TestSmaz2gSizeBound(t *testing.T) {
	rng := rand.New(rand.NewSource(1584))
	var inputs [][]byte
	for n := 0; n <= 400; n += 7 {
		letters := make([]byte, n)
		for i := range letters {
			letters[i] = "etaoin shrdlu,.0123456789"[rng.Intn(25)]
		}
		noise := make([]byte, n)
		rng.Read(noise)
		inputs = append(inputs, letters, noise)
	}
	inputs = append(inputs,
		[]byte("Η ομάδα είναι ασφαλής στη βόρεια πύλη, κινούμαστε"),
		[]byte("OK \U0001F44D back at base \U0001F3D5"),
		[]byte("Water level at the bridge is rising, road to the shelter is blocked, we need a boat"))
	for _, dict := range []compress.Dictionary{compress.DictDefault, compress.DictMeshtastic} {
		for _, in := range inputs {
			frame := encodeSmaz2g(in, dict)
			want := 1 + min(len(in), len(compress.Compress(in, dict)))
			if len(frame) != want || len(frame) > len(in)+1 {
				t.Fatalf("dict %d: %d-byte text framed in %d bytes, want %d", dict, len(in), len(frame), want)
			}
			back, err := decodeSmaz2g(frame, dict)
			if err != nil || !bytes.Equal(back, in) {
				t.Fatalf("dict %d: %d-byte text read back %d bytes, %v", dict, len(in), len(back), err)
			}
		}
	}
}

// The raw mode still costs its byte after encryption: an uncompressible text
// of n bytes leaves the chain as 1 + n + 28 bytes (the version byte the
// worker adds comes on top), and the capacity estimate counts it.
func TestSmaz2gFinalSizeAfterEncryption(t *testing.T) {
	chain := `[{"type":"smaz2g","params":{"dict":"meshtastic"}},{"type":"encrypt","params":{"key":"` + smaz2gTestKey + `"}}]`
	tp := NewTransformPipeline()
	for _, n := range []int{0, 1, 21, 22, 310, 311} {
		text := []byte(strings.Repeat("0123456789", 32)[:n]) // digits: literals, no bigram or word
		wire, err := tp.ApplyEgress(text, chain)
		if err != nil {
			t.Fatal(err)
		}
		if len(wire) != 1+n+28 {
			t.Fatalf("%d digits leave the chain as %d bytes, want %d", n, len(wire), 1+n+28)
		}
		back, err := tp.ApplyIngress(wire, chain)
		if err != nil || !bytes.Equal(back, text) {
			t.Fatalf("%d digits read back %q, %v", n, back, err)
		}
	}
	// 48 - 28 (GCM) - 1 (mode byte) = 19 usable bytes: under the 20-byte
	// warning line only because the mode byte is counted.
	warnings, errs := ValidateTransforms(chain, true, 48)
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if !containsSubstring(warnings, "~19 bytes of 48") {
		t.Fatalf("capacity warning does not count the mode byte: %v", warnings)
	}
}

func TestSmaz2gDictionaryIsPartOfTheChain(t *testing.T) {
	text := []byte("hello there, the satellite is in range")
	frame := encodeSmaz2g(text, compress.DictMeshtastic)
	back, err := decodeSmaz2g(frame, compress.DictDefault)
	if err == nil && bytes.Equal(back, text) {
		t.Fatal("a meshtastic-dictionary frame read the same with the default dictionary; the test text does not exercise the word table")
	}
}

func TestSmaz2gEncryptedChainAuthenticates(t *testing.T) {
	chain := `[{"type":"smaz2g","params":{"dict":"meshtastic"}},{"type":"encrypt","params":{"key":"` + smaz2gTestKey + `"}}]`
	other := strings.Replace(chain, smaz2gTestKey, "ff"+smaz2gTestKey[2:], 1)
	tp := NewTransformPipeline()
	wire, err := tp.ApplyEgress([]byte("SOS need medical assistance"), chain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tp.ApplyIngress(wire, other); err == nil {
		t.Fatal("another key opened the frame")
	}
	if !TransformsAuthenticate(chain) {
		t.Fatal("an encrypting smaz2g chain must authenticate")
	}
}

func TestValidateTransformsSmaz2g(t *testing.T) {
	chain := `[{"type":"smaz2g"},{"type":"encrypt","params":{"key":"` + smaz2gTestKey + `"}}]`
	warnings, errs := ValidateTransforms(chain, true, 0)
	if len(errs) != 0 {
		t.Fatalf("binary bearer: %v", errs)
	}
	if !containsSubstring(warnings, "read only by a MeshSat Bridge") {
		t.Fatalf("no warning that only a Bridge reads smaz2g: %v", warnings)
	}
	if _, errs := ValidateTransforms(chain, false, 0); len(errs) == 0 {
		t.Fatal("a text bearer took binary smaz2g output without base64")
	}
	// smaz2g alone: its mode byte and SMAZ2 codes are binary, with no
	// encrypt step to make the chain binary for it.
	if _, errs := ValidateTransforms(`[{"type":"smaz2g"}]`, false, 0); len(errs) == 0 {
		t.Fatal("a text bearer took a smaz2g-only chain without base64")
	}
	if _, errs := ValidateTransforms(strings.TrimSuffix(chain, "]")+`,{"type":"base64"}]`, false, 0); len(errs) != 0 {
		t.Fatalf("text bearer with base64 last: %v", errs)
	}
}

func containsSubstring(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// The receiving kit's dispatcher: a frame from a kit whose satellite chain
// dropped base64 (HeMB-NG S3-2), version byte first, comes out as the text
// its own ingress chain opens, and a text bearer carries that text on.
func TestDispatchAccess_BinarySatelliteChainReadsAsText(t *testing.T) {
	chain := `[{"type":"smaz2g","params":{"dict":"meshtastic"}},{"type":"encrypt","params":{"key":"` + smaz2gTestKey + `"}}]`
	opts, _ := json.Marshal(map[string]any{"cell_destinations": []string{"1001"}})
	d, db := cellRuleDispatcher(t, gateway.DefaultCellConfig(), database.AccessRule{
		InterfaceID: "iridium_imt_0", Direction: "ingress", Name: "Satellite to the cell", Enabled: true,
		Priority: 1, Action: "forward", ForwardTo: "cell_0", Filters: "{}", ForwardOptions: string(opts), QoSLevel: 1,
	})
	if err := db.InsertInterface(&database.Interface{ID: "iridium_imt_0", ChannelType: "iridium_imt", Enabled: true,
		Config: "{}", IngressTransforms: chain, EgressTransforms: chain}); err != nil {
		if err := db.SetInterfaceTransforms("iridium_imt_0", chain, chain); err != nil {
			t.Fatal(err)
		}
	}
	tp := NewTransformPipeline()
	d.SetTransformPipeline(tp)
	for _, text := range []string{"SOS need medical assistance", "Καλημέρα από τη βάση", "k"} {
		out, err := tp.ApplyEgress([]byte(text), chain)
		if err != nil {
			t.Fatal(err)
		}
		wire := codec.PrependVersionByte(out)
		if isPrintableASCII(wire[1:]) {
			t.Fatalf("%q: the test frame is text, not the binary a chain without base64 sends", text)
		}
		if n := d.DispatchAccess("iridium_imt_0", rules.RouteMessage{Text: string(wire), From: "300434060000000", PortNum: 1}, wire); n != 1 {
			t.Fatalf("%q: queued %d rows, want 1", text, n)
		}
		rows, err := db.GetPendingDeliveries("cell_0", 10)
		if err != nil || len(rows) != 1 {
			t.Fatalf("%q: rows %d, %v", text, len(rows), err)
		}
		if string(rows[0].Payload) != text {
			t.Fatalf("the cell carries %q, want %q", rows[0].Payload, text)
		}
		if _, err := db.Exec(`DELETE FROM message_deliveries`); err != nil {
			t.Fatal(err)
		}
	}
}

func isPrintableASCII(b []byte) bool {
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}
