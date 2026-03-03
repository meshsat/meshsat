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
	"meshsat/internal/transport"
)

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

// Evaluate returns the first matching rule for a given mesh message.
// Returns nil if no rule matches.
func (e *Engine) Evaluate(msg *transport.MeshMessage) *MatchResult {
	e.mu.RLock()
	defer e.mu.RUnlock()

	fromNode := fmt.Sprintf("!%08x", msg.From)

	for _, rule := range e.rules {
		if !rule.Enabled {
			continue
		}

		if !matchSource(rule, msg, fromNode) {
			continue
		}

		// Check rate limiter
		if limiter, ok := e.rates[rule.ID]; ok {
			if !limiter.Allow() {
				log.Debug().Int("rule_id", rule.ID).Str("rule", rule.Name).Msg("rule rate limited")
				continue
			}
		}

		// Update match stats (async, don't block evaluation)
		go e.recordMatch(rule.ID)

		return &MatchResult{Rule: rule}
	}

	return nil
}

func matchSource(rule database.ForwardingRule, msg *transport.MeshMessage, fromNode string) bool {
	switch rule.SourceType {
	case "any":
		return true

	case "channel":
		if rule.SourceChannels == nil {
			return true
		}
		var channels []int
		if err := json.Unmarshal([]byte(*rule.SourceChannels), &channels); err != nil {
			return false
		}
		for _, ch := range channels {
			if uint32(ch) == msg.Channel {
				return true
			}
		}
		return false

	case "node":
		if rule.SourceNodes == nil {
			return true
		}
		var nodes []string
		if err := json.Unmarshal([]byte(*rule.SourceNodes), &nodes); err != nil {
			return false
		}
		for _, n := range nodes {
			if n == fromNode {
				return true
			}
		}
		return false

	case "portnum":
		if rule.SourcePortnums == nil {
			return true
		}
		var portnums []int
		if err := json.Unmarshal([]byte(*rule.SourcePortnums), &portnums); err != nil {
			return false
		}
		for _, pn := range portnums {
			if pn == msg.PortNum {
				return true
			}
		}
		return false

	default:
		return false
	}
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

// EvaluateInbound returns the first matching inbound rule for a gateway message.
// Inbound rules have source_type = iridium/mqtt/external and dest_type = mesh.
// Returns nil if no rule matches.
func (e *Engine) EvaluateInbound(source string, text string) *MatchResult {
	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, rule := range e.rules {
		if !rule.Enabled {
			continue
		}
		if !matchInboundSource(rule, source) {
			continue
		}
		if !MatchesKeyword(rule, text) {
			continue
		}
		// Check rate limiter
		if limiter, ok := e.rates[rule.ID]; ok {
			if !limiter.Allow() {
				continue
			}
		}
		go e.recordMatch(rule.ID)
		return &MatchResult{Rule: rule}
	}
	return nil
}

func matchInboundSource(rule database.ForwardingRule, source string) bool {
	switch rule.SourceType {
	case "iridium":
		return source == "iridium"
	case "mqtt":
		return source == "mqtt"
	case "external":
		return source == "iridium" || source == "mqtt"
	default:
		return false
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
