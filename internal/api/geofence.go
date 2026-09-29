package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/engine"
)

// handleGetGeofences returns all configured geofence zones.
// @Summary List geofence zones
// @Description Returns all configured geofence zones
// @Tags geofences
// @Produce json
// @Success 200 {array} engine.GeofenceZone
// @Failure 503 {object} map[string]string
// @Router /api/geofences [get]
func (s *Server) handleGetGeofences(w http.ResponseWriter, r *http.Request) {
	if s.geofenceMon == nil {
		writeError(w, http.StatusServiceUnavailable, "geofence monitor not available")
		return
	}
	zones := s.geofenceMon.GetZones()
	writeJSON(w, http.StatusOK, zones)
}

// handleCreateGeofence creates a new geofence zone.
// @Summary Create geofence zone
// @Description Creates a new geofence zone with polygon and alert configuration
// @Tags geofences
// @Accept json
// @Produce json
// @Param body body engine.GeofenceZone true "Geofence zone"
// @Success 201 {object} engine.GeofenceZone
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/geofences [post]
func (s *Server) handleCreateGeofence(w http.ResponseWriter, r *http.Request) {
	if s.geofenceMon == nil {
		writeError(w, http.StatusServiceUnavailable, "geofence monitor not available")
		return
	}

	var zone engine.GeofenceZone
	if err := json.NewDecoder(r.Body).Decode(&zone); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if zone.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if len(zone.Polygon) < 3 {
		writeError(w, http.StatusBadRequest, "polygon must have at least 3 vertices")
		return
	}
	if zone.AlertOn == "" {
		zone.AlertOn = "both"
	}

	s.geofenceMon.AddZone(zone)
	writeJSON(w, http.StatusCreated, zone)
}

// handleDeleteGeofence removes a geofence zone by ID.
// @Summary Delete geofence zone
// @Description Removes a geofence zone by ID
// @Tags geofences
// @Param id path string true "Geofence zone ID"
// @Success 204
// @Failure 503 {object} map[string]string
// @Router /api/geofences/{id} [delete]
func (s *Server) handleDeleteGeofence(w http.ResponseWriter, r *http.Request) {
	if s.geofenceMon == nil {
		writeError(w, http.StatusServiceUnavailable, "geofence monitor not available")
		return
	}
	id := chi.URLParam(r, "id")
	s.geofenceMon.RemoveZone(id)
	w.WriteHeader(http.StatusNoContent)
}

// handleGetGeofenceEvents returns the newest zone crossings.
// @Summary Zone crossings
// @Description The newest crossings of the zones by mesh nodes (a mesh position that enters or leaves a zone set to alert on it), newest first, at most 50, as MeshSat Android's Zones screen lists them. Kept in memory until the Bridge restarts [MESHSAT-1414]
// @Tags geofences
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 503 {object} map[string]string
// @Router /api/geofences/events [get]
func (s *Server) handleGetGeofenceEvents(w http.ResponseWriter, r *http.Request) {
	if s.geofenceMon == nil {
		writeError(w, http.StatusServiceUnavailable, "geofence monitor not available")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"events": s.geofenceMon.Events(50)})
}
