package engine

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"meshsat/internal/codec"
	"meshsat/internal/gateway"
)

// HeMB-NG S3-2 measurement: every text through each candidate chain with the
// production TransformPipeline (a synthetic key), the version byte the
// delivery worker puts in front, then each send path's own framing and
// admission (gateway.SBDIntentFrame, SBDLegacyFrame, IMTIntentSize,
// SMSIntentWire, SMSLegacyLimits). Credits: 1 per started 50 bytes of what
// the 9603 sends. IMT is bytes only: no tariff is modelled.

const s3Key = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

type s3Chain struct {
	id, name, json string
	base64         bool
}

func s3Chains() []s3Chain {
	enc := `{"type":"encrypt","params":{"key":"` + s3Key + `"}}`
	m := `{"type":"smaz2","params":{"dict":"meshtastic"}}`
	gm := `{"type":"smaz2g","params":{"dict":"meshtastic"}}`
	return []s3Chain{
		{"A", "smaz2, encrypt, base64 (kits today)", `[{"type":"smaz2"},` + enc + `,{"type":"base64"}]`, true},
		{"B", "smaz2g, encrypt, base64", `[{"type":"smaz2g"},` + enc + `,{"type":"base64"}]`, true},
		{"C", "smaz2, encrypt", `[{"type":"smaz2"},` + enc + `]`, false},
		{"D", "smaz2g, encrypt", `[{"type":"smaz2g"},` + enc + `]`, false},
		{"E", "encrypt", `[` + enc + `]`, false},
		{"F", "encrypt, base64", `[` + enc + `,{"type":"base64"}]`, true},
		{"G", "smaz2 meshtastic, encrypt", `[` + m + `,` + enc + `]`, false},
		{"H", "smaz2g meshtastic, encrypt", `[` + gm + `,` + enc + `]`, false},
		{"I", "smaz2 meshtastic, encrypt, base64", `[` + m + `,` + enc + `,{"type":"base64"}]`, true},
		{"J", "smaz2g meshtastic, encrypt, base64", `[` + gm + `,` + enc + `,{"type":"base64"}]`, true},
	}
}

// s3Path is one send path's result for one wire.
type s3Path struct {
	admitted bool
	bytes    int // what the modem sends (SBD, IMT) or the SMS characters
	credits  int // SBD only
	cut      bool
}

func s3Credits(n int) int { return (n + 49) / 50 }

type s3Row struct {
	transform                                                              int
	imtLegacy, imtIntent, sbdIntent, sbdIntentPos, sbdLegacy, sbdLegacyPos s3Path
	smsIntent, smsLegacy                                                   s3Path
}

func s3Frame(t *testing.T, tp *TransformPipeline, c s3Chain, text []byte) s3Row {
	t.Helper()
	out, err := tp.ApplyEgress(text, c.json)
	if err != nil {
		t.Fatalf("%s: %v", c.id, err)
	}
	wire := codec.PrependVersionByte(out)
	r := s3Row{transform: len(wire)}
	r.imtLegacy = s3Path{admitted: true, bytes: len(wire)}
	if reason, _ := gateway.IMTIntentSize(wire); reason == "" {
		r.imtIntent = s3Path{admitted: true, bytes: len(wire)}
	}
	for _, p := range []struct {
		pos         bool
		intent, leg *s3Path
	}{{false, &r.sbdIntent, &r.sbdLegacy}, {true, &r.sbdIntentPos, &r.sbdLegacyPos}} {
		cfg := gateway.IridiumConfig{IncludePosition: p.pos}
		if mo, _, reason, _ := gateway.SBDIntentFrame(cfg, wire); reason == "" {
			*p.intent = s3Path{admitted: true, bytes: len(mo), credits: s3Credits(len(mo))}
		}
		data, _, cut, err := gateway.SBDLegacyFrame(cfg, wire)
		if err != nil {
			t.Fatal(err)
		}
		// A cut frame leaves (and is billed) but its ciphertext cannot be
		// opened: counted, flagged, never admitted.
		*p.leg = s3Path{admitted: !cut, bytes: len(data), credits: s3Credits(len(data)), cut: cut}
	}
	if c.base64 {
		if sms, reason, _ := gateway.SMSIntentWire(wire, true); reason == "" {
			r.smsIntent = s3Path{admitted: true, bytes: len(sms)}
		}
		whole, _ := gateway.SMSLegacyLimits(true, 1) // the kits: USB modem in text mode, max_sms_segments 1
		r.smsLegacy = s3Path{admitted: len(wire) <= whole, bytes: len(wire)}
	}
	return r
}

// The boundaries every number in the report rests on, on the real framing.
func TestS3FramingBoundaries(t *testing.T) {
	wireOf := func(n int) []byte { return codec.PrependVersionByte([]byte(strings.Repeat("A", n-1))) }
	for _, c := range []struct {
		chain, modem, credits int
		admitted              bool
	}{{39, 50, 1, true}, {40, 51, 2, true}, {89, 100, 2, true}, {90, 101, 3, true}, {255, 266, 6, true}, {256, 0, 0, false}} {
		mo, text, reason, _ := gateway.SBDIntentFrame(gateway.IridiumConfig{}, wireOf(c.chain))
		if (reason == "") != c.admitted || text || len(mo) != c.modem || (c.admitted && s3Credits(len(mo)) != c.credits) {
			t.Fatalf("SBD intent, %d chain bytes: %d modem bytes, credits %d, refusal %q", c.chain, len(mo), s3Credits(len(mo)), reason)
		}
	}
	if mo, _, reason, _ := gateway.SBDIntentFrame(gateway.IridiumConfig{IncludePosition: true}, wireOf(100)); reason != "" || len(mo) != 121 {
		t.Fatalf("SBD intent with position: %d bytes, %q", len(mo), reason)
	}
	if data, _, cut, _ := gateway.SBDLegacyFrame(gateway.IridiumConfig{}, wireOf(256)); !cut || len(data) != 266 {
		t.Fatalf("SBD legacy, 256 bytes: %d sent, cut %v", len(data), cut)
	}
	if _, _, cut, _ := gateway.SBDLegacyFrame(gateway.IridiumConfig{}, wireOf(255)); cut {
		t.Fatal("SBD legacy cut 255 bytes")
	}
	b64 := func(n int) []byte { return codec.PrependVersionByte([]byte(strings.Repeat("Q", n-1))) }
	if s, reason, _ := gateway.SMSIntentWire(b64(161), true); reason != "" || len(s) != 160 {
		t.Fatalf("SMS intent, 161: %d chars, %q", len(s), reason)
	}
	if _, reason, _ := gateway.SMSIntentWire(b64(162), true); reason != "skipped_oversize" {
		t.Fatalf("SMS intent, 162: %q", reason)
	}
	if whole, _ := gateway.SMSLegacyLimits(true, 1); whole != 160 {
		t.Fatalf("SMS legacy whole %d", whole)
	}
	if whole, maxLen := gateway.SMSLegacyLimits(true, 3); whole != 160 || maxLen != 480 {
		t.Fatalf("SMS legacy, text mode, 3 segments: whole %d, cut at %d", whole, maxLen)
	}
	if whole, _ := gateway.SMSLegacyLimits(false, 3); whole != 480 {
		t.Fatalf("SMS legacy, PDU modem, 3 segments: whole %d", whole)
	}
	if data, _, cut, _ := gateway.SBDLegacyFrame(gateway.IridiumConfig{IncludePosition: true}, wireOf(100)); cut || len(data) != 121 {
		t.Fatalf("SBD legacy with position: %d bytes, cut %v", len(data), cut)
	}
	if data, text, _, _ := gateway.SBDLegacyFrame(gateway.IridiumConfig{}, []byte("on my way")); !text || string(data) != "on my way" {
		t.Fatalf("SBD legacy, short ASCII: %q text=%v", data, text)
	}
	if reason, _ := gateway.IMTIntentSize(make([]byte, 1444)); reason != "" {
		t.Fatal("IMT intent refused one full segment")
	}
	if reason, _ := gateway.IMTIntentSize(make([]byte, 1445)); reason != "skipped_oversize" {
		t.Fatal("IMT intent took more than one segment")
	}
}

// Public synthetic texts, reproducible anywhere: the table in the report.
var s3Synthetic = [][2]string{
	{"en", "Team at the north gate, all safe, moving to camp"},
	{"en-long", "Water level at the bridge is rising, road to the shelter is blocked, we need a boat and medical assistance for two people, send position update every hour"},
	{"nl", "Team bij de noordpoort, iedereen veilig, op weg naar kamp"},
	{"el", "Η ομάδα είναι ασφαλής στη βόρεια πύλη, κινούμαστε"},
	{"emoji", "OK \U0001F44D back at base \U0001F3D5"},
	{"sos", "SOS 52.1601,4.4970 need medical assistance"},
	{"ascii-1", "k"},
	{"ascii-10", "on my way!"},
}

func TestS3FramingSynthetic(t *testing.T) {
	tp := NewTransformPipeline()
	for _, s := range s3Synthetic {
		var cells []string
		for _, c := range s3Chains() {
			r := s3Frame(t, tp, c, []byte(s[1]))
			cell := fmt.Sprintf("%s %d/imt %d/sbd %d %dcr", c.id, r.transform, r.imtLegacy.bytes, r.sbdIntent.bytes, r.sbdIntent.credits)
			if !r.sbdIntent.admitted {
				cell = fmt.Sprintf("%s %d/imt %d/sbd refused", c.id, r.transform, r.imtLegacy.bytes)
			}
			if c.base64 {
				cell += fmt.Sprintf("/sms %d %v", r.smsIntent.bytes, r.smsIntent.admitted)
			}
			cells = append(cells, cell)
			if c.id == "H" {
				raw := len(s[1])
				if r.transform > 1+1+raw+28 {
					t.Fatalf("%s: H is %d bytes, more than one byte over the text's %d", s[0], r.transform, raw)
				}
			}
		}
		t.Logf("%-8s raw %3d | %s", s[0], len(s[1]), strings.Join(cells, " | "))
	}
}

// The corpus replay: MESHSAT_S3_CORPUS names a JSON list of texts (kept
// off the repository), MESHSAT_S3_OUT the per-message CSV (lengths only).
func TestS3FramingCorpus(t *testing.T) {
	in, out := os.Getenv("MESHSAT_S3_CORPUS"), os.Getenv("MESHSAT_S3_OUT")
	if in == "" || out == "" {
		t.Skip("set MESHSAT_S3_CORPUS and MESHSAT_S3_OUT")
	}
	b, err := os.ReadFile(in)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	if err := json.Unmarshal(b, &texts); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write([]string{"i", "raw", "chain", "transform_bytes",
		"imt_bytes", "imt_intent_admitted",
		"sbd_intent_admitted", "sbd_intent_bytes", "sbd_intent_credits",
		"sbd_intent_pos_admitted", "sbd_intent_pos_bytes", "sbd_intent_pos_credits",
		"sbd_legacy_bytes", "sbd_legacy_credits", "sbd_legacy_cut",
		"sms_intent_admitted", "sms_intent_chars", "sms_legacy_admitted", "sms_legacy_chars"})
	tp := NewTransformPipeline()
	chains := s3Chains()
	rows := make([][]s3Row, len(texts))
	for i, text := range texts {
		rows[i] = make([]s3Row, len(chains))
		for ci, c := range chains {
			r := s3Frame(t, tp, c, []byte(text))
			rows[i][ci] = r
			yn := func(p s3Path) string { return strconv.FormatBool(p.admitted) }
			rec := []string{strconv.Itoa(i), strconv.Itoa(len(text)), c.id, strconv.Itoa(r.transform),
				strconv.Itoa(r.imtLegacy.bytes), yn(r.imtIntent),
				yn(r.sbdIntent), strconv.Itoa(r.sbdIntent.bytes), strconv.Itoa(r.sbdIntent.credits),
				yn(r.sbdIntentPos), strconv.Itoa(r.sbdIntentPos.bytes), strconv.Itoa(r.sbdIntentPos.credits),
				strconv.Itoa(r.sbdLegacy.bytes), strconv.Itoa(r.sbdLegacy.credits), strconv.FormatBool(r.sbdLegacy.cut)}
			if c.base64 {
				rec = append(rec, yn(r.smsIntent), strconv.Itoa(r.smsIntent.bytes), yn(r.smsLegacy), strconv.Itoa(r.smsLegacy.bytes))
			} else {
				rec = append(rec, "", "", "", "")
			}
			_ = w.Write(rec)
		}
	}
	w.Flush()

	raws := make([]int, len(texts))
	nonASCII := 0
	for i, s := range texts {
		raws[i] = len(s)
		for j := 0; j < len(s); j++ {
			if s[j] >= 0x80 {
				nonASCII++
				break
			}
		}
	}
	sort.Ints(raws)
	q := func(p float64) int { return raws[int(p*float64(len(raws)-1))] }
	t.Logf("corpus n=%d non-ASCII=%d raw median=%d p90=%d p99=%d max=%d", len(texts), nonASCII, q(.5), q(.9), q(.99), q(1))

	pick := func(r s3Row, path string) s3Path {
		switch path {
		case "imt":
			return r.imtLegacy
		case "sbd_intent":
			return r.sbdIntent
		case "sbd_intent_pos":
			return r.sbdIntentPos
		case "sbd_legacy":
			return r.sbdLegacy
		case "sms_intent":
			return r.smsIntent
		case "sms_legacy":
			return r.smsLegacy
		}
		return s3Path{}
	}
	for _, path := range []string{"imt", "sbd_legacy", "sbd_intent", "sbd_intent_pos", "sms_legacy", "sms_intent"} {
		for ci, c := range chains {
			if strings.HasPrefix(path, "sms") && !c.base64 {
				continue
			}
			adm, ref, cut, by, cr := 0, 0, 0, 0, 0
			for i := range texts {
				p := pick(rows[i][ci], path)
				if p.cut {
					cut++
				}
				if !p.admitted {
					ref++
					continue
				}
				adm++
				by += p.bytes
				cr += p.credits
			}
			// Matched cohort against A: texts both chains admit.
			mA, mC, both, onlyC, onlyA := 0, 0, 0, 0, 0
			for i := range texts {
				pa, pc := pick(rows[i][0], path), pick(rows[i][ci], path)
				switch {
				case pa.admitted && pc.admitted:
					both++
					if strings.HasPrefix(path, "sbd") {
						mA += pa.credits
						mC += pc.credits
					} else {
						mA += pa.bytes
						mC += pc.bytes
					}
				case pc.admitted:
					onlyC++
				case pa.admitted:
					onlyA++
				}
			}
			unit := "bytes"
			if strings.HasPrefix(path, "sbd") {
				unit = "credits"
			} else if strings.HasPrefix(path, "sms") {
				unit = "chars"
			}
			delta := 0.0
			if mA > 0 {
				delta = 100 * float64(mC-mA) / float64(mA)
			}
			t.Logf("%-14s %s %-36s admitted=%d refused=%d cut=%d bytes=%d credits=%d | vs A on %d both-admitted: %s %d -> %d (%+.2f%%), only this %d, only A %d",
				path, c.id, c.name, adm, ref, cut, by, cr, both, unit, mA, mC, delta, onlyC, onlyA)
		}
	}
	// G against H on the texts both admit, per path.
	for _, path := range []string{"imt", "sbd_intent"} {
		g, h, n := 0, 0, 0
		for i := range texts {
			pg, ph := pick(rows[i][6], path), pick(rows[i][7], path)
			if pg.admitted && ph.admitted {
				n++
				if path == "imt" {
					g, h = g+pg.bytes, h+ph.bytes
				} else {
					g, h = g+pg.credits, h+ph.credits
				}
			}
		}
		t.Logf("G vs H, %s, %d both-admitted: G %d, H %d", path, n, g, h)
	}
}
