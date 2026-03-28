// Package reticulum implements the Reticulum wire format for parsing and
// generating packets that are wire-compatible with the Python RNS reference
// implementation. This is a pure wire format library with no routing state,
// database, or network I/O — those belong in higher-level packages.
//
// Reference: https://reticulum.network/manual/understanding.html
package reticulum

// System-level constants from the Reticulum specification.
const (
	// MTU is the maximum transmission unit for Reticulum packets.
	MTU = 500

	// HeaderMinSize is the header size for Type 1 packets (single address).
	// 1 byte flags + 1 byte hops + 16 bytes destination hash + 1 byte context.
	HeaderMinSize = 19

	// HeaderMaxSize is the header size for Type 2 packets (transport, two addresses).
	// 1 byte flags + 1 byte hops + 16 bytes transport ID + 16 bytes dest hash + 1 byte context.
	HeaderMaxSize = 35

	// IFACMinSize is the minimum IFAC overhead.
	IFACMinSize = 1

	// MDU is the maximum data unit (payload capacity).
	MDU = MTU - HeaderMaxSize - IFACMinSize // 464

	// TruncatedHashLen is the length of truncated destination hashes (128 bits).
	TruncatedHashLen = 16

	// FullHashLen is the full SHA-256 hash length.
	FullHashLen = 32

	// NameHashLen is the truncated name hash length (80 bits).
	NameHashLen = 10

	// SignatureLen is the Ed25519 signature length.
	SignatureLen = 64

	// IdentityKeySize is the combined public key size (32B X25519 + 32B Ed25519).
	IdentityKeySize = 64

	// EncryptionPubLen is the X25519 public key length.
	EncryptionPubLen = 32

	// SigningPubLen is the Ed25519 public key length.
	SigningPubLen = 32

	// RatchetKeyLen is the X25519 ratchet key size.
	RatchetKeyLen = 32

	// RandomHashLen is the random nonce in announce packets.
	RandomHashLen = 10

	// PathfinderM is the maximum hop count.
	PathfinderM = 128

	// AnnounceCap is the maximum percentage of interface bandwidth for announces.
	AnnounceCap = 2

	// TokenOverhead is IV (16) + HMAC (32) for token encryption.
	TokenOverhead = 48

	// AES128BlockSize is the AES block size.
	AES128BlockSize = 16
)

// Packet types (bits 1-0 of flags byte).
const (
	PacketData        byte = 0x00
	PacketAnnounce    byte = 0x01
	PacketLinkRequest byte = 0x02
	PacketProof       byte = 0x03
)

// Destination types (bits 3-2 of flags byte).
const (
	DestSingle byte = 0x00
	DestGroup  byte = 0x01
	DestPlain  byte = 0x02
	DestLink   byte = 0x03
)

// Transport types (bit 4 of flags byte).
const (
	TransportBroadcast byte = 0x00
	TransportTransport byte = 0x01
)

// Header types (bit 6 of flags byte).
const (
	HeaderType1 byte = 0x00 // Single address
	HeaderType2 byte = 0x01 // Two addresses (transport)
)

// Context types (context byte after header).
const (
	ContextNone          byte = 0x00
	ContextResource      byte = 0x01
	ContextResourceAdv   byte = 0x02
	ContextResourceReq   byte = 0x03
	ContextResourceHMU   byte = 0x04
	ContextResourcePRF   byte = 0x05
	ContextResourceICL   byte = 0x06
	ContextResourceRCL   byte = 0x07
	ContextCacheRequest  byte = 0x08
	ContextRequest       byte = 0x09
	ContextResponse      byte = 0x0A
	ContextPathResponse  byte = 0x0B
	ContextCommand       byte = 0x0C
	ContextCommandStatus byte = 0x0D
	ContextChannel       byte = 0x0E
	ContextResourceRLNC  byte = 0x0F // RLNC coded resource segment (MESHSAT-411)
	ContextKeepalive     byte = 0xFA
	ContextLinkIdentify  byte = 0xFB
	ContextLinkClose     byte = 0xFC
	ContextLinkProof     byte = 0xFD
	ContextLRRTT         byte = 0xFE
	ContextLRProof       byte = 0xFF
)

// Link and key sizes used by Bridge-compatible framing.
const (
	// LinkIDLen is the length of a link identifier (SHA-256 of link request).
	LinkIDLen = 32

	// SymKeyLen is the AES-256 symmetric key length.
	SymKeyLen = 32

	// DestHashLen is an alias for TruncatedHashLen (Bridge compatibility).
	DestHashLen = TruncatedHashLen
)

// Bridge-compatible packet framing constants.
// These are used by the MeshSat Bridge for internal packet type identification
// on the PRIVATE_APP (PortNum 256) channel.
const (
	// Announce flag bits (byte 0 of Bridge announce framing).
	FlagIsAnnounce byte = 0x01
	FlagHasAppData byte = 0x02
	FlagHasRatchet byte = 0x04

	// ContextAnnounce is the Bridge announce context value.
	ContextAnnounce byte = 0x01

	// MaxAnnounceHops is the maximum propagation depth for announces.
	MaxAnnounceHops = PathfinderM

	// Bridge link/keepalive type bytes (first byte of packet).
	BridgeLinkRequest byte = 0x10
	BridgeLinkProof   byte = 0x11
	BridgeLinkData    byte = 0x12
	BridgeKeepalive   byte = 0x13

	// Bridge time sync type bytes (MESHSAT-410).
	BridgeTimeSyncReq  byte = 0x14
	BridgeTimeSyncResp byte = 0x15

	// Bridge DTN custody transfer type bytes (MESHSAT-408).
	BridgeCustodyOffer byte = 0x16
	BridgeCustodyACK   byte = 0x17

	// HMACLen is the HMAC-SHA256 tag length.
	HMACLen = 32
)
