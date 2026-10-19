package hubmsg

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
)

// ContentTag is the first field of the content digest's preimage (C1d).
const ContentTag = "meshsat.content/1"

// ContentPreimage is what content_sha256 hashes (C1d): each field as a 4-byte big-endian length
// and its bytes, in this order: the tag, mid (16 raw bytes), the recipient principal, the
// content type, mid_expires_at (unix ms, 8 bytes big-endian), the body.
func ContentPreimage(mid, recipient, contentType string, midExpiresAt int64, body []byte) ([]byte, error) {
	m, ok := bytesOf(mid)
	if !ok {
		return nil, errors.New("hubmsg: mid is not a canonical UUID")
	}
	// The reference packs both as unsigned (struct ">Q", ">I") and refuses what does not fit.
	if midExpiresAt < 0 {
		return nil, errors.New("hubmsg: mid_expires_at is negative")
	}
	var exp [8]byte
	binary.BigEndian.PutUint64(exp[:], uint64(midExpiresAt)) // #nosec G115 -- not negative, checked above
	var out []byte
	for _, f := range [][]byte{[]byte(ContentTag), m[:], []byte(recipient), []byte(contentType), exp[:], body} {
		if uint64(len(f)) > math.MaxUint32 {
			return nil, errors.New("hubmsg: a field longer than 4 GiB")
		}
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(f))) // #nosec G115 -- bounded just above
		out = append(out, n[:]...)
		out = append(out, f...)
	}
	return out, nil
}

// ContentSHA256 is C1d's digest, lower-case hex. Every hop recomputes it from the delivered
// fields; a carried value is only compared, never trusted.
func ContentSHA256(mid, recipient, contentType string, midExpiresAt int64, body []byte) (string, error) {
	p, err := ContentPreimage(mid, recipient, contentType, midExpiresAt, body)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(p)
	return hex.EncodeToString(sum[:]), nil
}
