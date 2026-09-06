package gateway

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Device health watchdog [MESHSAT-817].
//
// Every USB-attached device of a field kit is probed at protocol level on a
// fixed tick and, when it stops answering, healed through the same reset
// levels the OOB executor exposes (1 soft, 2 device, 3 hard = hub-port VBUS
// cut), one rung per grace window, with a per-target budget for hard resets
// and a global gap between them. The mechanics copy the APRS receive
// watchdog (rx_watchdog.go), which stays in charge of APRS for now and is
// listed here read-only.
//
// The engine knows nothing about transports: main.go registers one
// HealthTarget per device with a probe closure and the rung closures, most
// of them the very oob.Action values already registered for RESET.

// Health states of a target.
const (
	HealthStateUnknown  = "unknown"  // device absent or not configured; the ladder is idle
	HealthStateOK       = "ok"       // the last probe answered
	HealthStateDegraded = "degraded" // enough consecutive misses; the first rung runs now
	HealthStateHealing  = "healing"  // a rung ran, waiting for its grace to show a result
	HealthStateFailed   = "failed"   // ladder exhausted or hard-reset budget spent; re-arms after the cooldown
	HealthStatePaused   = "paused"   // operator pause: probes continue, no rung runs
)

// Reset levels, numerically identical to the OOB executor's (oob.LevelSoft,
// oob.LevelDevice, oob.LevelHard); repeated here so this package does not
// import oob.
const (
	HealLevelSoft   byte = 1
	HealLevelDevice byte = 2
	HealLevelHard   byte = 3
)

// Errors returned by the manual entry points.
var (
	ErrHealthTargetUnknown = errors.New("device health: unknown target")
	ErrHealthTargetBusy    = errors.New("device health: a recovery step is already running")
	ErrHealthTargetPaused  = errors.New("device health: target is paused")
	ErrHealthConfirm       = errors.New("device health: level above the automatic maximum needs confirm")
	ErrHealthNoStep        = errors.New("device health: no recovery step at that level")
	ErrHealthExternal      = errors.New("device health: target is managed by another watchdog")
)

// ProbeResult is what a probe closure reports.
type ProbeResult struct {
	// OK means the device answered at protocol level.
	OK bool
	// Unknown means there is nothing to probe (no port assigned, feature
	// disabled). It never counts as a miss and, outside a grace window,
	// resets the ladder.
	Unknown bool
	// Detail is a short human-readable reason shown on the status API.
	Detail string
}

// HealStep is one rung of a target's ladder.
type HealStep struct {
	Level byte
	Name  string
	// Run performs the recovery. A returned error is logged and audited;
	// the rung still counts as taken so the ladder keeps climbing.
	Run func(ctx context.Context) error
	// Grace is how long misses are ignored after the rung ran, so the
	// device has time to come back before the next rung fires.
	Grace time.Duration
	// Skip, when it returns true, makes the ladder jump over this rung
	// (for example admin reboot when the radio never told us its node
	// number, or a VBUS cut on a root port).
	Skip func() bool
}

// HealthTarget describes one device to the engine.
type HealthTarget struct {
	Name     string
	IfaceIDs []string
	Probe    func(ctx context.Context) ProbeResult
	// ProbeTimeout bounds one probe call (default 10 s).
	ProbeTimeout time.Duration
	Steps        []HealStep
	// Misses is how many consecutive misses declare the target degraded
	// (default DeviceHealthConfig.Misses).
	Misses int
	// HardBudget caps level-3 rungs per rolling hour (default config).
	HardBudget int
	// HardCooldown is how long a failed target waits before the ladder
	// re-arms from the first rung (default config).
	HardCooldown time.Duration
	// MaxLevel is the highest level the ladder runs on its own; rungs
	// above it need Heal(name, level, confirm=true) (default 3).
	MaxLevel byte
}

// PersistedTarget is the per-target state that survives a bridge restart.
type PersistedTarget struct {
	HardResets []time.Time `json:"hard_resets"`
	Paused     bool        `json:"paused"`
}

// TargetStatus is one row of the status API.
type TargetStatus struct {
	Name       string     `json:"name"`
	Interfaces []string   `json:"interfaces"`
	State      string     `json:"state"`
	Detail     string     `json:"detail,omitempty"`
	Misses     int        `json:"misses"`
	Step       int        `json:"step"`
	StepLevel  byte       `json:"step_level,omitempty"`
	StepName   string     `json:"step_name,omitempty"`
	StepAt     *time.Time `json:"step_at,omitempty"`
	GraceUntil *time.Time `json:"grace_until,omitempty"`
	LastOK     *time.Time `json:"last_ok,omitempty"`
	LastProbe  *time.Time `json:"last_probe,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
	HardResets int        `json:"hard_resets_last_hour"`
	HardBudget int        `json:"hard_budget"`
	Levels     []int      `json:"levels"`
	MaxLevel   byte       `json:"max_level"`
	Paused     bool       `json:"paused"`
	External   bool       `json:"external,omitempty"`
}

// DeviceHealthConfig tunes the engine. Zero fields take the defaults.
type DeviceHealthConfig struct {
	Tick         time.Duration // probe period (30 s)
	Misses       int           // consecutive misses to degraded (3)
	HardBudget   int           // level-3 rungs per rolling hour per target (3)
	HardGap      time.Duration // minimum spacing between any two level-3 rungs (45 s)
	HardCooldown time.Duration // wait after failed before re-arming (1 h)
	StepTimeout  time.Duration // context deadline for a rung (90 s)
	Now          func() time.Time
	Seed         map[string]PersistedTarget
}

// DeviceHealthActions are the engine's outputs.
type DeviceHealthActions struct {
	// Emit publishes an SSE event; data carries target, state, step, level.
	Emit func(eventType, message string, data map[string]any)
	// Audit appends to the signed audit chain.
	Audit func(target, ifaceID, detail string)
	// Persist stores the per-target state for the next process.
	Persist func(target string, p PersistedTarget)
}

type externalStatus func() (state, detail string)

type healthTargetState struct {
	t          HealthTarget
	state      string
	detail     string
	misses     int
	step       int // rungs taken in this episode (index of the next rung to run)
	stepLevel  byte
	stepName   string
	stepAt     time.Time
	graceUntil time.Time
	lastOK     time.Time
	lastProbe  time.Time
	lastErr    string
	failedAt   time.Time
	hardResets []time.Time
	paused     bool
	inflight   bool
	probing    bool
	external   externalStatus
}

// DeviceHealth is the engine.
type DeviceHealth struct {
	cfg DeviceHealthConfig
	act DeviceHealthActions
	now func() time.Time

	mu         sync.Mutex
	targets    map[string]*healthTargetState
	lastHardAt time.Time
	// runCtx is the engine's lifetime context; manual rungs run under it,
	// never under the HTTP request that asked for them (a request context
	// dies as soon as the 202 goes out and cancelled the agent dial of the
	// first manual cellular cut, tesseract 6 Sep 2026 09:33Z).
	runCtx context.Context
}

func (d *DeviceHealth) stepContext() context.Context {
	if d.runCtx != nil {
		return d.runCtx
	}
	return context.Background()
}

// NewDeviceHealth applies defaults to zero fields.
func NewDeviceHealth(cfg DeviceHealthConfig, act DeviceHealthActions) *DeviceHealth {
	if cfg.Tick <= 0 {
		cfg.Tick = 30 * time.Second
	}
	if cfg.Misses <= 0 {
		cfg.Misses = 3
	}
	if cfg.HardBudget <= 0 {
		cfg.HardBudget = 3
	}
	if cfg.HardGap <= 0 {
		cfg.HardGap = 45 * time.Second
	}
	if cfg.HardCooldown <= 0 {
		cfg.HardCooldown = time.Hour
	}
	if cfg.StepTimeout <= 0 {
		cfg.StepTimeout = 90 * time.Second
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &DeviceHealth{cfg: cfg, act: act, now: now, targets: make(map[string]*healthTargetState)}
}

// Register adds a target. Registering the same name again replaces it.
func (d *DeviceHealth) Register(t HealthTarget) {
	if t.ProbeTimeout <= 0 {
		t.ProbeTimeout = 10 * time.Second
	}
	if t.Misses <= 0 {
		t.Misses = d.cfg.Misses
	}
	if t.HardBudget <= 0 {
		t.HardBudget = d.cfg.HardBudget
	}
	if t.HardCooldown <= 0 {
		t.HardCooldown = d.cfg.HardCooldown
	}
	if t.MaxLevel == 0 {
		t.MaxLevel = HealLevelHard
	}
	ts := &healthTargetState{t: t, state: HealthStateUnknown}
	if seed, ok := d.cfg.Seed[t.Name]; ok {
		ts.hardResets = append(ts.hardResets, seed.HardResets...)
		ts.paused = seed.Paused
		if ts.paused {
			ts.state = HealthStatePaused
		}
	}
	d.mu.Lock()
	d.targets[t.Name] = ts
	d.mu.Unlock()
}

// RegisterExternal lists a target whose health another watchdog owns (the
// APRS receive watchdog): shown on the status API, never probed or healed
// here. status returns one of the health states plus a detail string.
func (d *DeviceHealth) RegisterExternal(name string, ifaceIDs []string, status func() (state, detail string)) {
	d.mu.Lock()
	d.targets[name] = &healthTargetState{
		t:        HealthTarget{Name: name, IfaceIDs: ifaceIDs},
		state:    HealthStateUnknown,
		external: status,
	}
	d.mu.Unlock()
}

// Run probes every target on each tick until ctx ends.
func (d *DeviceHealth) Run(ctx context.Context) {
	d.mu.Lock()
	d.runCtx = ctx
	d.mu.Unlock()
	t := time.NewTicker(d.cfg.Tick)
	defer t.Stop()
	d.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.tick(ctx)
		}
	}
}

// ReceiveDeaf implements engine.ReceiveChecker: an interface whose device is
// degraded, healing or failed scores 0 so failover groups route around it.
func (d *DeviceHealth) ReceiveDeaf(interfaceID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, ts := range d.targets {
		if ts.external != nil {
			continue
		}
		for _, id := range ts.t.IfaceIDs {
			if id != interfaceID {
				continue
			}
			switch ts.state {
			case HealthStateDegraded, HealthStateHealing, HealthStateFailed:
				return true
			}
		}
	}
	return false
}

// Status reports every target, sorted by name.
func (d *DeviceHealth) Status() []TargetStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	out := make([]TargetStatus, 0, len(d.targets))
	for _, ts := range d.targets {
		out = append(out, d.statusLocked(ts, now))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// TargetState returns the state of one target ("" when unknown name).
func (d *DeviceHealth) TargetState(name string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ts, ok := d.targets[name]; ok {
		if ts.external != nil {
			s, _ := ts.external()
			return s
		}
		return ts.state
	}
	return ""
}

func (d *DeviceHealth) statusLocked(ts *healthTargetState, now time.Time) TargetStatus {
	st := TargetStatus{
		Name:       ts.t.Name,
		Interfaces: append([]string(nil), ts.t.IfaceIDs...),
		State:      ts.state,
		Detail:     ts.detail,
		Misses:     ts.misses,
		Step:       ts.step,
		StepLevel:  ts.stepLevel,
		StepName:   ts.stepName,
		LastError:  ts.lastErr,
		HardResets: len(d.recentHard(ts, now)),
		HardBudget: ts.t.HardBudget,
		MaxLevel:   ts.t.MaxLevel,
		Paused:     ts.paused,
		Levels:     []int{},
	}
	if ts.external != nil {
		st.External = true
		st.State, st.Detail = ts.external()
		return st
	}
	seen := map[byte]bool{}
	for _, s := range ts.t.Steps {
		if !seen[s.Level] {
			seen[s.Level] = true
			st.Levels = append(st.Levels, int(s.Level))
		}
	}
	if !ts.stepAt.IsZero() {
		v := ts.stepAt
		st.StepAt = &v
	}
	if now.Before(ts.graceUntil) {
		v := ts.graceUntil
		st.GraceUntil = &v
	}
	if !ts.lastOK.IsZero() {
		v := ts.lastOK
		st.LastOK = &v
	}
	if !ts.lastProbe.IsZero() {
		v := ts.lastProbe
		st.LastProbe = &v
	}
	return st
}

// Pause stops the ladder for a target; probes continue.
func (d *DeviceHealth) Pause(name string) error {
	return d.setPaused(name, true)
}

// Resume re-enables the ladder for a target.
func (d *DeviceHealth) Resume(name string) error {
	return d.setPaused(name, false)
}

func (d *DeviceHealth) setPaused(name string, paused bool) error {
	d.mu.Lock()
	ts, ok := d.targets[name]
	if !ok {
		d.mu.Unlock()
		return ErrHealthTargetUnknown
	}
	if ts.external != nil {
		d.mu.Unlock()
		return ErrHealthExternal
	}
	ts.paused = paused
	if paused {
		ts.state = HealthStatePaused
	} else if ts.state == HealthStatePaused {
		ts.state = HealthStateUnknown
	}
	ts.misses, ts.step, ts.stepAt, ts.graceUntil = 0, 0, time.Time{}, time.Time{}
	p := PersistedTarget{HardResets: append([]time.Time(nil), ts.hardResets...), Paused: ts.paused}
	d.mu.Unlock()
	d.persist(name, p)
	log.Info().Str("target", name).Bool("paused", paused).Msg("device health: pause state changed")
	return nil
}

// Heal runs the first rung at the given level now, with the same accounting
// as the automatic ladder. Levels above the target's MaxLevel need confirm.
func (d *DeviceHealth) Heal(ctx context.Context, name string, level byte, confirm bool) error {
	d.mu.Lock()
	ts, ok := d.targets[name]
	if !ok {
		d.mu.Unlock()
		return ErrHealthTargetUnknown
	}
	if ts.external != nil {
		d.mu.Unlock()
		return ErrHealthExternal
	}
	if ts.inflight {
		d.mu.Unlock()
		return ErrHealthTargetBusy
	}
	if level > ts.t.MaxLevel && !confirm {
		d.mu.Unlock()
		return ErrHealthConfirm
	}
	idx := -1
	for i, s := range ts.t.Steps {
		if s.Level == level {
			idx = i
			break
		}
	}
	if idx < 0 {
		d.mu.Unlock()
		return ErrHealthNoStep
	}
	now := d.now()
	if level == HealLevelHard {
		if len(d.recentHard(ts, now)) >= ts.t.HardBudget && !confirm {
			d.mu.Unlock()
			return fmt.Errorf("device health: hard-reset budget of %d per hour spent", ts.t.HardBudget)
		}
		d.lastHardAt = now
		ts.hardResets = append(d.recentHard(ts, now), now)
		p := PersistedTarget{HardResets: append([]time.Time(nil), ts.hardResets...), Paused: ts.paused}
		go d.persist(name, p)
	}
	ts.state = HealthStateHealing
	_ = ctx // the caller's (HTTP) context must not own the rung
	d.startStepLocked(d.stepContext(), ts, idx, now, "manual")
	d.mu.Unlock()
	return nil
}

// NoteExternalReset is called when the OOB executor ran a RESET on a target
// so the ladder gives the device the rung's grace instead of counting the
// outage as misses, and a level-3 counts against the budget.
func (d *DeviceHealth) NoteExternalReset(name string, level byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ts, ok := d.targets[name]
	if !ok || ts.external != nil {
		return
	}
	now := d.now()
	grace := 60 * time.Second
	for _, s := range ts.t.Steps {
		if s.Level == level && s.Grace > grace {
			grace = s.Grace
		}
	}
	ts.graceUntil = now.Add(grace)
	ts.misses = 0
	if level == HealLevelHard {
		d.lastHardAt = now
		ts.hardResets = append(d.recentHard(ts, now), now)
		p := PersistedTarget{HardResets: append([]time.Time(nil), ts.hardResets...), Paused: ts.paused}
		go d.persist(name, p)
	}
	log.Info().Str("target", name).Uint8("level", level).Dur("grace", grace).
		Msg("device health: external reset noted, grace started")
}

func (d *DeviceHealth) recentHard(ts *healthTargetState, now time.Time) []time.Time {
	cutoff := now.Add(-time.Hour)
	out := ts.hardResets[:0:0]
	for _, t := range ts.hardResets {
		if t.After(cutoff) {
			out = append(out, t)
		}
	}
	return out
}

func (d *DeviceHealth) tick(ctx context.Context) {
	d.mu.Lock()
	names := make([]string, 0, len(d.targets))
	for name, ts := range d.targets {
		if ts.external != nil || ts.probing || ts.inflight || ts.t.Probe == nil {
			continue
		}
		ts.probing = true
		names = append(names, name)
	}
	d.mu.Unlock()
	for _, name := range names {
		go d.probe(ctx, name)
	}
}

func (d *DeviceHealth) probe(ctx context.Context, name string) {
	d.mu.Lock()
	ts, ok := d.targets[name]
	if !ok {
		d.mu.Unlock()
		return
	}
	probeFn, timeout := ts.t.Probe, ts.t.ProbeTimeout
	d.mu.Unlock()

	pctx, cancel := context.WithTimeout(ctx, timeout)
	res := probeFn(pctx)
	cancel()

	d.mu.Lock()
	defer d.mu.Unlock()
	ts.probing = false
	d.applyProbeLocked(ctx, ts, res, d.now())
}

func (d *DeviceHealth) applyProbeLocked(ctx context.Context, ts *healthTargetState, res ProbeResult, now time.Time) {
	ts.lastProbe = now
	inGrace := now.Before(ts.graceUntil)

	if res.OK {
		ts.lastOK = now
		ts.misses = 0
		ts.detail = res.Detail
		switch ts.state {
		case HealthStateDegraded, HealthStateHealing, HealthStateFailed:
			msg := fmt.Sprintf("%s recovered after step %d", ts.t.Name, ts.step)
			if ts.stepName != "" {
				msg += " (" + ts.stepName + ")"
			}
			d.notifyLocked(ts, "device_recovered", msg, false)
		}
		ts.step, ts.stepLevel, ts.stepName = 0, 0, ""
		ts.stepAt, ts.graceUntil, ts.failedAt = time.Time{}, time.Time{}, time.Time{}
		if ts.paused {
			ts.state = HealthStatePaused
		} else {
			ts.state = HealthStateOK
		}
		return
	}

	if res.Unknown {
		if inGrace {
			// The device is expected to be away while a rung settles.
			return
		}
		ts.detail = res.Detail
		ts.misses = 0
		ts.step, ts.stepLevel, ts.stepName = 0, 0, ""
		ts.stepAt, ts.failedAt = time.Time{}, time.Time{}
		if ts.paused {
			ts.state = HealthStatePaused
		} else {
			ts.state = HealthStateUnknown
		}
		return
	}

	// A miss.
	ts.detail = res.Detail
	if inGrace {
		return
	}
	ts.misses++
	if ts.paused {
		return
	}

	switch ts.state {
	case HealthStateFailed:
		if now.Sub(ts.failedAt) < ts.t.HardCooldown {
			return
		}
		// Cooldown over: re-arm from the first rung.
		ts.step, ts.stepLevel, ts.stepName = 0, 0, ""
		ts.state = HealthStateDegraded
		d.notifyLocked(ts, "device_unhealthy", fmt.Sprintf("%s still unhealthy after cooldown: %s", ts.t.Name, res.Detail), true)
	case HealthStateDegraded, HealthStateHealing:
		// Ladder in progress.
	default:
		if ts.misses < ts.t.Misses {
			return
		}
		ts.state = HealthStateDegraded
		ts.step, ts.stepLevel, ts.stepName = 0, 0, ""
		d.notifyLocked(ts, "device_unhealthy", fmt.Sprintf("%s unhealthy: %s (%d consecutive misses)", ts.t.Name, res.Detail, ts.misses), true)
	}
	d.nextStepLocked(ctx, ts, now)
}

// nextStepLocked runs the next rung of the ladder or declares the target failed.
func (d *DeviceHealth) nextStepLocked(ctx context.Context, ts *healthTargetState, now time.Time) {
	for i := ts.step; i < len(ts.t.Steps); i++ {
		s := ts.t.Steps[i]
		if s.Skip != nil && s.Skip() {
			log.Info().Str("target", ts.t.Name).Uint8("level", s.Level).Str("step", s.Name).Msg("device health: rung skipped")
			ts.step = i + 1
			continue
		}
		if s.Level > ts.t.MaxLevel {
			d.failLocked(ts, now, fmt.Sprintf("%s: level %d rung %q needs operator confirm (POST /api/devices/health/%s/heal {\"level\":%d,\"confirm\":true})",
				ts.t.Name, s.Level, s.Name, ts.t.Name, s.Level))
			return
		}
		if s.Level == HealLevelHard {
			if len(d.recentHard(ts, now)) >= ts.t.HardBudget {
				d.failLocked(ts, now, fmt.Sprintf("%s: hard-reset budget of %d per hour spent", ts.t.Name, ts.t.HardBudget))
				return
			}
			if !d.lastHardAt.IsZero() && now.Sub(d.lastHardAt) < d.cfg.HardGap {
				// Another target cut power a moment ago; try again next tick.
				ts.detail = "waiting for the hard-reset gap"
				return
			}
			d.lastHardAt = now
			ts.hardResets = append(d.recentHard(ts, now), now)
			p := PersistedTarget{HardResets: append([]time.Time(nil), ts.hardResets...), Paused: ts.paused}
			go d.persist(ts.t.Name, p)
		}
		ts.state = HealthStateHealing
		d.startStepLocked(ctx, ts, i, now, "auto")
		return
	}
	d.failLocked(ts, now, fmt.Sprintf("%s: ladder exhausted after %d rungs", ts.t.Name, ts.step))
}

func (d *DeviceHealth) failLocked(ts *healthTargetState, now time.Time, why string) {
	if ts.state == HealthStateFailed {
		return
	}
	ts.state = HealthStateFailed
	ts.failedAt = now
	d.notifyLocked(ts, "device_heal_failed", why, true)
}

func (d *DeviceHealth) startStepLocked(ctx context.Context, ts *healthTargetState, idx int, now time.Time, origin string) {
	s := ts.t.Steps[idx]
	ts.step = idx + 1
	ts.stepLevel = s.Level
	ts.stepName = s.Name
	ts.stepAt = now
	ts.graceUntil = now.Add(s.Grace)
	ts.inflight = true
	ts.lastErr = ""
	d.notifyLocked(ts, "device_heal_step", fmt.Sprintf("%s: step %d, level %d, %s (%s)", ts.t.Name, ts.step, s.Level, s.Name, origin), false)
	name := ts.t.Name
	go func() {
		var err error
		if s.Run == nil {
			err = errors.New("no action")
		} else {
			sctx, cancel := context.WithTimeout(ctx, d.cfg.StepTimeout)
			err = s.Run(sctx)
			cancel()
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		ts.inflight = false
		if err != nil {
			ts.lastErr = fmt.Sprintf("step %d %s: %v", idx+1, s.Name, err)
			log.Warn().Err(err).Str("target", name).Int("step", idx+1).Str("name", s.Name).Msg("device health: recovery step failed")
			if d.act.Audit != nil {
				iface := firstIface(ts.t.IfaceIDs)
				go d.act.Audit(name, iface, ts.lastErr)
			}
		}
	}()
}

func (d *DeviceHealth) notifyLocked(ts *healthTargetState, event, msg string, warn bool) {
	if warn {
		log.Warn().Str("event", event).Str("target", ts.t.Name).Msg(msg)
	} else {
		log.Info().Str("event", event).Str("target", ts.t.Name).Msg(msg)
	}
	data := map[string]any{
		"target": ts.t.Name,
		"state":  ts.state,
		"step":   ts.step,
		"level":  ts.stepLevel,
		"detail": ts.detail,
	}
	iface := firstIface(ts.t.IfaceIDs)
	if d.act.Emit != nil {
		go d.act.Emit(event, msg, data)
	}
	if d.act.Audit != nil {
		go d.act.Audit(ts.t.Name, iface, msg)
	}
}

func (d *DeviceHealth) persist(name string, p PersistedTarget) {
	if d.act.Persist != nil {
		d.act.Persist(name, p)
	}
}

func firstIface(ids []string) string {
	if len(ids) > 0 {
		return ids[0]
	}
	return ""
}
