package api

import (
	"net/http"
	"strconv"

	"meshsat/internal/engine"
)

// SetSigningService sets the signing service for audit endpoints.
func (s *Server) SetSigningService(ss *engine.SigningService) {
	s.signing = ss
}

// handleGetAuditLog returns recent audit log entries.
func (s *Server) handleGetAuditLog(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	interfaceID := r.URL.Query().Get("interface_id")

	var entries interface{}
	var err error
	if interfaceID != "" {
		entries, err = s.db.GetAuditLogByInterface(interfaceID, limit)
	} else {
		entries, err = s.db.GetAuditLog(limit)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, entries)
}

// handleVerifyAuditChain verifies the integrity of the audit log hash chain.
func (s *Server) handleVerifyAuditChain(w http.ResponseWriter, r *http.Request) {
	if s.signing == nil {
		writeError(w, http.StatusServiceUnavailable, "signing service not available")
		return
	}

	limit := 1000
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 10000 {
			limit = n
		}
	}

	valid, brokenAt := s.signing.VerifyChain(limit)

	result := map[string]interface{}{
		"verified":  brokenAt == -1,
		"valid":     valid,
		"checked":   limit,
		"broken_at": brokenAt,
	}

	writeJSON(w, http.StatusOK, result)
}

// handleGetSignerID returns the local node's public key (signer ID).
func (s *Server) handleGetSignerID(w http.ResponseWriter, r *http.Request) {
	if s.signing == nil {
		writeError(w, http.StatusServiceUnavailable, "signing service not available")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"signer_id": s.signing.SignerID(),
	})
}
