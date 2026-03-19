// Package certpin provides TLS certificate pinning for outbound HTTP connections.
// Pins are SHA-256 hashes of the DER-encoded Subject Public Key Info (SPKI).
// Supports primary + backup pin for zero-downtime certificate rotation.
package certpin

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/rs/zerolog/log"
)

// Pin holds one or more SPKI SHA-256 pin hashes (base64-encoded).
type Pin struct {
	Hashes []string // base64-encoded SHA-256 of SPKI
}

// NewPin creates a pin from one or more base64-encoded SPKI hashes.
func NewPin(hashes ...string) *Pin {
	return &Pin{Hashes: hashes}
}

// FromEnv creates a Pin from environment variables.
// Returns nil if no pins are configured.
func FromEnv(primaryKey, backupKey string) *Pin {
	primary := os.Getenv(primaryKey)
	backup := os.Getenv(backupKey)
	if primary == "" && backup == "" {
		return nil
	}
	var hashes []string
	if primary != "" {
		hashes = append(hashes, primary)
	}
	if backup != "" {
		hashes = append(hashes, backup)
	}
	return NewPin(hashes...)
}

// SPKIHash computes the base64-encoded SHA-256 hash of a certificate's SPKI.
func SPKIHash(cert *x509.Certificate) string {
	h := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(h[:])
}

// Verify checks if any certificate in the chain matches a pinned hash.
func (p *Pin) Verify(chains [][]*x509.Certificate) error {
	if len(p.Hashes) == 0 {
		return nil // no pins configured = allow all
	}

	pinSet := make(map[string]bool, len(p.Hashes))
	for _, h := range p.Hashes {
		pinSet[h] = true
	}

	for _, chain := range chains {
		for _, cert := range chain {
			hash := SPKIHash(cert)
			if pinSet[hash] {
				return nil // match found
			}
		}
	}

	return fmt.Errorf("certpin: no certificate in chain matches pinned hashes")
}

// PinnedTransport creates an http.Transport with certificate pinning.
// Falls back to default TLS if no pins configured.
func PinnedTransport(pin *Pin) *http.Transport {
	if pin == nil || len(pin.Hashes) == 0 {
		return http.DefaultTransport.(*http.Transport).Clone()
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			if err := pin.Verify(verifiedChains); err != nil {
				log.Warn().Err(err).Msg("certpin: certificate pin mismatch")
				return err
			}
			return nil
		},
	}

	return transport
}

// PinnedClient creates an http.Client with certificate pinning and timeout.
func PinnedClient(pin *Pin, timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: PinnedTransport(pin),
		Timeout:   timeout,
	}
}
