// Package federation implements the cross-bearer trust-and-capability
// fabric for auto-federating paired MeshSat kits (MESHSAT-634 epic).
//
// On-the-wire: a signed CapabilityManifest exchanged once over a
// trusted bearer (today: the BLE GATT client link, MESHSAT-633).
// Receivers verify the Ed25519 signature + replay-defence fields, then
// persist the peer in `trusted_peers` (v51). Downstream subsystems
// (auto-bond manager, per-bearer registration) drive off the persisted
// peer state — not off the raw wire.
//
// Wire format is JSON. Not because it's optimal (it isn't — CBOR or
// protobuf would be smaller) but because this is a bootstrap / debug
// phase, and being able to eyeball a manifest in a log is worth a
// few hundred extra bytes today. Swap later once the protocol has
// stabilised.
//
// [MESHSAT-635]
package federation

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// ProtocolVersion is bumped when the manifest wire shape changes in a
// backward-incompatible way.  v1 = JSON with Ed25519 over the
// canonicalised signed-fields block.
const ProtocolVersion = 1

// DefaultMaxAge is how old an inbound manifest may be before it's
// rejected as a potential replay.
const DefaultMaxAge = 10 * time.Minute

// Bearer describes one addressable transport the peer offers.  Each
// bearer type fills in a subset of the fields — kept in one flat
// struct for simple JSON round-tripping.  Unknown types are preserved
// as-is so a newer peer with a bearer we don't understand doesn't
// break the exchange.
type Bearer struct {
	// Type is the bearer family: "mesh", "aprs", "wifi_ibss", "tcp",
	// "sms", "iridium_sbd", "iridium_imt", "zigbee", "ble".
	Type string `json:"type"`
	// InterfaceID is the peer's local interface name, e.g. "mesh_0" or
	// "ble_0". Informational — we don't route by it.
	InterfaceID string `json:"interface_id,omitempty"`
	// Address is the bearer-specific addressable identifier.
	//   mesh         → channel id
	//   aprs         → callsign-ssid
	//   wifi_ibss    → (empty; SSID is derived per-pair)
	//   tcp          → host:port
	//   sms          → E.164 phone number
	//   iridium_sbd  → IMEI
	//   iridium_imt  → IMEI
	//   zigbee       → IEEE 64-bit MAC
	//   ble          → adapter MAC
	Address string `json:"address,omitempty"`
	// Cost is per-message in the same units the bridge already uses
	// (0 = free, 0.05 = ~$0.05 SBD, etc.). Matches the routing
	// InterfaceRegistry cost semantics.
	Cost float64 `json:"cost,omitempty"`
	// MTU the peer accepts on this bearer (bytes).
	MTU int `json:"mtu,omitempty"`
	// Extras is a per-bearer typed blob for things that don't warrant a
	// dedicated field (preset, freq_plan, etc.). Opaque to the core;
	// bearer-probe strategies interpret their own slice of it.
	Extras map[string]interface{} `json:"extras,omitempty"`
}

// manifestBody is the portion covered by the Ed25519 signature.  Keep
// the field order stable — JSON marshalling in Go is sorted by field
// declaration order via the tag, which gives us deterministic bytes as
// long as Go versions don't change the encoder.  We therefore
// sign+verify against the exact bytes emitted by json.Marshal of this
// struct.
type manifestBody struct {
	Protocol        int      `json:"protocol"`
	SignerID        string   `json:"signer_id"`
	RoutingIdentity string   `json:"routing_identity,omitempty"`
	Alias           string   `json:"alias,omitempty"`
	Bearers         []Bearer `json:"bearers"`
	Timestamp       int64    `json:"timestamp_unix"`
	Nonce           string   `json:"nonce"`
}

// CapabilityManifest is the full wire object: signed body + signature.
// The signature is NOT part of the signed bytes — it wraps them.
type CapabilityManifest struct {
	manifestBody
	Signature string `json:"signature"` // hex-encoded Ed25519
}

// NewManifest constructs an unsigned manifest with the given signer and
// bearer list. Timestamp + nonce are filled in automatically.  The
// caller is expected to Sign() it before sending.
func NewManifest(signerID, routingIdentity, alias string, bearers []Bearer) *CapabilityManifest {
	return &CapabilityManifest{
		manifestBody: manifestBody{
			Protocol:        ProtocolVersion,
			SignerID:        signerID,
			RoutingIdentity: routingIdentity,
			Alias:           alias,
			Bearers:         bearers,
			Timestamp:       time.Now().Unix(),
			Nonce:           randomNonceHex(),
		},
	}
}

// signedBytes returns the exact byte-slice that Sign / Verify operate
// over — json.Marshal of just the body. Extracted so both sign and
// verify go through the same canonicalisation path.
func (m *CapabilityManifest) signedBytes() ([]byte, error) {
	return json.Marshal(m.manifestBody)
}

// Sign applies an Ed25519 signature with the given private key. Panics
// on nil key — callers should gate on SigningService.KeyIsWrapped or
// similar before signing.
func (m *CapabilityManifest) Sign(priv ed25519.PrivateKey) error {
	if priv == nil {
		return fmt.Errorf("federation: nil private key")
	}
	body, err := m.signedBytes()
	if err != nil {
		return fmt.Errorf("federation: marshal body: %w", err)
	}
	m.Signature = hex.EncodeToString(ed25519.Sign(priv, body))
	return nil
}

// BodyBytes returns the canonical byte-slice to be signed. Exposed for
// callers whose signing key lives behind an abstraction (e.g. the
// wrapped-key SigningService) and who therefore can't use Sign(priv)
// directly. Pair with SetSignatureBytes to finish.
func (m *CapabilityManifest) BodyBytes() ([]byte, error) {
	return m.signedBytes()
}

// SetSignatureBytes stores an already-computed Ed25519 signature. The
// inverse of the hex-encoded-string path taken by Sign.
func (m *CapabilityManifest) SetSignatureBytes(sig []byte) {
	m.Signature = hex.EncodeToString(sig)
}

// SignWith uses an opaque callback (returning raw Ed25519 bytes) to
// sign. Convenience wrapper for the BodyBytes + SetSignatureBytes
// pattern when the caller has a `func([]byte) []byte` already.
func (m *CapabilityManifest) SignWith(signFn func([]byte) []byte) error {
	if signFn == nil {
		return fmt.Errorf("federation: nil sign function")
	}
	body, err := m.signedBytes()
	if err != nil {
		return err
	}
	sig := signFn(body)
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("federation: sign fn returned %d bytes, want %d", len(sig), ed25519.SignatureSize)
	}
	m.SetSignatureBytes(sig)
	return nil
}

// VerifyResult describes a verify outcome without throwing away the
// full reason — we might want to log it or surface in the UI.
type VerifyResult struct {
	OK         bool
	Reason     string
	Age        time.Duration
	SignerID   string
	NumBearers int
}

// Verify checks the signature, protocol version, and replay-defence
// fields against `now`.  maxAge is how far back a manifest may be
// stamped; use DefaultMaxAge for the common case.  The SignerID inside
// the body is the key used for verification — a manifest is
// self-authenticating, and the trust decision ("do we trust this
// signer_id?") is a higher-layer concern.
func (m *CapabilityManifest) Verify(now time.Time, maxAge time.Duration) VerifyResult {
	if m == nil {
		return VerifyResult{Reason: "nil manifest"}
	}
	if m.Protocol != ProtocolVersion {
		return VerifyResult{Reason: fmt.Sprintf("unsupported protocol %d", m.Protocol)}
	}
	if m.SignerID == "" {
		return VerifyResult{Reason: "empty signer_id"}
	}
	if m.Nonce == "" {
		return VerifyResult{Reason: "empty nonce"}
	}
	pub, err := hex.DecodeString(m.SignerID)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return VerifyResult{Reason: "invalid signer_id encoding"}
	}
	sig, err := hex.DecodeString(m.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return VerifyResult{Reason: "invalid signature encoding"}
	}
	body, err := m.signedBytes()
	if err != nil {
		return VerifyResult{Reason: "re-marshal body failed"}
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), body, sig) {
		return VerifyResult{Reason: "ed25519 verify failed"}
	}
	stamped := time.Unix(m.Timestamp, 0)
	age := now.Sub(stamped)
	if age < 0 {
		age = -age
	}
	if maxAge > 0 && age > maxAge {
		return VerifyResult{Reason: fmt.Sprintf("manifest too old (age=%s max=%s)", age, maxAge)}
	}
	return VerifyResult{
		OK:         true,
		Reason:     "",
		Age:        age,
		SignerID:   m.SignerID,
		NumBearers: len(m.Bearers),
	}
}

// Marshal / Unmarshal round-trip the full manifest including signature.
func (m *CapabilityManifest) Marshal() ([]byte, error) {
	return json.Marshal(m)
}

// UnmarshalManifest parses wire bytes back into a manifest. Does NOT
// verify — call .Verify() separately so the caller controls the clock
// and maxAge.
func UnmarshalManifest(data []byte) (*CapabilityManifest, error) {
	var m CapabilityManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("federation: unmarshal manifest: %w", err)
	}
	return &m, nil
}

// randomNonceHex returns 16 bytes of crypto/rand hex-encoded. Panics
// on /dev/urandom failure — that's a broken machine.
func randomNonceHex() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("federation: crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
