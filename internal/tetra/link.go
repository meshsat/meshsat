package tetra

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// LinkPID carries the MeshSat link control between two Bridges' radios:
// acknowledgements of texts and the hello a Bridge sends when its radio
// comes up. It is in the range ETSI leaves to applications without the
// SDS-TL header (0x40-0x7E, TS 100 392-2 table 29.21), so a plain handheld
// has no use for it and shows nothing, and the texts themselves stay plain
// SDS-TL text every handheld reads. [MESHSAT-1588]
//
// Why the Bridges acknowledge each other: in Direct Mode two radios that
// key up at the same instant both report the SDS sent and neither
// arrives (half duplex; measured 17 Oct 2026). Radio-level delivery
// reports would need each radio to have its own number; the kit radios
// share one, so the Bridges confirm receipt themselves.
const LinkPID byte = 0x4D

const linkVersion = 1

// Link control kinds.
const (
	LinkAck        byte = 1 // "I received your text <ref, hash>"
	LinkHello      byte = 2 // "a MeshSat Bridge listens on this group"
	LinkHelloReply byte = 3 // "and so do I"
)

// Link is one link-control SDS.
type Link struct {
	Kind byte
	Ref  byte   // ack: the SDS-TL message reference of the text
	Hash uint32 // ack: TextHash of the text's bytes
	Tag  uint32 // hello, hello reply: the sending Bridge's random tag
}

// TextHash identifies a text's bytes as they went on air (the body after
// the text header, without the identity the radio appends): a sender and
// a receiver compute the same value without the text carrying an id.
func TextHash(body []byte) uint32 {
	sum := sha256.Sum256(body)
	return binary.BigEndian.Uint32(sum[:4])
}

// BodyOf is the on-air bytes of a text as EncodeText sends it.
func BodyOf(text string) []byte {
	_, body := encodeBody(text)
	return body
}

// EncodeLink builds a link-control PDU and its length in bits.
func EncodeLink(l Link) ([]byte, int) {
	pdu := []byte{LinkPID, linkVersion<<4 | l.Kind&0x0F}
	switch l.Kind {
	case LinkAck:
		pdu = append(pdu, l.Ref)
		pdu = binary.BigEndian.AppendUint32(pdu, l.Hash)
	default:
		pdu = binary.BigEndian.AppendUint32(pdu, l.Tag)
	}
	return pdu, len(pdu) * 8
}

// DecodeLink reads a link-control PDU (PID LinkPID).
func DecodeLink(pdu []byte) (Link, error) {
	if len(pdu) < 2 || pdu[0] != LinkPID {
		return Link{}, fmt.Errorf("tetra: not a MeshSat link PDU")
	}
	if v := pdu[1] >> 4; v != linkVersion {
		return Link{}, fmt.Errorf("tetra: MeshSat link version %d", v)
	}
	l := Link{Kind: pdu[1] & 0x0F}
	switch l.Kind {
	case LinkAck:
		if len(pdu) < 7 {
			return Link{}, fmt.Errorf("tetra: link ack of %d bytes", len(pdu))
		}
		l.Ref = pdu[2]
		l.Hash = binary.BigEndian.Uint32(pdu[3:7])
	case LinkHello, LinkHelloReply:
		if len(pdu) < 6 {
			return Link{}, fmt.Errorf("tetra: link hello of %d bytes", len(pdu))
		}
		l.Tag = binary.BigEndian.Uint32(pdu[2:6])
	default:
		return Link{}, fmt.Errorf("tetra: link kind %d", l.Kind)
	}
	return l, nil
}
