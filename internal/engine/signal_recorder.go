package engine

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// SignalRecorder subscribes to HAL Iridium SSE events and persists signal bar
// readings to the signal_history table. It also runs a daily pruner to remove
// entries older than 90 days.
type SignalRecorder struct {
	db  *database.DB
	sat transport.SatTransport

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewSignalRecorder creates a new signal recorder.
func NewSignalRecorder(db *database.DB, sat transport.SatTransport) *SignalRecorder {
	return &SignalRecorder{db: db, sat: sat}
}

// Start launches the SSE subscription loop and daily pruner.
func (sr *SignalRecorder) Start(ctx context.Context) {
	ctx, sr.cancel = context.WithCancel(ctx)

	sr.wg.Add(2)
	go sr.subscribeLoop(ctx)
	go sr.dailyPruner(ctx)

	log.Info().Msg("signal recorder started")
}

// Stop cancels the recorder and waits for goroutines to exit.
func (sr *SignalRecorder) Stop() {
	if sr.cancel != nil {
		sr.cancel()
	}
	sr.wg.Wait()
	log.Info().Msg("signal recorder stopped")
}

func (sr *SignalRecorder) subscribeLoop(ctx context.Context) {
	defer sr.wg.Done()
	backoff := time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		start := time.Now()
		err := sr.readSignals(ctx)
		if ctx.Err() != nil {
			return
		}

		// Reset backoff if connection lasted > 10s
		if time.Since(start) > 10*time.Second {
			backoff = time.Second
		}

		if err != nil {
			log.Warn().Err(err).Dur("backoff", backoff).Msg("signal recorder: SSE disconnected, reconnecting")
		}

		// Exponential backoff capped at 2 minutes
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 2*time.Minute {
			backoff = 2 * time.Minute
		}
	}
}

func (sr *SignalRecorder) readSignals(ctx context.Context) error {
	events, err := sr.sat.Subscribe(ctx)
	if err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			// Only record "signal" events that carry bar count
			if ev.Type == "signal" && ev.Signal >= 0 {
				ts := time.Now().Unix()
				if err := sr.db.InsertSignalHistory("iridium", ts, float64(ev.Signal)); err != nil {
					log.Warn().Err(err).Msg("signal recorder: insert failed")
				}
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
