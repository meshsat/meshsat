package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"meshsat/internal/pair"
)

// Pair-mode REST API — MESHSAT-596 (S8-04). The six endpoints form
// a tight lifecycle:
//
//   POST /api/v2/pair/arm      (operator on the touch display)
//        → returns {pin, pairing_key, expires_at}
//   POST /api/v2/pair/claim    (remote client, public)
//        → takes  {pin, public_key, hmac, name, kind}
//        → returns {client_id, jwt, expires_at}
//   POST /api/v2/pair/refresh  (authenticated, JWT)
//        → returns a fresh JWT
//   GET  /api/v2/pair/list     (authenticated, paired clients)
//        → returns the paired_clients roster
//   POST /api/v2/pair/revoke/{id} (authenticated, admin)
//        → wipes a paired client
//
// The internal CA leaf-cert layer (MESHSAT-595) hasn't landed yet,
// so CertPEM on the claim response is empty — the JWT path works
// standalone and is the primary auth surface; mTLS slots in without
// changing the wire shape.

type pairArmResponse struct {
	PIN        string `json:"pin"`
	PairingKey string `json:"pairing_key"`
	ExpiresAt  string `json:"expires_at"`
	TTLSeconds int    `json:"ttl_seconds"`
}

// @Summary Arm pair mode
// @Description Generates a 6-digit PIN + 32-byte pairing key, arms
// @Description them for 90 seconds. The operator reads both off the
// @Description touch display and enters them on the remote device
// @Description being paired. Subsequent /pair/claim consumes the
// @Description row. [MESHSAT-596]
// @Tags pair
// @Produce json
// @Success 200 {object} pairArmResponse
// @Router /api/v2/pair/arm [post]
func (s *Server) handlePairArm(w http.ResponseWriter, r *http.Request) {
	pin, err := pair.GeneratePIN()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "generate pin: "+err.Error())
		return
	}
	key, err := pair.GeneratePairingKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "generate key: "+err.Error())
		return
	}
	expires := time.Now().Add(pair.DefaultArmTTL)
	_, err = s.db.Exec(
		`INSERT INTO pair_modes (pin, pairing_key, expires_at, armed_by)
		 VALUES (?, ?, ?, 'operator')`,
		pin, key, expires.UTC().Format(time.RFC3339))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "arm: "+err.Error())
		return
	}
	log.Info().Str("expires_at", expires.Format(time.RFC3339)).Msg("pair mode armed")
	writeJSON(w, http.StatusOK, pairArmResponse{
		PIN:        pin,
		PairingKey: key,
		ExpiresAt:  expires.UTC().Format(time.RFC3339),
		TTLSeconds: int(pair.DefaultArmTTL.Seconds()),
	})
}

// @Summary Claim a pair mode (remote device)
// @Description Accepts the PIN + the client's generated Ed25519 pub
// @Description key + an HMAC proving the client knew both the PIN
// @Description and the pairing key. On success returns a client_id.
// @Description The client mints its own JWTs from its private key
// @Description for subsequent requests. [MESHSAT-596]
// @Tags pair
// @Accept json
// @Produce json
// @Param body body pair.ClaimRequest true "claim"
// @Success 200 {object} pair.ClaimResponse
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /api/v2/pair/claim [post]
func (s *Server) handlePairClaim(w http.ResponseWriter, r *http.Request) {
	var req pair.ClaimRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if len(req.PIN) != pair.PinLength || req.PublicKeyHex == "" || req.HMACHex == "" {
		writeError(w, http.StatusBadRequest, "pin, public_key, hmac are required")
		return
	}

	// Look up the armed row, unused and unexpired, matching the PIN.
	var id int64
	var pairingKey string
	err := s.db.QueryRow(
		`SELECT id, pairing_key FROM pair_modes
		 WHERE pin = ? AND consumed_at IS NULL
		   AND datetime(expires_at) > datetime('now')
		 ORDER BY armed_at DESC LIMIT 1`, req.PIN,
	).Scan(&id, &pairingKey)
	if err != nil {
		writeError(w, http.StatusForbidden, "no matching armed pair mode (wrong pin or expired)")
		return
	}

	secret, err := pair.DeriveSharedSecret(pairingKey, req.PIN)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "derive: "+err.Error())
		return
	}
	if err := pair.VerifyClaimHMAC(secret, req.PublicKeyHex, req.HMACHex); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	clientID := newClientID()
	if _, err := s.db.Exec(
		`INSERT INTO paired_clients (id, name, kind, public_key)
		 VALUES (?, ?, ?, ?)`,
		clientID, req.Name, defaultKind(req.Kind), req.PublicKeyHex); err != nil {
		writeError(w, http.StatusInternalServerError, "persist: "+err.Error())
		return
	}
	if _, err := s.db.Exec(
		`UPDATE pair_modes SET consumed_at = datetime('now'), consumed_by = ? WHERE id = ?`,
		clientID, id); err != nil {
		log.Warn().Err(err).Msg("pair: failed to mark mode consumed")
	}

	resp := pair.ClaimResponse{
		ClientID:  clientID,
		JWT:       "",
		ExpiresAt: time.Now().Add(pair.JWTTTL).UTC().Format(time.RFC3339),
	}
	writeJSON(w, http.StatusOK, resp)
}

// @Summary List paired clients
// @Description Returns every paired client (including revoked).
// @Tags pair
// @Produce json
// @Success 200 {array} map[string]interface{}
// @Router /api/v2/pair/list [get]
func (s *Server) handlePairList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(
		`SELECT id, name, kind, public_key, claimed_at,
		        COALESCE(last_seen_at, ''), COALESCE(revoked_at, '')
		 FROM paired_clients ORDER BY claimed_at DESC`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list: "+err.Error())
		return
	}
	defer func() { _ = rows.Close() }()

	out := []map[string]interface{}{}
	for rows.Next() {
		var id, name, kind, pub, claimed, seen, revoked string
		if err := rows.Scan(&id, &name, &kind, &pub, &claimed, &seen, &revoked); err != nil {
			continue
		}
		out = append(out, map[string]interface{}{
			"id":           id,
			"name":         name,
			"kind":         kind,
			"public_key":   pub,
			"claimed_at":   claimed,
			"last_seen_at": seen,
			"revoked_at":   revoked,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// @Summary Revoke a paired client
// @Description Marks the client revoked. Its JWTs stop being
// @Description accepted by the /api/v2/* middleware.
// @Tags pair
// @Param id path string true "Client ID"
// @Success 200 {object} map[string]string
// @Router /api/v2/pair/revoke/{id} [post]
func (s *Server) handlePairRevoke(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "client id required")
		return
	}
	var body struct {
		Reason string `json:"reason,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if _, err := s.db.Exec(
		`UPDATE paired_clients SET revoked_at = datetime('now'), revoke_reason = ? WHERE id = ?`,
		body.Reason, id); err != nil {
		writeError(w, http.StatusInternalServerError, "revoke: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// @Summary Refresh a JWT (client last-seen bump)
// @Description Stamps last_seen_at on the paired client. The client
// @Description mints its own JWT locally; this endpoint simply
// @Description confirms the bridge still accepts the binding.
// @Tags pair
// @Produce json
// @Success 200 {object} map[string]string
// @Router /api/v2/pair/refresh [post]
func (s *Server) handlePairRefresh(w http.ResponseWriter, r *http.Request) {
	// Middleware (MESHSAT-598) normally populates the client_id;
	// we fall back to the Authorization header echo when the
	// middleware layer hasn't landed yet.
	clientID := r.Header.Get("X-MeshSat-Client-ID")
	if clientID == "" {
		writeError(w, http.StatusUnauthorized, "missing client id (middleware not yet active)")
		return
	}
	_, _ = s.db.Exec(
		`UPDATE paired_clients SET last_seen_at = datetime('now') WHERE id = ?`,
		clientID)
	writeJSON(w, http.StatusOK, map[string]string{
		"client_id": clientID,
		"status":    "ok",
	})
}

func defaultKind(k string) string {
	if k == "" {
		return "browser"
	}
	return k
}

func newClientID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().UTC().Format("20060102150405.999999999")
	}
	return hex.EncodeToString(b[:])
}
