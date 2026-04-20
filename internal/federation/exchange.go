package federation

// Wire-level manifest exchange over an arbitrary ReticulumInterface.
// The moment a trusted bearer comes up (today: ble_peer_N link
// established), the bridge sends its signed capability manifest;
// the other end receives, verifies, persists.  Later layers
// (auto-bond manager) drive off the persisted state.
//
// We share the bearer with regular Reticulum traffic, so manifests
// are tagged with a distinctive 4-byte magic prefix.  A random
// Reticulum packet happens to start with "MSMF" with probability
// ~1 in 2^32 — acceptable collision rate given the consequence
// (a malformed manifest is dropped by Verify).
//
// [MESHSAT-635 Phase 3]

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// magicPrefix is the 4-byte tag that identifies wire bytes as a
// MeshSat manifest frame rather than a regular Reticulum packet.
var magicPrefix = []byte{'M', 'S', 'M', 'F'}

// WrapManifest returns [magic || manifest-json-bytes].  The receiver
// uses UnwrapManifest to do the inverse.
func WrapManifest(m *CapabilityManifest) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("federation: wrap nil manifest")
	}
	body, err := m.Marshal()
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(magicPrefix)+len(body))
	out = append(out, magicPrefix...)
	out = append(out, body...)
	return out, nil
}

// IsManifestFrame tells a receiver whether a chunk of bytes starts
// with the manifest magic — cheap gate before the (more expensive)
// Unwrap+Verify path.
func IsManifestFrame(data []byte) bool {
	return len(data) > len(magicPrefix) && bytes.Equal(data[:len(magicPrefix)], magicPrefix)
}

// UnwrapManifest peels the magic prefix + unmarshals.  Does NOT
// verify — caller controls the clock + maxAge.
func UnwrapManifest(data []byte) (*CapabilityManifest, error) {
	if !IsManifestFrame(data) {
		return nil, fmt.Errorf("federation: not a manifest frame")
	}
	return UnmarshalManifest(data[len(magicPrefix):])
}

// BuildManifestBytes constructs + signs a manifest and returns the
// wire-ready bytes (magic-prefixed). Small convenience for callers
// that just want to hand the result to an interface Send.  Uses the
// opaque-signer path so callers with wrapped keys (SigningService)
// never need to expose ed25519.PrivateKey directly.
func BuildManifestBytes(signerID, routingIdentity, alias string, bearers []Bearer, signFn func([]byte) []byte) ([]byte, error) {
	m := NewManifest(signerID, routingIdentity, alias, bearers)
	if err := m.SignWith(signFn); err != nil {
		return nil, err
	}
	return WrapManifest(m)
}

// DispatchPacket is the receive-side demultiplexer.  Call it from any
// Reticulum-interface packet callback (ble_peer_N client-side, ble_0
// peripheral-side, etc.).  If the frame is a manifest, onManifest is
// called with the parsed (not verified) manifest.  Otherwise
// onReticulum is called with the raw bytes.  Both callbacks are
// allowed to be nil — the frame is then dropped silently.
func DispatchPacket(packet []byte, onManifest func(*CapabilityManifest), onReticulum func([]byte)) {
	if IsManifestFrame(packet) {
		if onManifest == nil {
			return
		}
		m, err := UnwrapManifest(packet)
		if err != nil {
			return
		}
		onManifest(m)
		return
	}
	if onReticulum != nil {
		onReticulum(packet)
	}
}

// ManifestSummary is what gets persisted alongside the raw manifest
// JSON — plays nicely with the trusted_peers.go UpsertTrustedPeer
// signature so callers don't have to fish the fields out themselves.
type ManifestSummary struct {
	SignerID        string
	RoutingIdentity string
	Alias           string
	ManifestJSON    string
}

// SummariseForPersist extracts the fields the trusted_peers store
// needs. Alias defaults to signer_id-short if the manifest left it
// empty — something the UI can always render.
func SummariseForPersist(m *CapabilityManifest) (*ManifestSummary, error) {
	if m == nil {
		return nil, fmt.Errorf("federation: summarise nil manifest")
	}
	alias := m.Alias
	if alias == "" && len(m.SignerID) >= 8 {
		alias = "peer-" + m.SignerID[:8]
	}
	// Re-marshal to normalise — the persisted JSON is the same
	// canonical shape regardless of spacing in what landed on the
	// wire.
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return &ManifestSummary{
		SignerID:        m.SignerID,
		RoutingIdentity: m.RoutingIdentity,
		Alias:           alias,
		ManifestJSON:    string(raw),
	}, nil
}
