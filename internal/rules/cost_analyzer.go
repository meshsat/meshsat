package rules

import (
	"encoding/json"
	"fmt"
	"strings"

	"meshsat/internal/database"
)

// RiskLevel indicates the cost risk of a forwarding rule.
type RiskLevel string

const (
	RiskSafe    RiskLevel = "safe"
	RiskWarning RiskLevel = "warning"
	RiskDanger  RiskLevel = "danger"
)

// RiskAssessment is the result of analyzing a forwarding rule for cost risk.
type RiskAssessment struct {
	Level                RiskLevel `json:"level"`
	Reasons              []string  `json:"reasons"`
	EstimatedMonthlyCost string    `json:"estimated_monthly_cost"`
}

// AnalyzeRule evaluates a forwarding rule for cost risk on paid transports.
// Returns a risk assessment with level, reasons, and estimated monthly cost.
func AnalyzeRule(rule database.ForwardingRule) RiskAssessment {
	assessment := RiskAssessment{
		Level:   RiskSafe,
		Reasons: []string{},
	}

	destType := rule.DestType

	// Only paid transports are risky
	if !isPaidDest(destType) {
		assessment.Reasons = append(assessment.Reasons, "Free transport (MQTT)")
		assessment.EstimatedMonthlyCost = "$0"
		return assessment
	}

	var reasons []string
	estimatedMsgsPerHour := 0.0

	// Check source broadness
	switch rule.SourceType {
	case "any":
		reasons = append(reasons, "Matches all traffic (source_type=any)")
		estimatedMsgsPerHour = 20 // conservative estimate with mixed traffic
	case "channel":
		channels := parseIntSlice(rule.SourceChannels)
		if len(channels) == 0 || containsInt(channels, 0) {
			reasons = append(reasons, "Matches default channel (ch0 catches most traffic)")
			estimatedMsgsPerHour = 15
		} else {
			estimatedMsgsPerHour = 5
		}
	case "node":
		estimatedMsgsPerHour = 3
	case "portnum":
		estimatedMsgsPerHour = 5
	}

	// Check for high-volume portnums on paid transports
	portnums := parseIntSlice(rule.SourcePortnums)
	hasHighVolume := false
	for _, pn := range portnums {
		if pn == 67 || pn == 70 { // TELEMETRY_APP, TRACEROUTE_APP
			hasHighVolume = true
			reasons = append(reasons, fmt.Sprintf("Includes high-volume portnum %d (fires every ~5min/node)", pn))
			estimatedMsgsPerHour += 12 // ~12/hour/node for telemetry
		}
	}

	// source_type=any without source_portnums implicitly includes telemetry
	if rule.SourceType == "any" && rule.SourcePortnums == nil {
		reasons = append(reasons, "No portnum filter — telemetry/traceroute included by default")
		if !hasHighVolume {
			estimatedMsgsPerHour += 12
		}
	}

	// Check rate limits
	hasRateLimit := rule.RateLimitPerMin > 0
	if !hasRateLimit {
		reasons = append(reasons, "No rate limit set")
	} else {
		// Rate limit caps effective messages
		effectiveMax := float64(rule.RateLimitPerMin) * 60
		if effectiveMax < estimatedMsgsPerHour {
			estimatedMsgsPerHour = effectiveMax
		}
	}

	// Calculate estimated monthly cost
	costPerMsg := estimateCostPerMessage(destType)
	monthlyMsgs := estimatedMsgsPerHour * 720 // 30 days
	monthlyCost := monthlyMsgs * costPerMsg

	if hasRateLimit {
		// Capped by rate limit
		cappedMonthly := float64(rule.RateLimitPerMin) * 60 * 720 * costPerMsg
		if cappedMonthly < monthlyCost {
			monthlyCost = cappedMonthly
		}
	}

	assessment.EstimatedMonthlyCost = fmt.Sprintf("$%.0f", monthlyCost)

	// Determine risk level
	if destType == "all" && !hasRateLimit {
		assessment.Level = RiskDanger
		reasons = append(reasons, "Floods all transports including paid ones")
	} else if !hasRateLimit && estimatedMsgsPerHour > 10 {
		assessment.Level = RiskDanger
	} else if hasRateLimit && isPaidDest(destType) {
		assessment.Level = RiskWarning
		reasons = append(reasons, "Rate-limited on paid transport")
	} else if isPaidDest(destType) && estimatedMsgsPerHour > 5 {
		assessment.Level = RiskWarning
	} else if isPaidDest(destType) {
		assessment.Level = RiskWarning
	}

	// Upgrade to danger for specific high-risk combos
	if rule.SourceType == "any" && !hasRateLimit && isPaidDest(destType) {
		assessment.Level = RiskDanger
	}

	assessment.Reasons = reasons
	return assessment
}

func isPaidDest(destType string) bool {
	return destType == "iridium" || destType == "cellular" || destType == "all" || destType == "both"
}

func estimateCostPerMessage(destType string) float64 {
	switch destType {
	case "iridium":
		return 0.05 // ~$0.05/credit, 1 credit per short message
	case "cellular":
		return 0.03 // ~$0.03/SMS in most markets
	case "all", "both":
		return 0.08 // combined
	default:
		return 0
	}
}

func parseIntSlice(s *string) []int {
	if s == nil {
		return nil
	}
	var result []int
	json.Unmarshal([]byte(*s), &result)
	return result
}

func containsInt(slice []int, val int) bool {
	for _, v := range slice {
		if v == val {
			return true
		}
	}
	return false
}

// AnalyzeRuleSet analyzes all rules and returns assessments.
func AnalyzeRuleSet(rules []database.ForwardingRule) map[int]RiskAssessment {
	result := make(map[int]RiskAssessment, len(rules))
	for _, rule := range rules {
		result[rule.ID] = AnalyzeRule(rule)
	}
	return result
}

// HasDangerRules returns true if any rule has danger-level risk.
func HasDangerRules(assessments map[int]RiskAssessment) bool {
	for _, a := range assessments {
		if a.Level == RiskDanger {
			return true
		}
	}
	return false
}

// FormatRiskSummary returns a human-readable summary of risk reasons.
func FormatRiskSummary(a RiskAssessment) string {
	if a.Level == RiskSafe {
		return "Low risk"
	}
	return strings.Join(a.Reasons, "; ")
}
