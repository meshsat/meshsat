// Package msgpack is a msgpack codec byte-identical to RNS.vendor.umsgpack
// for the value shapes Reticulum and LXMF use. LXMF re-encodes a decoded
// payload and hashes the re-encoding, so the encoder must produce exactly
// what umsgpack produces. [MESHSAT-1348]
package msgpack

// Rules: positive fixint / uint8 / uint16 / uint32 / uint64 for
// non-negative integers, negative fixint / int8..int64 for negative ones,
// float64 (0xCB) for floats, str for text, bin for bytes,
// fixarray/array16/32, and maps in insertion order.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Value is one msgpack value. Exactly one of the shapes is meaningful; Kind
// says which.
type Value struct {
	Kind  Kind
	Bool  bool
	Int   int64  // Kind == KindInt (negative or fits int64)
	Uint  uint64 // Kind == KindUint (non-negative)
	Float float64
	Str   string
	Bin   []byte
	Array []Value
	Map   []KV   // insertion-ordered
	Ext   []byte // raw bytes of an ext value, re-emitted verbatim
}

// KV is one map entry.
type KV struct {
	Key, Val Value
}

// Kind is the value shape.
type Kind int

const (
	KindNil Kind = iota
	KindBool
	KindInt
	KindUint
	KindFloat
	KindStr
	KindBin
	KindArray
	KindMap
	KindExt
)

// Constructors.
func Nil() Value             { return Value{Kind: KindNil} }
func Bool(b bool) Value      { return Value{Kind: KindBool, Bool: b} }
func Float(f float64) Value  { return Value{Kind: KindFloat, Float: f} }
func Str(s string) Value     { return Value{Kind: KindStr, Str: s} }
func Bin(b []byte) Value     { return Value{Kind: KindBin, Bin: b} }
func Array(v ...Value) Value { return Value{Kind: KindArray, Array: v} }
func MapOf(kv ...KV) Value   { return Value{Kind: KindMap, Map: kv} }
func Int(i int64) Value {
	if i >= 0 {
		return Value{Kind: KindUint, Uint: uint64(i)}
	}
	return Value{Kind: KindInt, Int: i}
}

// AsInt returns an integer value of either sign kind.
func (v Value) AsInt() (int64, bool) {
	switch v.Kind {
	case KindInt:
		return v.Int, true
	case KindUint:
		if v.Uint <= math.MaxInt64 {
			return int64(v.Uint), true
		}
	}
	return 0, false
}

// Get looks a key up in a map value by integer key.
func (v Value) Get(key int64) (Value, bool) {
	for _, kv := range v.Map {
		if k, ok := kv.Key.AsInt(); ok && k == key {
			return kv.Val, true
		}
	}
	return Value{}, false
}

// Pack encodes a value.
func Pack(v Value) []byte {
	var out []byte
	return appendValue(out, v)
}

func appendValue(out []byte, v Value) []byte {
	switch v.Kind {
	case KindNil:
		return append(out, 0xC0)
	case KindBool:
		if v.Bool {
			return append(out, 0xC3)
		}
		return append(out, 0xC2)
	case KindUint:
		u := v.Uint
		switch {
		case u < 128:
			return append(out, byte(u))
		case u < 1<<8:
			return append(out, 0xCC, byte(u))
		case u < 1<<16:
			return binary.BigEndian.AppendUint16(append(out, 0xCD), uint16(u))
		case u < 1<<32:
			return binary.BigEndian.AppendUint32(append(out, 0xCE), uint32(u))
		default:
			return binary.BigEndian.AppendUint64(append(out, 0xCF), u)
		}
	case KindInt:
		i := v.Int
		if i >= 0 {
			return appendValue(out, Value{Kind: KindUint, Uint: uint64(i)})
		}
		switch {
		case i >= -32:
			return append(out, byte(int8(i)))
		case i >= math.MinInt8:
			return append(out, 0xD0, byte(int8(i)))
		case i >= math.MinInt16:
			return binary.BigEndian.AppendUint16(append(out, 0xD1), uint16(int16(i)))
		case i >= math.MinInt32:
			return binary.BigEndian.AppendUint32(append(out, 0xD2), uint32(int32(i)))
		default:
			return binary.BigEndian.AppendUint64(append(out, 0xD3), uint64(i))
		}
	case KindFloat:
		return binary.BigEndian.AppendUint64(append(out, 0xCB), math.Float64bits(v.Float))
	case KindStr:
		n := len(v.Str)
		switch {
		case n < 32:
			out = append(out, 0xA0|byte(n))
		case n < 1<<8:
			out = append(out, 0xD9, byte(n))
		case n < 1<<16:
			out = binary.BigEndian.AppendUint16(append(out, 0xDA), uint16(n))
		default:
			out = binary.BigEndian.AppendUint32(append(out, 0xDB), uint32(n))
		}
		return append(out, v.Str...)
	case KindBin:
		n := len(v.Bin)
		switch {
		case n < 1<<8:
			out = append(out, 0xC4, byte(n))
		case n < 1<<16:
			out = binary.BigEndian.AppendUint16(append(out, 0xC5), uint16(n))
		default:
			out = binary.BigEndian.AppendUint32(append(out, 0xC6), uint32(n))
		}
		return append(out, v.Bin...)
	case KindArray:
		n := len(v.Array)
		switch {
		case n < 16:
			out = append(out, 0x90|byte(n))
		case n < 1<<16:
			out = binary.BigEndian.AppendUint16(append(out, 0xDC), uint16(n))
		default:
			out = binary.BigEndian.AppendUint32(append(out, 0xDD), uint32(n))
		}
		for _, e := range v.Array {
			out = appendValue(out, e)
		}
		return out
	case KindMap:
		n := len(v.Map)
		switch {
		case n < 16:
			out = append(out, 0x80|byte(n))
		case n < 1<<16:
			out = binary.BigEndian.AppendUint16(append(out, 0xDE), uint16(n))
		default:
			out = binary.BigEndian.AppendUint32(append(out, 0xDF), uint32(n))
		}
		for _, kv := range v.Map {
			out = appendValue(out, kv.Key)
			out = appendValue(out, kv.Val)
		}
		return out
	case KindExt:
		return append(out, v.Ext...)
	}
	return out
}

// ErrMsgpack is returned for malformed input.
var ErrMsgpack = errors.New("msgpack: malformed input")

// UnpackValue decodes one value and returns it with the number of bytes consumed.
func UnpackValue(b []byte) (Value, int, error) {
	d := &decoder{b: b}
	v, err := d.value()
	if err != nil {
		return Value{}, 0, err
	}
	return v, d.pos, nil
}

type decoder struct {
	b   []byte
	pos int
}

func (d *decoder) need(n int) ([]byte, error) {
	if d.pos+n > len(d.b) {
		return nil, ErrMsgpack
	}
	out := d.b[d.pos : d.pos+n]
	d.pos += n
	return out, nil
}

func (d *decoder) value() (Value, error) {
	hb, err := d.need(1)
	if err != nil {
		return Value{}, err
	}
	h := hb[0]
	switch {
	case h <= 0x7F:
		return Value{Kind: KindUint, Uint: uint64(h)}, nil
	case h >= 0xE0:
		return Value{Kind: KindInt, Int: int64(int8(h))}, nil
	case h&0xE0 == 0xA0:
		s, err := d.need(int(h & 0x1F))
		return Value{Kind: KindStr, Str: string(s)}, err
	case h&0xF0 == 0x90:
		return d.array(int(h & 0x0F))
	case h&0xF0 == 0x80:
		return d.mapv(int(h & 0x0F))
	}
	switch h {
	case 0xC0:
		return Value{Kind: KindNil}, nil
	case 0xC2:
		return Value{Kind: KindBool}, nil
	case 0xC3:
		return Value{Kind: KindBool, Bool: true}, nil
	case 0xC4, 0xC5, 0xC6:
		n, err := d.length(h - 0xC4)
		if err != nil {
			return Value{}, err
		}
		b, err := d.need(n)
		return Value{Kind: KindBin, Bin: append([]byte(nil), b...)}, err
	case 0xCA:
		b, err := d.need(4)
		if err != nil {
			return Value{}, err
		}
		return Value{Kind: KindFloat, Float: float64(math.Float32frombits(binary.BigEndian.Uint32(b)))}, nil
	case 0xCB:
		b, err := d.need(8)
		if err != nil {
			return Value{}, err
		}
		return Value{Kind: KindFloat, Float: math.Float64frombits(binary.BigEndian.Uint64(b))}, nil
	case 0xCC, 0xCD, 0xCE, 0xCF:
		n := 1 << (h - 0xCC)
		b, err := d.need(n)
		if err != nil {
			return Value{}, err
		}
		var u uint64
		for _, c := range b {
			u = u<<8 | uint64(c)
		}
		return Value{Kind: KindUint, Uint: u}, nil
	case 0xD0, 0xD1, 0xD2, 0xD3:
		n := 1 << (h - 0xD0)
		b, err := d.need(n)
		if err != nil {
			return Value{}, err
		}
		var u uint64
		for _, c := range b {
			u = u<<8 | uint64(c)
		}
		shift := uint(64 - 8*n)
		return Value{Kind: KindInt, Int: int64(u<<shift) >> shift}, nil
	case 0xD9, 0xDA, 0xDB:
		n, err := d.length(h - 0xD9)
		if err != nil {
			return Value{}, err
		}
		s, err := d.need(n)
		return Value{Kind: KindStr, Str: string(s)}, err
	case 0xDC, 0xDD:
		n, err := d.length(h - 0xDC + 1)
		if err != nil {
			return Value{}, err
		}
		return d.array(n)
	case 0xDE, 0xDF:
		n, err := d.length(h - 0xDE + 1)
		if err != nil {
			return Value{}, err
		}
		return d.mapv(n)
	case 0xD4, 0xD5, 0xD6, 0xD7, 0xD8:
		n := 1 << (h - 0xD4)
		start := d.pos - 1
		if _, err := d.need(1 + n); err != nil {
			return Value{}, err
		}
		return Value{Kind: KindExt, Ext: append([]byte(nil), d.b[start:d.pos]...)}, nil
	case 0xC7, 0xC8, 0xC9:
		start := d.pos - 1
		n, err := d.length(h - 0xC7)
		if err != nil {
			return Value{}, err
		}
		if _, err := d.need(1 + n); err != nil {
			return Value{}, err
		}
		return Value{Kind: KindExt, Ext: append([]byte(nil), d.b[start:d.pos]...)}, nil
	}
	return Value{}, fmt.Errorf("%w: unsupported type byte 0x%02x", ErrMsgpack, h)
}

func (d *decoder) length(size byte) (int, error) {
	n := 1 << size
	b, err := d.need(n)
	if err != nil {
		return 0, err
	}
	var u uint64
	for _, c := range b {
		u = u<<8 | uint64(c)
	}
	if u > 1<<31 {
		return 0, ErrMsgpack
	}
	return int(u), nil
}

func (d *decoder) array(n int) (Value, error) {
	out := Value{Kind: KindArray, Array: make([]Value, 0, min(n, 1024))}
	for i := 0; i < n; i++ {
		e, err := d.value()
		if err != nil {
			return Value{}, err
		}
		out.Array = append(out.Array, e)
	}
	return out, nil
}

func (d *decoder) mapv(n int) (Value, error) {
	out := Value{Kind: KindMap, Map: make([]KV, 0, min(n, 1024))}
	for i := 0; i < n; i++ {
		k, err := d.value()
		if err != nil {
			return Value{}, err
		}
		v, err := d.value()
		if err != nil {
			return Value{}, err
		}
		out.Map = append(out.Map, KV{Key: k, Val: v})
	}
	return out, nil
}
