package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/database"
	"meshsat/internal/hemb"
)

// @Summary List HeMB bond groups
// @Description Returns all bond groups with their member interfaces
// @Tags hemb
// @Produce json
// @Success 200 {array} object
// @Failure 500 {object} map[string]string
// @Router /api/bond-groups [get]
func (s *Server) handleGetBondGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.db.GetAllBondGroups()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if groups == nil {
		groups = []database.BondGroup{}
	}

	// Enrich each group with its members.
	type groupWithMembers struct {
		database.BondGroup
		Members []database.BondMember `json:"members"`
	}
	var result []groupWithMembers
	for _, g := range groups {
		members, _ := s.db.GetBondMembers(g.ID)
		if members == nil {
			members = []database.BondMember{}
		}
		result = append(result, groupWithMembers{BondGroup: g, Members: members})
	}

	writeJSON(w, http.StatusOK, result)
}

// @Summary Create HeMB bond group
// @Description Creates a new bond group with at least 2 member interfaces for multi-bearer delivery
// @Tags hemb
// @Accept json
// @Produce json
// @Param body body object true "Bond group" example({"id":"bond1","label":"LoRa+SBD","members":["mesh_0","sbd_0"],"min_reliability":0.95})
// @Success 201 {object} database.BondGroup
// @Failure 400 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/bond-groups [post]
func (s *Server) handleCreateBondGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID             string   `json:"id"`
		Label          string   `json:"label"`
		CostBudget     float64  `json:"cost_budget"`
		MinReliability float64  `json:"min_reliability"`
		Members        []string `json:"members"` // interface IDs
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "id required")
		return
	}
	if len(req.Members) < 2 {
		writeError(w, http.StatusBadRequest, "bond group requires at least 2 members")
		return
	}

	if req.MinReliability == 0 {
		req.MinReliability = 0.95
	}

	g := &database.BondGroup{
		ID:             req.ID,
		Label:          req.Label,
		CostBudget:     req.CostBudget,
		MinReliability: req.MinReliability,
	}
	if err := s.db.InsertBondGroup(g); err != nil {
		writeError(w, http.StatusConflict, fmt.Sprintf("bond group %s already exists", req.ID))
		return
	}

	for i, ifaceID := range req.Members {
		m := &database.BondMember{
			GroupID:     req.ID,
			InterfaceID: ifaceID,
			Priority:    i,
		}
		if err := s.db.InsertBondMember(m); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	writeJSON(w, http.StatusCreated, g)
}

// @Summary Delete HeMB bond group
// @Description Deletes a bond group and its members
// @Tags hemb
// @Param id path string true "Bond group ID"
// @Success 204
// @Failure 500 {object} map[string]string
// @Router /api/bond-groups/{id} [delete]
func (s *Server) handleDeleteBondGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.db.DeleteBondGroup(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUpdateBondGroup updates a bond group's metadata and transform
// chains. Used to toggle bond-level encryption (MESHSAT-664) by
// setting egress_transforms / ingress_transforms — same JSON shape as
// on interfaces. Membership edits go through the existing
// POST/DELETE bond-member endpoints; this handler leaves members
// untouched.
//
// @Summary Update HeMB bond group
// @Description Updates label, cost budget, min reliability, and egress/ingress transform chains
// @Tags hemb
// @Accept json
// @Produce json
// @Param id path string true "Bond group ID"
// @Param body body object true "Fields to update"
// @Success 200 {object} database.BondGroup
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/bond-groups/{id} [put]
func (s *Server) handleUpdateBondGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := s.db.GetBondGroup(id)
	if err != nil || existing == nil {
		writeError(w, http.StatusNotFound, "bond group not found")
		return
	}

	var req struct {
		Label             *string  `json:"label,omitempty"`
		CostBudget        *float64 `json:"cost_budget,omitempty"`
		MinReliability    *float64 `json:"min_reliability,omitempty"`
		EgressTransforms  *string  `json:"egress_transforms,omitempty"`
		IngressTransforms *string  `json:"ingress_transforms,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Label != nil {
		existing.Label = *req.Label
	}
	if req.CostBudget != nil {
		existing.CostBudget = *req.CostBudget
	}
	if req.MinReliability != nil {
		existing.MinReliability = *req.MinReliability
	}
	if req.EgressTransforms != nil {
		existing.EgressTransforms = *req.EgressTransforms
	}
	if req.IngressTransforms != nil {
		existing.IngressTransforms = *req.IngressTransforms
	}

	// Re-use the interfaces-side validator: same JSON shape + same set
	// of legal transform types, so any error here means the operator
	// tried to configure a transform the pipeline can't execute.
	validate := func(kind, raw string) error {
		if raw == "" || raw == "[]" {
			return nil
		}
		iface := database.Interface{
			ID:                existing.ID,
			ChannelType:       "bond",
			EgressTransforms:  raw,
			IngressTransforms: raw,
		}
		_, errs := s.validateInterfaceTransforms(iface)
		if len(errs) > 0 {
			return fmt.Errorf("%s transforms: %v", kind, errs)
		}
		return nil
	}
	if err := validate("egress", existing.EgressTransforms); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validate("ingress", existing.IngressTransforms); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.db.UpdateBondGroup(existing); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, existing)
}

// handleHeMBSend sends a payload through a HeMB bond group.
// @Summary Send payload via HeMB bond group
// @Tags hemb
// @Accept json
// @Produce json
// @Param body body object true "Send request" example({"bond_group":"bond1","payload_b64":"..."})
// @Success 200 {object} map[string]any
// @Router /api/hemb/send [post]
func (s *Server) handleHeMBSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BondGroup  string `json:"bond_group"`
		PayloadB64 string `json:"payload_b64"` // base64-encoded payload
		Text       string `json:"text"`        // plaintext alternative
		Size       int    `json:"size"`        // generate random payload of this size
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.BondGroup == "" {
		writeError(w, http.StatusBadRequest, "bond_group required")
		return
	}
	if s.dispatcher == nil {
		writeError(w, http.StatusServiceUnavailable, "dispatcher unavailable")
		return
	}

	var payload []byte
	switch {
	case req.PayloadB64 != "":
		var err error
		payload, err = base64.StdEncoding.DecodeString(req.PayloadB64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid base64 payload")
			return
		}
	case req.Text != "":
		payload = []byte(req.Text)
	case req.Size > 0:
		payload = make([]byte, req.Size)
		for i := range payload {
			payload[i] = byte(i % 256)
		}
	default:
		writeError(w, http.StatusBadRequest, "payload_b64, text, or size required")
		return
	}

	bearerCount, err := s.dispatcher.SendViaBondGroup(req.BondGroup, payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":       "sent",
		"bond_group":   req.BondGroup,
		"payload_size": len(payload),
		"bearers_used": bearerCount,
	})
}

// @Summary Get HeMB statistics
// @Description Returns HeMB coding statistics (streams, symbols, latency, cost)
// @Tags hemb
// @Produce json
// @Success 200 {object} map[string]any
// @Router /api/hemb/stats [get]
func (s *Server) handleGetHeMBStats(w http.ResponseWriter, r *http.Request) {
	st := hemb.Global.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"active_streams":        st.ActiveStreams,
		"symbols_sent":          st.SymbolsSent,
		"symbols_received":      st.SymbolsReceived,
		"generations_decoded":   st.GenerationsDecoded,
		"generations_failed":    st.GenerationsFailed,
		"bytes_free":            st.BytesFree,
		"bytes_paid":            st.BytesPaid,
		"cost_incurred":         st.CostIncurred,
		"decode_latency_p50_ms": st.DecodeLatencyP50,
		"decode_latency_p95_ms": st.DecodeLatencyP95,
	})
}

// handleHeMBFaultInject injects a fault on a bearer interface for field testing.
// @Summary Inject bearer fault
// @Tags hemb
// @Accept json
// @Produce json
// @Param body body object true "Fault inject request" example({"interface_id":"tcp_0"})
// @Success 200 {object} map[string]string
// @Router /hemb/fault-inject [post]
func (s *Server) handleHeMBFaultInject(w http.ResponseWriter, r *http.Request) {
	if s.dispatcher == nil {
		writeError(w, http.StatusServiceUnavailable, "dispatcher not available")
		return
	}
	fr := s.dispatcher.FailoverResolver()
	if fr == nil {
		writeError(w, http.StatusServiceUnavailable, "failover resolver not available")
		return
	}

	var req struct {
		InterfaceID string `json:"interface_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.InterfaceID == "" {
		writeError(w, http.StatusBadRequest, "interface_id required")
		return
	}

	fr.InjectFault(req.InterfaceID)
	writeJSON(w, http.StatusOK, map[string]string{
		"status":       "faulted",
		"interface_id": req.InterfaceID,
	})
}

// handleHeMBFaultClear clears a fault injection on a bearer interface.
// @Summary Clear bearer fault
// @Tags hemb
// @Produce json
// @Param id path string true "Interface ID"
// @Success 200 {object} map[string]string
// @Router /hemb/fault-inject/{id} [delete]
func (s *Server) handleHeMBFaultClear(w http.ResponseWriter, r *http.Request) {
	if s.dispatcher == nil {
		writeError(w, http.StatusServiceUnavailable, "dispatcher not available")
		return
	}
	fr := s.dispatcher.FailoverResolver()
	if fr == nil {
		writeError(w, http.StatusServiceUnavailable, "failover resolver not available")
		return
	}

	id := chi.URLParam(r, "id")
	fr.ClearFault(id)
	writeJSON(w, http.StatusOK, map[string]string{
		"status":       "restored",
		"interface_id": id,
	})
}

// handleHeMBFaultList lists currently faulted bearer interfaces.
// @Summary List faulted bearers
// @Tags hemb
// @Produce json
// @Success 200 {array} string
// @Router /hemb/fault-inject [get]
func (s *Server) handleHeMBFaultList(w http.ResponseWriter, r *http.Request) {
	if s.dispatcher == nil {
		writeError(w, http.StatusServiceUnavailable, "dispatcher not available")
		return
	}
	fr := s.dispatcher.FailoverResolver()
	if fr == nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	faulted := fr.FaultedInterfaces()
	if faulted == nil {
		faulted = []string{}
	}
	writeJSON(w, http.StatusOK, faulted)
}
