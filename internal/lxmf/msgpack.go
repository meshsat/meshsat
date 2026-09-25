package lxmf

import "meshsat/internal/msgpack"

// The msgpack value model is re-exported so callers write lxmf.MapOf(),
// lxmf.Bin() and friends without importing the codec package.
type (
	Value = msgpack.Value
	KV    = msgpack.KV
	Kind  = msgpack.Kind
)

const (
	KindNil   = msgpack.KindNil
	KindBool  = msgpack.KindBool
	KindInt   = msgpack.KindInt
	KindUint  = msgpack.KindUint
	KindFloat = msgpack.KindFloat
	KindStr   = msgpack.KindStr
	KindBin   = msgpack.KindBin
	KindArray = msgpack.KindArray
	KindMap   = msgpack.KindMap
	KindExt   = msgpack.KindExt
)

var (
	Nil         = msgpack.Nil
	Bool        = msgpack.Bool
	Float       = msgpack.Float
	Str         = msgpack.Str
	Bin         = msgpack.Bin
	Array       = msgpack.Array
	MapOf       = msgpack.MapOf
	Int         = msgpack.Int
	Pack        = msgpack.Pack
	UnpackValue = msgpack.UnpackValue
)
