package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/database"
	"meshsat/internal/hemb"
)

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

func (s *Server) handleDeleteBondGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.db.DeleteBondGroup(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetHeMBStats(w http.ResponseWriter, r *http.Request) {
	st := hemb.Global.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"active_streams":      st.ActiveStreams,
		"symbols_sent":        st.SymbolsSent,
		"symbols_received":    st.SymbolsReceived,
		"generations_decoded": st.GenerationsDecoded,
		"generations_failed":  st.GenerationsFailed,
		"bytes_free":          st.BytesFree,
		"bytes_paid":          st.BytesPaid,
		"cost_incurred":       st.CostIncurred,
	})
}
