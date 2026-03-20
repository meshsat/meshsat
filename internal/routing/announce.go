package routing

import (
	"crypto/ecdh"
	"crypto/ed25519"

	"meshsat/internal/reticulum"
)

// Announce packet constants re-exported from the reticulum package.
const (
	// MaxAnnounceHops is the maximum propagation depth for announces.
	MaxAnnounceHops = reticulum.PathfinderM

	// FlagIsAnnounce is used for packet dispatch detection.
	// In Bridge-format, this was the first byte flag. In Reticulum format,
	// PacketAnnounce (0x01) occupies the same bit position in the flags byte.
	FlagIsAnnounce = reticulum.FlagIsAnnounce
)

// Announce represents a parsed announce packet. It wraps a reticulum.Announce
// for wire-compatible serialization and exposes the fields needed by the
// relay and destination table.
type Announce struct {
	DestHash      [DestHashLen]byte
	HopCount      byte
	NameHash      [reticulum.NameHashLen]byte
	SigningPub    ed25519.PublicKey
	EncryptionPub *ecdh.PublicKey
	AppData       []byte
	Random        [reticulum.RandomHashLen]byte
	Signature     []byte

	// ret is the underlying Reticulum announce (set on create/unmarshal).
	ret *reticulum.Announce
}

// NewAnnounce creates a signed Reticulum-format announce packet from a
// routing identity. appData is optional application-level metadata.
func NewAnnounce(id *Identity, appData []byte) (*Announce, error) {
	retAnn, err := reticulum.NewAnnounce(id.ReticulumIdentity(), id.AppName(), appData)
	if err != nil {
		return nil, err
	}
	return announceFromReticulum(retAnn), nil
}

// Marshal serializes the announce to Reticulum wire format (header + payload).
func (a *Announce) Marshal() []byte {
	if a.ret != nil {
		a.ret.Hops = a.HopCount // sync hop count back
		return a.ret.MarshalPacket()
	}
	// Fallback: shouldn't happen if created via NewAnnounce or UnmarshalAnnounce
	return nil
}

// Verify checks that the announce signature is valid and the destination hash
// matches the embedded public keys and name hash.
func (a *Announce) Verify() bool {
	if a.ret != nil {
		return a.ret.Verify() == nil
	}
	// Legacy verification for announces without a reticulum backing
	computed := ComputeDestHash(a.SigningPub, a.EncryptionPub)
	return computed == a.DestHash
}

// IncrementHop increments the hop count for relay forwarding.
// Returns false if the hop count would exceed MaxAnnounceHops.
func (a *Announce) IncrementHop() bool {
	if int(a.HopCount) >= MaxAnnounceHops {
		return false
	}
	a.HopCount++
	if a.ret != nil {
		a.ret.Hops = a.HopCount
	}
	return true
}

// UnmarshalAnnounce parses a Reticulum-format announce packet from wire data.
func UnmarshalAnnounce(data []byte) (*Announce, error) {
	retAnn, err := reticulum.UnmarshalAnnouncePacket(data)
	if err != nil {
		return nil, err
	}
	return announceFromReticulum(retAnn), nil
}

// announceFromReticulum converts a reticulum.Announce to a routing.Announce.
func announceFromReticulum(retAnn *reticulum.Announce) *Announce {
	a := &Announce{
		DestHash:      retAnn.DestHash,
		HopCount:      retAnn.Hops,
		NameHash:      retAnn.NameHash,
		SigningPub:    retAnn.SigningPublicKey(),
		EncryptionPub: retAnn.EncryptionPublicKey(),
		AppData:       retAnn.AppData,
		Random:        retAnn.Random,
		Signature:     retAnn.Signature,
		ret:           retAnn,
	}
	return a
}
