package engine

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// SignalProvider abstracts signal reading so the recorder can use either a direct
// transport or the gateway manager's auto-selecting method.
type SignalProvider interface {
	GetSignalFast(ctx context.Context) (*transport.SignalInfo, error)
}

// SignalRecorder polls satellite transports for signal readings and persists
// them to the signal_history table. Uses GetSignalFast (non-blocking cached read)
// so it works alongside the gateway's Subscribe without conflicting goroutines.
// Supports multiple independent providers (e.g., SBD and IMT simultaneously).
// Also runs a daily pruner to remove entries older than 90 days.
type SignalRecorder struct {
	db        *database.DB
	providers []namedProvider

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// namedProvider pairs a signal provider with a fallback source name.
type namedProvider struct {
	provider     SignalProvider
	fallbackName string // used if provider doesn't set Source on SignalInfo
}

// NewSignalRecorder creates a new signal recorder with a single provider (backward compat).
func NewSignalRecorder(db *database.DB, provider SignalProvider) *SignalRecorder {
	return &SignalRecorder{
		db: db,
		providers: []namedProvider{
			{provider: provider, fallbackName: "iridium"},
		},
	}
}

// AddProvider registers an additional signal provider for independent polling.
func (sr *SignalRecorder) AddProvider(provider SignalProvider, fallbackName string) {
	sr.providers = append(sr.providers, namedProvider{provider: provider, fallbackName: fallbackName})
}

// Start launches the signal polling loop and daily pruner.
func (sr *SignalRecorder) Start(ctx context.Context) {
	ctx, sr.cancel = context.WithCancel(ctx)

	// One poll goroutine per provider
	sr.wg.Add(len(sr.providers) + 1)
	for _, np := range sr.providers {
		go sr.pollLoop(ctx, np)
	}
	go sr.dailyPruner(ctx)

	log.Info().Int("providers", len(sr.providers)).Msg("signal recorder started")
}

// Stop cancels the recorder and waits for goroutines to exit.
func (sr *SignalRecorder) Stop() {
	if sr.cancel != nil {
		sr.cancel()
	}
	sr.wg.Wait()
	log.Info().Msg("signal recorder stopped")
}

// pollLoop periodically reads the cached signal from a transport and records
// it to the database. Uses GetSignalFast which returns the last polled value
// without issuing serial commands — safe to call regardless of transport state.
func (sr *SignalRecorder) pollLoop(ctx context.Context, np namedProvider) {
	defer sr.wg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	var lastBars int = -1

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sig, err := np.provider.GetSignalFast(ctx)
			if err != nil || sig == nil {
				continue // transport not connected yet
			}
			// Only record when we have a valid reading (timestamp set)
			if sig.Timestamp == "" {
				continue
			}
			// Use the source key from the transport ("sbd" or "imt").
			// HAL transport doesn't set source — fall back to provider's fallback name.
			source := sig.Source
			if source == "" {
				source = np.fallbackName
			}
			// Record every reading (even if same as last) for history chart continuity
			ts := time.Now().Unix()
			if err := sr.db.InsertSignalHistory(source, ts, float64(sig.Bars)); err != nil {
				log.Warn().Err(err).Str("source", source).Msg("signal recorder: insert failed")
				continue
			}
			if sig.Bars != lastBars {
				log.Debug().Int("bars", sig.Bars).Str("source", source).Msg("signal recorder: recorded")
				lastBars = sig.Bars
			}
		}
	}
}

func (sr *SignalRecorder) dailyPruner(ctx context.Context) {
	defer sr.wg.Done()

	// Initial prune on startup
	if n, err := sr.db.PruneSignalHistory(90); err != nil {
		log.Warn().Err(err).Msg("signal recorder: initial prune failed")
	} else if n > 0 {
		log.Info().Int64("deleted", n).Msg("signal recorder: pruned old entries")
	}

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := sr.db.PruneSignalHistory(90); err != nil {
				log.Warn().Err(err).Msg("signal recorder: prune failed")
			} else if n > 0 {
				log.Info().Int64("deleted", n).Msg("signal recorder: pruned old entries")
			}
		}
	}
}
