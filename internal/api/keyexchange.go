package api

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
// @Description Returns metadata for all channel encryption keys (keys are redacted), each with the label that says where it came from: none when set here, hub-rotated-v<n> from the Hub, "Bridge <type> (<hash>)" from a bundle
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
// @Description Immediately invalidates all key versions for a channel+address. For an SMS chat's key (sms:<number>, or sms:* for every number) SMS to and from that number fall back to the wildcard key, then to the SMS link's own chain. The address may be URL-escaped (%2B31612345678) or not (+31612345678); "cellular" is the same key space as "sms".
// @Tags keys
// @Produce json
// @Param type path string true "Channel type (sms, mesh, iridium, etc)"
// @Param address path string true "Address (phone number with its +, node ID, * for every SMS number, etc)"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/keys/{type}/{address} [delete]
func (s *Server) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "key store not available")
		return
	}

	channelType := chi.URLParam(r, "type")
	address, err := keyAddress(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.keyStore.RevokeKey(channelType, address); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// "cellular" names the sms key space (CanonicalChannelType), where
	// PUT stores it.
	if canonical, ok := keystore.CanonicalChannelType(channelType); ok && canonical != channelType {
		if err := s.keyStore.RevokeKey(canonical, address); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// chatKeyMaxLabel bounds the label a key is given over the API.
const chatKeyMaxLabel = 64

// chatKeyResponse is one chat's key as GET /api/keys/{type}/{address}
// returns it.
type chatKeyResponse struct {
	Key     string `json:"key"`     // 64 hex characters, lower case
	Version int    `json:"version"` // the keystore's version of this address's key
	Label   string `json:"label"`   // "" set here, hub-rotated-v<n> from the Hub, "Bridge <type> (<hash>)" from a bundle
}

// chatKeyType is the keystore channel type of a chat key read or set over
// the API: every type the keystore knows but mgmt, whose keys belong to the
// OOB peers (/api/oob/peers) and are registered with them.
func chatKeyType(raw string) (string, bool) {
	ct, ok := keystore.CanonicalChannelType(raw)
	if !ok || ct == "mgmt" {
		return "", false
	}
	return ct, true
}

// keyAddress is the {address} of /api/keys/{type}/{address} as written. chi
// hands over the path as the client sent it, so "%2B31612345678" arrives
// escaped and "+31612345678" as it is; in a path "+" is a plus, never a space.
func keyAddress(r *http.Request) (string, error) {
	address, err := url.PathUnescape(chi.URLParam(r, "address"))
	if err != nil {
		return "", fmt.Errorf("address: %w", err)
	}
	if strings.TrimSpace(address) == "" {
		return "", fmt.Errorf("address is required")
	}
	return address, nil
}

// validChatKey reports whether k is an AES-256 key in hex: exactly 64 of
// 0-9, a-f, A-F, as MeshSat Android's chat key sheet accepts.
func validChatKey(k string) bool {
	if len(k) != 64 {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// handleGetChatKey returns the key kept for one chat.
// @Summary Get the key of one chat
// @Description Returns the active AES-256 key of a channel type and address: the key of an SMS chat (sms:<number>, or sms:* for every number without one of its own), a mesh node's or the satellite chat's. SMS to and from a number are sealed and opened with its key, else the wildcard's, else the SMS link's own chain. Keys set here, rotated by the Hub or imported from a bundle all show, with their label. Key material, on the local API only (as the SMS link's inline key in /api/interfaces). The address may be URL-escaped (%2B31612345678) or not (+31612345678).
// @Tags keys
// @Produce json
// @Param type path string true "Channel type: sms (cellular is the same), mesh, iridium, aprs, zigbee, mqtt, webhook or bond"
// @Param address path string true "Address: the number with its +, * for every SMS number, a node ID"
// @Success 200 {object} chatKeyResponse
// @Failure 400 {object} map[string]string "unknown channel type or no address"
// @Failure 404 {object} map[string]string "no key for this chat"
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string "key store not available"
// @Router /api/keys/{type}/{address} [get]
func (s *Server) handleGetChatKey(w http.ResponseWriter, r *http.Request) {
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "key store not available")
		return
	}
	channelType, ok := chatKeyType(chi.URLParam(r, "type"))
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown channel type \""+chi.URLParam(r, "type")+"\"")
		return
	}
	address, err := keyAddress(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	raw, version, label, found, err := s.keyStore.LookupKey(channelType, address)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "no key for "+channelType+":"+address)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, chatKeyResponse{Key: hex.EncodeToString(raw), Version: version, Label: label})
}

// handleSetChatKey sets the key of one chat.
// @Summary Set the key of one chat
// @Description Stores an AES-256 key for a channel type and address as its new version (the previous one is retired). The key of an SMS chat (sms:<number>) seals every SMS to that number and opens every SMS from it, whether or not the SMS link encrypts, as MeshSat Android's chat key does; sms:* does so for every number without a key of its own; a plaintext peer (the Hub) keeps its clear text. Sealed as Android seals: SMAZ2 when it makes the text shorter (MSVQ-SC instead when the SMS link's chain uses it), AES-256-GCM, the protocol version byte, base64. The same key and label again changes nothing. Mesh and satellite chats keep a key that changes nothing on the air. The address may be URL-escaped (%2B31612345678) or not (+31612345678).
// @Tags keys
// @Accept json
// @Produce json
// @Param type path string true "Channel type: sms (cellular is the same), mesh, iridium, aprs, zigbee, mqtt, webhook or bond"
// @Param address path string true "Address: the number with its +, * for every SMS number, a node ID"
// @Param body body object true "The key, 64 hex characters, and an optional label" example({"key":"00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"})
// @Success 200 {object} map[string]interface{} "status saved or unchanged, channel_type, address, version, label"
// @Failure 400 {object} map[string]string "unknown channel type, no address, or a key that is not 64 hex characters"
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string "key store not available"
// @Router /api/keys/{type}/{address} [put]
func (s *Server) handleSetChatKey(w http.ResponseWriter, r *http.Request) {
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "key store not available")
		return
	}
	channelType, ok := chatKeyType(chi.URLParam(r, "type"))
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown channel type \""+chi.URLParam(r, "type")+"\"")
		return
	}
	address, err := keyAddress(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Key   string `json:"key"`
		Label string `json:"label"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !validChatKey(req.Key) {
		writeError(w, http.StatusBadRequest, "key must be 64 hexadecimal characters (AES-256)")
		return
	}
	label := strings.TrimSpace(req.Label)
	if len(label) > chatKeyMaxLabel {
		writeError(w, http.StatusBadRequest, "label is longer than "+strconv.Itoa(chatKeyMaxLabel)+" characters")
		return
	}
	rawKey, _ := hex.DecodeString(req.Key)

	resp := map[string]interface{}{"channel_type": channelType, "address": address, "label": label}
	// Saving the key a chat already has (Save tapped twice) keeps its version.
	if cur, version, curLabel, found, err := s.keyStore.LookupKey(channelType, address); err == nil && found &&
		subtle.ConstantTimeCompare(cur, rawKey) == 1 && curLabel == label {
		resp["status"], resp["version"] = "unchanged", version
		writeJSON(w, http.StatusOK, resp)
		return
	}
	version, err := s.keyStore.StoreKeyLabelled(channelType, address, rawKey, label)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp["status"], resp["version"] = "saved", version
	writeJSON(w, http.StatusOK, resp)
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
// @Description fingerprint so the operator can confirm. Each key is
// @Description labelled "Bridge <type> (<hash>)" as MeshSat Android labels
// @Description it; an sms:<number> entry is that SMS chat's key.
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
	// Labelled as MeshSat Android labels an imported conversation key:
	// "Bridge <type> (<first 8 hex of the bundle's bridge hash>)".
	bridgeHash := hex.EncodeToString(parsed.BridgeHash[:])[:8]
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
		ver, serr := s.keyStore.StoreKeyLabelled(ct, e.Address, e.Key[:], "Bridge "+ct+" ("+bridgeHash+")")
		if serr != nil {
			writeError(w, http.StatusInternalServerError, "store "+ct+":"+e.Address+": "+serr.Error())
			return
		}
		// A mgmt entry pairs this kit as the importer of an OOB management
		// key: register the issuer as a readonly peer. [MESHSAT-756]
		if ct == "mgmt" && s.oob != nil {
			if p, perr := s.oob.RegisterImportedPeer(e.Address, e.Key[:]); perr != nil {
				log.Warn().Err(perr).Str("issuer", e.Address).Msg("keys/import: mgmt entry stored but peer not registered")
			} else {
				log.Info().Str("issuer", e.Address).Uint16("peer_id", p.PeerID).Msg("keys/import: OOB management peer registered")
			}
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
