package api

import (
	"encoding/json"
	"net/http"

	"meshsat/internal/gateway"
)

type takEnrollRequest struct {
	ServerURL string `json:"server_url"` // e.g., https://tak-server:8446
	Username  string `json:"username"`
	Password  string `json:"password"`
}

type takEnrollResponse struct {
	Success     bool   `json:"success"`
	Subject     string `json:"subject,omitempty"`
	Expires     string `json:"expires,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Error       string `json:"error,omitempty"`
}

// handleTAKEnroll performs certificate enrollment against a TAK Server.
// @Summary Enroll with TAK Server
// @Description Requests a client certificate from a TAK Server via port 8446 enrollment API
// @Tags tak
// @Accept json
// @Produce json
// @Param body body takEnrollRequest true "Enrollment credentials"
// @Success 200 {object} takEnrollResponse
// @Failure 400 {object} takEnrollResponse
// @Router /api/tak/enroll [post]
func (s *Server) handleTAKEnroll(w http.ResponseWriter, r *http.Request) {
	var req takEnrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, takEnrollResponse{Error: "invalid request body"})
		return
	}

	cfg := gateway.TAKEnrollConfig{
		ServerURL: req.ServerURL,
		Username:  req.Username,
		Password:  req.Password,
	}

	result, err := gateway.TAKEnroll(cfg)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, takEnrollResponse{Error: err.Error()})
		return
	}

	// Store in credential cache
	if err := gateway.StoreEnrolledCert(s.db, result); err != nil {
		writeJSON(w, http.StatusInternalServerError, takEnrollResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, takEnrollResponse{
		Success:     true,
		Subject:     result.Subject,
		Expires:     result.NotAfter.Format("2006-01-02"),
		Fingerprint: result.Fingerprint,
	})
}

// handleTAKEnrollStatus returns the status of the enrolled TAK certificate.
// @Summary TAK enrollment status
// @Description Returns the current TAK Server certificate enrollment status
// @Tags tak
// @Produce json
// @Success 200 {object} takEnrollResponse
// @Router /api/tak/enroll/status [get]
func (s *Server) handleTAKEnrollStatus(w http.ResponseWriter, r *http.Request) {
	row, err := s.db.GetCredentialCache("tak-enrolled-cert")
	if err != nil || row == nil {
		writeJSON(w, http.StatusOK, takEnrollResponse{
			Success: false,
			Error:   "not enrolled",
		})
		return
	}

	writeJSON(w, http.StatusOK, takEnrollResponse{
		Success:     true,
		Subject:     row.CertSubject,
		Expires:     row.CertNotAfter,
		Fingerprint: row.CertFingerprint,
	})
}
