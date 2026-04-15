package api

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	qrcode "github.com/skip2/go-qrcode"

	"meshsat/internal/keystore"
)

// handleGenerateKeyBundle generates a signed key bundle for specified channels.
// @Summary Generate key bundle
// @Description Creates AES-256 keys (if needed) and returns a signed meshsat:// URL for QR sharing
// @Tags keys
// @Param body body object true "{entries: [{channel_type, address}, ...]}"
// @Success 201 {object} map[string]interface{}
// @Failure 400 {object} map[string]string
// @Router /api/keys/bundle [post]
func (s *Server) handleGenerateKeyBundle(w http.ResponseWriter, r *http.Request) {
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "key store not available")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}

	var req struct {
		Entries []keystore.BundleRequest `json:"entries"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "parse body: "+err.Error())
		return
	}
	if len(req.Entries) == 0 {
		writeError(w, http.StatusBadRequest, "entries is required")
		return
	}

	_, url, err := s.keyStore.CreateBundle(req.Entries)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"url":         url,
		"signing_pub": hex.EncodeToString(s.keyStore.SigningPublicKey()),
		"entries":     len(req.Entries),
	})
}

// handleGetKeyBundleQR renders a key bundle as a QR code PNG image.
// @Summary Get key bundle as QR code
// @Description Generates a QR code PNG for scanning with MeshSat Android
// @Tags keys
// @Param channels query string true "Comma-separated channel:address pairs (e.g. sms:+1234,mesh:!abcd)"
// @Success 200 {file} image/png
// @Failure 400 {object} map[string]string
// @Router /api/keys/bundle/qr [get]
func (s *Server) handleGetKeyBundleQR(w http.ResponseWriter, r *http.Request) {
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "key store not available")
		return
	}

	channelsParam := r.URL.Query().Get("channels")
	if channelsParam == "" {
		writeError(w, http.StatusBadRequest, "channels param required (e.g. sms:+1234,mesh:!abcd)")
		return
	}

	var requests []keystore.BundleRequest
	for _, pair := range strings.Split(channelsParam, ",") {
		parts := strings.SplitN(strings.TrimSpace(pair), ":", 2)
		if len(parts) != 2 {
			writeError(w, http.StatusBadRequest, "invalid channel format: "+pair+" (expected type:address)")
			return
		}
		requests = append(requests, keystore.BundleRequest{
			ChannelType: parts[0],
			Address:     parts[1],
		})
	}

	_, url, err := s.keyStore.CreateBundle(requests)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	png, err := qrcode.Encode(url, qrcode.Medium, 512)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "qr encode: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write(png)
}

// handleRotateKey rotates the encryption key for a channel+address.
// @Summary Rotate channel key
// @Description Generates a new key version and retires the old one with a grace period
// @Tags keys
// @Param body body object true "{channel_type, address, grace_period_hours}"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]string
// @Router /api/keys/rotate [post]
func (s *Server) handleRotateKey(w http.ResponseWriter, r *http.Request) {
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "key store not available")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}

	var req struct {
		ChannelType      string `json:"channel_type"`
		Address          string `json:"address"`
		GracePeriodHours int    `json:"grace_period_hours"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "parse body: "+err.Error())
		return
	}
	if req.ChannelType == "" || req.Address == "" {
		writeError(w, http.StatusBadRequest, "channel_type and address are required")
		return
	}

	_, newVersion, err := s.keyStore.RotateKey(req.ChannelType, req.Address, req.GracePeriodHours)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "rotated",
		"new_version": newVersion,
		"grace_hours": req.GracePeriodHours,
	})
}

// handleListKeys returns all key metadata (no raw key material).
// @Summary List managed keys
// @Description Returns metadata for all channel encryption keys (keys are redacted)
// @Tags keys
// @Success 200 {array} keystore.KeyMeta
// @Router /api/keys [get]
func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "key store not available")
		return
	}

	keys, err := s.keyStore.ListKeys()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"keys": keys})
}

// handleRevokeKey revokes all keys for a channel+address.
// @Summary Revoke channel key
// @Description Immediately invalidates all key versions for a channel+address
// @Tags keys
// @Param type path string true "Channel type (sms, mesh, iridium, etc)"
// @Param address path string true "Address (phone number, node ID, etc)"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Router /api/keys/{type}/{address} [delete]
func (s *Server) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "key store not available")
		return
	}

	channelType := chi.URLParam(r, "type")
	address := chi.URLParam(r, "address")

	if err := s.keyStore.RevokeKey(channelType, address); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// handleGetSigningKey returns the bridge's Ed25519 signing public key and fingerprint.
// @Summary Get bridge signing key
// @Description Returns the Ed25519 public key used to sign key bundles, plus a truncated SHA-256 fingerprint
// @Tags keys
// @Success 200 {object} map[string]string
// @Router /api/keys/signing [get]
func (s *Server) handleGetSigningKey(w http.ResponseWriter, r *http.Request) {
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "key store not available")
		return
	}

	pub := s.keyStore.SigningPublicKey()
	if pub == nil {
		writeError(w, http.StatusServiceUnavailable, "signing key not available")
		return
	}

	pubHex := hex.EncodeToString(pub)
	fingerprint := keystore.SigningKeyFingerprint(pub)

	writeJSON(w, http.StatusOK, map[string]string{
		"signing_pub": pubHex,
		"fingerprint": fingerprint,
		"algorithm":   "Ed25519",
	})
}

// handleGetKeyStats returns key inventory statistics.
// @Summary Key store statistics
// @Description Returns counts of active, retired, and revoked keys
// @Tags keys
// @Success 200 {object} map[string]interface{}
// @Router /api/keys/stats [get]
func (s *Server) handleGetKeyStats(w http.ResponseWriter, r *http.Request) {
	if s.keyStore == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"enabled": false,
		})
		return
	}

	active, retired, revoked, err := s.keyStore.Stats()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled": true,
		"active":  active,
		"retired": retired,
		"revoked": revoked,
	})
}
