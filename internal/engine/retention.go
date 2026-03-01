package engine

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// StartRetentionWorker runs a daily pruning job that deletes records older than retentionDays.
func StartRetentionWorker(ctx context.Context, db *database.DB, retentionDays int) {
	if retentionDays <= 0 {
		log.Info().Msg("retention worker disabled (days <= 0)")
		return
	}

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	// Run once at startup
	prune(db, retentionDays)

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("retention worker stopped")
			return
		case <-ticker.C:
			prune(db, retentionDays)
		}
	}
}

func prune(db *database.DB, days int) {
	deleted, err := db.PruneOlderThan(days)
	if err != nil {
		log.Error().Err(err).Msg("retention prune failed")
		return
	}
	if deleted > 0 {
		log.Info().Int64("deleted", deleted).Int("retention_days", days).Msg("retention prune complete")
	}

	// Prune sent/expired dead letters (same retention window)
	dlqDeleted, err := db.PruneDeadLetters(days)
	if err != nil {
		log.Error().Err(err).Msg("dlq prune failed")
		return
	}
	if dlqDeleted > 0 {
		log.Info().Int64("deleted", dlqDeleted).Msg("dlq prune complete")
	}
}
