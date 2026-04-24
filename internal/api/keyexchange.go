package api

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
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

	// [MESHSAT-681] Validate + canonicalise channel_type before the
	// keystore call. Previously the bundle marshaller accepted 0xFF for
	// any unknown type and the import on the other side silently dropped
	// the entry, so /api/keys/import returned `imported:1` for a key
	// that was never stored. Reject here with a clear error instead.
	normalised := make([]keystore.BundleRequest, 0, len(req.Entries))
	for i, e := range req.Entries {
		canonical, ok := keystore.CanonicalChannelType(e.ChannelType)
		if !ok {
			writeError(w, http.StatusBadRequest,
				"entry "+strconv.Itoa(i)+": unknown channel_type \""+e.ChannelType+
					"\" — supported: "+strings.Join(keystore.SupportedChannelTypes(), ", "))
			return
		}
		e.ChannelType = canonical
		normalised = append(normalised, e)
	}

	_, url, err := s.keyStore.CreateBundle(normalised)
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
		canonical, ok := keystore.CanonicalChannelType(parts[0])
		if !ok {
			writeError(w, http.StatusBadRequest,
				"unknown channel_type \""+parts[0]+"\" — supported: "+
					strings.Join(keystore.SupportedChannelTypes(), ", "))
			return
		}
		requests = append(requests, keystore.BundleRequest{
			ChannelType: canonical,
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

// handleImportKeyBundle ingests a `meshsat://key/...` URL (or raw
// base64url-encoded bundle) emitted by another MeshSat bridge or
// Android client. Each entry in the bundle is re-wrapped under the
// local master key and stored in key_bundles, making it available to
// the transform pipeline via its `<type>:<address>` key_ref. Required
// to sync a shared AES-256 key (e.g. `aprs:shared`) across two field
// kits for cross-bridge decryption. [MESHSAT-663]
//
// @Summary Import a signed key bundle
// @Description Imports a meshsat:// key bundle. Bundle signature is
// @Description Ed25519-verified using the v2-embedded public key;
// @Description v1 bundles need an explicit `signing_pub` hex param.
// @Description Each channel key inside is wrapped under the local
// @Description master key and stored. TOFU-style: returns the signing
// @Description fingerprint so the operator can confirm.
// @Tags keys
// @Accept json
// @Produce json
// @Param body body object true "{url?: string, bundle?: base64url, signing_pub?: hex}"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/keys/import [post]
func (s *Server) handleImportKeyBundle(w http.ResponseWriter, r *http.Request) {
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "key store not available")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 8192))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var req struct {
		URL        string `json:"url"`
		Bundle     string `json:"bundle"`      // base64url of raw bundle bytes (alt to url)
		SigningPub string `json:"signing_pub"` // hex-encoded, required only for v1 bundles
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "parse body: "+err.Error())
		return
	}

	// Resolve bundle bytes: either from a meshsat://key/<b64> URL or
	// from an explicit base64url payload (useful for callers that
	// already stripped the scheme prefix, e.g. config-import flows).
	var bundleBytes []byte
	switch {
	case req.URL != "":
		bundleBytes, err = keystore.URLToBundle(req.URL)
	case req.Bundle != "":
		bundleBytes, err = base64.RawURLEncoding.DecodeString(strings.TrimSpace(req.Bundle))
	default:
		writeError(w, http.StatusBadRequest, "either 'url' or 'bundle' is required")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "decode bundle: "+err.Error())
		return
	}

	// Verify. v2 bundles carry the signing pub inline — VerifyBundle
	// ignores the passed signingPub in that case. v1 requires the
	// caller to pass signing_pub explicitly; refuse without it rather
	// than accept an unverified bundle.
	var signingPub ed25519.PublicKey
	if len(bundleBytes) >= 1 && bundleBytes[0] == 0x01 { // v1
		if req.SigningPub == "" {
			writeError(w, http.StatusBadRequest, "v1 bundles require signing_pub (hex)")
			return
		}
		raw, decErr := hex.DecodeString(req.SigningPub)
		if decErr != nil || len(raw) != ed25519.PublicKeySize {
			writeError(w, http.StatusBadRequest, "invalid signing_pub")
			return
		}
		signingPub = ed25519.PublicKey(raw)
	}
	if !keystore.VerifyBundle(bundleBytes, signingPub) {
		writeError(w, http.StatusBadRequest, "bundle signature verification failed")
		return
	}
	parsed, err := keystore.UnmarshalBundle(bundleBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, "unmarshal bundle: "+err.Error())
		return
	}

	// Re-wrap each entry under the local master key via the existing
	// StoreKey helper — that's the same path the Hub `key_rotate`
	// command uses, so rotation/versioning semantics stay consistent.
	//
	// [MESHSAT-681] Unknown channel-type bytes used to be skipped with
	// only a warn log, and the response reported imported:N with no
	// indication of the drop. Now we collect skipped entries into an
	// explicit response array so the caller sees exactly what didn't
	// land. If ALL entries skipped, return 400 — the bundle was junk
	// from this bridge's perspective.
	imported := make([]map[string]interface{}, 0, len(parsed.Entries))
	skipped := make([]map[string]interface{}, 0)
	for _, e := range parsed.Entries {
		ct := keystore.ByteToChannelType(e.ChannelType)
		if ct == "unknown" {
			log.Warn().Uint8("type", e.ChannelType).Str("addr", e.Address).
				Msg("keys/import: skipping entry with unknown channel type")
			skipped = append(skipped, map[string]interface{}{
				"channel_type_byte": e.ChannelType,
				"address":           e.Address,
				"reason":            "unknown channel_type byte — bundle may be from a newer bridge version",
			})
			continue
		}
		ver, serr := s.keyStore.StoreKey(ct, e.Address, e.Key[:])
		if serr != nil {
			writeError(w, http.StatusInternalServerError, "store "+ct+":"+e.Address+": "+serr.Error())
			return
		}
		imported = append(imported, map[string]interface{}{
			"channel_type": ct,
			"address":      e.Address,
			"version":      ver,
		})
	}
	if len(imported) == 0 && len(skipped) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":   "bundle contained no recognised channel types",
			"skipped": skipped,
		})
		return
	}

	// Fingerprint of the SIGNER of this bundle — operator TOFU-confirms
	// this matches the other bridge's /api/keys/signing output.
	var signerPubHex, signerFp string
	if len(parsed.SigningPub) == ed25519.PublicKeySize {
		signerPubHex = hex.EncodeToString(parsed.SigningPub)
		signerFp = keystore.SigningKeyFingerprint(parsed.SigningPub)
	} else if signingPub != nil {
		signerPubHex = hex.EncodeToString(signingPub)
		signerFp = keystore.SigningKeyFingerprint(signingPub)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"imported":           imported,
		"imported_count":     len(imported),
		"skipped":            skipped, // [MESHSAT-681] per-entry skip reasons
		"skipped_count":      len(skipped),
		"bundle_version":     parsed.Version,
		"bundle_timestamp":   parsed.Timestamp,
		"signer_pub":         signerPubHex,
		"signer_fingerprint": signerFp,
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
