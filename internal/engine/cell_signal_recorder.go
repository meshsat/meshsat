package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// CellSignalRecorder subscribes to cellular modem events and persists all
// transport-level data to the database: signal readings, SMS messages,
// cell broadcast alerts, and cell tower info.
// This runs independently of the CellularGateway — events are persisted
// regardless of whether a gateway is configured.
type CellSignalRecorder struct {
	db   *database.DB
	cell transport.CellTransport
	proc *Processor // optional: forward events to SSE stream

	// Track latest network technology and operator from cell_info events
	// so signal insertions use actual values instead of hardcoded "LTE".
	techMu   sync.RWMutex
	lastTech string
	lastOp   string

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewCellSignalRecorder creates a new cellular signal recorder.
func NewCellSignalRecorder(db *database.DB, cell transport.CellTransport) *CellSignalRecorder {
	return &CellSignalRecorder{db: db, cell: cell, lastTech: "LTE"}
}

// SetProcessor sets the event processor for SSE forwarding.
func (r *CellSignalRecorder) SetProcessor(p *Processor) {
	r.proc = p
}

// Start launches the SSE subscription loop.
func (r *CellSignalRecorder) Start(ctx context.Context) {
	ctx, r.cancel = context.WithCancel(ctx)

	r.wg.Add(1)
	go r.subscribeLoop(ctx)

	log.Info().Msg("cellular event recorder started")
}

// Stop cancels the recorder and waits for goroutines to exit.
func (r *CellSignalRecorder) Stop() {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
	log.Info().Msg("cellular event recorder stopped")
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
		err := r.readEvents(ctx)
		if ctx.Err() != nil {
			return
		}

		if time.Since(start) > 10*time.Second {
			backoff = time.Second
		}

		if err != nil {
			log.Warn().Err(err).Dur("backoff", backoff).Msg("cellular event recorder: disconnected, reconnecting")
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

func (r *CellSignalRecorder) readEvents(ctx context.Context) error {
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
			switch ev.Type {
			case "signal":
				r.handleSignal(ev)
			case "sms_received":
				r.handleSMSReceived(ev)
			case "cbs_received":
				r.handleCBSReceived(ev)
			case "cell_info_update":
				r.handleCellInfoUpdate(ev)
			}
		}
	}
}

// emitSSE forwards a cellular event to the web UI via the processor's SSE stream.
func (r *CellSignalRecorder) emitSSE(evType, message string, data json.RawMessage) {
	if r.proc == nil {
		return
	}
	r.proc.Emit(transport.MeshEvent{
		Type:    evType,
		Message: message,
		Data:    data,
		Time:    time.Now().UTC().Format(time.RFC3339),
	})
}

func (r *CellSignalRecorder) handleSignal(ev transport.CellEvent) {
	if ev.Signal < 0 {
		return
	}

	ts := time.Now().Unix()
	dBm := -113
	switch ev.Signal {
	case 1:
		dBm = -111
	case 2:
		dBm = -103
	case 3:
		dBm = -93
	case 4:
		dBm = -83
	case 5:
		dBm = -73
	}

	r.techMu.RLock()
	tech := r.lastTech
	op := r.lastOp
	r.techMu.RUnlock()

	if err := r.db.InsertCellularSignal(ts, ev.Signal, dBm, tech, op); err != nil {
		log.Warn().Err(err).Msg("cellular event recorder: signal insert failed")
	}

	r.emitSSE("cellular", fmt.Sprintf("Signal: %d/5 bars (%d dBm) %s %s", ev.Signal, dBm, tech, op), nil)
}

func (r *CellSignalRecorder) handleSMSReceived(ev transport.CellEvent) {
	sender := ""
	if ev.Data != nil {
		var sms transport.SMSMessage
		if err := json.Unmarshal(ev.Data, &sms); err == nil {
			sender = sms.Sender
		}
	}

	// Dedup: skip if identical SMS (same sender+text) was inserted in the last 60 seconds.
	// Modems can re-send +CMTI if AT+CMGD fails or the URC is retransmitted.
	if dup, _ := r.db.IsDuplicateSMS(sender, ev.Message, 60); dup {
		log.Info().Str("sender", sender).Msg("cellular: duplicate SMS suppressed")
		return
	}

	if _, err := r.db.InsertSMSMessage("rx", sender, ev.Message, "delivered", time.Now().Unix()); err != nil {
		log.Warn().Err(err).Msg("cellular event recorder: SMS insert failed")
	}
	log.Info().Str("sender", sender).Msg("cellular: inbound SMS persisted")
	r.emitSSE("cellular", fmt.Sprintf("SMS received from %s: %s", sender, truncateStr(ev.Message, 60)), ev.Data)
}

func (r *CellSignalRecorder) handleCBSReceived(ev transport.CellEvent) {
	if ev.Data == nil {
		return
	}
	var cbs transport.CellBroadcastMsg
	if err := json.Unmarshal(ev.Data, &cbs); err != nil {
		return
	}
	if _, err := r.db.InsertCellBroadcast(cbs.SerialNumber, cbs.MessageID, cbs.Channel, cbs.Severity, cbs.Text, time.Now().Unix()); err != nil {
		log.Warn().Err(err).Msg("cellular event recorder: CBS insert failed")
	}
	log.Info().Int("mid", cbs.MessageID).Str("severity", cbs.Severity).Msg("cellular: CBS alert persisted")
	r.emitSSE("cellular", fmt.Sprintf("CBS alert [%s]: %s", cbs.Severity, truncateStr(cbs.Text, 80)), ev.Data)
}

func (r *CellSignalRecorder) handleCellInfoUpdate(ev transport.CellEvent) {
	if ev.Data == nil {
		return
	}
	var ci transport.CellInfo
	if err := json.Unmarshal(ev.Data, &ci); err != nil {
		return
	}
	if err := r.db.InsertCellInfo(ci.MCC, ci.MNC, ci.LAC, ci.CellID, ci.NetworkType, ci.RSRP, ci.RSRQ, time.Now().Unix()); err != nil {
		log.Warn().Err(err).Msg("cellular event recorder: cell info insert failed")
	}

	// Update tracked technology and operator for signal insertions
	if ci.NetworkType != "" {
		r.techMu.Lock()
		r.lastTech = ci.NetworkType
		if ci.MCC != "" && ci.MNC != "" {
			r.lastOp = ci.MCC + ci.MNC
		}
		r.techMu.Unlock()
	}

	r.emitSSE("cellular", fmt.Sprintf("Cell info: %s/%s LAC=%s CID=%s %s", ci.MCC, ci.MNC, ci.LAC, ci.CellID, ci.NetworkType), ev.Data)
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
