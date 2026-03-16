package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/engine"
)

// handleGetGeofences returns all configured geofence zones.
func (s *Server) handleGetGeofences(w http.ResponseWriter, r *http.Request) {
	if s.geofenceMon == nil {
		writeError(w, http.StatusServiceUnavailable, "geofence monitor not available")
		return
	}
	zones := s.geofenceMon.GetZones()
	writeJSON(w, http.StatusOK, zones)
}

// handleCreateGeofence creates a new geofence zone.
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
func (s *Server) handleDeleteGeofence(w http.ResponseWriter, r *http.Request) {
	if s.geofenceMon == nil {
		writeError(w, http.StatusServiceUnavailable, "geofence monitor not available")
		return
	}
	id := chi.URLParam(r, "id")
	s.geofenceMon.RemoveZone(id)
	w.WriteHeader(http.StatusNoContent)
}
