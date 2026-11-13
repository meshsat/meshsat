package engine

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// StartRetentionWorker runs every retention rule once at start and then
// daily: the database's rules (database.RetentionRules) and, with signing,
// the audit trail's field-level redaction. One failing rule does not stop
// the others. [MESHSAT-1137]
func StartRetentionWorker(ctx context.Context, db *database.DB, retentionDays int, signing *SigningService) {
	if retentionDays <= 0 {
		log.Info().Msg("retention worker disabled (days <= 0)")
		return
	}

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	// Run once at startup
	prune(db, retentionDays, signing)

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("retention worker stopped")
			return
		case <-ticker.C:
			prune(db, retentionDays, signing)
		}
	}
}

func prune(db *database.DB, days int, signing *SigningService) {
	now := time.Now()
	for _, r := range db.RunRetention(now, days) {
		switch {
		case r.Err != nil:
			log.Error().Err(r.Err).Str("rule", r.Rule).Msg("retention rule failed")
		case r.N > 0:
			log.Info().Str("rule", r.Rule).Int64("rows", r.N).Int("retention_days", days).Msg("retention rule applied")
		}
	}
	if signing != nil {
		if n, err := signing.RedactOlderThan(now.AddDate(0, 0, -days), now); err != nil {
			log.Error().Err(err).Msg("audit trail redaction failed")
		} else if n > 0 {
			log.Info().Int64("rows", n).Int("retention_days", days).Msg("audit trail: personal fields redacted after a signed checkpoint")
		}
	}
}
