package api

import (
	"fmt"
	"io"
	"net/http"

	"meshsat/internal/gateway"
)

// handleTAKMissions lists available missions from the connected TAK Server.
// @Summary List TAK missions
// @Description Returns missions from the TAK Server Marti API
// @Tags tak
// @Produce json
// @Success 200 {array} gateway.MartiMission
// @Router /api/tak/missions [get]
func (s *Server) handleTAKMissions(w http.ResponseWriter, r *http.Request) {
	client, err := s.getMartiClient()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	missions, err := client.ListMissions()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, missions)
}

// handleTAKUpload uploads a file to the TAK Server as a DataPackage.
// @Summary Upload file to TAK Server
// @Description Uploads a file to the TAK Server via Marti sync API
// @Tags tak
// @Accept multipart/form-data
// @Produce json
// @Param file formance file true "File to upload"
// @Success 200 {object} map[string]string
// @Router /api/tak/upload [post]
func (s *Server) handleTAKUpload(w http.ResponseWriter, r *http.Request) {
	client, err := s.getMartiClient()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file required"})
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read file: " + err.Error()})
		return
	}

	hash, err := client.UploadContent(header.Filename, data, header.Header.Get("Content-Type"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"hash": hash, "filename": header.Filename})
}

// handleTAKDownload downloads content from the TAK Server by hash.
// @Summary Download from TAK Server
// @Description Downloads a file from the TAK Server by content hash
// @Tags tak
// @Produce octet-stream
// @Param hash query string true "Content hash"
// @Success 200 {file} binary
// @Router /api/tak/download [get]
func (s *Server) handleTAKDownload(w http.ResponseWriter, r *http.Request) {
	hash := r.URL.Query().Get("hash")
	if hash == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "hash parameter required"})
		return
	}

	client, err := s.getMartiClient()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	data, filename, err := client.DownloadContent(hash)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	if filename != "" {
		w.Header().Set("Content-Disposition", filename)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(data) //nolint:errcheck
}

// handleTAKSASnapshot returns the current SA snapshot from the TAK Server.
// @Summary TAK SA snapshot
// @Description Returns the current situational awareness CoT snapshot
// @Tags tak
// @Produce json
// @Success 200 {string} string
// @Router /api/tak/sa [get]
func (s *Server) handleTAKSASnapshot(w http.ResponseWriter, r *http.Request) {
	client, err := s.getMartiClient()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	data, err := client.GetSASnapshot()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/xml")
	w.Write(data) //nolint:errcheck
}

// getMartiClient creates a MartiClient from the current TAK gateway config.
func (s *Server) getMartiClient() (*gateway.MartiClient, error) {
	// Get TAK gateway config from DB
	gwCfg, err := s.db.GetGatewayConfig("tak")
	if err != nil || gwCfg == nil {
		return nil, fmt.Errorf("TAK gateway not configured")
	}

	cfg, err := gateway.ParseTAKConfig(gwCfg.Config)
	if err != nil {
		return nil, err
	}

	// Marti API uses HTTPS on port 8443 (same host as CoT on 8087/8089)
	martiURL := fmt.Sprintf("https://%s:8443", cfg.Host)
	return gateway.NewMartiClient(martiURL, cfg.CertFile, cfg.KeyFile, cfg.CAFile)
}
