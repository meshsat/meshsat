package api

import (
	"net/http"
	"strconv"

	"meshsat/internal/database"
	"meshsat/internal/engine"
)

// SetSigningService sets the signing service for audit endpoints.
func (s *Server) SetSigningService(ss *engine.SigningService) {
	s.signing = ss
}

// handleGetAuditLog returns recent audit log entries.
// @Summary Get audit log
// @Description Returns recent signed audit log entries, optionally filtered by interface
// @Tags audit
// @Produce json
// @Param limit query integer false "Max entries (default: 100, max: 1000)"
// @Param interface_id query string false "Filter by interface ID"
// @Param before query integer false "Only entries with a smaller id: the next page of a full copy"
// @Success 200 {array} database.AuditLogEntry
// @Failure 500 {object} map[string]string
// @Router /api/audit [get]
func (s *Server) handleGetAuditLog(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	var before int64
	if q := r.URL.Query().Get("before"); q != "" {
		if n, err := strconv.ParseInt(q, 10, 64); err == nil && n > 0 {
			before = n
		}
	}

	entries, err := s.db.GetAuditLogPage(r.URL.Query().Get("interface_id"), before, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entries == nil {
		entries = []database.AuditLogEntry{}
	}

	writeJSON(w, http.StatusOK, entries)
}

// handleCountAuditLog returns how many entries the audit log holds.
// @Summary Count audit log entries
// @Description Returns the number of entries in the audit log, for "N entries" above a page of it [MESHSAT-1402]
// @Tags audit
// @Produce json
// @Success 200 {object} map[string]int
// @Failure 500 {object} map[string]string
// @Router /api/audit/count [get]
func (s *Server) handleCountAuditLog(w http.ResponseWriter, r *http.Request) {
	n, err := s.db.CountAuditLog()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": n})
}

// handleVerifyAuditChain verifies the integrity of the audit log hash chain.
// @Summary Verify audit chain
// @Description Verifies the audit log's SHA-256 hash chain over the newest entries: each row's hash and its link to the row before it
// @Tags audit
// @Produce json
// @Param limit query integer false "Max entries to verify (default: 1000, max: 100000)"
// @Success 200 {object} map[string]interface{}
// @Failure 503 {object} map[string]string
// @Router /api/audit/verify [get]
func (s *Server) handleVerifyAuditChain(w http.ResponseWriter, r *http.Request) {
	if s.signing == nil {
		writeError(w, http.StatusServiceUnavailable, "signing service not available")
		return
	}

	limit := 1000
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 100000 {
			limit = n
		}
	}

	valid, brokenAt := s.signing.VerifyChain(limit)
	if brokenAt == -1 {
		limit = valid // "checked" is what was there to check [MESHSAT-1842]
	}

	result := map[string]interface{}{
		"verified":  brokenAt == -1,
		"valid":     valid,
		"checked":   limit,
		"broken_at": brokenAt,
	}
	// The whole chain, with the rows redacted after the retention period
	// counted apart: their links are checked, their hashes not. [MESHSAT-1137]
	if all, err := s.signing.VerifyAll(); err == nil {
		result["chain"] = all
	}

	writeJSON(w, http.StatusOK, result)
}

// handleGetSignerID returns the local node's public key (signer ID).
// @Summary Get signer identity
// @Description Returns the local node's Ed25519 public key used for audit log signing
// @Tags audit
// @Produce json
// @Success 200 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/audit/signer [get]
func (s *Server) handleGetSignerID(w http.ResponseWriter, r *http.Request) {
	if s.signing == nil {
		writeError(w, http.StatusServiceUnavailable, "signing service not available")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"signer_id": s.signing.SignerID(),
	})
}
