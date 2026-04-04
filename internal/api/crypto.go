package api

import (
	"net/http"

	"meshsat/internal/engine"
)

// handleGenerateEncryptionKey generates a random AES-256 key for payload encryption.
// @Summary Generate encryption key
// @Description Generates a random AES-256-GCM key for payload encryption on interfaces
// @Tags crypto
// @Produce json
// @Success 200 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/crypto/generate-key [post]
func (s *Server) handleGenerateEncryptionKey(w http.ResponseWriter, r *http.Request) {
	key, err := engine.GenerateEncryptionKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"key":       key,
		"algorithm": "AES-256-GCM",
		"key_bytes": "32",
	})
}
