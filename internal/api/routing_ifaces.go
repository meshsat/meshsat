package api

// Dynamic Reticulum interfaces (rnode, udp, auto, kiss): CRUD, restart and
// stats from Settings > Routing, plus the RNode preset and port pickers.
// [MESHSAT-1350]

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/database"
	"meshsat/internal/rnode"
	"meshsat/internal/routing"
	"meshsat/internal/transport"
)

// SetIfaceManager wires the dynamic Reticulum interface manager.
func (s *Server) SetIfaceManager(m *routing.IfaceManager) { s.dynIfaces = m }

// dynIfaceRequest is the body of POST and PUT.
type dynIfaceRequest struct {
	Type    string          `json:"type"`
	Enabled *bool           `json:"enabled,omitempty"`
	Config  json.RawMessage `json:"config"`
}

// rnodePortOption is one entry of the RNode port picker.
type rnodePortOption struct {
	Value     string `json:"value"`
	Label     string `json:"label"`
	DevPath   string `json:"dev_path,omitempty"`
	VIDPID    string `json:"vid_pid,omitempty"`
	USBSerial string `json:"usb_serial,omitempty"`
	Role      string `json:"role,omitempty"`
	Capable   bool   `json:"capable"`
	InUse     bool   `json:"in_use"`
}

func (s *Server) dynIfaceErr(w http.ResponseWriter, err error) {
	if errors.Is(err, database.ErrRoutingIfaceNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

// handleListDynIfaces lists the dynamic Reticulum interfaces.
// @Summary List dynamic Reticulum interfaces
// @Description RNode, UDP, AutoInterface and KISS TNC instances with their running state and live statistics.
// @Tags routing
// @Produce json
// @Success 200 {array} routing.DynIfaceStatus
// @Failure 503 {object} ErrorResponse
// @Router /api/routing/ifaces [get]
func (s *Server) handleListDynIfaces(w http.ResponseWriter, r *http.Request) {
	if s.dynIfaces == nil {
		writeError(w, http.StatusServiceUnavailable, "interface manager not initialized")
		return
	}
	writeJSON(w, http.StatusOK, s.dynIfaces.List())
}

// handleCreateDynIface creates and starts a dynamic interface.
// @Summary Create a dynamic Reticulum interface
// @Description Validates the type-specific config, persists it and starts the interface when enabled. Type is one of rnode, udp, auto, kiss.
// @Tags routing
// @Accept json
// @Produce json
// @Param body body dynIfaceRequest true "Interface definition"
// @Success 201 {object} routing.DynIfaceStatus
// @Failure 400 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/routing/ifaces [post]
func (s *Server) handleCreateDynIface(w http.ResponseWriter, r *http.Request) {
	if s.dynIfaces == nil {
		writeError(w, http.StatusServiceUnavailable, "interface manager not initialized")
		return
	}
	var req dynIfaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	id, err := s.dynIfaces.Create(req.Type, req.Config, enabled)
	if err != nil && id == "" {
		s.dynIfaceErr(w, err)
		return
	}
	st, gerr := s.dynIfaces.Get(id)
	if gerr != nil {
		s.dynIfaceErr(w, gerr)
		return
	}
	if err != nil && st.LastError == "" {
		st.LastError = err.Error()
	}
	writeJSON(w, http.StatusCreated, st)
}

// handleGetDynIface returns one dynamic interface.
// @Summary Get a dynamic Reticulum interface
// @Tags routing
// @Produce json
// @Param id path string true "Interface id, e.g. rnode_0"
// @Success 200 {object} routing.DynIfaceStatus
// @Failure 404 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/routing/ifaces/{id} [get]
func (s *Server) handleGetDynIface(w http.ResponseWriter, r *http.Request) {
	if s.dynIfaces == nil {
		writeError(w, http.StatusServiceUnavailable, "interface manager not initialized")
		return
	}
	st, err := s.dynIfaces.Get(chi.URLParam(r, "id"))
	if err != nil {
		s.dynIfaceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleUpdateDynIface replaces a dynamic interface's config and restarts it.
// @Summary Update a dynamic Reticulum interface
// @Description Replaces the config (omit it to keep the stored one), sets enabled, and restarts the interface.
// @Tags routing
// @Accept json
// @Produce json
// @Param id path string true "Interface id"
// @Param body body dynIfaceRequest true "New definition (type is ignored)"
// @Success 200 {object} routing.DynIfaceStatus
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/routing/ifaces/{id} [put]
func (s *Server) handleUpdateDynIface(w http.ResponseWriter, r *http.Request) {
	if s.dynIfaces == nil {
		writeError(w, http.StatusServiceUnavailable, "interface manager not initialized")
		return
	}
	id := chi.URLParam(r, "id")
	var req dynIfaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	cur, err := s.dynIfaces.Get(id)
	if err != nil {
		s.dynIfaceErr(w, err)
		return
	}
	enabled := cur.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	var raw json.RawMessage
	if len(req.Config) > 0 {
		raw = req.Config
	}
	uerr := s.dynIfaces.Update(id, raw, enabled)
	st, err := s.dynIfaces.Get(id)
	if err != nil {
		s.dynIfaceErr(w, err)
		return
	}
	if uerr != nil {
		if errors.Is(uerr, database.ErrRoutingIfaceNotFound) {
			s.dynIfaceErr(w, uerr)
			return
		}
		// Config was rejected or the start failed: 400 with the status attached.
		if st.LastError == "" {
			st.LastError = uerr.Error()
		}
		writeJSON(w, http.StatusBadRequest, st)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleDeleteDynIface stops and removes a dynamic interface.
// @Summary Delete a dynamic Reticulum interface
// @Tags routing
// @Produce json
// @Param id path string true "Interface id"
// @Success 204 "removed"
// @Failure 404 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/routing/ifaces/{id} [delete]
func (s *Server) handleDeleteDynIface(w http.ResponseWriter, r *http.Request) {
	if s.dynIfaces == nil {
		writeError(w, http.StatusServiceUnavailable, "interface manager not initialized")
		return
	}
	if err := s.dynIfaces.Delete(chi.URLParam(r, "id")); err != nil {
		s.dynIfaceErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRestartDynIface restarts a dynamic interface.
// @Summary Restart a dynamic Reticulum interface
// @Tags routing
// @Produce json
// @Param id path string true "Interface id"
// @Success 200 {object} routing.DynIfaceStatus
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/routing/ifaces/{id}/restart [post]
func (s *Server) handleRestartDynIface(w http.ResponseWriter, r *http.Request) {
	if s.dynIfaces == nil {
		writeError(w, http.StatusServiceUnavailable, "interface manager not initialized")
		return
	}
	id := chi.URLParam(r, "id")
	rerr := s.dynIfaces.Restart(id)
	st, err := s.dynIfaces.Get(id)
	if err != nil {
		s.dynIfaceErr(w, err)
		return
	}
	if rerr != nil {
		if st.LastError == "" {
			st.LastError = rerr.Error()
		}
		writeJSON(w, http.StatusBadRequest, st)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleDynIfaceStats returns one interface's live statistics.
// @Summary Live statistics of a dynamic Reticulum interface
// @Description For an RNode: frequency, bandwidth, RSSI, SNR, airtime and channel load as the radio reports them.
// @Tags routing
// @Produce json
// @Param id path string true "Interface id"
// @Success 200 {object} map[string]interface{}
// @Failure 404 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/routing/ifaces/{id}/stats [get]
func (s *Server) handleDynIfaceStats(w http.ResponseWriter, r *http.Request) {
	if s.dynIfaces == nil {
		writeError(w, http.StatusServiceUnavailable, "interface manager not initialized")
		return
	}
	st, err := s.dynIfaces.Get(chi.URLParam(r, "id"))
	if err != nil {
		s.dynIfaceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id": st.ID, "type": st.Type, "running": st.Running, "online": st.Online,
		"summary": st.Summary, "last_error": st.LastError, "stats": st.Stats,
	})
}

// handleRNodePresets lists the RNode radio presets.
// @Summary RNode radio presets
// @Description The starter presets CrossTalk ships (us-915, eu-868, au-915, ism-433) with their frequency, bandwidth, SF, CR and TX power.
// @Tags routing
// @Produce json
// @Success 200 {array} rnode.Preset
// @Router /api/routing/rnode/presets [get]
func (s *Server) handleRNodePresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, rnode.Presets)
}

// handleRNodePorts lists serial ports for the RNode port picker.
// @Summary Serial ports for the RNode picker
// @Description Labels come from the device supervisor's registry (VID:PID and claimed role); nothing is probed. A port is "capable" when its VID:PID is one an RNode board uses and "in_use" when another driver owns it. Prefer the usb_serial value: it survives re-enumeration.
// @Tags routing
// @Produce json
// @Success 200 {array} rnodePortOption
// @Router /api/routing/rnode/ports [get]
func (s *Server) handleRNodePorts(w http.ResponseWriter, r *http.Request) {
	out := []rnodePortOption{{Value: "auto", Label: "Auto (device supervisor probes RNode-capable boards)", Capable: true}}
	if s.devSupervisor != nil {
		for _, e := range s.devSupervisor.Registry().ListAll() {
			opt := rnodePortOption{
				DevPath: e.DevPath, VIDPID: e.VIDPID, USBSerial: e.USBSerial, Role: string(e.Role),
				Capable: transport.RNodeCapableVIDPID(e.VIDPID),
				InUse:   e.Role != transport.RoleNone && e.Role != transport.RoleRNode,
			}
			if e.USBSerial != "" {
				opt.Value = "usb_serial:" + e.USBSerial
			} else {
				opt.Value = e.DevPath
			}
			label := e.DevPath
			if t := transport.ClassifyDevice(e.VIDPID); t != "" {
				label += " (" + t
				if e.Role != transport.RoleNone {
					label += ", " + string(e.Role)
				}
				label += ")"
			} else if e.VIDPID != "" {
				label += " (" + e.VIDPID + ")"
			}
			opt.Label = label
			out = append(out, opt)
		}
	}
	sort.SliceStable(out[1:], func(i, j int) bool { return out[i+1].DevPath < out[j+1].DevPath })
	writeJSON(w, http.StatusOK, out)
}
