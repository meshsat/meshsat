// Package lxmf implements the Lightweight Extensible Message Format on top of
// internal/rns, wire-compatible with Python LXMF 1.1.0 (LXMF/LXMessage.py,
// LXMF/LXMRouter.py, LXMF/LXStamper.py). [MESHSAT-1348]
package lxmf

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"meshsat/internal/reticulum"
)

// Sizes and limits (LXMessage.py).
const (
	AppName          = "lxmf"
	DeliveryAspect   = "delivery"
	DeliveryName     = AppName + "." + DeliveryAspect
	DestLen          = 16
	SigLen           = 64
	StampLen         = 32
	TimestampSize    = 8
	StructOverhead   = 8
	Overhead         = 2*DestLen + SigLen + TimestampSize + StructOverhead // 112
	EncryptedPktMDU  = reticulum.EncryptedMDU + TimestampSize              // 391
	PacketMaxContent = EncryptedPktMDU - Overhead + DestLen                // 295
	LinkMaxContent   = 431 - Overhead                                      // 319 at MTU 500
)

// Method is how a message travels.
type Method int

const (
	Opportunistic Method = 1 // one encrypted packet to lxmf.delivery
	Direct        Method = 2 // over a link (packet or resource)
)

func (m Method) String() string {
	switch m {
	case Opportunistic:
		return "opportunistic"
	case Direct:
		return "direct"
	}
	return "unknown"
}

// Message is one LXMF message.
type Message struct {
	Dest      [DestLen]byte
	Source    [DestLen]byte
	Timestamp float64
	Title     []byte
	Content   []byte
	Fields    Value // map, may be empty
	Stamp     []byte
	Hash      [32]byte
	Signature []byte
	Packed    []byte

	// Set on inbound
	SignatureValid bool
	SourceKnown    bool
	StampValid     bool
	StampChecked   bool
	StampValue     int
	Method         Method
	Iface          string
	ReceivedAt     time.Time
}

// ContentSize is what the method choice is judged on: packed payload minus
// timestamp and structure overhead (LXMessage.pack content_size).
func (m *Message) ContentSize() int {
	return len(m.payloadBytes(false)) - TimestampSize - StructOverhead
}

func (m *Message) payloadValue(withStamp bool) Value {
	fields := m.Fields
	if fields.Kind != KindMap {
		fields = MapOf()
	}
	v := Array(Float(m.Timestamp), Bin(m.Title), Bin(m.Content), fields)
	if withStamp && m.Stamp != nil {
		v.Array = append(v.Array, Bin(m.Stamp))
	}
	return v
}

func (m *Message) payloadBytes(withStamp bool) []byte { return Pack(m.payloadValue(withStamp)) }

// Pack computes the hash, generates a stamp when stampCost > 0, signs with
// the source identity and produces the packed form (LXMessage.pack).
func (m *Message) Pack(source *reticulum.Identity, stampCost int, stamper func(hash [32]byte, cost int) []byte) error {
	if m.Timestamp == 0 {
		m.Timestamp = float64(time.Now().UnixNano()) / 1e9
	}
	if m.Title == nil {
		m.Title = []byte{}
	}
	if m.Content == nil {
		m.Content = []byte{}
	}
	payload := m.payloadBytes(false)
	hashed := make([]byte, 0, 2*DestLen+len(payload))
	hashed = append(hashed, m.Dest[:]...)
	hashed = append(hashed, m.Source[:]...)
	hashed = append(hashed, payload...)
	m.Hash = sha256.Sum256(hashed)
	if stampCost > 0 && stamper != nil {
		m.Stamp = stamper(m.Hash, stampCost)
		if m.Stamp == nil {
			return errors.New("lxmf: stamp generation failed")
		}
	}
	signed := append(append([]byte(nil), hashed...), m.Hash[:]...)
	m.Signature = source.Sign(signed)
	m.SignatureValid = true
	packed := make([]byte, 0, 2*DestLen+SigLen+len(payload)+40)
	packed = append(packed, m.Dest[:]...)
	packed = append(packed, m.Source[:]...)
	packed = append(packed, m.Signature...)
	packed = append(packed, m.payloadBytes(true)...)
	m.Packed = packed
	return nil
}

// Unpack parses a packed message (LXMessage.unpack_from_bytes). The
// signature is verified when sourceSigPub is not nil.
func Unpack(packed []byte, sourceSigPub func(source [DestLen]byte) []byte) (*Message, error) {
	if len(packed) < 2*DestLen+SigLen+1 {
		return nil, errors.New("lxmf: packed message too short")
	}
	m := &Message{}
	copy(m.Dest[:], packed[:DestLen])
	copy(m.Source[:], packed[DestLen:2*DestLen])
	m.Signature = append([]byte(nil), packed[2*DestLen:2*DestLen+SigLen]...)
	payloadRaw := packed[2*DestLen+SigLen:]
	v, n, err := UnpackValue(payloadRaw)
	if err != nil {
		return nil, err
	}
	if n != len(payloadRaw) || v.Kind != KindArray || len(v.Array) < 4 {
		return nil, errors.New("lxmf: payload is not a 4-element array")
	}
	if len(v.Array) > 4 && v.Array[4].Kind == KindBin {
		m.Stamp = v.Array[4].Bin
	}
	if v.Array[0].Kind == KindFloat {
		m.Timestamp = v.Array[0].Float
	} else if i, ok := v.Array[0].AsInt(); ok {
		m.Timestamp = float64(i)
	}
	m.Title = binOrStr(v.Array[1])
	m.Content = binOrStr(v.Array[2])
	m.Fields = v.Array[3]
	// The hash is over the re-encoding of the first four elements.
	payload := Pack(Array(v.Array[0], v.Array[1], v.Array[2], v.Array[3]))
	hashed := make([]byte, 0, 2*DestLen+len(payload))
	hashed = append(hashed, m.Dest[:]...)
	hashed = append(hashed, m.Source[:]...)
	hashed = append(hashed, payload...)
	m.Hash = sha256.Sum256(hashed)
	m.Packed = append([]byte(nil), packed...)
	if sourceSigPub != nil {
		if pub := sourceSigPub(m.Source); pub != nil {
			m.SourceKnown = true
			signed := append(append([]byte(nil), hashed...), m.Hash[:]...)
			m.SignatureValid = reticulum.VerifySignature(pub, signed, m.Signature)
		}
	}
	return m, nil
}

func binOrStr(v Value) []byte {
	switch v.Kind {
	case KindBin:
		return v.Bin
	case KindStr:
		return []byte(v.Str)
	}
	return []byte{}
}

// String is a short description for logs.
func (m *Message) String() string {
	return fmt.Sprintf("<LXMessage %x>", m.Hash[:8])
}
