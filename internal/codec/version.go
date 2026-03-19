package codec

import "github.com/rs/zerolog/log"

// Protocol version byte support for MeshSat wire protocol future-proofing.
// All MeshSat-originated payloads can optionally start with a version byte.
//
// Detection:
//   - 0x01 = MeshSat protocol v1 (current: SMAZ2, AES-256-GCM, 2-byte frag)
//   - 0x50, 0x44, 0xCA = known magic bytes (no version prefix)
//   - Other values = legacy (no version byte, treat as raw payload)
//
// The version byte is OPTIONAL and backwards compatible — existing devices
// without version bytes work unchanged. New devices prepend it.

const (
	// ProtoVersion1 is the current MeshSat wire protocol version.
	ProtoVersion1 byte = 0x01

	// Known magic bytes that indicate payload type (not a version byte).
	MagicGPSBridgeFull  byte = 0x50 // Bridge full position
	MagicGPSBridgeDelta byte = 0x44 // Bridge delta position
	MagicCannedMessage  byte = 0xCA // Canned military brevity codebook
)

// StripVersionByte checks if a payload starts with a known version byte.
// If so, returns the version and the payload without the version prefix.
// If not (legacy/magic byte), returns version 0 and the original payload.
// A version mismatch is logged as a warning but not rejected.
func StripVersionByte(payload []byte) (version byte, data []byte) {
	if len(payload) == 0 {
		return 0, payload
	}

	first := payload[0]

	// Check if first byte is a known magic (not a version byte).
	switch first {
	case MagicGPSBridgeFull, MagicGPSBridgeDelta, MagicCannedMessage:
		return 0, payload // Known payload type, no version byte
	}

	// Check if it's a known version byte.
	if first == ProtoVersion1 {
		return first, payload[1:]
	}

	// Unknown first byte — treat as legacy (no version).
	return 0, payload
}

// PrependVersionByte adds the current protocol version byte to a payload.
func PrependVersionByte(payload []byte) []byte {
	result := make([]byte, 1+len(payload))
	result[0] = ProtoVersion1
	copy(result[1:], payload)
	return result
}

// LogVersionInfo logs protocol version detection results.
func LogVersionInfo(version byte, source string) {
	if version == 0 {
		log.Debug().Str("source", source).Msg("protocol: legacy message (no version byte)")
	} else if version != ProtoVersion1 {
		log.Warn().Str("source", source).Uint8("version", version).
			Uint8("expected", ProtoVersion1).
			Msg("protocol: version mismatch, processing anyway")
	} else {
		log.Debug().Str("source", source).Uint8("version", version).
			Msg("protocol: version byte detected")
	}
}
