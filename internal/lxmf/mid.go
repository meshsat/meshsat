package lxmf

import (
	"encoding/hex"
	"errors"
	"strings"
)

// The message id a MeshSat sender puts in an LXMF message's fields (HeMB-NG
// contract C2): FIELD_CUSTOM_TYPE (0xFB) names the type, FIELD_CUSTOM_DATA
// (0xFC) carries the 16-byte mid as msgpack bin. One mid is one logical
// message across every route and retry, so a receiver keeps a message once
// whatever its LXMF hash (a retry with a new timestamp has a new one). A
// message that already carries another custom type is sent without the mid,
// never over it. Fixture: internal/hembfixtures/testdata/v1/lxmf_mid_field.json.
// [MESHSAT-1641]
const (
	FieldCustomType = 0xFB
	FieldCustomData = 0xFC
	MidType         = "meshsat.mid/1"
)

// MidFields is the fields map that carries only mid.
func MidFields(mid [16]byte) Value {
	return MapOf(KV{Key: Int(FieldCustomType), Val: Str(MidType)}, KV{Key: Int(FieldCustomData), Val: Bin(mid[:])})
}

// WithMid adds mid to a fields map. A map that already has a custom type
// (another one, or a mid) or custom data is returned unchanged, and so is a
// value that is neither a map nor nil (nil becomes MidFields).
func WithMid(fields Value, mid [16]byte) Value {
	switch fields.Kind {
	case KindNil:
		return MidFields(mid)
	case KindMap:
	default:
		return fields
	}
	_, typed := fields.Get(FieldCustomType)
	_, data := fields.Get(FieldCustomData)
	if typed || data {
		return fields
	}
	out := append([]KV(nil), fields.Map...)
	out = append(out, MidFields(mid).Map...)
	return MapOf(out...)
}

// MidOf reads the mid from a received message's fields; false when the
// message carries none (another custom type, a wrong length, no fields).
func MidOf(fields Value) ([16]byte, bool) {
	var mid [16]byte
	t, ok := fields.Get(FieldCustomType)
	if !ok || t.Kind != KindStr || t.Str != MidType {
		return mid, false
	}
	d, ok := fields.Get(FieldCustomData)
	if !ok || d.Kind != KindBin || len(d.Bin) != len(mid) {
		return mid, false
	}
	copy(mid[:], d.Bin)
	return mid, true
}

// ParseMid reads a mid in UUID text form (an intent id).
func ParseMid(s string) ([16]byte, error) {
	var mid [16]byte
	h := strings.ReplaceAll(s, "-", "")
	if len(h) != 32 || len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return mid, errors.New("lxmf: a mid is a UUID")
	}
	if _, err := hex.Decode(mid[:], []byte(h)); err != nil {
		return mid, errors.New("lxmf: a mid is a UUID")
	}
	return mid, nil
}

// FormatMid writes a mid in UUID text form, lower case.
func FormatMid(mid [16]byte) string {
	h := hex.EncodeToString(mid[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
