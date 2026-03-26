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
	bundleVersion   = 0x01
	bundleURLScheme = "meshsat://key/"
	maxEntries      = 8
	destHashLen     = 16
	signatureLen    = 64
	aesKeyLen       = 32
)

// Channel type enum — matches Android ConversationKey types.
const (
	ChannelSMS       byte = 0x00
	ChannelMesh      byte = 0x01
	ChannelIridium   byte = 0x02
	ChannelAstrocast byte = 0x03
	ChannelZigBee    byte = 0x04
	ChannelMQTT      byte = 0x05
	ChannelWebhook   byte = 0x06
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
	case "astrocast":
		return ChannelAstrocast
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
	case ChannelAstrocast:
		return "astrocast"
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

// MarshalBundle serializes a key bundle to compact binary and signs it.
func MarshalBundle(bridgeHash [destHashLen]byte, entries []BundleEntry, signer Signer) ([]byte, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("no entries")
	}
	if len(entries) > maxEntries {
		return nil, fmt.Errorf("too many entries (max %d)", maxEntries)
	}

	// Header: version(1) + bridgeHash(16) + timestamp(4) + entryCount(1) = 22 bytes
	header := make([]byte, 22)
	header[0] = bundleVersion
	copy(header[1:17], bridgeHash[:])
	binary.BigEndian.PutUint32(header[17:21], uint32(time.Now().Unix()))
	header[21] = byte(len(entries))

	// Entries
	var entryData []byte
	for _, e := range entries {
		addrBytes := []byte(e.Address)
		if len(addrBytes) > 255 {
			return nil, fmt.Errorf("address too long: %s", e.Address)
		}
		entryData = append(entryData, e.ChannelType)
		entryData = append(entryData, byte(len(addrBytes)))
		entryData = append(entryData, addrBytes...)
		entryData = append(entryData, e.Key[:]...)
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

// UnmarshalBundle parses a compact binary key bundle.
func UnmarshalBundle(data []byte) (*KeyBundle, error) {
	if len(data) < 22+signatureLen {
		return nil, fmt.Errorf("bundle too short")
	}

	b := &KeyBundle{
		Version: data[0],
	}
	if b.Version != bundleVersion {
		return nil, fmt.Errorf("unsupported bundle version: %d", b.Version)
	}

	copy(b.BridgeHash[:], data[1:17])
	b.Timestamp = binary.BigEndian.Uint32(data[17:21])
	entryCount := int(data[21])
	copy(b.Signature[:], data[22:22+signatureLen])

	// Parse entries
	offset := 22 + signatureLen
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

// VerifyBundle checks the Ed25519 signature on a bundle.
func VerifyBundle(data []byte, signingPub ed25519.PublicKey) bool {
	if len(data) < 22+signatureLen {
		return false
	}

	header := data[:22]
	sig := data[22 : 22+signatureLen]
	entries := data[22+signatureLen:]

	sigData := append(header, entries...)
	return ed25519.Verify(signingPub, sigData, sig)
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
