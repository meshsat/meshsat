package reticulum

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// RNS link wire format (RNS/Link.py). These types replace the MeshSat-private
// link handshake in link.go, which is kept only until the routing layer has
// moved over.
const (
	// RNSLinkIDLen: a link id is a truncated packet hash.
	RNSLinkIDLen = TruncatedHashLen
	// LinkECPubSize is x25519 pub (32) + ed25519 pub (32).
	LinkECPubSize = 64
	// LinkMTUSize is the length of the optional signalling bytes.
	LinkMTUSize = 3
	// LinkMTUByteMask masks the 21-bit MTU inside the signalling value.
	LinkMTUByteMask = 0x1FFFFF
	// LinkModeByteMask masks the 3-bit mode in the top of the first byte.
	LinkModeByteMask = 0xE0

	LinkModeAES128CBC byte = 0x00
	LinkModeAES256CBC byte = 0x01 // the only enabled mode in RNS 1.5.x
	LinkModeDefault        = LinkModeAES256CBC

	// KeepaliveInitiator / KeepaliveResponse are the 1-byte keepalive bodies.
	KeepaliveInitiator byte = 0xFF
	KeepaliveResponse  byte = 0xFE
)

// LinkMDU returns the largest link packet plaintext for an MTU:
// floor((mtu - 1 - 19 - 48) / 16) * 16 - 1, 431 at MTU 500.
func LinkMDU(mtu int) int {
	return ((mtu-IFACMinSize-HeaderMinSize-TokenOverhead)/AES128BlockSize)*AES128BlockSize - 1
}

// SignallingBytes encodes MTU and mode into the 3 trailing bytes of a link
// request or proof: big-endian uint32((mtu & 0x1FFFFF) | ((mode<<5)&0xE0)<<16)[1:].
func SignallingBytes(mtu int, mode byte) [LinkMTUSize]byte {
	v := uint32(mtu&LinkMTUByteMask) | (uint32((mode<<5)&LinkModeByteMask) << 16)
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return [LinkMTUSize]byte{b[1], b[2], b[3]}
}

// ParseSignalling decodes MTU and mode from 3 signalling bytes.
func ParseSignalling(s []byte) (mtu int, mode byte) {
	if len(s) < LinkMTUSize {
		return MTU, LinkModeDefault
	}
	mtu = (int(s[0])<<16 | int(s[1])<<8 | int(s[2])) & LinkMTUByteMask
	mode = (s[0] & LinkModeByteMask) >> 5
	return mtu, mode
}

// RNSLinkRequest is the data of a LINKREQUEST packet (64 or 67 bytes).
type RNSLinkRequest struct {
	EphPub    *ecdh.PublicKey   // initiator's ephemeral x25519 key
	EphSigPub ed25519.PublicKey // initiator's ephemeral ed25519 key
	MTU       int               // from signalling bytes, MTU when absent
	Mode      byte
	Signalled bool // true when the 3 signalling bytes were present
}

// Marshal returns the packet data field, always with signalling bytes.
func (lr *RNSLinkRequest) Marshal() []byte {
	out := make([]byte, 0, LinkECPubSize+LinkMTUSize)
	out = append(out, lr.EphPub.Bytes()...)
	out = append(out, lr.EphSigPub...)
	s := SignallingBytes(lr.MTU, lr.Mode)
	return append(out, s[:]...)
}

// UnmarshalRNSLinkRequest parses a LINKREQUEST data field.
func UnmarshalRNSLinkRequest(data []byte) (*RNSLinkRequest, error) {
	if len(data) != LinkECPubSize && len(data) != LinkECPubSize+LinkMTUSize {
		return nil, fmt.Errorf("%w: link request must be %d or %d bytes, got %d", ErrTooShort, LinkECPubSize, LinkECPubSize+LinkMTUSize, len(data))
	}
	pub, err := ecdh.X25519().NewPublicKey(data[:EncryptionPubLen])
	if err != nil {
		return nil, fmt.Errorf("reticulum: bad link ephemeral key: %w", err)
	}
	lr := &RNSLinkRequest{EphPub: pub, EphSigPub: make(ed25519.PublicKey, SigningPubLen), MTU: MTU, Mode: LinkModeDefault}
	copy(lr.EphSigPub, data[EncryptionPubLen:LinkECPubSize])
	if len(data) == LinkECPubSize+LinkMTUSize {
		lr.MTU, lr.Mode = ParseSignalling(data[LinkECPubSize:])
		lr.Signalled = true
	}
	if lr.Mode != LinkModeAES256CBC {
		return nil, fmt.Errorf("reticulum: link mode %d not enabled", lr.Mode)
	}
	return lr, nil
}

// LinkIDFromRequestPacket computes the link id: the truncated hash of the
// request packet's hashable part with the signalling bytes removed.
// Reference: RNS/Link.py link_id_from_lr_packet.
func LinkIDFromRequestPacket(raw []byte) ([RNSLinkIDLen]byte, error) {
	var id [RNSLinkIDLen]byte
	h, err := UnmarshalHeader(raw)
	if err != nil {
		return id, err
	}
	hashable := HashablePart(raw)
	if diff := len(h.Data) - LinkECPubSize; diff > 0 {
		hashable = hashable[:len(hashable)-diff]
	}
	sum := PacketHashOf(hashable)
	copy(id[:], sum[:RNSLinkIDLen])
	return id, nil
}

// RNSLinkProof is the data of the LRPROOF packet answering a link request:
// signature(64) || responder ephemeral x25519 pub (32) || signalling(3).
type RNSLinkProof struct {
	Signature []byte
	EphPub    *ecdh.PublicKey
	MTU       int
	Mode      byte
	Signalled bool
}

// LinkProofSignedData is what the responder's IDENTITY signs:
// link_id || responder eph pub || responder identity ed25519 pub || signalling.
func LinkProofSignedData(linkID [RNSLinkIDLen]byte, ephPub *ecdh.PublicKey, identitySigPub ed25519.PublicKey, mtu int, mode byte) []byte {
	out := make([]byte, 0, RNSLinkIDLen+EncryptionPubLen+SigningPubLen+LinkMTUSize)
	out = append(out, linkID[:]...)
	out = append(out, ephPub.Bytes()...)
	out = append(out, identitySigPub...)
	s := SignallingBytes(mtu, mode)
	return append(out, s[:]...)
}

// Marshal returns the LRPROOF data field.
func (lp *RNSLinkProof) Marshal() []byte {
	out := make([]byte, 0, SignatureLen+EncryptionPubLen+LinkMTUSize)
	out = append(out, lp.Signature...)
	out = append(out, lp.EphPub.Bytes()...)
	s := SignallingBytes(lp.MTU, lp.Mode)
	return append(out, s[:]...)
}

// UnmarshalRNSLinkProof parses an LRPROOF data field (96 or 99 bytes).
func UnmarshalRNSLinkProof(data []byte) (*RNSLinkProof, error) {
	base := SignatureLen + EncryptionPubLen
	if len(data) != base && len(data) != base+LinkMTUSize {
		return nil, fmt.Errorf("%w: link proof must be %d or %d bytes, got %d", ErrTooShort, base, base+LinkMTUSize, len(data))
	}
	pub, err := ecdh.X25519().NewPublicKey(data[SignatureLen:base])
	if err != nil {
		return nil, fmt.Errorf("reticulum: bad link proof ephemeral key: %w", err)
	}
	lp := &RNSLinkProof{Signature: append([]byte(nil), data[:SignatureLen]...), EphPub: pub, MTU: MTU, Mode: LinkModeDefault}
	if len(data) == base+LinkMTUSize {
		lp.MTU, lp.Mode = ParseSignalling(data[base:])
		lp.Signalled = true
	}
	return lp, nil
}

// DeriveLinkKey is the link handshake key: HKDF(64, shared, salt=link_id).
func DeriveLinkKey(shared []byte, linkID [RNSLinkIDLen]byte) []byte {
	return HKDF(DerivedKeyLen, shared, linkID[:], nil)
}

// RTTData encodes a link RTT in seconds as msgpack float64 (0xCB + 8 bytes),
// the plaintext of the LRRTT packet.
func RTTData(rttSeconds float64) []byte {
	out := make([]byte, 9)
	out[0] = 0xCB
	binary.BigEndian.PutUint64(out[1:], math.Float64bits(rttSeconds))
	return out
}

// ParseRTTData decodes an LRRTT plaintext. RNS packs with umsgpack, which
// emits float64 for Python floats; ints are accepted for robustness.
func ParseRTTData(b []byte) (float64, error) {
	if len(b) == 0 {
		return 0, ErrTooShort
	}
	switch {
	case b[0] == 0xCB && len(b) == 9:
		return math.Float64frombits(binary.BigEndian.Uint64(b[1:])), nil
	case b[0] == 0xCA && len(b) == 5:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b[1:]))), nil
	case b[0] < 0x80 && len(b) == 1:
		return float64(b[0]), nil
	}
	return 0, errors.New("reticulum: unsupported RTT encoding")
}

// LinkIdentifyData is the plaintext of a LINKIDENTIFY packet:
// identity public key (64) || sign(link_id || public key).
func LinkIdentifyData(id *Identity, linkID [RNSLinkIDLen]byte) []byte {
	pub := id.PublicBytes()
	signed := make([]byte, 0, RNSLinkIDLen+IdentityKeySize)
	signed = append(signed, linkID[:]...)
	signed = append(signed, pub...)
	return append(pub, id.Sign(signed)...)
}

// ParseLinkIdentify verifies a LINKIDENTIFY plaintext and returns the
// identified public key.
func ParseLinkIdentify(data []byte, linkID [RNSLinkIDLen]byte) ([]byte, error) {
	if len(data) != IdentityKeySize+SignatureLen {
		return nil, ErrTooShort
	}
	pub := data[:IdentityKeySize]
	_, sigPub, err := ParsePublicKeys(pub)
	if err != nil {
		return nil, err
	}
	signed := make([]byte, 0, RNSLinkIDLen+IdentityKeySize)
	signed = append(signed, linkID[:]...)
	signed = append(signed, pub...)
	if !VerifySignature(sigPub, signed, data[IdentityKeySize:]) {
		return nil, errors.New("reticulum: link identify signature invalid")
	}
	return append([]byte(nil), pub...), nil
}
