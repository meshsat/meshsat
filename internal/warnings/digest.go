// Package warnings is the kit's side of the last-mile warning relay
// (MESHSAT-1734): the Hub hands this Bridge a compact digest of an official
// public warning for its area, signed by the Hub's directory key the Bridge
// already pins, on the Bridge's own {root}/bridge/<id>/warn/in or as a
// satellite MT; the Bridge verifies it, keeps it, and puts it on the mesh,
// on APRS, on its 2G cell, to the LXMF peers heard recently, on its event
// stream and at the top of the Community Post portal (the Bridge's
// docs/warning-relay.md). The contract, the byte layout and the test vector
// are the Hub's docs/warning-relay.md; the layout here must stay byte for
// byte the Hub's internal/warnings.
package warnings

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"

	"meshsat/internal/compress"
)

// Digest v1. Every integer is big-endian; the last 64 bytes sign everything
// before them (ECDSA P-256 over SHA-256, r then s, 32 bytes each).
//
//	off len  field
//	 0   1   magic 0x57 'W' (the satellite MT type byte too)
//	 1   1   version 0x01
//	 2   1   msgType in the low 4 bits (1 Alert, 2 Update, 3 Cancel); bit 7 = test or exercise
//	 3   1   source (1 dwd, 2 meteoalarm-nl, 255 test)
//	 4   8   id: the first 8 bytes of SHA-256 of the CAP identifier
//	12   8   ref: the same of the identifier this Update or Cancel replaces; zero for an Alert
//	20   4   sent, Unix seconds
//	24   4   expires, Unix seconds, 0 for none
//	28   1   severity << 4 | urgency
//	29   1   certainty << 4 | category
//	30   1   event code
//	31   2   language, ISO 639-1 ASCII; 0 0 unknown
//	33   1   area kind << 4 | count (0 none; 1 geocodes, at most 8; 2 polygon, at most 12 points)
//	34   var area: a geocode is [type 1][value 9 bytes ASCII, zero padded]; a point is [lat int24][lon int24], degrees * 10000
//	var  1   text length n
//	var  n   text, SMAZ2 with the Meshtastic dictionary
//	end  64  signature over bytes [0, end)
const (
	Magic   byte = 0x57
	Version byte = 0x01

	MaxDigestBytes = 234
	SignatureBytes = 64
	headerBytes    = 34
	maxGeocodes    = 8
	maxPolygon     = 12
	geocodeBytes   = 10
	pointBytes     = 6
	MinDigestBytes = headerBytes + 1 + SignatureBytes

	flagTest = 0x80
)

// Message types.
const (
	TypeAlert  = 1
	TypeUpdate = 2
	TypeCancel = 3
)

// Sources.
const (
	SourceDWD          = 1
	SourceMeteoAlarmNL = 2
	SourceTest         = 255
)

// SourceName names a source as the Hub's feed rows do.
func SourceName(id int) string {
	switch id {
	case SourceDWD:
		return "dwd"
	case SourceMeteoAlarmNL:
		return "meteoalarm-nl"
	case SourceTest:
		return "test"
	}
	return "unknown"
}

// SourceLabel names a source as the label on a radio does: the sender a
// person would recognise.
func SourceLabel(id int) string {
	switch id {
	case SourceDWD:
		return "DWD"
	case SourceMeteoAlarmNL:
		return "KNMI via MeteoAlarm"
	case SourceTest:
		return "MeshSat test"
	}
	return "unknown source"
}

// Area kinds and geocode types.
const (
	AreaNone     = 0
	AreaGeocodes = 1
	AreaPolygon  = 2

	GeocodeOther      = 0
	GeocodeWarnCellID = 1
	GeocodeEMMAID     = 2
	GeocodeNUTS       = 3
)

var (
	severities  = []string{"Unknown", "Minor", "Moderate", "Severe", "Extreme"}
	urgencies   = []string{"Unknown", "Future", "Expected", "Immediate", "Past"}
	certainties = []string{"Unknown", "Unlikely", "Possible", "Likely", "Observed"}
	categories  = []string{"Other", "Met", "Geo", "Safety", "Security", "Rescue", "Fire", "Health", "Env", "Transport", "Infra", "CBRNE"}
	eventNames  = []string{"Other", "Wind", "Rain", "Snow/ice", "Thunderstorm", "Fog", "Heat", "Cold", "Coastal", "Flood", "Avalanche", "Wildfire", "Tornado"}
)

// Names of the codes; an unknown code reads as the table's first entry.
func SeverityName(c int) string  { return nameAt(severities, c) }
func UrgencyName(c int) string   { return nameAt(urgencies, c) }
func CertaintyName(c int) string { return nameAt(certainties, c) }
func CategoryName(c int) string  { return nameAt(categories, c) }
func EventName(c int) string     { return nameAt(eventNames, c) }

func nameAt(t []string, c int) string {
	if c < 0 || c >= len(t) {
		return t[0]
	}
	return t[c]
}

// Point is one polygon vertex.
type Point struct {
	Lat, Lon float64
}

// Geocode is one area code.
type Geocode struct {
	Type  int
	Value string
}

// Digest is a decoded digest.
type Digest struct {
	MsgType   int
	Test      bool
	Source    int
	ID        [8]byte
	Ref       [8]byte
	Sent      time.Time
	Expires   time.Time // zero for none
	Severity  int
	Urgency   int
	Certainty int
	Category  int
	Event     int
	Language  string
	Geocodes  []Geocode
	Polygon   []Point
	Text      string
}

var (
	ErrBadDigest = errors.New("warnings: not a digest")
	ErrSignature = errors.New("warnings: signature does not verify")
	ErrNoKey     = errors.New("warnings: no Hub directory key pinned")
)

// IsDigest reports whether a payload starts like a digest: the satellite
// gateways and the processor use it to hand such bytes over untouched.
func IsDigest(b []byte) bool {
	return len(b) >= MinDigestBytes && len(b) <= MaxDigestBytes && b[0] == Magic && b[1] == Version
}

// Verify checks a digest's signature against the pinned PKIX P-256 public
// key and decodes it. The signature is checked over the exact bytes before
// anything is read, so a field this version does not know is still covered.
func Verify(pubPKIX, wire []byte) (*Digest, error) {
	if len(pubPKIX) == 0 {
		return nil, ErrNoKey
	}
	if len(wire) < MinDigestBytes || len(wire) > MaxDigestBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrBadDigest, len(wire))
	}
	key, err := x509.ParsePKIXPublicKey(pubPKIX)
	if err != nil {
		return nil, fmt.Errorf("warnings: pinned key: %w", err)
	}
	pub, ok := key.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("warnings: the pinned key is not ECDSA")
	}
	body, sig := wire[:len(wire)-SignatureBytes], wire[len(wire)-SignatureBytes:]
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	digest := sha256.Sum256(body)
	if !ecdsa.Verify(pub, digest[:], r, s) {
		return nil, ErrSignature
	}
	return Decode(wire)
}

// Decode reads a digest without checking its signature.
func Decode(wire []byte) (*Digest, error) {
	if len(wire) < MinDigestBytes || len(wire) > MaxDigestBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrBadDigest, len(wire))
	}
	if wire[0] != Magic || wire[1] != Version {
		return nil, fmt.Errorf("%w: magic %#x version %d", ErrBadDigest, wire[0], wire[1])
	}
	body := wire[:len(wire)-SignatureBytes]
	d := &Digest{
		MsgType: int(body[2] & 0x0F),
		Test:    body[2]&flagTest != 0,
		Source:  int(body[3]),
	}
	if d.MsgType < TypeAlert || d.MsgType > TypeCancel {
		return nil, fmt.Errorf("%w: message type %d", ErrBadDigest, d.MsgType)
	}
	copy(d.ID[:], body[4:12])
	copy(d.Ref[:], body[12:20])
	d.Sent = fromUnix32(binary.BigEndian.Uint32(body[20:24]))
	d.Expires = fromUnix32(binary.BigEndian.Uint32(body[24:28]))
	d.Severity, d.Urgency = int(body[28]>>4), int(body[28]&0x0F)
	d.Certainty, d.Category = int(body[29]>>4), int(body[29]&0x0F)
	d.Event = int(body[30])
	if body[31] != 0 && body[32] != 0 {
		d.Language = string(body[31:33])
	}
	kind, count := int(body[33]>>4), int(body[33]&0x0F)
	off := headerBytes
	switch kind {
	case AreaNone:
		if count != 0 {
			return nil, fmt.Errorf("%w: area count without an area", ErrBadDigest)
		}
	case AreaGeocodes:
		if count > maxGeocodes || len(body) < off+count*geocodeBytes {
			return nil, fmt.Errorf("%w: geocodes", ErrBadDigest)
		}
		for i := 0; i < count; i++ {
			slot := body[off : off+geocodeBytes]
			d.Geocodes = append(d.Geocodes, Geocode{Type: int(slot[0]), Value: strings.TrimRight(string(slot[1:]), "\x00")})
			off += geocodeBytes
		}
	case AreaPolygon:
		if count < 3 || count > maxPolygon || len(body) < off+count*pointBytes {
			return nil, fmt.Errorf("%w: polygon", ErrBadDigest)
		}
		for i := 0; i < count; i++ {
			d.Polygon = append(d.Polygon, Point{Lat: fromInt24(body[off : off+3]), Lon: fromInt24(body[off+3 : off+6])})
			off += pointBytes
		}
	default:
		return nil, fmt.Errorf("%w: area kind %d", ErrBadDigest, kind)
	}
	if len(body) < off+1 {
		return nil, fmt.Errorf("%w: no text length", ErrBadDigest)
	}
	n := int(body[off])
	off++
	if len(body) != off+n {
		return nil, fmt.Errorf("%w: text length %d, %d bytes left", ErrBadDigest, n, len(body)-off)
	}
	if n > 0 {
		text, err := compress.Decompress(body[off:off+n], compress.DictMeshtastic)
		if err != nil || !utf8.Valid(text) {
			return nil, fmt.Errorf("%w: text", ErrBadDigest)
		}
		d.Text = string(text)
	}
	return d, nil
}

// IDHex is the id as the log and the API show it.
func (d *Digest) IDHex() string { return fmt.Sprintf("%x", d.ID[:]) }

// RefHex is the ref as the log and the API show it; "" for none.
func (d *Digest) RefHex() string {
	if d.Ref == [8]byte{} {
		return ""
	}
	return fmt.Sprintf("%x", d.Ref[:])
}

// TypeName names the message type.
func (d *Digest) TypeName() string {
	switch d.MsgType {
	case TypeAlert:
		return "Alert"
	case TypeUpdate:
		return "Update"
	case TypeCancel:
		return "Cancel"
	}
	return "Unknown"
}

func fromUnix32(u uint32) time.Time {
	if u == 0 {
		return time.Time{}
	}
	return time.Unix(int64(u), 0).UTC()
}

func fromInt24(b []byte) float64 {
	u := int64(b[0])<<16 | int64(b[1])<<8 | int64(b[2])
	if u&0x800000 != 0 {
		u -= 1 << 24
	}
	return float64(u) / 10000
}
