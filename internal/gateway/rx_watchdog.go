package gateway

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ReceiveHealth is what the Direwolf supervisor knows about the receive side
// of the APRS chain: the audio level Direwolf reports every few seconds with
// -a (present even when nothing decodes), the last decoded frame, and the
// audio device error count. Level is -1 until the first report. [MESHSAT-814]
type ReceiveHealth struct {
	Running         bool
	Level           int
	LevelAt         time.Time
	LastDecodeAt    time.Time
	LastDecodeLevel int
	AudioErrors     int64
	RxFrames        int64
}

// Receive states shown on the gateway status.
const (
	ReceiveStateUnknown = "unknown" // external Direwolf, or no data yet
	ReceiveStateOK      = "ok"      // a frame decoded within the silence window
	ReceiveStateQuiet   = "quiet"   // nothing decoded, but nothing was expected either
	ReceiveStateDeaf    = "deaf"    // the channel was alive and went silent; recovery running
)

// RxWatchdogConfig tunes the APRS receive watchdog.
type RxWatchdogConfig struct {
	// Silence is how long the receiver may decode nothing, after having
	// decoded something within HeardWithin, before it is declared deaf.
	Silence time.Duration
	// HeardWithin bounds the expectation: a channel that produced no frame
	// for this long is treated as quiet, not deaf, so a kit alone in the
	// field never power-cycles its AIOC every few minutes.
	HeardWithin time.Duration
	// StatsStale: Direwolf running but no audio report for this long means
	// the process is hung; that is treated as deaf immediately.
	StatsStale time.Duration
	// Tick is the evaluation period.
	Tick time.Duration
	// BridgeCooldown is the minimum spacing between bridge restarts.
	BridgeCooldown time.Duration
	// LastHeard seeds the expectation across a bridge restart: a receiver
	// that heard the peer before a deploy, or before the ladder's own
	// bridge restart, is still expected to hear it afterwards, so the
	// failover and the ladder work from the first tick instead of waiting
	// for a frame that a deaf receiver never delivers. Parallax stayed
	// deaf for 28 minutes after the 5 Sep 2026 19:38Z deploy in state
	// "quiet" because the new process had never heard anything.
	LastHeard time.Time
	// LastBridgeRestart seeds the bridge-restart cooldown the same way, so
	// the last step of the ladder cannot loop through restarts.
	LastBridgeRestart time.Time
}

// RxWatchdogActions are the recovery steps, wired by main.go to the same
// paths the OOB executor uses: level 1 restarts the APRS gateway (Direwolf
// respawn), level 3 cuts the AIOC's hub port through the host agent, and
// the last resort restarts the bridge.
type RxWatchdogActions struct {
	Probe          func() (ReceiveHealth, bool)
	RestartGateway func(ctx context.Context) error
	PowerCycle     func(ctx context.Context) error
	RestartBridge  func()
	SetState       func(state string)
	Emit           EventEmitFunc
	Audit          func(detail string)
	// Persist stores the last-heard and last-bridge-restart times for the
	// next process to seed LastHeard and LastBridgeRestart from. Called at
	// most once a minute while frames arrive, and once, synchronously,
	// before a bridge restart.
	Persist func(lastHeard, bridgeRestartAt time.Time)
}

// RxWatchdog notices when the APRS receiver goes silent while the channel
// was known to be alive and escalates through the recovery ladder. It
// exists because on 5 Sep 2026 parallax decoded nothing for 39 minutes
// with transmit working, through a Direwolf restart and an AIOC power
// cycle, and nobody knew until an operator looked. [MESHSAT-814]
type RxWatchdog struct {
	cfg RxWatchdogConfig
	act RxWatchdogActions
	now func() time.Time

	mu              sync.Mutex
	state           string
	deaf            bool
	lastFrames      int64
	lastHeardAt     time.Time
	step            int
	stepAt          time.Time
	bridgeRestartAt time.Time
	persistedAt     time.Time
}

// NewRxWatchdog applies defaults to zero fields.
func NewRxWatchdog(cfg RxWatchdogConfig, act RxWatchdogActions) *RxWatchdog {
	if cfg.Silence <= 0 {
		cfg.Silence = 5 * time.Minute
	}
	if cfg.HeardWithin <= 0 {
		cfg.HeardWithin = 2 * time.Hour
	}
	if cfg.StatsStale <= 0 {
		cfg.StatsStale = 90 * time.Second
	}
	if cfg.Tick <= 0 {
		cfg.Tick = 30 * time.Second
	}
	if cfg.BridgeCooldown <= 0 {
		cfg.BridgeCooldown = time.Hour
	}
	return &RxWatchdog{
		cfg: cfg, act: act, now: time.Now, state: ReceiveStateUnknown,
		lastHeardAt: cfg.LastHeard, bridgeRestartAt: cfg.LastBridgeRestart,
	}
}

// Run evaluates on every tick until ctx ends.
func (w *RxWatchdog) Run(ctx context.Context) {
	t := time.NewTicker(w.cfg.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.tick(ctx)
		}
	}
}

// ReceiveDeaf implements engine.ReceiveChecker: a deaf APRS receiver zeroes
// the health of both the APRS gateway interface and the AX.25 Reticulum
// interface that rides on the same Direwolf, so the dispatcher's failover
// groups route around it.
func (w *RxWatchdog) ReceiveDeaf(interfaceID string) bool {
	if interfaceID != "aprs_0" && interfaceID != "ax25_0" {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.deaf
}

// State reports the current receive state.
func (w *RxWatchdog) State() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}

func (w *RxWatchdog) setState(s string) {
	if w.state == s {
		return
	}
	w.state = s
	if w.act.SetState != nil {
		w.act.SetState(s)
	}
}

func (w *RxWatchdog) tick(ctx context.Context) {
	now := w.now()
	h, ok := w.act.Probe()
	w.mu.Lock()
	defer w.mu.Unlock()
	if !ok {
		w.setState(ReceiveStateUnknown)
		return
	}

	// Anything decoded since the last tick means the receiver works.
	heard := h.RxFrames > w.lastFrames || (!h.LastDecodeAt.IsZero() && h.LastDecodeAt.After(w.lastHeardAt))
	if heard {
		w.lastFrames = h.RxFrames
		w.lastHeardAt = now
		if !h.LastDecodeAt.IsZero() && h.LastDecodeAt.Before(now) {
			w.lastHeardAt = h.LastDecodeAt
		}
		if w.deaf {
			w.deaf = false
			msg := fmt.Sprintf("APRS receive recovered after step %d", w.step)
			w.notify("aprs_rx_recovered", msg)
		}
		w.step = 0
		w.stepAt = time.Time{}
		w.setState(ReceiveStateOK)
		if w.act.Persist != nil && now.Sub(w.persistedAt) >= time.Minute {
			w.persistedAt = now
			go w.act.Persist(w.lastHeardAt, w.bridgeRestartAt)
		}
		return
	}

	hung := h.Running && !h.LevelAt.IsZero() && now.Sub(h.LevelAt) > w.cfg.StatsStale
	expected := !w.lastHeardAt.IsZero() && now.Sub(w.lastHeardAt) <= w.cfg.HeardWithin
	silent := !w.lastHeardAt.IsZero() && now.Sub(w.lastHeardAt) >= w.cfg.Silence
	if !hung && !(expected && silent) {
		if w.deaf {
			// Expectation expired while deaf: stop escalating, keep the flag
			// off so the health score recovers with the next real frame.
			w.deaf = false
		}
		if !w.lastHeardAt.IsZero() && now.Sub(w.lastHeardAt) < w.cfg.Silence {
			w.setState(ReceiveStateOK)
		} else {
			w.setState(ReceiveStateQuiet)
		}
		return
	}

	if !w.deaf {
		w.deaf = true
		w.step = 0
		w.stepAt = time.Time{}
		why := fmt.Sprintf("no frame decoded for %s while the channel was alive %s ago", now.Sub(w.lastHeardAt).Truncate(time.Second), now.Sub(w.lastHeardAt).Truncate(time.Second))
		if hung {
			why = fmt.Sprintf("Direwolf running but no audio report for %s", now.Sub(h.LevelAt).Truncate(time.Second))
		}
		w.notify("aprs_rx_deaf", "APRS receive silent: "+why)
	}
	w.setState(ReceiveStateDeaf)

	// One step per silence window, so each recovery gets time to show.
	if !w.stepAt.IsZero() && now.Sub(w.stepAt) < w.cfg.Silence {
		return
	}
	switch w.step {
	case 0:
		w.runStep(ctx, 1, "restart APRS gateway (Direwolf respawn)", w.act.RestartGateway)
	case 1:
		w.runStep(ctx, 2, "AIOC USB port power cycle then gateway restart", func(ctx context.Context) error {
			if w.act.PowerCycle == nil {
				return fmt.Errorf("no power cycle action")
			}
			if err := w.act.PowerCycle(ctx); err != nil {
				return err
			}
			if w.act.RestartGateway != nil {
				time.AfterFunc(10*time.Second, func() {
					rctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
					defer cancel()
					if err := w.act.RestartGateway(rctx); err != nil {
						log.Warn().Err(err).Msg("aprs rx watchdog: gateway restart after power cycle failed")
					}
				})
			}
			return nil
		})
	case 2:
		if now.Sub(w.bridgeRestartAt) < w.cfg.BridgeCooldown {
			return
		}
		w.bridgeRestartAt = now
		w.notify("aprs_rx_watchdog", "APRS receive still silent after gateway restart and AIOC power cycle: restarting the bridge")
		w.step = 3
		w.stepAt = now
		if w.act.Persist != nil {
			w.act.Persist(w.lastHeardAt, w.bridgeRestartAt)
		}
		if w.act.RestartBridge != nil {
			w.act.RestartBridge()
		}
	default:
		// Ladder exhausted; wait for recovery or the cooldown.
	}
}

func (w *RxWatchdog) runStep(ctx context.Context, step int, what string, fn func(context.Context) error) {
	w.step = step
	w.stepAt = w.now()
	w.notify("aprs_rx_watchdog", fmt.Sprintf("APRS receive silent: step %d, %s", step, what))
	if fn == nil {
		return
	}
	go func() {
		sctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		if err := fn(sctx); err != nil {
			log.Warn().Err(err).Int("step", step).Msg("aprs rx watchdog: recovery step failed")
			if w.act.Audit != nil {
				w.act.Audit(fmt.Sprintf("step %d failed: %v", step, err))
			}
		}
	}()
}

func (w *RxWatchdog) notify(event, msg string) {
	log.Warn().Str("event", event).Msg(msg)
	if w.act.Emit != nil {
		w.act.Emit(event, msg)
	}
	if w.act.Audit != nil {
		w.act.Audit(msg)
	}
}
