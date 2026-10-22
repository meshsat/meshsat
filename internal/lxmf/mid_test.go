package lxmf_test

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"meshsat/internal/lxmf"
)

func midFixture(t *testing.T) (fieldsHex, mid string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "hembfixtures", "testdata", "v1", "lxmf_mid_field.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		FieldsHex string `json:"fields_msgpack_hex"`
		Mid       string `json:"mid"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f.FieldsHex, f.Mid
}

// The fields map is the fixture's bytes both ways (contract C2). [MESHSAT-1641]
func TestMidFieldsAreTheFixtureBytes(t *testing.T) {
	fieldsHex, midText := midFixture(t)
	mid, err := lxmf.ParseMid(midText)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(lxmf.Pack(lxmf.MidFields(mid))); got != fieldsHex {
		t.Fatalf("packed %s\nwant   %s", got, fieldsHex)
	}
	raw, _ := hex.DecodeString(fieldsHex)
	v, n, err := lxmf.UnpackValue(raw)
	if err != nil || n != len(raw) {
		t.Fatalf("unpack: %v (%d of %d)", err, n, len(raw))
	}
	got, ok := lxmf.MidOf(v)
	if !ok || lxmf.FormatMid(got) != midText {
		t.Fatalf("mid %s %v, want %s", lxmf.FormatMid(got), ok, midText)
	}
}

func TestWithMidNeverOverwritesAnotherCustomType(t *testing.T) {
	var mid [16]byte
	mid[0] = 1
	other := lxmf.MapOf(lxmf.KV{Key: lxmf.Int(lxmf.FieldCustomType), Val: lxmf.Str("someone.else/2")},
		lxmf.KV{Key: lxmf.Int(lxmf.FieldCustomData), Val: lxmf.Bin([]byte("theirs"))})
	if got := lxmf.WithMid(other, mid); hex.EncodeToString(lxmf.Pack(got)) != hex.EncodeToString(lxmf.Pack(other)) {
		t.Fatal("another custom type was overwritten")
	}
	if _, ok := lxmf.MidOf(other); ok {
		t.Fatal("another custom type read as a mid")
	}
	dataOnly := lxmf.MapOf(lxmf.KV{Key: lxmf.Int(lxmf.FieldCustomData), Val: lxmf.Bin([]byte("x"))})
	if got := lxmf.WithMid(dataOnly, mid); len(got.Map) != 1 {
		t.Fatal("custom data without a type was overwritten")
	}
	// Other fields stay, in order, and the mid follows them.
	plain := lxmf.MapOf(lxmf.KV{Key: lxmf.Int(0x01), Val: lxmf.Str("e")})
	got := lxmf.WithMid(plain, mid)
	if len(got.Map) != 3 {
		t.Fatalf("%d entries", len(got.Map))
	}
	if m, ok := lxmf.MidOf(got); !ok || m != mid {
		t.Fatal("mid not added beside the other field")
	}
	if got := lxmf.WithMid(lxmf.Nil(), mid); hex.EncodeToString(lxmf.Pack(got)) != hex.EncodeToString(lxmf.Pack(lxmf.MidFields(mid))) {
		t.Fatal("nil fields did not become the mid fields")
	}
	short := lxmf.MapOf(lxmf.KV{Key: lxmf.Int(lxmf.FieldCustomType), Val: lxmf.Str(lxmf.MidType)},
		lxmf.KV{Key: lxmf.Int(lxmf.FieldCustomData), Val: lxmf.Bin(make([]byte, 15))})
	if _, ok := lxmf.MidOf(short); ok {
		t.Fatal("a 15-byte mid was taken")
	}
}

func TestParseMid(t *testing.T) {
	for _, bad := range []string{"", "01a14f3166807a0b8c0d0e0f10111213", "01a14f31-6680-7a0b-8c0d-0e0f1011121z", "01a14f31-6680-7a0b-8c0d0-e0f10111213"} {
		if _, err := lxmf.ParseMid(bad); err == nil {
			t.Fatalf("%q parsed", bad)
		}
	}
	m, err := lxmf.ParseMid("01A14F31-6680-7A0B-8C0D-0E0F10111213")
	if err != nil || lxmf.FormatMid(m) != "01a14f31-6680-7a0b-8c0d-0e0f10111213" {
		t.Fatalf("%v %s", err, lxmf.FormatMid(m))
	}
}
