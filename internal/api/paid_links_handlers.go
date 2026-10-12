package api

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/rns"
)

// Paid links [MESHSAT-1547]: a paid interface (satellite, SMS) is never
// flooded; a policy opens it to a short list of Reticulum destinations so
// their announces and path requests can reach the far side.

type paidLinkResponse struct {
	ID     string             `json:"id"`
	Type   string             `json:"type"`
	Online bool               `json:"online"`
	Policy rns.PaidPolicyJSON `json:"policy"`
}

func (s *Server) paidLinkView(id string) paidLinkResponse {
	out := paidLinkResponse{ID: id, Policy: rns.PaidPolicyJSON{Dests: []string{}}}
	if iface := s.ifaceRegistry.Get(id); iface != nil {
		out.Type = string(iface.Type())
		out.Online = iface.IsOnline()
	}
	if p, ok := s.rnsNode.PaidPolicies()[id]; ok {
		out.Policy = p.ToJSON()
	}
	return out
}

// handleGetPaidLinks lists every paid (never flooded) interface with the
// destinations it is open to.
// @Summary List paid links and their Reticulum allow-lists
// @Description Paid interfaces never carry announces or path requests, except for the destinations on their list.
// @Tags routing
// @Produce json
// @Success 200 {array} paidLinkResponse
// @Failure 503 {object} map[string]string
// @Router /api/routing/paid-links [get]
func (s *Server) handleGetPaidLinks(w http.ResponseWriter, r *http.Request) {
	if s.ifaceRegistry == nil || s.rnsNode == nil {
		writeError(w, http.StatusServiceUnavailable, "rns node not running")
		return
	}
	ids := map[string]bool{}
	for _, iface := range s.ifaceRegistry.All() {
		if !iface.IsFloodable() {
			ids[iface.ID()] = true
		}
	}
	for id := range s.rnsNode.PaidPolicies() {
		ids[id] = true
	}
	sorted := make([]string, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Strings(sorted)
	out := make([]paidLinkResponse, 0, len(sorted))
	for _, id := range sorted {
		out = append(out, s.paidLinkView(id))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSetPaidLink replaces the allow-list of one paid interface. An empty
// list closes the link again.
// @Summary Set the Reticulum allow-list of a paid link
// @Description Each listed destination's announces and path requests cross the link at most once per min_interval_sec (default 300). Both ends of the link should list the same destinations.
// @Tags routing
// @Accept json
// @Produce json
// @Param ifaceID path string true "Interface ID (e.g. iridium_imt_0)"
// @Param body body rns.PaidPolicyJSON true "Destinations (32 hex each), interval, optional announce-cap bit rate"
// @Success 200 {object} paidLinkResponse
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/routing/paid-links/{ifaceID} [put]
func (s *Server) handleSetPaidLink(w http.ResponseWriter, r *http.Request) {
	if s.ifaceRegistry == nil || s.rnsNode == nil {
		writeError(w, http.StatusServiceUnavailable, "rns node not running")
		return
	}
	id := chi.URLParam(r, "ifaceID")
	iface := s.ifaceRegistry.Get(id)
	if iface == nil {
		writeError(w, http.StatusNotFound, "interface not found: "+id)
		return
	}
	if iface.IsFloodable() {
		writeError(w, http.StatusBadRequest, id+" is floodable, so it carries every announce already; an allow-list is for paid links")
		return
	}
	var req rns.PaidPolicyJSON
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	p, err := rns.ParsePaidPolicy(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.rnsNode.SetPaidPolicy(id, p)
	if err := s.savePaidPolicies(); err != nil {
		writeError(w, http.StatusInternalServerError, "applied, but not saved: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.paidLinkView(id))
}

// handleDeletePaidLink closes a paid link to every destination again.
// @Summary Remove the Reticulum allow-list of a paid link
// @Tags routing
// @Produce json
// @Param ifaceID path string true "Interface ID (e.g. iridium_imt_0)"
// @Success 200 {object} paidLinkResponse
// @Failure 503 {object} map[string]string
// @Router /api/routing/paid-links/{ifaceID} [delete]
func (s *Server) handleDeletePaidLink(w http.ResponseWriter, r *http.Request) {
	if s.ifaceRegistry == nil || s.rnsNode == nil {
		writeError(w, http.StatusServiceUnavailable, "rns node not running")
		return
	}
	id := chi.URLParam(r, "ifaceID")
	s.rnsNode.SetPaidPolicy(id, nil)
	if err := s.savePaidPolicies(); err != nil {
		writeError(w, http.StatusInternalServerError, "removed, but not saved: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.paidLinkView(id))
}

func (s *Server) savePaidPolicies() error {
	raw, err := s.rnsNode.MarshalPaidPolicies()
	if err != nil {
		return err
	}
	return s.db.SetSystemConfig(rns.PaidPoliciesConfigKey, raw)
}
