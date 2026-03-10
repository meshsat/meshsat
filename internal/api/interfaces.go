package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/engine"
)

// ---- Interfaces ----

func (s *Server) handleGetInterfaces(w http.ResponseWriter, r *http.Request) {
	if s.ifaceMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "interface manager not available")
		return
	}
	statuses := s.ifaceMgr.GetAllStatus()
	writeJSON(w, http.StatusOK, statuses)
}

func (s *Server) handleGetInterface(w http.ResponseWriter, r *http.Request) {
	if s.ifaceMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "interface manager not available")
		return
	}
	id := chi.URLParam(r, "id")
	status, err := s.ifaceMgr.GetStatus(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleCreateInterface(w http.ResponseWriter, r *http.Request) {
	if s.ifaceMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "interface manager not available")
		return
	}
	var iface database.Interface
	if err := json.NewDecoder(r.Body).Decode(&iface); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if iface.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if iface.ChannelType == "" {
		writeError(w, http.StatusBadRequest, "channel_type is required")
		return
	}
	if err := s.ifaceMgr.CreateInterface(iface); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, iface)
}

func (s *Server) handleUpdateInterface(w http.ResponseWriter, r *http.Request) {
	if s.ifaceMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "interface manager not available")
		return
	}
	id := chi.URLParam(r, "id")

	var iface database.Interface
	if err := json.NewDecoder(r.Body).Decode(&iface); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	iface.ID = id

	// Validate transforms against channel capabilities
	if warns, errs := s.validateInterfaceTransforms(iface); len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":    "transform validation failed",
			"errors":   errs,
			"warnings": warns,
		})
		return
	}

	if err := s.ifaceMgr.UpdateInterface(iface); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, iface)
}

// handleValidateTransforms checks transform chain compatibility with a channel type.
func (s *Server) handleValidateTransforms(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChannelType string `json:"channel_type"`
		Transforms  string `json:"transforms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	binaryCapable := false
	maxPayload := 0
	if s.registry != nil {
		binaryCapable = s.registry.BinaryCapable(req.ChannelType)
		if desc, ok := s.registry.Get(req.ChannelType); ok {
			maxPayload = desc.MaxPayload
		}
	}

	warns, errs := engine.ValidateTransforms(req.Transforms, binaryCapable, maxPayload)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"valid":    len(errs) == 0,
		"errors":   errs,
		"warnings": warns,
	})
}

func (s *Server) validateInterfaceTransforms(iface database.Interface) (warnings []string, errors []string) {
	binaryCapable := false
	maxPayload := 0
	if s.registry != nil {
		binaryCapable = s.registry.BinaryCapable(iface.ChannelType)
		if desc, ok := s.registry.Get(iface.ChannelType); ok {
			maxPayload = desc.MaxPayload
		}
	}

	var allWarns, allErrs []string
	for _, dir := range []struct {
		label string
		json  string
	}{
		{"egress", iface.EgressTransforms},
		{"ingress", iface.IngressTransforms},
	} {
		w, e := engine.ValidateTransforms(dir.json, binaryCapable, maxPayload)
		for _, msg := range w {
			allWarns = append(allWarns, dir.label+": "+msg)
		}
		for _, msg := range e {
			allErrs = append(allErrs, dir.label+": "+msg)
		}
	}
	return allWarns, allErrs
}

func (s *Server) handleDeleteInterface(w http.ResponseWriter, r *http.Request) {
	if s.ifaceMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "interface manager not available")
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.ifaceMgr.DeleteInterface(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Device Binding ----

func (s *Server) handleBindDevice(w http.ResponseWriter, r *http.Request) {
	if s.ifaceMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "interface manager not available")
		return
	}
	id := chi.URLParam(r, "id")

	var req struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.DeviceID == "" {
		writeError(w, http.StatusBadRequest, "device_id is required")
		return
	}
	if err := s.ifaceMgr.BindDevice(id, req.DeviceID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "bound"})
}

func (s *Server) handleUnbindDevice(w http.ResponseWriter, r *http.Request) {
	if s.ifaceMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "interface manager not available")
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.ifaceMgr.UnbindDevice(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "unbound"})
}

func (s *Server) handleGetDevices(w http.ResponseWriter, r *http.Request) {
	if s.ifaceMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "interface manager not available")
		return
	}
	devices := s.ifaceMgr.GetDetectedDevices()
	writeJSON(w, http.StatusOK, devices)
}

// ---- Access Rules ----

func (s *Server) handleGetAccessRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.db.GetAllAccessRules()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

func (s *Server) handleCreateAccessRule(w http.ResponseWriter, r *http.Request) {
	var rule database.AccessRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if rule.InterfaceID == "" {
		writeError(w, http.StatusBadRequest, "interface_id is required")
		return
	}
	if rule.Direction != "ingress" && rule.Direction != "egress" {
		writeError(w, http.StatusBadRequest, "direction must be ingress or egress")
		return
	}
	if rule.Action == "" {
		writeError(w, http.StatusBadRequest, "action is required")
		return
	}

	id, err := s.db.InsertAccessRule(&rule)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rule.ID = id
	s.reloadAccessRules()
	writeJSON(w, http.StatusCreated, rule)
}

func (s *Server) handleUpdateAccessRule(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule ID")
		return
	}

	var rule database.AccessRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	rule.ID = id

	if err := s.db.UpdateAccessRule(&rule); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadAccessRules()
	writeJSON(w, http.StatusOK, rule)
}

func (s *Server) handleDeleteAccessRule(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule ID")
		return
	}
	if err := s.db.DeleteAccessRule(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadAccessRules()
	w.WriteHeader(http.StatusNoContent)
}

// ---- Object Groups ----

func (s *Server) handleGetObjectGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.db.GetAllObjectGroups()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

func (s *Server) handleCreateObjectGroup(w http.ResponseWriter, r *http.Request) {
	var group database.ObjectGroup
	if err := json.NewDecoder(r.Body).Decode(&group); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if group.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if group.Type == "" {
		writeError(w, http.StatusBadRequest, "type is required")
		return
	}
	if err := s.db.InsertObjectGroup(&group); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, group)
}

func (s *Server) handleUpdateObjectGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var group database.ObjectGroup
	if err := json.NewDecoder(r.Body).Decode(&group); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	group.ID = id

	if err := s.db.UpdateObjectGroup(&group); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, group)
}

func (s *Server) handleDeleteObjectGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.db.DeleteObjectGroup(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Failover Groups ----

type failoverGroupWithMembers struct {
	database.FailoverGroup
	Members []database.FailoverMember `json:"members"`
}

func (s *Server) handleGetFailoverGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.db.GetAllFailoverGroups()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	result := make([]failoverGroupWithMembers, len(groups))
	for i, g := range groups {
		members, err := s.db.GetFailoverMembers(g.ID)
		if err != nil {
			members = nil
		}
		result[i] = failoverGroupWithMembers{
			FailoverGroup: g,
			Members:       members,
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCreateFailoverGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		database.FailoverGroup
		Members []database.FailoverMember `json:"members"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if req.Mode == "" {
		req.Mode = "priority"
	}
	if err := s.db.InsertFailoverGroup(&req.FailoverGroup); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, m := range req.Members {
		m.GroupID = req.ID
		if err := s.db.InsertFailoverMember(&m); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusCreated, req)
}

func (s *Server) handleDeleteFailoverGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.db.DeleteFailoverGroup(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reloadAccessRules refreshes the in-memory access rules after CRUD mutations.
func (s *Server) reloadAccessRules() {
	if s.accessEval != nil {
		if err := s.accessEval.ReloadFromDB(); err != nil {
			log.Error().Err(err).Msg("failed to reload access rules")
		}
	}
}
