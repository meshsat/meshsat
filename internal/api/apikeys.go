package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/auth"
)

// @Summary Create API key
// @Description Generates a new API key. The plaintext key is returned once — store it securely.
// @Tags auth
// @Accept json
// @Produce json
// @Param body body object true "Key config" SchemaExample({"label":"Field Device 1","role":"operator","device_id":1})
// @Success 201 {object} map[string]interface{}
// @Failure 400 {object} map[string]string "invalid request"
// @Router /api/auth/keys [post]
func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label     string  `json:"label"`
		Role      string  `json:"role"`
		DeviceID  *int64  `json:"device_id,omitempty"`
		ExpiresAt *string `json:"expires_at,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Label == "" {
		writeError(w, http.StatusBadRequest, "label is required")
		return
	}
	if req.Role == "" {
		req.Role = "viewer"
	}
	if !auth.ValidRole(req.Role) {
		writeError(w, http.StatusBadRequest, "invalid role: must be viewer, operator, or owner")
		return
	}

	tid := auth.TenantIDFromContext(r.Context())
	plaintext, hash, prefix, err := auth.GenerateAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate key")
		return
	}

	id, err := s.db.CreateAPIKey(hash, prefix, tid, req.DeviceID, req.Role, req.Label, req.ExpiresAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create key: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":         id,
		"key":        plaintext, // returned once, never again
		"key_prefix": prefix,
		"role":       req.Role,
		"label":      req.Label,
		"tenant_id":  tid,
	})
}

// @Summary List API keys
// @Description Returns all API keys for the current tenant (hashes never exposed).
// @Tags auth
// @Produce json
// @Success 200 {array} database.APIKeyRecord
// @Router /api/auth/keys [get]
func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	tid := auth.TenantIDFromContext(r.Context())
	keys, err := s.db.GetAPIKeys(tid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list keys")
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

// @Summary Revoke API key
// @Description Permanently revokes an API key by ID.
// @Tags auth
// @Param id path int true "Key ID"
// @Success 204
// @Failure 404 {object} map[string]string "not found"
// @Router /api/auth/keys/{id} [delete]
func (s *Server) handleDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid key ID")
		return
	}
	tid := auth.TenantIDFromContext(r.Context())
	if err := s.db.DeleteAPIKey(id, tid); err != nil {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
