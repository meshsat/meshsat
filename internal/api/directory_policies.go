package api

import (
	"net/http"

	"meshsat/internal/directory"
	"meshsat/internal/engine"
)

// policyView is a dispatch policy row with what the bridge does with it
// (HeMB-NG r3 D12, stage 0b). [MESHSAT-1584]
type policyView struct {
	ID                string `json:"id"`
	ScopeType         string `json:"scope_type"`
	ScopeID           string `json:"scope_id"`
	RequestedStrategy string `json:"requested_strategy"`
	EffectiveStrategy string `json:"effective_strategy"`
	// StrategyReason is set when the two differ:
	// legacy_compat_all_bearers or unknown_strategy_primary_only.
	StrategyReason string `json:"strategy_reason,omitempty"`
	Supported      bool   `json:"supported"`
	Seeded         bool   `json:"seeded"`
	// Origin is "local", or "hub:<tenant>" for a scope the Hub manages:
	// its row wins at every snapshot (r19 Q4).
	Origin       string `json:"origin"`
	MaxCostCents *int   `json:"max_cost_cents,omitempty"`
	UpdatedAt    string `json:"updated_at"`
}

type policiesResponse struct {
	DeliveryIntents     bool         `json:"delivery_intents"`
	SupportedStrategies []string     `json:"supported_strategies"`
	Policies            []policyView `json:"policies"`
}

// handleListDirectoryPolicies lists the dispatch policies and what the
// bridge does with each.
// @Summary List dispatch policies and how each resolves
// @Description Every dispatch policy row (contact, precedence and default
// @Description scope) with the strategy it holds (requested_strategy) and the
// @Description one a send uses (effective_strategy). A row holding a strategy
// @Description the bridge does not implement is used as ALL_BEARERS
// @Description (strategy_reason legacy_compat_all_bearers) or, for an unknown
// @Description value, PRIMARY_ONLY; such a row is never presented as
// @Description implemented. seeded: the row still holds the value the bridge's
// @Description migrations wrote. supported_strategies depends on
// @Description MESHSAT_DELIVERY_INTENTS (ORDERED_FALLBACK only with it on).
// @Description origin "hub:<tenant>": the Hub manages the scope. Its row
// @Description wins at every snapshot, so a change made on the bridge holds
// @Description only until the next one; when the Hub stops sending the
// @Description scope, a precedence or default scope returns to the bridge's
// @Description own default and a contact scope goes. A seeded Hub row never
// @Description replaces the bridge's own.
// @Tags directory
// @Produce json
// @Param unsupported query string false "1 = only the rows that need attention (strategy not implemented)"
// @Success 200 {object} policiesResponse
// @Failure 500 {object} map[string]string
// @Router /api/directory/policies [get]
func (s *Server) handleListDirectoryPolicies(w http.ResponseWriter, r *http.Request) {
	intents := s.dispatcher != nil && s.dispatcher.DeliveryIntents()
	rows, err := directory.NewSQLStore(s.db.DB).ListPolicies(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	onlyUnsupported := r.URL.Query().Get("unsupported") == "1"
	out := policiesResponse{DeliveryIntents: intents, Policies: []policyView{}}
	for _, st := range engine.SupportedStrategies(intents) {
		out.SupportedStrategies = append(out.SupportedStrategies, string(st))
	}
	for _, p := range rows {
		eff, reason := engine.StoredStrategy(p.Strategy, intents)
		if onlyUnsupported && reason == "" {
			continue
		}
		out.Policies = append(out.Policies, policyView{
			ID: p.ID, ScopeType: string(p.ScopeType), ScopeID: p.ScopeID,
			RequestedStrategy: string(p.Strategy), EffectiveStrategy: string(eff), StrategyReason: reason,
			Supported: reason == "", Seeded: p.Seeded, Origin: p.Origin, MaxCostCents: p.MaxCostCents, UpdatedAt: p.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
