package rules

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/ratelimit"
)

// RouteMessage is a transport-agnostic message envelope for unified rule evaluation.
type RouteMessage struct {
	Text    string // message text
	From    string // source identifier (node ID, phone number, etc.)
	To      string // optional target
	Channel int    // mesh channel (0 if non-mesh)
	PortNum int    // portnum (1=text, 67=telemetry, etc.)
	RawData []byte // original payload
}

// MatchResult contains the matched rule and resolved destination details.
type MatchResult struct {
	Rule database.ForwardingRule
}

// Engine evaluates forwarding rules against incoming mesh messages.
// First-match semantics: the first matching rule wins.
type Engine struct {
	mu    sync.RWMutex
	rules []database.ForwardingRule
	rates map[int]*ratelimit.TokenBucket // per-rule rate limiters
	db    *database.DB
}

// NewEngine creates a new rule engine.
func NewEngine(db *database.DB) *Engine {
	return &Engine{
		db:    db,
		rates: make(map[int]*ratelimit.TokenBucket),
	}
}

// ReloadFromDB refreshes rules from the database and rebuilds rate limiters.
func (e *Engine) ReloadFromDB() error {
	if e.db == nil {
		return nil
	}

	rules, err := e.db.GetForwardingRules()
	if err != nil {
		return fmt.Errorf("load forwarding rules: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.rules = rules
	e.rates = make(map[int]*ratelimit.TokenBucket)

	for _, r := range rules {
		if r.RateLimitPerMin > 0 {
			e.rates[r.ID] = ratelimit.NewRuleLimiter(r.RateLimitPerMin, r.RateLimitWindow)
		}
	}

	log.Info().Int("count", len(rules)).Msg("forwarding rules loaded")
	return nil
}

// RuleCount returns the number of loaded rules.
func (e *Engine) RuleCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.rules)
}

// EvaluateRoute returns ALL matching rules for a message from a given source channel.
// Multi-match: a message can match multiple rules with different destinations.
// Self-loop prevention: rules where dest_type == source are skipped.
func (e *Engine) EvaluateRoute(source string, msg RouteMessage) []MatchResult {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var results []MatchResult

	for _, rule := range e.rules {
		if !rule.Enabled {
			continue
		}

		// Self-loop prevention
		if rule.DestType == source {
			continue
		}

		// Match source
		if !matchRouteSource(rule, source, msg) {
			continue
		}

		// Keyword filter
		if !MatchesKeyword(rule, msg.Text) {
			continue
		}

		// Rate limiter
		if limiter, ok := e.rates[rule.ID]; ok {
			if !limiter.Allow() {
				log.Debug().Int("rule_id", rule.ID).Str("rule", rule.Name).Msg("rule rate limited")
				continue
			}
		}

		go e.recordMatch(rule.ID)
		results = append(results, MatchResult{Rule: rule})
	}

	return results
}

// matchRouteSource checks if a rule's source_type matches the given source channel and message.
func matchRouteSource(rule database.ForwardingRule, source string, msg RouteMessage) bool {
	switch rule.SourceType {
	case "any":
		return matchRoutePortnums(rule, msg.PortNum)

	case "mesh":
		if source != "mesh" {
			return false
		}
		return matchRouteMeshFilters(rule, msg) && matchRoutePortnums(rule, msg.PortNum)

	case "channel":
		// Legacy: mesh channel filter
		if source != "mesh" {
			return false
		}
		if rule.SourceChannels != nil {
			var channels []int
			if err := json.Unmarshal([]byte(*rule.SourceChannels), &channels); err == nil {
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
		return matchRoutePortnums(rule, msg.PortNum)

	case "node":
		// Legacy: mesh node filter
		if source != "mesh" {
			return false
		}
		if rule.SourceNodes != nil {
			var nodes []string
			if err := json.Unmarshal([]byte(*rule.SourceNodes), &nodes); err == nil {
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
		return matchRoutePortnums(rule, msg.PortNum)

	case "portnum":
		if source != "mesh" {
			return false
		}
		return matchRoutePortnums(rule, msg.PortNum)

	case "iridium":
		return source == "iridium"
	case "astrocast":
		return source == "astrocast"
	case "cellular":
		return source == "cellular"
	case "webhook":
		return source == "webhook"
	case "mqtt":
		return source == "mqtt"
	case "external":
		return source != "mesh"

	default:
		return false
	}
}

func matchRouteMeshFilters(rule database.ForwardingRule, msg RouteMessage) bool {
	// Channel filter
	if rule.SourceChannels != nil {
		var channels []int
		if err := json.Unmarshal([]byte(*rule.SourceChannels), &channels); err == nil {
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
	if rule.SourceNodes != nil {
		var nodes []string
		if err := json.Unmarshal([]byte(*rule.SourceNodes), &nodes); err == nil {
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
	return true
}

func matchRoutePortnums(rule database.ForwardingRule, portNum int) bool {
	if rule.SourcePortnums == nil {
		return true
	}
	var portnums []int
	if err := json.Unmarshal([]byte(*rule.SourcePortnums), &portnums); err != nil {
		return false
	}
	for _, pn := range portnums {
		if pn == portNum {
			return true
		}
	}
	return false
}

func (e *Engine) recordMatch(ruleID int) {
	if e.db == nil {
		return
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	if err := e.db.UpdateRuleMatch(ruleID, now); err != nil {
		log.Warn().Err(err).Int("rule_id", ruleID).Msg("failed to update rule match stats")
	}
}

// MatchesKeyword checks if the message text contains the rule's keyword (case-insensitive).
// This is called after source matching for additional filtering.
func MatchesKeyword(rule database.ForwardingRule, text string) bool {
	if rule.SourceKeyword == nil || *rule.SourceKeyword == "" {
		return true
	}
	return strings.Contains(strings.ToLower(text), strings.ToLower(*rule.SourceKeyword))
}
