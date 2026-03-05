package api

import (
	"encoding/json"
	"net/http"
)

// handleSendPosition broadcasts MeshSat's own position to the mesh.
// @Summary Share own position
// @Description Sends a Position packet to the mesh with the specified coordinates
// @Tags position
// @Accept json
// @Param body body object{latitude=number,longitude=number,altitude=int} true "Position"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/position/send [post]
func (s *Server) handleSendPosition(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	var req struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Altitude  int32   `json:"altitude"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Latitude == 0 && req.Longitude == 0 {
		writeError(w, http.StatusBadRequest, "latitude and longitude are required")
		return
	}

	if err := s.mesh.SendPosition(r.Context(), req.Latitude, req.Longitude, req.Altitude); err != nil {
		writeError(w, http.StatusInternalServerError, "send position failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "position sent"})
}

// handleSetFixedPosition sets a fixed GPS position on the device.
// @Summary Set fixed position
// @Description Sets a fixed GPS position on the Meshtastic device via admin message
// @Tags position
// @Accept json
// @Param body body object{latitude=number,longitude=number,altitude=int} true "Position"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/position/fixed [post]
func (s *Server) handleSetFixedPosition(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	var req struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Altitude  int32   `json:"altitude"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Latitude == 0 && req.Longitude == 0 {
		writeError(w, http.StatusBadRequest, "latitude and longitude are required")
		return
	}

	if err := s.mesh.SetFixedPosition(r.Context(), req.Latitude, req.Longitude, req.Altitude); err != nil {
		writeError(w, http.StatusInternalServerError, "set fixed position failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "fixed position set"})
}

// handleRemoveFixedPosition removes the fixed position from the device.
// @Summary Remove fixed position
// @Description Removes the fixed GPS position from the Meshtastic device
// @Tags position
// @Success 200 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/position/fixed [delete]
func (s *Server) handleRemoveFixedPosition(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	if err := s.mesh.RemoveFixedPosition(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "remove fixed position failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "fixed position removed"})
}
