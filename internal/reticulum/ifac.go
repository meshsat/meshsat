package reticulum

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// Interface access codes (RNS/Transport.py handle_outgoing_ifac / handle_ifac,
// RNS/Reticulum.py IFAC_SALT, TCPInterface.py key derivation).
//
// Two peers that share a network name and/or passphrase derive the same
// 64-byte key, whose second half seeds an Ed25519 signing key. Every packet
// carries the last ifac_size bytes of the signature over the clean packet,
// inserted after the hops byte, and everything except the tag itself is
// XOR-masked with HKDF(ifac, salt=key). Bit 7 of the flags byte marks an
// IFAC packet; packets that do not match the interface's expectation are
// dropped either way.

// IFACSalt is RNS.Reticulum.IFAC_SALT.
var IFACSalt = mustHex("adf54d882c9a9b80771eb4995d702d4a3e733391b2a0f53f416d9f907e55cff8")

// DefaultIFACSize is the tag length for TCP interfaces (bytes).
const DefaultIFACSize = 16

// ErrIFAC is returned for an invalid or missing access code.
var ErrIFAC = errors.New("reticulum: invalid IFAC")

// IFACKey derives the 64-byte interface key from network name and passphrase
// (either may be empty, but not both).
func IFACKey(netname, netkey string) []byte {
	var origin []byte
	if netname != "" {
		h := sha256.Sum256([]byte(netname))
		origin = append(origin, h[:]...)
	}
	if netkey != "" {
		h := sha256.Sum256([]byte(netkey))
		origin = append(origin, h[:]...)
	}
	originHash := sha256.Sum256(origin)
	return HKDF(64, originHash[:], IFACSalt, nil)
}

// IFAC wraps and unwraps packets for one interface.
type IFAC struct {
	key    []byte
	size   int
	signer ed25519.PrivateKey
}

// NewIFAC builds an IFAC for a network name / passphrase and tag size.
func NewIFAC(netname, netkey string, size int) (*IFAC, error) {
	if netname == "" && netkey == "" {
		return nil, errors.New("reticulum: IFAC needs a network name or passphrase")
	}
	if size < IFACMinSize || size > 64 {
		return nil, errors.New("reticulum: IFAC size must be 1..64 bytes")
	}
	key := IFACKey(netname, netkey)
	return &IFAC{key: key, size: size, signer: ed25519.NewKeyFromSeed(key[32:])}, nil
}

// Size is the tag length in bytes.
func (f *IFAC) Size() int { return f.size }

// Wrap adds the access code and mask to a clean packet.
func (f *IFAC) Wrap(raw []byte) []byte {
	if len(raw) < 2 {
		return raw
	}
	sig := ed25519.Sign(f.signer, raw)
	ifac := sig[len(sig)-f.size:]
	mask := HKDF(len(raw)+f.size, ifac, f.key, nil)
	out := make([]byte, 0, len(raw)+f.size)
	out = append(out, raw[0]|0x80, raw[1])
	out = append(out, ifac...)
	out = append(out, raw[2:]...)
	for i := range out {
		if i >= 2 && i < 2+f.size {
			continue
		}
		out[i] ^= mask[i]
	}
	out[0] |= 0x80
	return out
}

// Unwrap verifies and strips the access code, returning the clean packet.
func (f *IFAC) Unwrap(raw []byte) ([]byte, error) {
	if len(raw) <= 2+f.size {
		return nil, ErrIFAC
	}
	if raw[0]&0x80 == 0 {
		return nil, ErrIFAC
	}
	ifac := raw[2 : 2+f.size]
	mask := HKDF(len(raw), ifac, f.key, nil)
	unmasked := make([]byte, len(raw))
	for i := range raw {
		if i >= 2 && i < 2+f.size {
			unmasked[i] = raw[i]
			continue
		}
		unmasked[i] = raw[i] ^ mask[i]
	}
	clean := make([]byte, 0, len(raw)-f.size)
	clean = append(clean, unmasked[0]&0x7F, unmasked[1])
	clean = append(clean, unmasked[2+f.size:]...)
	sig := ed25519.Sign(f.signer, clean)
	if string(sig[len(sig)-f.size:]) != string(ifac) {
		return nil, ErrIFAC
	}
	return clean, nil
}

// HasIFACFlag reports whether a raw packet carries the IFAC bit.
func HasIFACFlag(raw []byte) bool { return len(raw) > 0 && raw[0]&0x80 != 0 }

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}
