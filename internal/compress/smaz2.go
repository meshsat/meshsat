package compress

import (
	"github.com/lib-x/smaz2"
)

// Dictionary selects which word table to use for SMAZ2 compression.
type Dictionary int

const (
	DictDefault    Dictionary = iota // Standard English (SMAZ2 default)
	DictMeshtastic                   // Meshtastic field/SAR terms
)

// Compress compresses text using SMAZ2 with the specified dictionary.
func Compress(data []byte, dict Dictionary) []byte {
	if len(data) == 0 {
		return nil
	}
	switch dict {
	case DictMeshtastic:
		return compressMeshtastic(data)
	default:
		return compressDefault(data)
	}
}

// compressDefault is lib-x smaz2 with one fix: a byte 0x01-0x08 goes out as
// its own verbatim run (0x01 then the byte). lib-x wrote it as a literal,
// which every decoder, its own included, reads as an escape (1-5 a verbatim
// count, 6-8 a word), so such text came back as other bytes. Compressing the
// text around each such byte with lib-x is exactly the fixed compressor: no
// word or bigram contains one, and lib-x ends its verbatim run there. Text
// without them compresses byte for byte as before, which receivers that
// check a payload by compressing its text again rely on. [MESHSAT-1626]
func compressDefault(data []byte) []byte {
	var out []byte
	start := 0
	for i, b := range data {
		if b >= 0x01 && b <= 0x08 {
			if start < i {
				out = append(out, smaz2.CompressBytes(data[start:i])...)
			}
			out = append(out, 0x01, b)
			start = i + 1
		}
	}
	if start == 0 {
		return smaz2.CompressBytes(data)
	}
	if start < len(data) {
		out = append(out, smaz2.CompressBytes(data[start:])...)
	}
	return out
}

// Decompress decompresses SMAZ2 data with the specified dictionary.
func Decompress(data []byte, dict Dictionary) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	switch dict {
	case DictMeshtastic:
		return decompressMeshtastic(data)
	default:
		return smaz2.DecompressToBytes(data)
	}
}

// CompressString is a convenience wrapper for string input.
func CompressString(s string, dict Dictionary) []byte {
	return Compress([]byte(s), dict)
}

// DecompressString is a convenience wrapper returning a string.
func DecompressString(data []byte, dict Dictionary) (string, error) {
	b, err := Decompress(data, dict)
	return string(b), err
}
