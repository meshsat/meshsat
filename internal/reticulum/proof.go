package reticulum

import (
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
)

// Packet proofs (RNS/Identity.py prove, RNS/Packet.py PacketReceipt).
//
// A proof for a packet sent to a SINGLE destination is a PROOF packet whose
// destination hash is the truncated hash of the proved packet, context NONE,
// and data either the 64-byte signature over the full packet hash (implicit,
// the RNS default) or packet_hash(32) || signature(64) (explicit). Proofs
// for packets on a link are always explicit and are signed with the link's
// signing key instead of an identity.

const (
	ImplicitProofLen = SignatureLen
	ExplicitProofLen = FullHashLen + SignatureLen
)

// PacketHashOf hashes an already-computed hashable part.
func PacketHashOf(hashable []byte) [FullHashLen]byte { return sha256.Sum256(hashable) }

// ImplicitProof signs the packet hash with the identity: the RNS default.
func (id *Identity) ImplicitProof(packetHash [FullHashLen]byte) []byte {
	return id.Sign(packetHash[:])
}

// ExplicitProof is packet_hash || signature.
func (id *Identity) ExplicitProof(packetHash [FullHashLen]byte) []byte {
	out := make([]byte, 0, ExplicitProofLen)
	out = append(out, packetHash[:]...)
	return append(out, id.Sign(packetHash[:])...)
}

// BuildProofPacket wraps proof data in a PROOF packet addressed to the
// proved packet's truncated hash (SINGLE, HEADER_1, hops 0).
func BuildProofPacket(provedRaw []byte, proofData []byte) []byte {
	h := &Header{
		HeaderType: HeaderType1,
		DestType:   DestSingle,
		PacketType: PacketProof,
		DestHash:   TruncatedPacketHash(provedRaw),
		Context:    ContextNone,
		Data:       proofData,
	}
	return h.Marshal()
}

// ErrProofInvalid is returned when a proof does not verify.
var ErrProofInvalid = errors.New("reticulum: proof invalid")

// ValidateProof checks proof data against the expected packet hash and the
// destination identity's signing key. Both implicit and explicit forms are
// accepted, as PacketReceipt.validate_proof does.
func ValidateProof(proofData []byte, packetHash [FullHashLen]byte, sigPub ed25519.PublicKey) error {
	var sig []byte
	switch len(proofData) {
	case ImplicitProofLen:
		sig = proofData
	case ExplicitProofLen:
		if string(proofData[:FullHashLen]) != string(packetHash[:]) {
			return ErrProofInvalid
		}
		sig = proofData[FullHashLen:]
	default:
		return ErrProofInvalid
	}
	if !VerifySignature(sigPub, packetHash[:], sig) {
		return ErrProofInvalid
	}
	return nil
}
