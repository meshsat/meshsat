package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/database"
	"meshsat/internal/rules"
)

// ruleWithRisk wraps a forwarding rule with its cost risk assessment.
type ruleWithRisk struct {
	database.ForwardingRule
	Risk *rules.RiskAssessment `json:"risk,omitempty"`
}

func (s *Server) handleGetRules(w http.ResponseWriter, r *http.Request) {
	ruleList, err := s.db.GetForwardingRules()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Attach risk assessments
	result := make([]ruleWithRisk, len(ruleList))
	for i, rule := range ruleList {
		assessment := rules.AnalyzeRule(rule)
		result[i] = ruleWithRisk{
			ForwardingRule: rule,
			Risk:           &assessment,
		}
	}
	writeJSON(w, http.StatusOK, result)
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
	if rule.DestType != "iridium" && rule.DestType != "mqtt" && rule.DestType != "both" && rule.DestType != "mesh" && rule.DestType != "cellular" && rule.DestType != "all" {
		writeError(w, http.StatusBadRequest, "dest_type must be iridium, mqtt, cellular, both, all, or mesh")
		return
	}
	if rule.SourceType == "" {
		rule.SourceType = "any"
	}
	// Validate inbound rule constraints
	if rule.DestType == "mesh" {
		if rule.SourceType != "iridium" && rule.SourceType != "mqtt" && rule.SourceType != "cellular" && rule.SourceType != "external" {
			writeError(w, http.StatusBadRequest, "inbound rules require source_type: iridium, mqtt, cellular, or external")
			return
		}
		if rule.DestChannel < 0 || rule.DestChannel > 7 {
			writeError(w, http.StatusBadRequest, "dest_channel must be 0-7")
			return
		}
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
	assessment := rules.AnalyzeRule(rule)
	writeJSON(w, http.StatusCreated, ruleWithRisk{
		ForwardingRule: rule,
		Risk:           &assessment,
	})
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

	assessment := rules.AnalyzeRule(rule)
	writeJSON(w, http.StatusOK, ruleWithRisk{
		ForwardingRule: rule,
		Risk:           &assessment,
	})
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
