package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

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

// takCertEntry is a single certificate in the TAK certificate lifecycle response.
type takCertEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	CredType    string `json:"cred_type"`
	Subject     string `json:"subject,omitempty"`
	Expires     string `json:"expires,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Source      string `json:"source"`
	Applied     bool   `json:"applied"`
	DaysLeft    int    `json:"days_left"`
	Status      string `json:"status"` // "valid", "expiring", "expired", "unknown"
	UpdatedAt   string `json:"updated_at"`
}

// takCertificatesResponse is the response for GET /api/tak/certificates.
type takCertificatesResponse struct {
	Enrolled     bool           `json:"enrolled"`
	Certificates []takCertEntry `json:"certificates"`
	Alerts       []string       `json:"alerts,omitempty"`
}

// handleTAKCertificates returns TAK certificate lifecycle information.
// @Summary TAK certificate lifecycle
// @Description Returns all TAK-related certificates with enrollment status, expiry alerts, and days remaining
// @Tags tak
// @Produce json
// @Success 200 {object} takCertificatesResponse
// @Router /api/tak/certificates [get]
func (s *Server) handleTAKCertificates(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.GetCredentialsByProvider("tak")
	if err != nil {
		writeJSON(w, http.StatusOK, takCertificatesResponse{})
		return
	}

	now := time.Now()
	resp := takCertificatesResponse{
		Certificates: make([]takCertEntry, 0, len(rows)),
	}

	for _, row := range rows {
		// Skip key and truststore rows — only show certs
		if row.CredType == "client_key" || row.CredType == "private_key" {
			continue
		}

		entry := takCertEntry{
			ID:          row.ID,
			Name:        row.Name,
			CredType:    row.CredType,
			Subject:     row.CertSubject,
			Expires:     row.CertNotAfter,
			Fingerprint: row.CertFingerprint,
			Source:      row.Source,
			Applied:     row.Applied == 1,
			DaysLeft:    -1,
			Status:      "unknown",
			UpdatedAt:   row.UpdatedAt,
		}

		if row.CertNotAfter != "" {
			// Try multiple date formats used in the codebase
			var expiry time.Time
			for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
				if t, err := time.Parse(layout, row.CertNotAfter); err == nil {
					expiry = t
					break
				}
			}
			if !expiry.IsZero() {
				daysLeft := int(expiry.Sub(now).Hours() / 24)
				entry.DaysLeft = daysLeft
				switch {
				case daysLeft < 0:
					entry.Status = "expired"
					resp.Alerts = append(resp.Alerts, "Certificate '"+row.Name+"' has expired")
				case daysLeft <= 30:
					entry.Status = "expiring"
					resp.Alerts = append(resp.Alerts, fmt.Sprintf("Certificate '%s' expires in %d days", row.Name, daysLeft))
				default:
					entry.Status = "valid"
				}
			}
		}

		if entry.Applied {
			resp.Enrolled = true
		}

		resp.Certificates = append(resp.Certificates, entry)
	}

	// If no TAK certs found, check enrolled cert specifically
	if len(resp.Certificates) == 0 {
		row, err := s.db.GetCredentialCache("tak-enrolled-cert")
		if err == nil && row != nil {
			resp.Enrolled = true
			entry := takCertEntry{
				ID:          row.ID,
				Name:        row.Name,
				CredType:    row.CredType,
				Subject:     row.CertSubject,
				Expires:     row.CertNotAfter,
				Fingerprint: row.CertFingerprint,
				Source:      row.Source,
				Applied:     row.Applied == 1,
				DaysLeft:    -1,
				Status:      "unknown",
				UpdatedAt:   row.UpdatedAt,
			}
			if row.CertNotAfter != "" {
				for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
					if t, err := time.Parse(layout, row.CertNotAfter); err == nil {
						daysLeft := int(t.Sub(now).Hours() / 24)
						entry.DaysLeft = daysLeft
						switch {
						case daysLeft < 0:
							entry.Status = "expired"
							resp.Alerts = append(resp.Alerts, "Enrolled certificate has expired")
						case daysLeft <= 30:
							entry.Status = "expiring"
							resp.Alerts = append(resp.Alerts, "Enrolled certificate expires soon")
						default:
							entry.Status = "valid"
						}
						break
					}
				}
			}
			resp.Certificates = append(resp.Certificates, entry)
		}
	}

	writeJSON(w, http.StatusOK, resp)
}
