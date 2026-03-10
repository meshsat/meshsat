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
		return smaz2.CompressBytes(data)
	}
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
