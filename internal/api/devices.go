package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/database"
	"meshsat/internal/device"
)

// deviceResponse extends the DB model with a computed status field.
type deviceResponse struct {
	ID        int64   `json:"id"`
	IMEI      string  `json:"imei"`
	Label     string  `json:"label"`
	Type      string  `json:"type"`
	Notes     string  `json:"notes"`
	Status    string  `json:"status"`
	LastSeen  *string `json:"last_seen,omitempty"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// computeStatus returns "online", "offline", or "never_seen" based on last_seen.
func computeStatus(lastSeen *string) string {
	if lastSeen == nil || *lastSeen == "" {
		return "never_seen"
	}
	t, err := time.Parse("2006-01-02 15:04:05", *lastSeen)
	if err != nil {
		// Try RFC3339 as fallback
		t, err = time.Parse(time.RFC3339, *lastSeen)
		if err != nil {
			return "never_seen"
		}
	}
	if time.Since(t) < 24*time.Hour {
		return "online"
	}
	return "offline"
}

func toDeviceResponse(d *database.Device) deviceResponse {
	return deviceResponse{
		ID:        d.ID,
		IMEI:      d.IMEI,
		Label:     d.Label,
		Type:      d.Type,
		Notes:     d.Notes,
		Status:    computeStatus(d.LastSeen),
		LastSeen:  d.LastSeen,
		CreatedAt: d.CreatedAt,
		UpdatedAt: d.UpdatedAt,
	}
}

// @Summary List registered devices
// @Description Returns all registered devices with computed status (online/offline/never_seen).
// @Tags devices
// @Produce json
// @Success 200 {array} deviceResponse
// @Router /api/device-registry [get]
func (s *Server) handleGetRegisteredDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.db.GetDevices()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list devices")
		return
	}
	resp := make([]deviceResponse, len(devices))
	for i := range devices {
		resp[i] = toDeviceResponse(&devices[i])
	}
	writeJSON(w, http.StatusOK, resp)
}

// @Summary Get a registered device
// @Description Returns a single device by ID with computed status.
// @Tags devices
// @Produce json
// @Param id path int true "Device ID"
// @Success 200 {object} deviceResponse
// @Failure 400 {object} map[string]string "invalid ID"
// @Failure 404 {object} map[string]string "not found"
// @Router /api/device-registry/{id} [get]
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
	writeJSON(w, http.StatusOK, toDeviceResponse(d))
}

// @Summary Create a registered device
// @Description Registers a new device. Validates IMEI (15-digit Luhn check). Rejects duplicate IMEIs with 409.
// @Tags devices
// @Accept json
// @Produce json
// @Param body body object true "Device data" SchemaExample({"imei":"300234063904190","label":"Field Unit 1"})
// @Success 201 {object} deviceResponse
// @Failure 400 {object} map[string]string "invalid IMEI or missing fields"
// @Failure 409 {object} map[string]string "duplicate IMEI"
// @Router /api/device-registry [post]
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
	req.IMEI = strings.TrimSpace(req.IMEI)
	if req.IMEI == "" {
		writeError(w, http.StatusBadRequest, "imei is required")
		return
	}
	if !device.ValidateIMEI(req.IMEI) {
		writeError(w, http.StatusBadRequest, "invalid IMEI: must be exactly 15 digits")
		return
	}

	// Check for duplicate IMEI
	existing, _ := s.db.GetDeviceByIMEI(req.IMEI)
	if existing != nil {
		writeError(w, http.StatusConflict, "device with this IMEI already exists")
		return
	}

	if req.Label == "" {
		req.Label = "Device " + req.IMEI[len(req.IMEI)-4:]
	}
	id, err := s.db.CreateDevice(req.IMEI, req.Label, req.Type, req.Notes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create device: "+err.Error())
		return
	}
	d, _ := s.db.GetDevice(id)

	// Fire device-created callback (e.g., auto-provision VPN peer)
	if s.onDeviceCreated != nil {
		go s.onDeviceCreated(id, req.Label)
	}

	writeJSON(w, http.StatusCreated, toDeviceResponse(d))
}

// @Summary Update a registered device
// @Description Updates label, type, and notes of an existing device.
// @Tags devices
// @Accept json
// @Produce json
// @Param id path int true "Device ID"
// @Param body body object true "Update data" SchemaExample({"label":"Updated Name","type":"rockblock","notes":"deployed"})
// @Success 200 {object} deviceResponse
// @Failure 400 {object} map[string]string "invalid ID or JSON"
// @Failure 404 {object} map[string]string "not found"
// @Router /api/device-registry/{id} [put]
func (s *Server) handleUpdateRegisteredDevice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device ID")
		return
	}
	// Verify device exists
	if _, err := s.db.GetDevice(id); err != nil {
		writeError(w, http.StatusNotFound, "device not found")
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
	writeJSON(w, http.StatusOK, toDeviceResponse(d))
}

// @Summary Delete a registered device
// @Description Removes a device by ID.
// @Tags devices
// @Param id path int true "Device ID"
// @Success 204
// @Failure 400 {object} map[string]string "invalid ID"
// @Failure 404 {object} map[string]string "not found"
// @Router /api/device-registry/{id} [delete]
func (s *Server) handleDeleteRegisteredDevice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device ID")
		return
	}
	if _, err := s.db.GetDevice(id); err != nil {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}
	// Fire device-deleted callback synchronously before DB cascade removes vpn_peers row
	if s.onDeviceDeleted != nil {
		s.onDeviceDeleted(id)
	}

	if err := s.db.DeleteDevice(id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete device")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetOnMOCallback sets a callback that fires when a RockBLOCK MO message is received.
// Used by main.go to wire device last_seen updates.
func (s *Server) SetOnMOCallback(fn func(imei string)) {
	s.onMOCallback = fn
}
