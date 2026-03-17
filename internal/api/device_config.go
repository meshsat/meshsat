package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
)

// deviceConfigVersionResponse is the API response for a config version.
type deviceConfigVersionResponse struct {
	Version   int    `json:"version"`
	YAML      string `json:"yaml"`
	Comment   string `json:"comment"`
	CreatedAt string `json:"created_at"`
}

// @Summary Get current device configuration
// @Description Returns the latest YAML configuration version for a device, or 204 if no config exists.
// @Tags devices
// @Produce json
// @Param id path int true "Device ID"
// @Success 200 {object} deviceConfigVersionResponse
// @Success 204 "No configuration exists"
// @Failure 400 {object} map[string]string "invalid ID"
// @Failure 404 {object} map[string]string "device not found"
// @Router /api/device-registry/{id}/config [get]
func (s *Server) handleGetDeviceConfig(w http.ResponseWriter, r *http.Request) {
	deviceID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device ID")
		return
	}
	if _, err := s.db.GetDevice(deviceID); err != nil {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}

	v, err := s.db.GetDeviceConfigLatest(deviceID)
	if err != nil {
		if err == sql.ErrNoRows {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get config")
		return
	}
	writeJSON(w, http.StatusOK, deviceConfigVersionResponse{
		Version:   v.Version,
		YAML:      v.YAML,
		Comment:   v.Comment,
		CreatedAt: v.CreatedAt,
	})
}

// @Summary Save device configuration
// @Description Saves a new YAML configuration version for a device. The YAML is validated before saving.
// @Tags devices
// @Accept json
// @Produce json
// @Param id path int true "Device ID"
// @Param body body object true "Config data" SchemaExample({"yaml":"key: value","comment":"initial config"})
// @Success 201 {object} deviceConfigVersionResponse
// @Failure 400 {object} map[string]string "invalid YAML or missing fields"
// @Failure 404 {object} map[string]string "device not found"
// @Router /api/device-registry/{id}/config [put]
func (s *Server) handlePutDeviceConfig(w http.ResponseWriter, r *http.Request) {
	deviceID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device ID")
		return
	}
	if _, err := s.db.GetDevice(deviceID); err != nil {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}

	var req struct {
		YAML    string `json:"yaml"`
		Comment string `json:"comment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	req.YAML = strings.TrimSpace(req.YAML)
	if req.YAML == "" {
		writeError(w, http.StatusBadRequest, "yaml is required")
		return
	}

	// Validate YAML syntax
	var parsed interface{}
	if err := yaml.Unmarshal([]byte(req.YAML), &parsed); err != nil {
		writeError(w, http.StatusBadRequest, "invalid YAML: "+err.Error())
		return
	}

	v, err := s.db.CreateDeviceConfigVersion(deviceID, req.YAML, req.Comment)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save config: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, deviceConfigVersionResponse{
		Version:   v.Version,
		YAML:      v.YAML,
		Comment:   v.Comment,
		CreatedAt: v.CreatedAt,
	})
}

// @Summary List device configuration versions
// @Description Returns all configuration versions for a device, newest first.
// @Tags devices
// @Produce json
// @Param id path int true "Device ID"
// @Success 200 {array} deviceConfigVersionResponse
// @Failure 400 {object} map[string]string "invalid ID"
// @Failure 404 {object} map[string]string "device not found"
// @Router /api/device-registry/{id}/config/versions [get]
func (s *Server) handleGetDeviceConfigVersions(w http.ResponseWriter, r *http.Request) {
	deviceID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device ID")
		return
	}
	if _, err := s.db.GetDevice(deviceID); err != nil {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}

	versions, err := s.db.GetDeviceConfigVersions(deviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list config versions")
		return
	}
	resp := make([]deviceConfigVersionResponse, len(versions))
	for i, v := range versions {
		resp[i] = deviceConfigVersionResponse{
			Version:   v.Version,
			YAML:      v.YAML,
			Comment:   v.Comment,
			CreatedAt: v.CreatedAt,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// @Summary Get a specific device configuration version
// @Description Returns a single configuration version by version number.
// @Tags devices
// @Produce json
// @Param id path int true "Device ID"
// @Param version path int true "Version number"
// @Success 200 {object} deviceConfigVersionResponse
// @Failure 400 {object} map[string]string "invalid ID or version"
// @Failure 404 {object} map[string]string "device or version not found"
// @Router /api/device-registry/{id}/config/versions/{version} [get]
func (s *Server) handleGetDeviceConfigVersion(w http.ResponseWriter, r *http.Request) {
	deviceID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device ID")
		return
	}
	if _, err := s.db.GetDevice(deviceID); err != nil {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}

	version, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil || version < 1 {
		writeError(w, http.StatusBadRequest, "invalid version number")
		return
	}

	v, err := s.db.GetDeviceConfigVersion(deviceID, version)
	if err != nil {
		writeError(w, http.StatusNotFound, "config version not found")
		return
	}
	writeJSON(w, http.StatusOK, deviceConfigVersionResponse{
		Version:   v.Version,
		YAML:      v.YAML,
		Comment:   v.Comment,
		CreatedAt: v.CreatedAt,
	})
}

// @Summary Rollback device configuration to a previous version
// @Description Creates a new config version with the YAML content from the specified version.
// @Tags devices
// @Produce json
// @Param id path int true "Device ID"
// @Param version path int true "Version number to rollback to"
// @Success 201 {object} deviceConfigVersionResponse
// @Failure 400 {object} map[string]string "invalid ID or version"
// @Failure 404 {object} map[string]string "device or version not found"
// @Router /api/device-registry/{id}/config/rollback/{version} [post]
func (s *Server) handleRollbackDeviceConfig(w http.ResponseWriter, r *http.Request) {
	deviceID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device ID")
		return
	}
	if _, err := s.db.GetDevice(deviceID); err != nil {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}

	version, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil || version < 1 {
		writeError(w, http.StatusBadRequest, "invalid version number")
		return
	}

	old, err := s.db.GetDeviceConfigVersion(deviceID, version)
	if err != nil {
		writeError(w, http.StatusNotFound, "config version not found")
		return
	}

	comment := "rollback to v" + strconv.Itoa(version)
	v, err := s.db.CreateDeviceConfigVersion(deviceID, old.YAML, comment)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create rollback version: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, deviceConfigVersionResponse{
		Version:   v.Version,
		YAML:      v.YAML,
		Comment:   v.Comment,
		CreatedAt: v.CreatedAt,
	})
}
