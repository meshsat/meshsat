package rules

import (
	"fmt"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// MigrateCompoundDestTypes splits rules with dest_type "both" or "all" into
// separate per-channel rules. Idempotent — checks for existing split rules.
func MigrateCompoundDestTypes(db *database.DB) error {
	rules, err := db.GetForwardingRules()
	if err != nil {
		return fmt.Errorf("load rules for migration: %w", err)
	}

	for _, rule := range rules {
		switch rule.DestType {
		case "both":
			// Split into iridium + mqtt
			if err := splitRule(db, rule, []string{"iridium", "mqtt"}); err != nil {
				return err
			}
		case "all":
			// Split into all known gateway types
			if err := splitRule(db, rule, []string{"iridium", "mqtt", "cellular", "webhook", "astrocast"}); err != nil {
				return err
			}
		}
	}
	return nil
}

func splitRule(db *database.DB, original database.ForwardingRule, destTypes []string) error {
	// Check if split rules already exist (idempotent)
	allRules, _ := db.GetForwardingRules()
	for _, dest := range destTypes {
		exists := false
		for _, r := range allRules {
			if r.Name == original.Name+" ["+dest+"]" {
				exists = true
				break
			}
		}
		if exists {
			continue
		}

		// Create new rule with same filters but specific dest_type
		newRule := original
		newRule.ID = 0
		newRule.Name = original.Name + " [" + dest + "]"
		newRule.DestType = dest

		if _, err := db.InsertForwardingRule(&newRule); err != nil {
			return fmt.Errorf("create split rule %s for %s: %w", dest, original.Name, err)
		}
		log.Info().Str("original", original.Name).Str("dest", dest).Msg("split compound rule")
	}

	// Disable original compound rule (don't delete — preserve for rollback)
	if err := db.SetForwardingRuleEnabled(original.ID, false); err != nil {
		return fmt.Errorf("disable compound rule %d: %w", original.ID, err)
	}
	return nil
}
