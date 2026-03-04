package engine

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// CellSignalRecorder subscribes to cellular modem events and persists signal
// readings to the cellular_signal_history table.
type CellSignalRecorder struct {
	db   *database.DB
	cell transport.CellTransport

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewCellSignalRecorder creates a new cellular signal recorder.
func NewCellSignalRecorder(db *database.DB, cell transport.CellTransport) *CellSignalRecorder {
	return &CellSignalRecorder{db: db, cell: cell}
}

// Start launches the SSE subscription loop.
func (r *CellSignalRecorder) Start(ctx context.Context) {
	ctx, r.cancel = context.WithCancel(ctx)

	r.wg.Add(1)
	go r.subscribeLoop(ctx)

	log.Info().Msg("cellular signal recorder started")
}

// Stop cancels the recorder and waits for goroutines to exit.
func (r *CellSignalRecorder) Stop() {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
	log.Info().Msg("cellular signal recorder stopped")
}

func (r *CellSignalRecorder) subscribeLoop(ctx context.Context) {
	defer r.wg.Done()
	backoff := time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		start := time.Now()
		err := r.readSignals(ctx)
		if ctx.Err() != nil {
			return
		}

		if time.Since(start) > 10*time.Second {
			backoff = time.Second
		}

		if err != nil {
			log.Warn().Err(err).Dur("backoff", backoff).Msg("cellular signal recorder: disconnected, reconnecting")
		}

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

func (r *CellSignalRecorder) readSignals(ctx context.Context) error {
	events, err := r.cell.Subscribe(ctx)
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
			if ev.Type == "signal" && ev.Signal >= 0 {
				ts := time.Now().Unix()
				if err := r.db.InsertCellularSignal(ts, ev.Signal, -113+(2*ev.Signal), "LTE", ""); err != nil {
					log.Warn().Err(err).Msg("cellular signal recorder: insert failed")
				}
			}
		}
	}
}
