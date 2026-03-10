package rules

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/ratelimit"
)

// AccessFilters is the parsed representation of an access rule's filters JSON.
type AccessFilters struct {
	Keyword  string `json:"keyword"`
	Channels string `json:"channels"` // JSON array string: "[1,2]"
	Nodes    string `json:"nodes"`    // JSON array string: "[\"!abc\"]"
	Portnums string `json:"portnums"` // JSON array string: "[1,67]"
}

// AccessMatchResult holds a matched access rule and resolved forwarding target.
type AccessMatchResult struct {
	Rule      database.AccessRule
	ForwardTo string // resolved interface ID (e.g. "iridium_0")
}

// AccessEvaluator evaluates v0.3.0 access rules for ingress/egress decisions.
// Cisco ASA-style: implicit deny — if no rule matches, the message is dropped.
type AccessEvaluator struct {
	mu     sync.RWMutex
	rules  []database.AccessRule
	rates  map[int64]*ratelimit.TokenBucket
	groups map[string][]string // object group ID → resolved members
	db     *database.DB
}

// NewAccessEvaluator creates a new access rule evaluator.
func NewAccessEvaluator(db *database.DB) *AccessEvaluator {
	return &AccessEvaluator{
		db:     db,
		rates:  make(map[int64]*ratelimit.TokenBucket),
		groups: make(map[string][]string),
	}
}

// ReloadFromDB refreshes access rules and object groups from the database.
func (e *AccessEvaluator) ReloadFromDB() error {
	if e.db == nil {
		return nil
	}

	rules, err := e.db.GetAllAccessRules()
	if err != nil {
		return err
	}

	groups, err := e.db.GetAllObjectGroups()
	if err != nil {
		log.Warn().Err(err).Msg("access-eval: failed to load object groups")
		groups = nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.rules = rules
	e.rates = make(map[int64]*ratelimit.TokenBucket)
	e.groups = make(map[string][]string)

	for _, r := range rules {
		if r.RateLimitPerMin > 0 && r.RateLimitWindow > 0 {
			e.rates[r.ID] = ratelimit.NewRuleLimiter(r.RateLimitPerMin, r.RateLimitWindow)
		}
	}

	for _, g := range groups {
		var members []string
		if err := json.Unmarshal([]byte(g.Members), &members); err == nil {
			e.groups[g.ID] = members
		}
	}

	log.Info().Int("rules", len(rules)).Int("groups", len(e.groups)).Msg("access rules loaded")
	return nil
}

// RuleCount returns the number of loaded access rules.
func (e *AccessEvaluator) RuleCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.rules)
}

// EvaluateIngress evaluates ingress access rules for a message arriving on an interface.
// Returns all matching rules with their forwarding targets.
// If no rules match, the implicit deny drops the message (empty result).
func (e *AccessEvaluator) EvaluateIngress(interfaceID string, msg RouteMessage) []AccessMatchResult {
	return e.evaluate(interfaceID, "ingress", msg)
}

// EvaluateEgress evaluates egress access rules before sending to a destination interface.
// Returns all matching rules. Empty result means implicit deny (drop).
func (e *AccessEvaluator) EvaluateEgress(interfaceID string, msg RouteMessage) []AccessMatchResult {
	return e.evaluate(interfaceID, "egress", msg)
}

func (e *AccessEvaluator) evaluate(interfaceID, direction string, msg RouteMessage) []AccessMatchResult {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var results []AccessMatchResult

	for _, rule := range e.rules {
		if !rule.Enabled {
			continue
		}
		if rule.InterfaceID != interfaceID {
			continue
		}
		if rule.Direction != direction {
			continue
		}

		// Self-loop prevention: don't forward to the same interface
		if direction == "ingress" && rule.ForwardTo == interfaceID {
			continue
		}

		// Evaluate filters
		if !e.matchFilters(rule, msg) {
			continue
		}

		// Object group filters
		if !e.matchObjectGroups(rule, msg) {
			continue
		}

		// Rate limiter
		if limiter, ok := e.rates[rule.ID]; ok {
			if !limiter.Allow() {
				log.Debug().Int64("rule_id", rule.ID).Str("rule", rule.Name).Msg("access rule rate limited")
				continue
			}
		}

		// Action handling
		switch rule.Action {
		case "drop":
			// Explicit drop — stop evaluating further rules
			log.Debug().Int64("rule_id", rule.ID).Str("rule", rule.Name).Msg("access rule: explicit drop")
			go e.recordMatch(rule.ID)
			return nil
		case "log":
			// Log and continue evaluating
			log.Info().Int64("rule_id", rule.ID).Str("rule", rule.Name).
				Str("interface", interfaceID).Str("direction", direction).
				Msg("access rule: log match")
			go e.recordMatch(rule.ID)
			continue
		case "forward":
			go e.recordMatch(rule.ID)
			results = append(results, AccessMatchResult{
				Rule:      rule,
				ForwardTo: rule.ForwardTo,
			})
		}
	}

	return results
}

// matchFilters checks the rule's filters JSON against the message.
func (e *AccessEvaluator) matchFilters(rule database.AccessRule, msg RouteMessage) bool {
	if rule.Filters == "" || rule.Filters == "{}" {
		return true
	}

	var filters AccessFilters
	if err := json.Unmarshal([]byte(rule.Filters), &filters); err != nil {
		return true // malformed filters = permissive
	}

	// Keyword filter
	if filters.Keyword != "" {
		if !strings.Contains(strings.ToLower(msg.Text), strings.ToLower(filters.Keyword)) {
			return false
		}
	}

	// Channel filter
	if filters.Channels != "" && filters.Channels != "[]" {
		var channels []int
		if err := json.Unmarshal([]byte(filters.Channels), &channels); err == nil && len(channels) > 0 {
			found := false
			for _, ch := range channels {
				if ch == msg.Channel {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}

	// Node filter
	if filters.Nodes != "" && filters.Nodes != "[]" {
		var nodes []string
		if err := json.Unmarshal([]byte(filters.Nodes), &nodes); err == nil && len(nodes) > 0 {
			found := false
			for _, n := range nodes {
				if n == msg.From {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}

	// Portnum filter
	if filters.Portnums != "" && filters.Portnums != "[]" {
		var portnums []int
		if err := json.Unmarshal([]byte(filters.Portnums), &portnums); err == nil && len(portnums) > 0 {
			found := false
			for _, pn := range portnums {
				if pn == msg.PortNum {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}

	return true
}

// matchObjectGroups checks object group membership filters.
func (e *AccessEvaluator) matchObjectGroups(rule database.AccessRule, msg RouteMessage) bool {
	// Node group filter
	if rule.FilterNodeGroup != nil && *rule.FilterNodeGroup != "" {
		members, ok := e.groups[*rule.FilterNodeGroup]
		if ok && len(members) > 0 {
			found := false
			for _, m := range members {
				if m == msg.From {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}

	// Sender group filter (same as node but using From field)
	if rule.FilterSenderGroup != nil && *rule.FilterSenderGroup != "" {
		members, ok := e.groups[*rule.FilterSenderGroup]
		if ok && len(members) > 0 {
			found := false
			for _, m := range members {
				if m == msg.From {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}

	// Portnum group filter
	if rule.FilterPortnumGroup != nil && *rule.FilterPortnumGroup != "" {
		members, ok := e.groups[*rule.FilterPortnumGroup]
		if ok && len(members) > 0 {
			found := false
			portStr := strconv.Itoa(msg.PortNum)
			for _, m := range members {
				if m == portStr {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}

	return true
}

func (e *AccessEvaluator) recordMatch(ruleID int64) {
	if e.db == nil {
		return
	}
	if err := e.db.UpdateAccessRuleMatch(ruleID); err != nil {
		log.Warn().Err(err).Int64("rule_id", ruleID).Msg("failed to update access rule match stats")
	}
}
