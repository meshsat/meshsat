package api

import (
	"encoding/json"
	"io"
	"net/http"

	"meshsat/internal/selfpos"
)

// SetSelfPosition installs the device's own position: the store the app
// writes through PUT /api/position/self, and the resolver GET answers from
// (the app's fix while fresh, then the local node, then the GPS reader).
// [MESHSAT-1421]
func (s *Server) SetSelfPosition(store *selfpos.Store, resolve func() (selfpos.Fix, bool)) {
	s.selfPos = store
	s.selfPosResolve = resolve
}

// selfPositionBody is what PUT /api/position/self takes. Latitude and
// longitude are required; a value the app does not know is left out.
type selfPositionBody struct {
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	AltitudeM float64  `json:"altitude_m"`
	SpeedMPS  float64  `json:"speed_mps"`
	CourseDeg float64  `json:"course_deg"`
	AccuracyM float64  `json:"accuracy_m"`
}

// handlePutSelfPosition stores the position of the device the Bridge runs on.
// @Summary Set this device's own position
// @Description Takes the position the app on the same device reports (geoclue on a Linux phone) and keeps it in memory with the time it arrived. For 10 minutes it is the Bridge's own position: the APRS-IS position beacon and, when the APRS gateway has no filter centre of its own, the APRS-IS filter use it. After that the local node's position from the mesh node table stands in, then the GPS reader. latitude and longitude are required, 0,0 is refused, and a value that is not known must be left out rather than sent as -1. Answers the stored fix with its time.
// @Tags position
// @Accept json
// @Produce json
// @Param body body object{latitude=number,longitude=number,altitude_m=number,speed_mps=number,course_deg=number,accuracy_m=number} true "Own position; altitude_m, speed_mps, course_deg and accuracy_m are optional"
// @Success 200 {object} selfpos.Fix
// @Failure 400 {object} map[string]string "missing or out of range"
// @Failure 503 {object} map[string]string "own position not wired"
// @Router /api/position/self [put]
func (s *Server) handlePutSelfPosition(w http.ResponseWriter, r *http.Request) {
	if s.selfPos == nil {
		writeError(w, http.StatusServiceUnavailable, "own position not available")
		return
	}
	var body selfPositionBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Latitude == nil || body.Longitude == nil {
		writeError(w, http.StatusBadRequest, "latitude and longitude are required")
		return
	}
	fix := selfpos.Fix{
		Latitude:  *body.Latitude,
		Longitude: *body.Longitude,
		AltitudeM: body.AltitudeM,
		SpeedMPS:  body.SpeedMPS,
		CourseDeg: body.CourseDeg,
		AccuracyM: body.AccuracyM,
	}
	if err := s.selfPos.Set(fix); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	stored, _ := s.selfPos.Get()
	writeJSON(w, http.StatusOK, stored)
}

// handleGetSelfPosition answers the device's own position as the Bridge
// resolves it now.
// @Summary Get this device's own position
// @Description Answers the Bridge's own position as it resolves it now: the app's fix (PUT /api/position/self) while it is younger than 10 minutes, else the local node's position from the mesh node table, else the GPS reader's fix. 404 when none is known.
// @Tags position
// @Produce json
// @Success 200 {object} selfpos.Fix
// @Failure 404 {object} map[string]string "no position known"
// @Router /api/position/self [get]
func (s *Server) handleGetSelfPosition(w http.ResponseWriter, r *http.Request) {
	var (
		fix selfpos.Fix
		ok  bool
	)
	switch {
	case s.selfPosResolve != nil:
		fix, ok = s.selfPosResolve()
	case s.selfPos != nil:
		fix, ok = selfpos.Resolve(s.selfPos, nil, selfpos.MaxAge)
	}
	if !ok {
		writeError(w, http.StatusNotFound, "no position known")
		return
	}
	writeJSON(w, http.StatusOK, fix)
}

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
