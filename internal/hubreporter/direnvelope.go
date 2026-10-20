package hubreporter

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The v2 directory envelope (contract C7.2; the Hub's
// internal/directory/envelope.go; fixture docs/fixtures/v1/
// directory_v2.json): {"v":2,"key_id","alg":"ES256","payload","sig"}, the
// signature over the exact payload bytes. [MESHSAT-1584]
const (
	EnvelopeVersion = 2
	EnvelopeAlg     = "ES256"
)

// Errors from OpenEnvelope.
var (
	ErrEnvelopeFormat = errors.New("not a v2 ES256 envelope")
	ErrUnknownKey     = errors.New("signed by a key this bridge does not trust")
	ErrBadSignature   = errors.New("signature does not verify")
)

// KeyID is a directory key's id: SHA-256 of its PKIX DER, hex.
func KeyID(pkix []byte) string {
	sum := sha256.Sum256(pkix)
	return hex.EncodeToString(sum[:])
}

func p256Key(pkix []byte) (*ecdsa.PublicKey, error) {
	k, err := x509.ParsePKIXPublicKey(pkix)
	if err != nil {
		return nil, err
	}
	pub, ok := k.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, errors.New("not an ECDSA P-256 key")
	}
	return pub, nil
}

// OpenEnvelope verifies a v2 envelope under trusted (PKIX DER) and returns
// its payload bytes. Nothing of the payload is read before the signature
// verifies.
func OpenEnvelope(raw []byte, trusted []byte) ([]byte, error) {
	var env struct {
		V       int    `json:"v"`
		KeyID   string `json:"key_id"`
		Alg     string `json:"alg"`
		Payload []byte `json:"payload"`
		Sig     []byte `json:"sig"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.V != EnvelopeVersion || env.Alg != EnvelopeAlg || len(env.Sig) == 0 {
		return nil, ErrEnvelopeFormat
	}
	if env.KeyID != KeyID(trusted) {
		return nil, fmt.Errorf("%w: %s", ErrUnknownKey, env.KeyID)
	}
	pub, err := p256Key(trusted)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnknownKey, err)
	}
	digest := sha256.Sum256(env.Payload)
	if !ecdsa.VerifyASN1(pub, digest[:], env.Sig) {
		return nil, ErrBadSignature
	}
	return env.Payload, nil
}

// directoryKeyClient is the HTTP client of FetchDirectoryKey (system
// roots); a test swaps in its server's client. FetchDirectoryKey never lets
// it follow a redirect, whatever its own policy.
var directoryKeyClient = http.DefaultClient

// FetchDirectoryKey asks the Hub for its directory signing key, for a
// bridge that has pinned none (GET /api/relay/directory-key, public). Only
// an https URL with a host is used (web PKI vouches for the Hub), no
// redirect is followed (an https answer that points elsewhere, plain http
// included, is refused: r19 C7-R4), and the key must hash to the id the Hub
// names. This is the one time a key is taken without a signature; after it,
// only a signed rotation moves it, and a key already pinned is never
// fetched again (pinDirectoryKey). [MESHSAT-1584 C7]
func FetchDirectoryKey(ctx context.Context, hubAPIURL string) ([]byte, error) {
	base, err := url.Parse(strings.TrimSuffix(hubAPIURL, "/"))
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil {
		return nil, fmt.Errorf("directory key: the Hub API URL %q is not an https URL", hubAPIURL)
	}
	endpoint := base.JoinPath("api", "relay", "directory-key")
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	client := *directoryKeyClient
	client.CheckRedirect = func(next *http.Request, _ []*http.Request) error {
		return fmt.Errorf("directory key: redirect to %s refused", next.URL.Redacted())
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if final := resp.Request.URL; final.Scheme != "https" || final.Host != endpoint.Host {
		return nil, fmt.Errorf("directory key: answered from %s, not %s", final.Redacted(), endpoint.Redacted())
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("directory key: HTTP %d", resp.StatusCode)
	}
	var k struct {
		KeyID string `json:"key_id"`
		PKIX  []byte `json:"pkix"`
		Alg   string `json:"alg"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&k); err != nil {
		return nil, fmt.Errorf("directory key: %w", err)
	}
	if k.Alg != EnvelopeAlg || KeyID(k.PKIX) != k.KeyID {
		return nil, errors.New("directory key: the key does not hash to its id")
	}
	if _, err := p256Key(k.PKIX); err != nil {
		return nil, fmt.Errorf("directory key: %w", err)
	}
	return k.PKIX, nil
}
