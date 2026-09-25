package reticulum

import "crypto/sha256"

// HashablePart returns the bytes RNS hashes for a raw packet: the flags byte
// masked to destination type and packet type, then everything after the hops
// byte, skipping the transport id of a HEADER_2 packet. Because the mask drops
// the header type, transport type, context flag and IFAC bits, the hash
// survives the HEADER_1 <-> HEADER_2 rewriting done in transport, which is
// what makes proofs and reverse tables work across hops.
// Reference: RNS/Packet.py get_hashable_part.
func HashablePart(raw []byte) []byte {
	if len(raw) < 2 {
		return nil
	}
	start := 2
	if (raw[0]&0x40)>>6 == HeaderType2 {
		start = 2 + TruncatedHashLen
	}
	if len(raw) < start {
		return nil
	}
	out := make([]byte, 0, 1+len(raw)-start)
	out = append(out, raw[0]&0x0F)
	out = append(out, raw[start:]...)
	return out
}

// PacketHash is SHA-256 over HashablePart(raw).
func PacketHash(raw []byte) [FullHashLen]byte {
	return sha256.Sum256(HashablePart(raw))
}

// TruncatedPacketHash is the first 16 bytes of PacketHash; proofs are
// addressed to it and reverse-table entries are keyed by it.
func TruncatedPacketHash(raw []byte) [TruncatedHashLen]byte {
	full := PacketHash(raw)
	var out [TruncatedHashLen]byte
	copy(out[:], full[:TruncatedHashLen])
	return out
}

// RewriteForTransport converts a HEADER_1 packet into the HEADER_2 form a
// transport node emits: header type 2, transport type TRANSPORT, the given
// next-hop transport id inserted after the hops byte, everything else
// untouched (the hops byte is NOT changed here; callers set it).
// Reference: RNS/Transport.py outbound, "if RNS.Transport.hops_to(...) > 1".
func RewriteForTransport(raw []byte, transportID [TruncatedHashLen]byte) []byte {
	if len(raw) < HeaderMinSize {
		return raw
	}
	if (raw[0]&0x40)>>6 == HeaderType2 {
		out := make([]byte, len(raw))
		copy(out, raw)
		copy(out[2:2+TruncatedHashLen], transportID[:])
		out[0] |= 0x10
		return out
	}
	out := make([]byte, 0, len(raw)+TruncatedHashLen)
	out = append(out, (raw[0]&0x0F)|(HeaderType2<<6)|(TransportTransport<<4)|(raw[0]&0x20), raw[1])
	out = append(out, transportID[:]...)
	out = append(out, raw[2:]...)
	return out
}

// StripTransport converts a HEADER_2 packet back to HEADER_1 with transport
// type BROADCAST, as a transport node does on the last hop.
func StripTransport(raw []byte) []byte {
	if len(raw) < HeaderMaxSize || (raw[0]&0x40)>>6 != HeaderType2 {
		return raw
	}
	out := make([]byte, 0, len(raw)-TruncatedHashLen)
	out = append(out, (raw[0]&0x0F)|(raw[0]&0x20), raw[1])
	out = append(out, raw[2+TruncatedHashLen:]...)
	return out
}
