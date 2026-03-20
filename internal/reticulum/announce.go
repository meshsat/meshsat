package reticulum

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// Announce represents a parsed Reticulum announce packet payload.
// Wire layout of the data field (after packet header):
//
//	[64B public_key][10B name_hash][10B random_hash][optional 32B ratchet][64B signature][optional app_data]
//
// The public_key is ordered as [32B X25519 enc][32B Ed25519 sig] per the RNS spec.
type Announce struct {
	// Header fields (from packet header, not serialized in payload).
	DestHash    [TruncatedHashLen]byte
	Hops        byte
	ContextFlag byte // 1 if ratchet present

	// Payload fields.
	PublicKey []byte              // 64 bytes: [32B X25519][32B Ed25519]
	NameHash  [NameHashLen]byte   // 10 bytes
	Random    [RandomHashLen]byte // 10 bytes
	Ratchet   [RatchetKeyLen]byte // 32 bytes (only if ContextFlag set)
	Signature []byte              // 64 bytes
	AppData   []byte              // variable, optional

	// Parsed public keys (derived from PublicKey on unmarshal).
	encPub *ecdh.PublicKey
	sigPub ed25519.PublicKey
}

// Announce payload size constants.
const (
	// AnnounceMinPayload: pubkey(64) + name_hash(10) + random(10) + signature(64) = 148
	AnnounceMinPayload = IdentityKeySize + NameHashLen + RandomHashLen + SignatureLen
	// AnnounceRatchetPayload adds the 32-byte ratchet key.
	AnnounceRatchetPayload = AnnounceMinPayload + RatchetKeyLen
)

// NewAnnounce creates a signed announce from an identity and app name.
func NewAnnounce(id *Identity, appName string, appData []byte) (*Announce, error) {
	destHash := id.DestHash(appName)
	nameHash := ComputeNameHash(appName)

	a := &Announce{
		DestHash:  destHash,
		Hops:      0,
		PublicKey: id.PublicBytes(),
		NameHash:  nameHash,
		AppData:   appData,
		encPub:    id.EncryptionPublicKey(),
		sigPub:    id.SigningPublicKey(),
	}

	if _, err := io.ReadFull(rand.Reader, a.Random[:]); err != nil {
		return nil, fmt.Errorf("generate random: %w", err)
	}

	body := a.signableBody()
	a.Signature = id.Sign(body)

	return a, nil
}

// MarshalPayload serializes the announce payload (data field after packet header).
func (a *Announce) MarshalPayload() []byte {
	size := IdentityKeySize + NameHashLen + RandomHashLen + SignatureLen
	if a.ContextFlag != 0 {
		size += RatchetKeyLen
	}
	size += len(a.AppData)

	buf := make([]byte, 0, size)
	buf = append(buf, a.PublicKey...)
	buf = append(buf, a.NameHash[:]...)
	buf = append(buf, a.Random[:]...)
	if a.ContextFlag != 0 {
		buf = append(buf, a.Ratchet[:]...)
	}
	buf = append(buf, a.Signature...)
	buf = append(buf, a.AppData...)
	return buf
}

// MarshalPacket serializes the full announce packet (header + payload).
func (a *Announce) MarshalPacket() []byte {
	h := &Header{
		HeaderType:    HeaderType1,
		ContextFlag:   a.ContextFlag,
		TransportType: TransportBroadcast,
		DestType:      DestSingle,
		PacketType:    PacketAnnounce,
		Hops:          a.Hops,
		DestHash:      a.DestHash,
		Context:       ContextNone,
		Data:          a.MarshalPayload(),
	}
	return h.Marshal()
}

// UnmarshalAnnouncePayload parses an announce payload from the data field of
// a packet. The header must already be parsed; pass destHash, hops, and
// contextFlag from it.
func UnmarshalAnnouncePayload(data []byte, destHash [TruncatedHashLen]byte, hops, contextFlag byte) (*Announce, error) {
	minSize := AnnounceMinPayload
	if contextFlag != 0 {
		minSize = AnnounceRatchetPayload
	}
	if len(data) < minSize {
		return nil, fmt.Errorf("%w: announce payload %d bytes, need at least %d", ErrTooShort, len(data), minSize)
	}

	a := &Announce{
		DestHash:    destHash,
		Hops:        hops,
		ContextFlag: contextFlag,
	}

	pos := 0

	// Public key (64 bytes: [32B X25519][32B Ed25519])
	a.PublicKey = make([]byte, IdentityKeySize)
	copy(a.PublicKey, data[pos:pos+IdentityKeySize])
	pos += IdentityKeySize

	var err error
	a.encPub, a.sigPub, err = ParsePublicKeys(a.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("parse announce public keys: %w", err)
	}

	// Name hash (10 bytes)
	copy(a.NameHash[:], data[pos:pos+NameHashLen])
	pos += NameHashLen

	// Random hash (10 bytes)
	copy(a.Random[:], data[pos:pos+RandomHashLen])
	pos += RandomHashLen

	// Optional ratchet (32 bytes)
	if contextFlag != 0 {
		copy(a.Ratchet[:], data[pos:pos+RatchetKeyLen])
		pos += RatchetKeyLen
	}

	// Signature (64 bytes)
	a.Signature = make([]byte, SignatureLen)
	copy(a.Signature, data[pos:pos+SignatureLen])
	pos += SignatureLen

	// Optional app data (remainder)
	if pos < len(data) {
		a.AppData = make([]byte, len(data)-pos)
		copy(a.AppData, data[pos:])
	}

	return a, nil
}

// UnmarshalAnnouncePacket parses a full announce packet (header + payload).
func UnmarshalAnnouncePacket(raw []byte) (*Announce, error) {
	h, err := UnmarshalHeader(raw)
	if err != nil {
		return nil, fmt.Errorf("parse announce header: %w", err)
	}
	if h.PacketType != PacketAnnounce {
		return nil, fmt.Errorf("%w: expected ANNOUNCE, got %s", ErrInvalidFlag, PacketTypeString(h.PacketType))
	}
	return UnmarshalAnnouncePayload(h.Data, h.DestHash, h.Hops, h.ContextFlag)
}

// Verify checks that the announce signature is valid and the destination hash
// matches the embedded public keys and name hash.
func (a *Announce) Verify() error {
	if a.encPub == nil || a.sigPub == nil {
		var err error
		a.encPub, a.sigPub, err = ParsePublicKeys(a.PublicKey)
		if err != nil {
			return fmt.Errorf("parse public keys: %w", err)
		}
	}

	// Verify destination hash: SHA256(nameHash || identityHash)[:16]
	identityHash := ComputeIdentityHash(a.encPub, a.sigPub)
	computed := ComputeDestHash(a.NameHash, identityHash)
	if computed != a.DestHash {
		return errors.New("destination hash mismatch")
	}

	body := a.signableBody()
	if !VerifySignature(a.sigPub, body, a.Signature) {
		return errors.New("invalid announce signature")
	}

	return nil
}

// signableBody returns the bytes covered by the signature:
// dest_hash + public_key + name_hash + random_hash + [ratchet] + app_data
func (a *Announce) signableBody() []byte {
	size := TruncatedHashLen + IdentityKeySize + NameHashLen + RandomHashLen + len(a.AppData)
	if a.ContextFlag != 0 {
		size += RatchetKeyLen
	}

	buf := make([]byte, 0, size)
	buf = append(buf, a.DestHash[:]...)
	buf = append(buf, a.PublicKey...)
	buf = append(buf, a.NameHash[:]...)
	buf = append(buf, a.Random[:]...)
	if a.ContextFlag != 0 {
		buf = append(buf, a.Ratchet[:]...)
	}
	buf = append(buf, a.AppData...)
	return buf
}

// SigningPublicKey returns the parsed Ed25519 public key.
func (a *Announce) SigningPublicKey() ed25519.PublicKey {
	return a.sigPub
}

// EncryptionPublicKey returns the parsed X25519 public key.
func (a *Announce) EncryptionPublicKey() *ecdh.PublicKey {
	return a.encPub
}

// IncrementHop increments the hop count. Returns false if max hops exceeded.
func (a *Announce) IncrementHop() bool {
	if int(a.Hops) >= PathfinderM {
		return false
	}
	a.Hops++
	return true
}
