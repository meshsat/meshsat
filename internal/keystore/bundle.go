package keystore

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

const (
	bundleVersionV1 = 0x01
	bundleVersionV2 = 0x02 // adds 32-byte signing pubkey after header
	bundleURLScheme = "meshsat://key/"
	maxEntries      = 8
	destHashLen     = 16
	signatureLen    = 64
	aesKeyLen       = 32
	pubKeyLen       = 32
)

// Channel type enum — matches Android ConversationKey types.
const (
	ChannelSMS     byte = 0x00
	ChannelMesh    byte = 0x01
	ChannelIridium byte = 0x02
	ChannelZigBee  byte = 0x04
	ChannelMQTT    byte = 0x05
	ChannelWebhook byte = 0x06
)

// ChannelTypeToByte maps string channel types to bundle enum values.
func ChannelTypeToByte(ct string) byte {
	switch ct {
	case "sms":
		return ChannelSMS
	case "mesh":
		return ChannelMesh
	case "iridium":
		return ChannelIridium
	case "zigbee":
		return ChannelZigBee
	case "mqtt":
		return ChannelMQTT
	case "webhook":
		return ChannelWebhook
	default:
		return 0xFF
	}
}

// ByteToChannelType maps bundle enum values to string channel types.
func ByteToChannelType(b byte) string {
	switch b {
	case ChannelSMS:
		return "sms"
	case ChannelMesh:
		return "mesh"
	case ChannelIridium:
		return "iridium"
	case ChannelZigBee:
		return "zigbee"
	case ChannelMQTT:
		return "mqtt"
	case ChannelWebhook:
		return "webhook"
	default:
		return "unknown"
	}
}

// KeyBundle is a signed collection of channel encryption keys.
type KeyBundle struct {
	Version    byte
	BridgeHash [destHashLen]byte
	Timestamp  uint32
	Entries    []BundleEntry
	SigningPub []byte // v2 only: 32-byte Ed25519 public key (nil for v1)
	Signature  [signatureLen]byte
}

// BundleEntry is a single channel key within a bundle.
type BundleEntry struct {
	ChannelType byte
	Address     string
	Key         [aesKeyLen]byte
}

// Signer signs data with an Ed25519 private key.
type Signer interface {
	Sign(data []byte) []byte
}

// MarshalBundle serializes a v1 key bundle to compact binary and signs it.
// Deprecated: use MarshalBundleV2 which embeds the signing public key.
func MarshalBundle(bridgeHash [destHashLen]byte, entries []BundleEntry, signer Signer) ([]byte, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("no entries")
	}
	if len(entries) > maxEntries {
		return nil, fmt.Errorf("too many entries (max %d)", maxEntries)
	}

	// Header: version(1) + bridgeHash(16) + timestamp(4) + entryCount(1) = 22 bytes
	header := make([]byte, 22)
	header[0] = bundleVersionV1
	copy(header[1:17], bridgeHash[:])
	binary.BigEndian.PutUint32(header[17:21], uint32(time.Now().Unix()))
	header[21] = byte(len(entries))

	entryData := marshalEntries(entries)
	if entryData == nil {
		return nil, fmt.Errorf("address too long")
	}

	// Sign header + entries
	sigData := append(header, entryData...)
	sig := signer.Sign(sigData)

	// Final: header + signature(64) + entries
	result := make([]byte, 0, len(header)+signatureLen+len(entryData))
	result = append(result, header...)
	result = append(result, sig...)
	result = append(result, entryData...)

	return result, nil
}

// MarshalBundleV2 serializes a v2 key bundle with embedded signing public key.
// Format: header(22) + signingPub(32) + signature(64) + entries(variable).
// Signed data: header || signingPub || entries.
func MarshalBundleV2(bridgeHash [destHashLen]byte, entries []BundleEntry, signer Signer, signingPub ed25519.PublicKey) ([]byte, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("no entries")
	}
	if len(entries) > maxEntries {
		return nil, fmt.Errorf("too many entries (max %d)", maxEntries)
	}
	if len(signingPub) != pubKeyLen {
		return nil, fmt.Errorf("signing public key must be %d bytes", pubKeyLen)
	}

	header := make([]byte, 22)
	header[0] = bundleVersionV2
	copy(header[1:17], bridgeHash[:])
	binary.BigEndian.PutUint32(header[17:21], uint32(time.Now().Unix()))
	header[21] = byte(len(entries))

	entryData := marshalEntries(entries)
	if entryData == nil {
		return nil, fmt.Errorf("address too long")
	}

	// Sign: header + pubkey + entries
	sigData := make([]byte, 0, len(header)+pubKeyLen+len(entryData))
	sigData = append(sigData, header...)
	sigData = append(sigData, signingPub...)
	sigData = append(sigData, entryData...)
	sig := signer.Sign(sigData)

	// Final: header + pubkey(32) + signature(64) + entries
	result := make([]byte, 0, len(header)+pubKeyLen+signatureLen+len(entryData))
	result = append(result, header...)
	result = append(result, signingPub...)
	result = append(result, sig...)
	result = append(result, entryData...)

	return result, nil
}

// marshalEntries serializes bundle entries to binary. Returns nil if any address is too long.
func marshalEntries(entries []BundleEntry) []byte {
	var entryData []byte
	for _, e := range entries {
		addrBytes := []byte(e.Address)
		if len(addrBytes) > 255 {
			return nil
		}
		entryData = append(entryData, e.ChannelType)
		entryData = append(entryData, byte(len(addrBytes)))
		entryData = append(entryData, addrBytes...)
		entryData = append(entryData, e.Key[:]...)
	}
	return entryData
}

// UnmarshalBundle parses a compact binary key bundle (v1 or v2).
func UnmarshalBundle(data []byte) (*KeyBundle, error) {
	if len(data) < 22+signatureLen {
		return nil, fmt.Errorf("bundle too short")
	}

	b := &KeyBundle{
		Version: data[0],
	}

	copy(b.BridgeHash[:], data[1:17])
	b.Timestamp = binary.BigEndian.Uint32(data[17:21])
	entryCount := int(data[21])

	var offset int
	switch b.Version {
	case bundleVersionV1:
		copy(b.Signature[:], data[22:22+signatureLen])
		offset = 22 + signatureLen
	case bundleVersionV2:
		if len(data) < 22+pubKeyLen+signatureLen {
			return nil, fmt.Errorf("v2 bundle too short")
		}
		b.SigningPub = make([]byte, pubKeyLen)
		copy(b.SigningPub, data[22:22+pubKeyLen])
		copy(b.Signature[:], data[22+pubKeyLen:22+pubKeyLen+signatureLen])
		offset = 22 + pubKeyLen + signatureLen
	default:
		return nil, fmt.Errorf("unsupported bundle version: %d", b.Version)
	}

	for i := 0; i < entryCount; i++ {
		if offset+2 > len(data) {
			return nil, fmt.Errorf("truncated entry %d", i)
		}
		ct := data[offset]
		addrLen := int(data[offset+1])
		offset += 2

		if offset+addrLen+aesKeyLen > len(data) {
			return nil, fmt.Errorf("truncated entry %d data", i)
		}

		var entry BundleEntry
		entry.ChannelType = ct
		entry.Address = string(data[offset : offset+addrLen])
		offset += addrLen
		copy(entry.Key[:], data[offset:offset+aesKeyLen])
		offset += aesKeyLen
		b.Entries = append(b.Entries, entry)
	}

	return b, nil
}

// VerifyBundle checks the Ed25519 signature on a v1 or v2 bundle.
// For v1, signingPub must be provided externally.
// For v2, the embedded pubkey is used (signingPub parameter is ignored).
func VerifyBundle(data []byte, signingPub ed25519.PublicKey) bool {
	if len(data) < 22+signatureLen {
		return false
	}

	version := data[0]
	header := data[:22]

	switch version {
	case bundleVersionV1:
		sig := data[22 : 22+signatureLen]
		entries := data[22+signatureLen:]
		sigData := make([]byte, 0, len(header)+len(entries))
		sigData = append(sigData, header...)
		sigData = append(sigData, entries...)
		return ed25519.Verify(signingPub, sigData, sig)

	case bundleVersionV2:
		if len(data) < 22+pubKeyLen+signatureLen {
			return false
		}
		embeddedPub := ed25519.PublicKey(data[22 : 22+pubKeyLen])
		sig := data[22+pubKeyLen : 22+pubKeyLen+signatureLen]
		entries := data[22+pubKeyLen+signatureLen:]
		// Signed data: header + pubkey + entries
		sigData := make([]byte, 0, len(header)+pubKeyLen+len(entries))
		sigData = append(sigData, header...)
		sigData = append(sigData, embeddedPub...)
		sigData = append(sigData, entries...)
		return ed25519.Verify(embeddedPub, sigData, sig)

	default:
		return false
	}
}

// BundleToURL encodes a bundle as a meshsat:// URL.
func BundleToURL(data []byte) string {
	return bundleURLScheme + base64.RawURLEncoding.EncodeToString(data)
}

// URLToBundle decodes a meshsat:// URL to raw bundle bytes.
func URLToBundle(url string) ([]byte, error) {
	if !strings.HasPrefix(url, bundleURLScheme) {
		return nil, fmt.Errorf("not a meshsat key URL")
	}
	encoded := url[len(bundleURLScheme):]
	return base64.RawURLEncoding.DecodeString(encoded)
}
