// Package relayclient is the bridge end of the Hub's WebSocket relay
// (MESHSAT-613; contract in meshsat-hub docs/relay.md). A kit behind carrier
// NAT opens one outbound WebSocket to the Hub and serves its clients through
// it: every client that connects to the Hub naming this bridge becomes one
// net.Conn here, the bridge terminates TLS on it with its Hub-issued
// certificate (requiring the client's), and the ordinary API router answers
// HTTP inside. The Hub only ever sees ciphertext.
package relayclient

import (
	"errors"
	"fmt"
)

// envelopeVersion is the first byte of every frame on the bridge socket:
// [0x01][len u8][client_id][payload]. The Hub strips it towards the client.
const envelopeVersion = 0x01

// maxFrame is the largest frame the Hub accepts; the TLS stream is chunked
// well under it.
const (
	maxFrame  = 64 << 10
	chunkSize = 32 << 10
)

var errEnvelope = errors.New("relayclient: malformed envelope")

func encodeEnvelope(clientID string, payload []byte) ([]byte, error) {
	if clientID == "" || len(clientID) > 255 {
		return nil, fmt.Errorf("relayclient: client id length %d", len(clientID))
	}
	n := len(clientID) // 1..255, checked above
	out := make([]byte, 0, 2+n+len(payload))
	out = append(out, envelopeVersion, byte(n)) // #nosec G115 -- bounded to 255 above
	out = append(out, clientID...)
	return append(out, payload...), nil
}

func decodeEnvelope(frame []byte) (clientID string, payload []byte, err error) {
	if len(frame) < 2 || frame[0] != envelopeVersion {
		return "", nil, errEnvelope
	}
	n := int(frame[1])
	if n == 0 || len(frame) < 2+n {
		return "", nil, errEnvelope
	}
	return string(frame[2 : 2+n]), frame[2+n:], nil
}
