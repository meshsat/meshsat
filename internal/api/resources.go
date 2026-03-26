package api

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// handleGetResources lists all received resources (without data blobs).
// @Summary List received resources
// @Description Returns metadata for all resources received via Reticulum transfer
// @Tags resources
// @Success 200 {array} database.ReceivedResource
// @Router /api/resources [get]
func (s *Server) handleGetResources(w http.ResponseWriter, r *http.Request) {
	resources, err := s.db.GetReceivedResources(100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"resources": resources})
}

// handleGetResourceData downloads the binary data for a received resource.
// @Summary Download resource data
// @Description Returns the raw binary data for a received resource by hash
// @Tags resources
// @Param hash path string true "Resource SHA-256 hash (hex)"
// @Success 200 {file} binary
// @Failure 404 {object} map[string]string
// @Router /api/resources/{hash}/data [get]
func (s *Server) handleGetResourceData(w http.ResponseWriter, r *http.Request) {
	hash := chi.URLParam(r, "hash")
	data, err := s.db.GetReceivedResourceData(hash)
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename="+hash[:16]+".bin")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// handleDeleteResource removes a received resource.
// @Summary Delete received resource
// @Description Permanently removes a received resource from storage
// @Tags resources
// @Param hash path string true "Resource SHA-256 hash (hex)"
// @Success 200 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/resources/{hash} [delete]
func (s *Server) handleDeleteResource(w http.ResponseWriter, r *http.Request) {
	hash := chi.URLParam(r, "hash")
	if err := s.db.DeleteReceivedResource(hash); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleOfferResource offers data as a Reticulum resource on a specified interface.
// @Summary Offer resource for transfer
// @Description Advertises data for chunked transfer on the specified Reticulum interface
// @Tags resources
// @Param body body object true "{data: base64, iface: string}"
// @Success 201 {object} map[string]string "hash"
// @Failure 400 {object} map[string]string
// @Router /api/resources/offer [post]
func (s *Server) handleOfferResource(w http.ResponseWriter, r *http.Request) {
	if s.resourceXfer == nil {
		writeError(w, http.StatusServiceUnavailable, "resource transfer not available")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB max
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}

	var req struct {
		Data  []byte `json:"data"`  // base64-encoded binary
		Iface string `json:"iface"` // target interface ID (e.g. "tcp_0", "iridium_0")
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "parse body: "+err.Error())
		return
	}
	if len(req.Data) == 0 {
		writeError(w, http.StatusBadRequest, "data is required")
		return
	}
	if req.Iface == "" {
		writeError(w, http.StatusBadRequest, "iface is required")
		return
	}

	hash, err := s.resourceXfer.Offer(r.Context(), req.Data, req.Iface)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"hash":   hex.EncodeToString(hash[:]),
		"status": "offered",
		"iface":  req.Iface,
	})
}

// handleGetResourceStats returns current transfer statistics.
// @Summary Resource transfer statistics
// @Description Returns counts of active outbound and inbound transfers
// @Tags resources
// @Success 200 {object} map[string]int
// @Router /api/resources/stats [get]
func (s *Server) handleGetResourceStats(w http.ResponseWriter, r *http.Request) {
	if s.resourceXfer == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"outbound": 0,
			"inbound":  0,
			"enabled":  false,
		})
		return
	}

	outbound, inbound := s.resourceXfer.Stats()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"outbound": outbound,
		"inbound":  inbound,
		"enabled":  true,
	})
}
