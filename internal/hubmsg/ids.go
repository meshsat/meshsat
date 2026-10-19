// Package hubmsg is the Bridge's side of the app route hub_msg (HeMB-NG stage 2, MESHSAT-1584):
// the identities (contract C1e), the content digest (C1d), the receiver's inbox (C3), the
// submission a sender publishes (C4), the recipient's answer (C5) and the order-independent
// reducer of an operation's evidence (C6, C6a). The contract is the Hub's
// docs/bridge-hub-protocol.md "Messaging v1"; the shared test vectors are
// internal/hembfixtures/testdata/v1 (release fixtures-v1.1), which this package's tests read.
// The Hub's internal/hubmsg is the same logic in the other codebase: a fix here goes there too.
package hubmsg

import (
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // UUIDv5 is defined over SHA-1 (RFC 9562 5.5); not a security use
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Namespace is the pinned UUIDv5 namespace of HeMB-NG (C1e):
// UUIDv5(NAMESPACE_URL, "https://meshsat.net/ns/hemb-ng/v1").
const Namespace = "a389cf74-e475-53d9-b656-d994a485fe0a"

// IDContext carries what the two documented UUIDv5 exceptions are checked against (C1e).
type IDContext struct {
	// Sender is "hub" for a mid the Hub derived for provider ingress (C1c), and Name the
	// "<provider>:<provider message id>" it was derived from.
	Sender, Name string
	// BridgeID and RunStartMs: an SOS run in the old format (C10); RunStartMs < 0 means absent.
	BridgeID   string
	RunStartMs int64
}

// parseCanonical returns the 16 bytes of a UUID in its canonical text: lower case, 8-4-4-4-12.
func parseCanonical(s string) ([16]byte, bool) {
	var b [16]byte
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return b, false
	}
	hexs := s[0:8] + s[9:13] + s[14:18] + s[19:23] + s[24:36]
	if strings.ToLower(hexs) != hexs {
		return b, false
	}
	if _, err := hex.Decode(b[:], []byte(hexs)); err != nil {
		return b, false
	}
	return b, true
}

func format(b [16]byte) string {
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// ValidV7 reports whether s is a UUIDv7 in canonical text with the RFC variant.
func ValidV7(s string) bool {
	b, ok := parseCanonical(s)
	return ok && b[8]>>6 == 0b10 && b[6]>>4 == 7
}

// IDValid is C1e: v7 everywhere; v5 only for a Hub-derived mid (C1c) or an old-format SOS id (C10).
func IDValid(s, field string, c IDContext) bool {
	b, ok := parseCanonical(s)
	if !ok || b[8]>>6 != 0b10 {
		return false
	}
	switch b[6] >> 4 {
	case 7:
		// An SOS id names its run: its time is the run's start (C10, r4), so the same
		// run has the same id on every bearer and after a restart.
		if field == "sos_id" {
			return c.RunStartMs >= 0 && TimeOfV7(b) == c.RunStartMs
		}
		return true
	case 5:
		switch {
		case field == "mid" && c.Sender == "hub" && c.Name != "":
			return UUIDv5(c.Name) == s
		case field == "sos_id" && c.BridgeID != "" && c.RunStartMs >= 0:
			return UUIDv5(c.BridgeID+":"+strconv.FormatInt(c.RunStartMs, 10)) == s
		}
	}
	return false
}

// SOSIDValid is C10's rule for an sos_id carried with its run's start: a v7 whose first 48
// bits are runStartMs, or the old-format run's v5 under the bridge id the ingress
// authenticated. Anything else is refused.
func SOSIDValid(sosID, bridgeID string, runStartMs int64) bool {
	return IDValid(sosID, "sos_id", IDContext{BridgeID: bridgeID, RunStartMs: runStartMs})
}

// NewSOSID is the sos_id of a run that starts at runStartMs (C10): a UUIDv7 whose first 48
// bits are that start.
func NewSOSID(runStartMs int64) string { return NewV7(time.UnixMilli(runStartMs)) }

// LegacySOSID is the sos_id of a run that started before its Bridge spoke C10 (its id is the
// old millisecond run id): UUIDv5 of "<bridge id>:<run_start_ms>".
func LegacySOSID(bridgeID string, runStartMs int64) string {
	return UUIDv5(bridgeID + ":" + strconv.FormatInt(runStartMs, 10))
}

// TimeOfV7 is the Unix millisecond time in a UUIDv7's first 48 bits.
func TimeOfV7(b [16]byte) int64 {
	var ms [8]byte
	copy(ms[2:8], b[0:6])
	return int64(binary.BigEndian.Uint64(ms[:])) // #nosec G115 -- 48 bits fit
}

// UUIDBytes returns the 16 bytes of a UUID in canonical text, for a binary frame.
func UUIDBytes(s string) ([16]byte, bool) { return parseCanonical(s) }

// UUIDText is the canonical text of 16 UUID bytes, as a frame carries them.
func UUIDText(b [16]byte) string { return format(b) }

// UUIDv5 is the UUIDv5 of name under Namespace.
func UUIDv5(name string) string {
	ns, _ := parseCanonical(Namespace)
	h := sha1.New() //nolint:gosec // see the import
	h.Write(ns[:])
	h.Write([]byte(name))
	var b [16]byte
	copy(b[:], h.Sum(nil))
	b[6] = 0x50 | b[6]&0x0F
	b[8] = 0x80 | b[8]&0x3F
	return format(b)
}

// NewV7 makes a UUIDv7 for now (RFC 9562 5.7), the time in its first 48 bits.
func NewV7(now time.Time) string {
	var b [16]byte
	if _, err := rand.Read(b[6:]); err != nil {
		panic(fmt.Sprintf("hubmsg: no randomness: %v", err))
	}
	var ms [8]byte
	binary.BigEndian.PutUint64(ms[:], uint64(max(now.UnixMilli(), 0))) // #nosec G115 -- clamped to 0 and up
	copy(b[0:6], ms[2:8])
	b[6] = 0x70 | b[6]&0x0F
	b[8] = 0x80 | b[8]&0x3F
	return format(b)
}

// bytesOf returns the 16 raw bytes of a valid UUID text.
func bytesOf(s string) ([16]byte, bool) { return parseCanonical(s) }
