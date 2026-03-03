package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/database"
)

func (s *Server) handleGetRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.db.GetForwardingRules()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

func (s *Server) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	var rule database.ForwardingRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if rule.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if rule.DestType == "" {
		writeError(w, http.StatusBadRequest, "dest_type is required")
		return
	}
	if rule.DestType != "iridium" && rule.DestType != "mqtt" && rule.DestType != "both" {
		writeError(w, http.StatusBadRequest, "dest_type must be iridium, mqtt, or both")
		return
	}
	if rule.SourceType == "" {
		rule.SourceType = "any"
	}

	id, err := s.db.InsertForwardingRule(&rule)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Reload rules in engine
	if s.ruleEngine != nil {
		s.ruleEngine.ReloadFromDB()
	}

	rule.ID = int(id)
	writeJSON(w, http.StatusCreated, rule)
}

func (s *Server) handleGetRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule ID")
		return
	}

	rule, err := s.db.GetForwardingRule(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

func (s *Server) handleUpdateRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule ID")
		return
	}

	var rule database.ForwardingRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	rule.ID = id

	if err := s.db.UpdateForwardingRule(&rule); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if s.ruleEngine != nil {
		s.ruleEngine.ReloadFromDB()
	}

	writeJSON(w, http.StatusOK, rule)
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule ID")
		return
	}

	if err := s.db.DeleteForwardingRule(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if s.ruleEngine != nil {
		s.ruleEngine.ReloadFromDB()
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleEnableRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule ID")
		return
	}
	if err := s.db.SetForwardingRuleEnabled(id, true); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.ruleEngine != nil {
		s.ruleEngine.ReloadFromDB()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": true})
}

func (s *Server) handleDisableRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule ID")
		return
	}
	if err := s.db.SetForwardingRuleEnabled(id, false); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.ruleEngine != nil {
		s.ruleEngine.ReloadFromDB()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": false})
}

func (s *Server) handleReorderRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RuleIDs []int `json:"rule_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if len(req.RuleIDs) == 0 {
		writeError(w, http.StatusBadRequest, "rule_ids is required")
		return
	}

	if err := s.db.ReorderForwardingRules(req.RuleIDs); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if s.ruleEngine != nil {
		s.ruleEngine.ReloadFromDB()
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "reordered"})
}

func (s *Server) handleGetRuleStats(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule ID")
		return
	}

	matchCount, lastMatch, monthlyCost, err := s.db.GetRuleStats(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"match_count":            matchCount,
		"last_match":             lastMatch,
		"estimated_monthly_cost": monthlyCost,
	})
}
