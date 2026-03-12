package routing

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Announce packet wire format constants.
const (
	// Header: 1 byte flags + 1 byte hop count.
	AnnounceHeaderLen = 2
	// Context byte: identifies the announce type.
	AnnounceContextLen = 1
	// Random blob for uniqueness (makes each announce distinct even for same identity).
	AnnounceRandomLen = 16
	// Ed25519 signature length.
	AnnounceSignatureLen = 64

	// Minimum announce size: header(2) + context(1) + dest_hash(16) + signing_pub(32) +
	// encryption_pub(32) + random(16) + signature(64) = 163 bytes.
	AnnounceMinLen = AnnounceHeaderLen + AnnounceContextLen + DestHashLen +
		ed25519.PublicKeySize + 32 + AnnounceRandomLen + AnnounceSignatureLen

	// Flag bits in the first header byte.
	FlagIsAnnounce byte = 0x01 // bit 0: this is an announce packet
	FlagHasAppData byte = 0x02 // bit 1: app data present
	FlagHasRatchet byte = 0x04 // bit 2: ratchet key present (future)

	// Context values.
	ContextAnnounce byte = 0x01

	// MaxHops is the maximum propagation depth for announces.
	MaxAnnounceHops = 128

	// RatchetKeyLen is the X25519 ratchet key size (future use).
	RatchetKeyLen = 32
)

// Announce represents a parsed announce packet.
type Announce struct {
	Flags         byte
	HopCount      byte
	Context       byte
	DestHash      [DestHashLen]byte
	SigningPub    ed25519.PublicKey // 32 bytes
	EncryptionPub *ecdh.PublicKey   // 32 bytes (X25519)
	AppData       []byte            // variable length, optional
	Random        [AnnounceRandomLen]byte
	Signature     []byte // 64 bytes (Ed25519)
}

// NewAnnounce creates a signed announce packet from a routing identity.
// appData is optional application-level metadata (e.g. node name, capabilities).
func NewAnnounce(id *Identity, appData []byte) (*Announce, error) {
	a := &Announce{
		Flags:         FlagIsAnnounce,
		HopCount:      0,
		Context:       ContextAnnounce,
		DestHash:      id.DestHash(),
		SigningPub:    id.SigningPublicKey(),
		EncryptionPub: id.EncryptionPublicKey(),
		AppData:       appData,
	}

	if len(appData) > 0 {
		a.Flags |= FlagHasAppData
	}

	// Generate random blob for uniqueness
	if _, err := io.ReadFull(rand.Reader, a.Random[:]); err != nil {
		return nil, fmt.Errorf("generate random: %w", err)
	}

	// Sign the announce body (everything except the signature itself)
	body := a.signableBody()
	a.Signature = id.Sign(body)

	return a, nil
}

// Marshal serializes the announce to wire format.
func (a *Announce) Marshal() []byte {
	encPubBytes := a.EncryptionPub.Bytes()
	size := AnnounceHeaderLen + AnnounceContextLen + DestHashLen +
		ed25519.PublicKeySize + len(encPubBytes) + AnnounceRandomLen + AnnounceSignatureLen
	if len(a.AppData) > 0 {
		size += 2 + len(a.AppData) // 2-byte length prefix + data
	}

	buf := make([]byte, 0, size)
	buf = append(buf, a.Flags, a.HopCount)
	buf = append(buf, a.Context)
	buf = append(buf, a.DestHash[:]...)
	buf = append(buf, a.SigningPub...)
	buf = append(buf, encPubBytes...)
	if len(a.AppData) > 0 {
		var appLen [2]byte
		binary.BigEndian.PutUint16(appLen[:], uint16(len(a.AppData)))
		buf = append(buf, appLen[:]...)
		buf = append(buf, a.AppData...)
	}
	buf = append(buf, a.Random[:]...)
	buf = append(buf, a.Signature...)
	return buf
}

// UnmarshalAnnounce parses a wire-format announce packet.
func UnmarshalAnnounce(data []byte) (*Announce, error) {
	if len(data) < AnnounceMinLen {
		return nil, errors.New("announce too short")
	}

	a := &Announce{}
	pos := 0

	a.Flags = data[pos]
	pos++
	if a.Flags&FlagIsAnnounce == 0 {
		return nil, errors.New("not an announce packet")
	}

	a.HopCount = data[pos]
	pos++

	a.Context = data[pos]
	pos++

	copy(a.DestHash[:], data[pos:pos+DestHashLen])
	pos += DestHashLen

	a.SigningPub = make([]byte, ed25519.PublicKeySize)
	copy(a.SigningPub, data[pos:pos+ed25519.PublicKeySize])
	pos += ed25519.PublicKeySize

	encPub, err := ecdh.X25519().NewPublicKey(data[pos : pos+32])
	if err != nil {
		return nil, fmt.Errorf("invalid encryption key: %w", err)
	}
	a.EncryptionPub = encPub
	pos += 32

	if a.Flags&FlagHasAppData != 0 {
		if pos+2 > len(data) {
			return nil, errors.New("truncated app data length")
		}
		appLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
		pos += 2
		if pos+appLen > len(data) {
			return nil, errors.New("truncated app data")
		}
		a.AppData = make([]byte, appLen)
		copy(a.AppData, data[pos:pos+appLen])
		pos += appLen
	}

	remaining := len(data) - pos
	if remaining < AnnounceRandomLen+AnnounceSignatureLen {
		return nil, errors.New("truncated random or signature")
	}

	copy(a.Random[:], data[pos:pos+AnnounceRandomLen])
	pos += AnnounceRandomLen

	a.Signature = make([]byte, AnnounceSignatureLen)
	copy(a.Signature, data[pos:pos+AnnounceSignatureLen])

	return a, nil
}

// Verify checks that the announce signature is valid and the destination hash
// matches the embedded public keys.
func (a *Announce) Verify() bool {
	// Verify destination hash matches public keys
	computed := ComputeDestHash(a.SigningPub, a.EncryptionPub)
	if computed != a.DestHash {
		return false
	}

	// Verify Ed25519 signature over the signable body
	body := a.signableBody()
	return VerifyWith(a.SigningPub, body, a.Signature)
}

// signableBody returns the bytes that are signed: everything except the
// signature and the mutable hop count. The hop count is excluded because
// relay nodes increment it — including it would invalidate the signature.
func (a *Announce) signableBody() []byte {
	encPubBytes := a.EncryptionPub.Bytes()
	size := 1 + AnnounceContextLen + DestHashLen +
		ed25519.PublicKeySize + len(encPubBytes) + AnnounceRandomLen
	if len(a.AppData) > 0 {
		size += 2 + len(a.AppData)
	}

	buf := make([]byte, 0, size)
	buf = append(buf, a.Flags) // flags only, NOT hop count
	buf = append(buf, a.Context)
	buf = append(buf, a.DestHash[:]...)
	buf = append(buf, a.SigningPub...)
	buf = append(buf, encPubBytes...)
	if len(a.AppData) > 0 {
		var appLen [2]byte
		binary.BigEndian.PutUint16(appLen[:], uint16(len(a.AppData)))
		buf = append(buf, appLen[:]...)
		buf = append(buf, a.AppData...)
	}
	buf = append(buf, a.Random[:]...)
	return buf
}

// IncrementHop increments the hop count for relay forwarding.
// Returns false if the hop count would exceed MaxAnnounceHops.
func (a *Announce) IncrementHop() bool {
	if int(a.HopCount) >= MaxAnnounceHops {
		return false
	}
	a.HopCount++
	return true
}
