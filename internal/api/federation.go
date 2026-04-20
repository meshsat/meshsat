package api

// /api/federation/peers — trusted-peer CRUD.  Surface on top of the
// database layer (internal/database/trusted_peers.go) and the wire
// primitives (internal/federation/manifest.go). [MESHSAT-636]

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// @Summary List trusted peers
// @Description Returns every kit we've exchanged a capability manifest with, newest updated_at first. Used by the Federation card in Settings > Routing.
// @Tags federation
// @Produce json
// @Success 200 {array} database.TrustedPeer
// @Router /api/federation/peers [get]
func (s *Server) handleFederationPeersList(w http.ResponseWriter, r *http.Request) {
	peers, err := s.db.ListTrustedPeers()
	if err != nil {
		log.Error().Err(err).Msg("federation: list peers")
		writeError(w, http.StatusInternalServerError, "list peers")
		return
	}
	// Empty slice renders as [] — avoids the UI having to handle
	// the JSON null case separately from a real empty set.
	if peers == nil {
		peers = []database.TrustedPeer{}
	}
	writeJSON(w, http.StatusOK, peers)
}

// @Summary Get a trusted peer by signer_id
// @Tags federation
// @Produce json
// @Param signer_id path string true "Ed25519 public key, hex-encoded"
// @Success 200 {object} database.TrustedPeer
// @Failure 404 {object} map[string]string
// @Router /api/federation/peers/{signer_id} [get]
func (s *Server) handleFederationPeerGet(w http.ResponseWriter, r *http.Request) {
	sid := validateSignerID(chi.URLParam(r, "signer_id"))
	if sid == "" {
		writeError(w, http.StatusBadRequest, "invalid signer_id")
		return
	}
	p, err := s.db.GetTrustedPeer(sid)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "peer not found")
		return
	}
	if err != nil {
		log.Error().Err(err).Str("signer_id", sid).Msg("federation: get peer")
		writeError(w, http.StatusInternalServerError, "get peer")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// @Summary Revoke (delete) a trusted peer
// @Description Also tears down any active BLE-peer link for this signer (if the peer's BLE address is in the manifest) and flips auto_federate off in the bond manager once that lands.
// @Tags federation
// @Param signer_id path string true "Ed25519 public key, hex-encoded"
// @Success 200 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/federation/peers/{signer_id} [delete]
func (s *Server) handleFederationPeerDelete(w http.ResponseWriter, r *http.Request) {
	sid := validateSignerID(chi.URLParam(r, "signer_id"))
	if sid == "" {
		writeError(w, http.StatusBadRequest, "invalid signer_id")
		return
	}
	// Best-effort BLE peer teardown based on the ble address in the
	// persisted manifest, if any. The auto-bond manager (MESHSAT-639)
	// will hook in here once it lands.
	if p, err := s.db.GetTrustedPeer(sid); err == nil && s.blePeerMgr != nil {
		if addr := bleAddrFromManifest(p.ManifestJSON); addr != "" {
			s.blePeerMgr.RemovePeer(addr)
		}
	}
	ok, err := s.db.DeleteTrustedPeer(sid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete peer")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "peer not found")
		return
	}
	writeSuccess(w, "peer revoked")
}

// autoFederateRequest is the body shape for the toggle endpoint.
type autoFederateRequest struct {
	On bool `json:"on"`
}

// @Summary Toggle the auto-federate flag for a trusted peer
// @Tags federation
// @Accept json
// @Produce json
// @Param signer_id path string true "Ed25519 public key, hex-encoded"
// @Param body body autoFederateRequest true "{on: true|false}"
// @Success 200 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/federation/peers/{signer_id}/auto-federate [put]
func (s *Server) handleFederationPeerAutoFederate(w http.ResponseWriter, r *http.Request) {
	sid := validateSignerID(chi.URLParam(r, "signer_id"))
	if sid == "" {
		writeError(w, http.StatusBadRequest, "invalid signer_id")
		return
	}
	r = limitBody(r, 1<<14)
	var req autoFederateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.db.SetTrustedPeerAutoFederate(sid, req.On); err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "peer not found")
		return
	} else if err != nil {
		log.Error().Err(err).Str("signer_id", sid).Msg("federation: set auto-federate")
		writeError(w, http.StatusInternalServerError, "set auto-federate")
		return
	}
	if req.On {
		writeSuccess(w, "auto-federate enabled")
	} else {
		writeSuccess(w, "auto-federate disabled")
	}
}

// validateSignerID enforces the lower-case 64-hex-char shape of an
// Ed25519 public key. Returns the canonicalised form or "" on
// rejection — any surface accepting a signer_id should route
// through this so the rest of the stack can assume well-formed input.
func validateSignerID(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if len(s) != 64 {
		return ""
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return ""
		}
	}
	return s
}

// bleAddrFromManifest pulls the BLE adapter MAC out of a serialised
// capability manifest, if any. Used to tear down the corresponding
// BLE peer when the operator revokes a trusted peer. Returns "" if
// the manifest is unreadable or the peer doesn't ship a BLE bearer.
func bleAddrFromManifest(jsonBlob string) string {
	if jsonBlob == "" {
		return ""
	}
	var m struct {
		Bearers []struct {
			Type    string `json:"type"`
			Address string `json:"address"`
		} `json:"bearers"`
	}
	if err := json.Unmarshal([]byte(jsonBlob), &m); err != nil {
		return ""
	}
	for _, b := range m.Bearers {
		if b.Type == "ble" {
			return b.Address
		}
	}
	return ""
}
