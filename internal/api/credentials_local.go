package api

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// handleUploadCredential uploads a ZIP or PEM file containing TLS certificates/credentials.
// @Summary Upload credential (ZIP or PEM)
// @Description Uploads a certificate bundle (ZIP with PEM files) or a single PEM file, parses x509 metadata, encrypts and stores
// @Tags credentials
// @Accept multipart/form-data
// @Param file formance file true "ZIP or PEM file"
// @Param provider formData string true "Provider identifier (cloudloop_mqtt, rockblock, etc)"
// @Param name formData string true "Human-readable label"
// @Success 201 {object} map[string]interface{}
// @Failure 400 {object} map[string]string
// @Router /api/credentials/upload [post]
func (s *Server) handleUploadCredential(w http.ResponseWriter, r *http.Request) {
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "key store not available")
		return
	}

	if err := r.ParseMultipartForm(1 << 20); err != nil { // 1MB max
		writeError(w, http.StatusBadRequest, "parse form: "+err.Error())
		return
	}

	provider := r.FormValue("provider")
	name := r.FormValue("name")
	if provider == "" {
		writeError(w, http.StatusBadRequest, "provider is required")
		return
	}
	if name == "" {
		name = provider
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file is required: "+err.Error())
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read file: "+err.Error())
		return
	}

	// Detect ZIP vs PEM
	var pemFiles map[string][]byte
	if isZIP(data) {
		pemFiles, err = extractPEMsFromZIP(data)
		if err != nil {
			writeError(w, http.StatusBadRequest, "extract ZIP: "+err.Error())
			return
		}
	} else {
		// Treat as single PEM file
		pemFiles = map[string][]byte{header.Filename: data}
	}

	if len(pemFiles) == 0 {
		writeError(w, http.StatusBadRequest, "no PEM files found")
		return
	}

	// Classify PEM contents
	bundle, err := classifyPEMs(pemFiles)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Encrypt the bundle data with the bridge master key
	bundleJSON := bundle.toJSON()
	wrapped, err := s.keyStore.WrapData(bundleJSON)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encrypt: "+err.Error())
		return
	}

	// Generate ID from fingerprint or content hash
	id := bundle.id()

	row := &database.CredentialCacheRow{
		ID:              id,
		Provider:        provider,
		Name:            name,
		CredType:        bundle.credType(),
		EncryptedData:   wrapped,
		CertNotAfter:    bundle.notAfter,
		CertSubject:     bundle.subject,
		CertFingerprint: bundle.fingerprint,
		Version:         1,
		Source:          "local",
	}

	if err := s.db.InsertCredentialCache(row); err != nil {
		writeError(w, http.StatusInternalServerError, "store: "+err.Error())
		return
	}

	log.Info().Str("id", id).Str("provider", provider).Str("name", name).
		Str("type", bundle.credType()).Str("subject", bundle.subject).
		Str("expires", bundle.notAfter).Msg("credential uploaded")

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":          id,
		"provider":    provider,
		"name":        name,
		"cred_type":   bundle.credType(),
		"subject":     bundle.subject,
		"fingerprint": bundle.fingerprint,
		"not_after":   bundle.notAfter,
		"files_found": len(pemFiles),
	})
}

// handleListCredentials lists all cached credentials (no secrets).
// @Summary List credentials
// @Description Returns metadata for all cached credentials (encrypted data excluded)
// @Tags credentials
// @Success 200 {object} map[string]interface{}
// @Router /api/credentials [get]
func (s *Server) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.ListCredentialCache()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"credentials": rows})
}

// handleGetCredential returns credential metadata by ID.
// @Summary Get credential details
// @Tags credentials
// @Param id path string true "Credential ID"
// @Success 200 {object} database.CredentialCacheRow
// @Router /api/credentials/{id} [get]
func (s *Server) handleGetCredential(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	row, err := s.db.GetCredentialCache(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}
	// Strip encrypted data from response
	row.EncryptedData = nil
	writeJSON(w, http.StatusOK, row)
}

// handleDeleteCredential removes a credential.
// @Summary Delete credential
// @Tags credentials
// @Param id path string true "Credential ID"
// @Success 200 {object} map[string]string
// @Router /api/credentials/{id} [delete]
func (s *Server) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.db.DeleteCredentialCache(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleApplyCredential marks a credential as applied to a gateway.
// @Summary Apply credential to gateway
// @Tags credentials
// @Param id path string true "Credential ID"
// @Success 200 {object} map[string]string
// @Router /api/credentials/{id}/apply [post]
func (s *Server) handleApplyCredential(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.db.SetCredentialApplied(id, true); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "applied"})
}

// handleListExpiringCredentials returns credentials expiring within N days.
// @Summary List expiring credentials
// @Tags credentials
// @Param days query int false "Days until expiry (default 30)"
// @Success 200 {object} map[string]interface{}
// @Router /api/credentials/expiry [get]
func (s *Server) handleListExpiringCredentials(w http.ResponseWriter, r *http.Request) {
	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		fmt.Sscanf(d, "%d", &days)
	}
	rows, err := s.db.ListExpiringCredentials(days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"credentials": rows,
		"within_days": days,
	})
}

// --- PEM parsing helpers ---

type parsedBundle struct {
	caCertPEM     string
	clientCertPEM string
	clientKeyPEM  string
	subject       string
	notAfter      string
	fingerprint   string
}

func (b *parsedBundle) credType() string {
	if b.clientCertPEM != "" || b.caCertPEM != "" {
		return "mtls_bundle"
	}
	return "pem_file"
}

func (b *parsedBundle) id() string {
	if b.fingerprint != "" {
		return b.fingerprint[:16]
	}
	h := sha256.Sum256([]byte(b.caCertPEM + b.clientCertPEM))
	return hex.EncodeToString(h[:8])
}

func (b *parsedBundle) toJSON() []byte {
	// Simple JSON marshaling without importing encoding/json to avoid circular issues
	parts := []string{}
	if b.caCertPEM != "" {
		parts = append(parts, fmt.Sprintf(`"ca_cert_pem":%q`, b.caCertPEM))
	}
	if b.clientCertPEM != "" {
		parts = append(parts, fmt.Sprintf(`"client_cert_pem":%q`, b.clientCertPEM))
	}
	if b.clientKeyPEM != "" {
		parts = append(parts, fmt.Sprintf(`"client_key_pem":%q`, b.clientKeyPEM))
	}
	return []byte("{" + strings.Join(parts, ",") + "}")
}

func isZIP(data []byte) bool {
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K' && data[2] == 0x03 && data[3] == 0x04
}

func extractPEMsFromZIP(data []byte) (map[string][]byte, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}

	result := make(map[string][]byte)
	for _, f := range r.File {
		name := strings.ToLower(f.Name)
		if strings.HasSuffix(name, ".pem") || strings.HasSuffix(name, ".crt") || strings.HasSuffix(name, ".key") || strings.HasSuffix(name, ".cer") {
			rc, err := f.Open()
			if err != nil {
				continue
			}
			content, err := io.ReadAll(io.LimitReader(rc, 1<<20))
			rc.Close()
			if err != nil {
				continue
			}
			result[f.Name] = content
		}
	}
	return result, nil
}

func classifyPEMs(pemFiles map[string][]byte) (*parsedBundle, error) {
	bundle := &parsedBundle{}

	for name, data := range pemFiles {
		rest := data
		for {
			var block *pem.Block
			block, rest = pem.Decode(rest)
			if block == nil {
				break
			}

			switch block.Type {
			case "CERTIFICATE":
				cert, err := x509.ParseCertificate(block.Bytes)
				if err != nil {
					log.Debug().Err(err).Str("file", name).Msg("credential: failed to parse certificate")
					continue
				}
				if cert.IsCA {
					bundle.caCertPEM = string(pem.EncodeToMemory(block))
				} else {
					bundle.clientCertPEM = string(pem.EncodeToMemory(block))
					bundle.subject = cert.Subject.CommonName
					bundle.notAfter = cert.NotAfter.Format("2006-01-02 15:04:05")
					fp := sha256.Sum256(cert.Raw)
					bundle.fingerprint = hex.EncodeToString(fp[:])
				}

			case "RSA PRIVATE KEY", "EC PRIVATE KEY", "PRIVATE KEY":
				bundle.clientKeyPEM = string(pem.EncodeToMemory(block))
			}
		}
	}

	if bundle.caCertPEM == "" && bundle.clientCertPEM == "" && bundle.clientKeyPEM == "" {
		return nil, fmt.Errorf("no certificates or keys found in uploaded files")
	}

	return bundle, nil
}
