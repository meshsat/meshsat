package gateway

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// TAKEnrollConfig holds enrollment-specific configuration.
type TAKEnrollConfig struct {
	ServerURL string `json:"enroll_server_url"` // e.g., https://tak-server:8446
	Username  string `json:"enroll_username"`
	Password  string `json:"enroll_password"`
}

// TAKEnrollResult holds the result of a certificate enrollment.
type TAKEnrollResult struct {
	ClientCertPEM []byte
	ClientKeyPEM  []byte
	TruststorePEM []byte
	Subject       string
	NotAfter      time.Time
	Fingerprint   string
}

// TAKEnroll performs certificate enrollment against a TAK Server on port 8446.
// Flow: POST CSR with username/password → receive signed cert + truststore in ZIP.
func TAKEnroll(cfg TAKEnrollConfig) (*TAKEnrollResult, error) {
	if cfg.ServerURL == "" {
		return nil, fmt.Errorf("tak enroll: server URL required")
	}
	if cfg.Username == "" || cfg.Password == "" {
		return nil, fmt.Errorf("tak enroll: username and password required")
	}

	// TAK Server enrollment uses basic auth over TLS (skip verify for enrollment)
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // enrollment bootstraps trust
		},
	}

	enrollURL := strings.TrimRight(cfg.ServerURL, "/") + "/Marti/api/tls/signClient/v2"
	req, err := http.NewRequest("GET", enrollURL, nil)
	if err != nil {
		return nil, fmt.Errorf("tak enroll: build request: %w", err)
	}
	req.SetBasicAuth(cfg.Username, cfg.Password)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tak enroll: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("tak enroll: server returned %d: %s", resp.StatusCode, string(body))
	}

	zipData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("tak enroll: read response: %w", err)
	}

	return parseEnrollmentPackage(zipData)
}

// parseEnrollmentPackage extracts cert, key, and truststore from the enrollment ZIP.
func parseEnrollmentPackage(data []byte) (*TAKEnrollResult, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("tak enroll: invalid ZIP: %w", err)
	}

	result := &TAKEnrollResult{}

	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			continue
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			continue
		}

		name := strings.ToLower(f.Name)
		switch {
		case strings.Contains(name, "truststore") && strings.HasSuffix(name, ".p12"):
			// PKCS12 truststore — for now store raw, could convert to PEM
			result.TruststorePEM = content
		case strings.HasSuffix(name, ".p12") && !strings.Contains(name, "truststore"):
			// Client PKCS12 — store raw for now
			result.ClientCertPEM = content
		case strings.HasSuffix(name, ".pem") && strings.Contains(name, "cert"):
			result.ClientCertPEM = content
		case strings.HasSuffix(name, ".pem") && strings.Contains(name, "key"):
			result.ClientKeyPEM = content
		case strings.HasSuffix(name, ".pem") && strings.Contains(name, "trust"):
			result.TruststorePEM = content
		case strings.HasSuffix(name, ".pem"):
			// Generic PEM — try to parse as cert
			block, _ := pem.Decode(content)
			if block != nil && block.Type == "CERTIFICATE" {
				if result.TruststorePEM == nil {
					result.TruststorePEM = content
				}
			}
		}
	}

	// Parse cert metadata
	if len(result.ClientCertPEM) > 0 {
		block, _ := pem.Decode(result.ClientCertPEM)
		if block != nil {
			cert, err := x509.ParseCertificate(block.Bytes)
			if err == nil {
				result.Subject = cert.Subject.CommonName
				result.NotAfter = cert.NotAfter
				hash := sha256.Sum256(cert.Raw)
				result.Fingerprint = fmt.Sprintf("%x", hash[:8])
			}
		}
	}

	if result.ClientCertPEM == nil {
		return nil, fmt.Errorf("tak enroll: no client certificate found in enrollment package")
	}

	log.Info().
		Str("subject", result.Subject).
		Time("expires", result.NotAfter).
		Msg("tak: certificate enrollment successful")

	return result, nil
}

// StoreEnrolledCert saves the enrolled certificate to the credential cache.
func StoreEnrolledCert(db *database.DB, result *TAKEnrollResult) error {
	row := &database.CredentialCacheRow{
		ID:              "tak-enrolled-cert",
		Provider:        "tak",
		Name:            "TAK Server Enrolled Certificate",
		CredType:        "client_cert",
		EncryptedData:   result.ClientCertPEM,
		CertSubject:     result.Subject,
		CertNotAfter:    result.NotAfter.Format(time.RFC3339),
		CertFingerprint: result.Fingerprint,
		Version:         1,
		Source:          "enrollment",
		Applied:         1,
	}
	if err := db.InsertCredentialCache(row); err != nil {
		return fmt.Errorf("tak enroll: store cert: %w", err)
	}

	// Store truststore separately
	if result.TruststorePEM != nil {
		tsRow := &database.CredentialCacheRow{
			ID:            "tak-enrolled-truststore",
			Provider:      "tak",
			Name:          "TAK Server Truststore",
			CredType:      "ca_cert",
			EncryptedData: result.TruststorePEM,
			Version:       1,
			Source:        "enrollment",
			Applied:       1,
		}
		if err := db.InsertCredentialCache(tsRow); err != nil {
			return fmt.Errorf("tak enroll: store truststore: %w", err)
		}
	}

	// Store key separately
	if result.ClientKeyPEM != nil {
		keyRow := &database.CredentialCacheRow{
			ID:            "tak-enrolled-key",
			Provider:      "tak",
			Name:          "TAK Server Enrolled Key",
			CredType:      "client_key",
			EncryptedData: result.ClientKeyPEM,
			Version:       1,
			Source:        "enrollment",
			Applied:       1,
		}
		if err := db.InsertCredentialCache(keyRow); err != nil {
			return fmt.Errorf("tak enroll: store key: %w", err)
		}
	}

	return nil
}
