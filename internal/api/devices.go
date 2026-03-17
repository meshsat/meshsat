package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/database"
)

func (s *Server) handleGetRegisteredDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.db.GetDevices()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list devices")
		return
	}
	if devices == nil {
		devices = []database.Device{}
	}
	writeJSON(w, http.StatusOK, devices)
}

func (s *Server) handleGetRegisteredDevice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device ID")
		return
	}
	d, err := s.db.GetDevice(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleCreateRegisteredDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IMEI  string `json:"imei"`
		Label string `json:"label"`
		Type  string `json:"type"`
		Notes string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.IMEI == "" {
		writeError(w, http.StatusBadRequest, "imei is required")
		return
	}
	if req.Label == "" {
		// Auto-generate label from last 4 IMEI digits
		if len(req.IMEI) >= 4 {
			req.Label = "Device " + req.IMEI[len(req.IMEI)-4:]
		} else {
			req.Label = "Device " + req.IMEI
		}
	}
	id, err := s.db.CreateDevice(req.IMEI, req.Label, req.Type, req.Notes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create device: "+err.Error())
		return
	}
	d, _ := s.db.GetDevice(id)
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) handleUpdateRegisteredDevice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device ID")
		return
	}
	var req struct {
		Label string `json:"label"`
		Type  string `json:"type"`
		Notes string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := s.db.UpdateDevice(id, req.Label, req.Type, req.Notes); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update device")
		return
	}
	d, _ := s.db.GetDevice(id)
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleDeleteRegisteredDevice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device ID")
		return
	}
	if err := s.db.DeleteDevice(id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete device")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
