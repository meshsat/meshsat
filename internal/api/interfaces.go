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

// @Summary List interfaces
// @Description Returns all interface statuses including state, bound device, and health
// @Tags interfaces
// @Produce json
// @Success 200 {array} engine.InterfaceStatus
// @Failure 503 {object} map[string]string
// @Router /api/interfaces [get]
func (s *Server) handleGetInterfaces(w http.ResponseWriter, r *http.Request) {
	if s.ifaceMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "interface manager not available")
		return
	}
	statuses := s.ifaceMgr.GetAllStatus()
	writeJSON(w, http.StatusOK, statuses)
}

// @Summary Get interface
// @Description Returns status of a single interface by ID
// @Tags interfaces
// @Produce json
// @Param id path string true "Interface ID (e.g. sbd_0, tcp_0)"
// @Success 200 {object} engine.InterfaceStatus
// @Failure 404 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/interfaces/{id} [get]
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

// @Summary Create interface
// @Description Creates a new transport interface
// @Tags interfaces
// @Accept json
// @Produce json
// @Param body body database.Interface true "Interface definition"
// @Success 201 {object} database.Interface
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/interfaces [post]
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
	// [MESHSAT-680] Validate transforms at write time so typos like
	// {"type":"decrypt"} or {"type":"aes-gcm"} fail the Settings save
	// instead of silently dispatching plaintext at first message.
	// Mirrors the same gate on PUT /api/interfaces/{id}.
	if warns, errs := s.validateInterfaceTransforms(iface); len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":    "transform validation failed",
			"errors":   errs,
			"warnings": warns,
		})
		return
	}
	if err := s.ifaceMgr.CreateInterface(iface); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, iface)
}

// @Summary Update interface
// @Description Updates an existing interface configuration including transforms
// @Tags interfaces
// @Accept json
// @Produce json
// @Param id path string true "Interface ID"
// @Param body body database.Interface true "Interface definition"
// @Success 200 {object} database.Interface
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/interfaces/{id} [put]
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
// @Summary Validate transform chain
// @Description Checks if a transform chain is compatible with a channel type's capabilities
// @Tags crypto
// @Accept json
// @Produce json
// @Param body body object true "Validation request" example({"channel_type":"sbd","transforms":"[\"smaz2\",\"encrypt\",\"base64\"]"})
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]string
// @Router /api/crypto/validate-transforms [post]
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

// @Summary Delete interface
// @Description Deletes a transport interface
// @Tags interfaces
// @Param id path string true "Interface ID"
// @Success 204
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/interfaces/{id} [delete]
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

// handleEnableInterface enables an interface.
// @Summary Enable interface
// @Description Enables a transport interface
// @Tags interfaces
// @Produce json
// @Param id path string true "Interface ID"
// @Success 200 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/interfaces/{id}/enable [post]
func (s *Server) handleEnableInterface(w http.ResponseWriter, r *http.Request) {
	s.setInterfaceEnabled(w, r, true)
}

// handleDisableInterface disables an interface.
// @Summary Disable interface
// @Description Disables a transport interface
// @Tags interfaces
// @Produce json
// @Param id path string true "Interface ID"
// @Success 200 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/interfaces/{id}/disable [post]
func (s *Server) handleDisableInterface(w http.ResponseWriter, r *http.Request) {
	s.setInterfaceEnabled(w, r, false)
}

func (s *Server) setInterfaceEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	if s.ifaceMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "interface manager not available")
		return
	}
	id := chi.URLParam(r, "id")
	iface, err := s.db.GetInterface(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	iface.Enabled = enabled
	if err := s.db.UpdateInterface(iface); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": state})
}

// ---- Device Binding ----

// @Summary Bind device to interface
// @Description Binds a USB serial device to a transport interface
// @Tags interfaces
// @Accept json
// @Produce json
// @Param id path string true "Interface ID"
// @Param body body object true "Device" example({"device_id":"/dev/ttyUSB0"})
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/interfaces/{id}/bind [post]
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

// @Summary Unbind device from interface
// @Description Unbinds the serial device from a transport interface
// @Tags interfaces
// @Produce json
// @Param id path string true "Interface ID"
// @Success 200 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/interfaces/{id}/unbind [post]
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

// @Summary List detected devices
// @Description Returns all detected serial devices from the interface manager
// @Tags interfaces
// @Produce json
// @Success 200 {array} engine.DetectedDevice
// @Failure 503 {object} map[string]string
// @Router /api/devices [get]
func (s *Server) handleGetDevices(w http.ResponseWriter, r *http.Request) {
	if s.ifaceMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "interface manager not available")
		return
	}
	devices := s.ifaceMgr.GetDetectedDevices()
	writeJSON(w, http.StatusOK, devices)
}

// ---- Access Rules ----

// @Summary List access rules
// @Description Returns all access control rules ordered by priority
// @Tags access-rules
// @Produce json
// @Success 200 {array} database.AccessRule
// @Failure 500 {object} map[string]string
// @Router /api/access-rules [get]
func (s *Server) handleGetAccessRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.db.GetAllAccessRules()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

// @Summary Create access rule
// @Description Creates a new access control rule for an interface
// @Tags access-rules
// @Accept json
// @Produce json
// @Param body body database.AccessRule true "Access rule"
// @Success 201 {object} database.AccessRule
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/access-rules [post]
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

// @Summary Update access rule
// @Description Updates an existing access control rule
// @Tags access-rules
// @Accept json
// @Produce json
// @Param id path integer true "Rule ID"
// @Param body body database.AccessRule true "Access rule"
// @Success 200 {object} database.AccessRule
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/access-rules/{id} [put]
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

// handleEnableAccessRule enables an access rule.
// @Summary Enable access rule
// @Description Enables an access control rule
// @Tags access-rules
// @Produce json
// @Param id path integer true "Rule ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/access-rules/{id}/enable [post]
func (s *Server) handleEnableAccessRule(w http.ResponseWriter, r *http.Request) {
	s.setAccessRuleEnabled(w, r, true)
}

// handleDisableAccessRule disables an access rule.
// @Summary Disable access rule
// @Description Disables an access control rule
// @Tags access-rules
// @Produce json
// @Param id path integer true "Rule ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/access-rules/{id}/disable [post]
func (s *Server) handleDisableAccessRule(w http.ResponseWriter, r *http.Request) {
	s.setAccessRuleEnabled(w, r, false)
}

func (s *Server) setAccessRuleEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule ID")
		return
	}
	if err := s.db.SetAccessRuleEnabled(id, enabled); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadAccessRules()
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": state})
}

// handleReorderAccessRules reorders rules within an interface+direction.
// @Summary Reorder access rules
// @Description Reorders access rules by setting priority based on array position
// @Tags access-rules
// @Accept json
// @Produce json
// @Param body body object true "Reorder request" example({"interface_id":"sbd_0","direction":"egress","rule_ids":[3,1,2]})
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/access-rules/reorder [post]
func (s *Server) handleReorderAccessRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InterfaceID string  `json:"interface_id"`
		Direction   string  `json:"direction"`
		RuleIDs     []int64 `json:"rule_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.InterfaceID == "" || req.Direction == "" || len(req.RuleIDs) == 0 {
		writeError(w, http.StatusBadRequest, "interface_id, direction, and rule_ids are required")
		return
	}
	for i, id := range req.RuleIDs {
		if err := s.db.SetAccessRulePriority(id, i+1); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	s.reloadAccessRules()
	writeJSON(w, http.StatusOK, map[string]string{"status": "reordered"})
}

// handleGetAccessRuleStats returns match count and cost estimate for a rule.
// @Summary Get access rule statistics
// @Description Returns match count and cost estimate for a specific access rule
// @Tags access-rules
// @Produce json
// @Param id path integer true "Rule ID"
// @Success 200 {object} database.AccessRuleStats
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/access-rules/{id}/stats [get]
func (s *Server) handleGetAccessRuleStats(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule ID")
		return
	}
	stats, err := s.db.GetAccessRuleStats(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// @Summary Delete access rule
// @Description Deletes an access control rule
// @Tags access-rules
// @Param id path integer true "Rule ID"
// @Success 204
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/access-rules/{id} [delete]
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

// @Summary List object groups
// @Description Returns all object groups (address/port groups for access rules)
// @Tags access-rules
// @Produce json
// @Success 200 {array} database.ObjectGroup
// @Failure 500 {object} map[string]string
// @Router /api/object-groups [get]
func (s *Server) handleGetObjectGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.db.GetAllObjectGroups()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

// @Summary Create object group
// @Description Creates a new object group for use in access rules
// @Tags access-rules
// @Accept json
// @Produce json
// @Param body body database.ObjectGroup true "Object group"
// @Success 201 {object} database.ObjectGroup
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/object-groups [post]
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

// @Summary Update object group
// @Description Updates an existing object group
// @Tags access-rules
// @Accept json
// @Produce json
// @Param id path string true "Object group ID"
// @Param body body database.ObjectGroup true "Object group"
// @Success 200 {object} database.ObjectGroup
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/object-groups/{id} [put]
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

// @Summary Delete object group
// @Description Deletes an object group
// @Tags access-rules
// @Param id path string true "Object group ID"
// @Success 204
// @Failure 500 {object} map[string]string
// @Router /api/object-groups/{id} [delete]
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

// @Summary List failover groups
// @Description Returns all failover groups with their member interfaces
// @Tags failover
// @Produce json
// @Success 200 {array} failoverGroupWithMembers
// @Failure 500 {object} map[string]string
// @Router /api/failover-groups [get]
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

// @Summary Create failover group
// @Description Creates a new failover group with member interfaces and priority/round-robin mode
// @Tags failover
// @Accept json
// @Produce json
// @Param body body object true "Failover group with members"
// @Success 201 {object} object
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/failover-groups [post]
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

// @Summary Update failover group
// @Description Updates a failover group and replaces its member list
// @Tags failover
// @Accept json
// @Produce json
// @Param id path string true "Failover group ID"
// @Param body body object true "Failover group with members"
// @Success 200 {object} object
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/failover-groups/{id} [put]
func (s *Server) handleUpdateFailoverGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		database.FailoverGroup
		Members []database.FailoverMember `json:"members"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	req.ID = id
	if err := s.db.UpdateFailoverGroup(&req.FailoverGroup); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Replace members: delete existing, insert new
	if err := s.db.DeleteFailoverMembers(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, m := range req.Members {
		m.GroupID = id
		if err := s.db.InsertFailoverMember(&m); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, req)
}

// @Summary Delete failover group
// @Description Deletes a failover group and its members
// @Tags failover
// @Param id path string true "Failover group ID"
// @Success 204
// @Failure 500 {object} map[string]string
// @Router /api/failover-groups/{id} [delete]
func (s *Server) handleDeleteFailoverGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.db.DeleteFailoverGroup(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Health Scores ----

// handleGetHealthScores returns composite health scores for all interfaces.
// @Summary Get interface health scores
// @Description Returns composite health scores for all interfaces (signal, latency, error rate)
// @Tags interfaces
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 503 {object} map[string]string
// @Router /api/interfaces/health [get]
func (s *Server) handleGetHealthScores(w http.ResponseWriter, r *http.Request) {
	if s.healthScorer == nil {
		writeError(w, http.StatusServiceUnavailable, "health scorer not available")
		return
	}
	scores := s.healthScorer.ScoreAll()
	writeJSON(w, http.StatusOK, scores)
}

// reloadAccessRules refreshes the in-memory access rules after CRUD mutations.
func (s *Server) reloadAccessRules() {
	if s.accessEval != nil {
		if err := s.accessEval.ReloadFromDB(); err != nil {
			log.Error().Err(err).Msg("failed to reload access rules")
		}
	}
}
