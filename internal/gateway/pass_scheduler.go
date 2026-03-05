package gateway

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// PassPrediction describes a single satellite pass (decoupled from engine package).
type PassPrediction struct {
	Satellite   string  `json:"satellite"`
	AOS         int64   `json:"aos"`
	LOS         int64   `json:"los"`
	DurationMin float64 `json:"duration_min"`
	PeakElevDeg float64 `json:"peak_elev_deg"`
	PeakAzimuth float64 `json:"peak_azimuth"`
	IsActive    bool    `json:"is_active"`
}

// PassPredictor abstracts TLE-based pass prediction (breaks engine→gateway→engine cycle).
type PassPredictor interface {
	GeneratePasses(lat, lon, altKm float64, hours int, minElevDeg float64, startTime int64) ([]PassPrediction, error)
}

// ScheduleMode represents the current scheduling state.
type ScheduleMode int

const (
	ModeIdle     ScheduleMode = iota // No pass within pre-wake window
	ModePreWake                      // Pass starts within pre-wake window
	ModeActive                       // Satellite is overhead (AOS <= now <= LOS)
	ModePostPass                     // Within grace period after LOS
)

// String returns the human-readable mode name.
func (m ScheduleMode) String() string {
	switch m {
	case ModeIdle:
		return "idle"
	case ModePreWake:
		return "pre_wake"
	case ModeActive:
		return "active"
	case ModePostPass:
		return "post_pass"
	default:
		return "unknown"
	}
}

// DisplayName returns a formatted mode name for the API/UI.
func (m ScheduleMode) DisplayName() string {
	switch m {
	case ModeIdle:
		return "Idle"
	case ModePreWake:
		return "Pre-Wake"
	case ModeActive:
		return "Active"
	case ModePostPass:
		return "Post-Pass"
	default:
		return "Unknown"
	}
}

// TimingParams holds the dynamic intervals for gateway workers.
type TimingParams struct {
	PollInterval     time.Duration `json:"-"`
	DLQCheckInterval time.Duration `json:"-"`
	DLQRetryBase     time.Duration `json:"-"`
	Mode             ScheduleMode  `json:"mode"`
	ModeName         string        `json:"mode_name"`
	NextTransition   time.Time     `json:"next_transition"`
	CurrentPass      *ScoredPass   `json:"current_pass,omitempty"`
}

// ScoredPass is a pass prediction with quality scoring.
type ScoredPass struct {
	PassPrediction
	QualityScore float64 `json:"quality_score"`
	Priority     string  `json:"priority"` // "high", "medium", "low"
}

// PassSchedule holds the computed schedule state.
type PassSchedule struct {
	ComputedAt     time.Time    `json:"computed_at"`
	LocationName   string       `json:"location_name"`
	Lat            float64      `json:"lat"`
	Lon            float64      `json:"lon"`
	AltM           float64      `json:"alt_m"`
	CurrentMode    ScheduleMode `json:"current_mode"`
	NextTransition time.Time    `json:"next_transition"`
	UpcomingPasses []ScoredPass `json:"upcoming_passes"`
}

// PassCounterSource provides per-pass MO attempt/success counters from the gateway.
type PassCounterSource interface {
	ResetPassCounters() (attempts, successes int64)
}

// PassScheduler dynamically adjusts gateway timing based on TLE pass predictions.
type PassScheduler struct {
	tleMgr   PassPredictor
	db       *database.DB
	config   IridiumConfig
	counters PassCounterSource // gateway's per-pass counters (set after construction)

	mu       sync.RWMutex
	schedule *PassSchedule
	mode     ScheduleMode

	modeCh chan ScheduleMode // workers listen for mode transitions
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewPassScheduler creates a new pass-aware scheduler.
func NewPassScheduler(tleMgr PassPredictor, db *database.DB, cfg IridiumConfig) *PassScheduler {
	return &PassScheduler{
		tleMgr: tleMgr,
		db:     db,
		config: cfg,
		modeCh: make(chan ScheduleMode, 4), // buffered to avoid blocking
		mode:   ModeIdle,
	}
}

// SetCounterSource sets the gateway's per-pass counter source.
func (ps *PassScheduler) SetCounterSource(src PassCounterSource) {
	ps.counters = src
}

// Start launches the scheduler recompute loop.
func (ps *PassScheduler) Start(ctx context.Context) {
	ctx, ps.cancel = context.WithCancel(ctx)

	ps.wg.Add(1)
	go ps.run(ctx)

	log.Info().Msg("iridium: pass scheduler started")
}

// Stop cancels the scheduler and waits for goroutines.
func (ps *PassScheduler) Stop() {
	if ps.cancel != nil {
		ps.cancel()
	}
	ps.wg.Wait()
	log.Info().Msg("iridium: pass scheduler stopped")
}

// GetTimingParams returns the current dynamic timing parameters.
func (ps *PassScheduler) GetTimingParams() TimingParams {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	params := ps.timingForMode(ps.mode)
	params.Mode = ps.mode
	params.ModeName = ps.mode.String()

	if ps.schedule != nil {
		params.NextTransition = ps.schedule.NextTransition

		// Find current active pass if any
		now := time.Now().Unix()
		for i := range ps.schedule.UpcomingPasses {
			p := &ps.schedule.UpcomingPasses[i]
			if p.AOS <= now && p.LOS >= now {
				params.CurrentPass = p
				break
			}
		}
	}

	return params
}

// ModeCh returns the channel that emits mode transitions.
func (ps *PassScheduler) ModeCh() <-chan ScheduleMode {
	return ps.modeCh
}

// Schedule returns the current computed schedule (for the API endpoint).
func (ps *PassScheduler) Schedule() *PassSchedule {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.schedule
}

// Mode returns the current scheduling mode.
func (ps *PassScheduler) Mode() ScheduleMode {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.mode
}

// run is the main scheduler loop. It recomputes passes periodically and
// sets precise timers for mode transitions.
func (ps *PassScheduler) run(ctx context.Context) {
	defer ps.wg.Done()

	// Initial compute
	ps.recompute()

	// Recompute every 15 minutes, but also set precise mode boundary timers
	recomputeTicker := time.NewTicker(15 * time.Minute)
	defer recomputeTicker.Stop()

	// Set initial transition timer
	transTimer := ps.nextTransitionTimer()
	defer transTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-recomputeTicker.C:
			ps.recompute()
			transTimer.Stop()
			transTimer = ps.nextTransitionTimer()

		case <-transTimer.C:
			// Mode boundary hit — recompute and check for transition
			oldMode := ps.Mode()
			ps.recompute()
			newMode := ps.Mode()

			if newMode != oldMode {
				log.Info().
					Str("from", oldMode.String()).
					Str("to", newMode.String()).
					Msg("iridium: scheduler mode transition")

				// Non-blocking send on mode channel
				select {
				case ps.modeCh <- newMode:
				default:
				}

				// Log pass quality on PostPass → Idle transition
				if oldMode == ModePostPass && newMode == ModeIdle {
					go ps.logPassQuality()
				}
			}

			transTimer.Stop()
			transTimer = ps.nextTransitionTimer()
		}
	}
}

// recompute fetches upcoming passes, scores them, and determines the current mode.
func (ps *PassScheduler) recompute() {
	loc := ps.preferredLocation()
	if loc == nil {
		ps.mu.Lock()
		ps.schedule = &PassSchedule{
			ComputedAt:  time.Now(),
			CurrentMode: ModeIdle,
		}
		ps.mode = ModeIdle
		ps.mu.Unlock()
		return
	}

	minElev := float64(ps.config.MinElevDeg)
	if minElev <= 0 {
		minElev = 5.0
	}
	passes, err := ps.tleMgr.GeneratePasses(loc.Lat, loc.Lon, loc.AltM/1000.0, 12, minElev, 0)
	if err != nil {
		log.Warn().Err(err).Msg("iridium: scheduler failed to generate passes")
		return
	}

	scored := ps.scorePasses(passes)
	mode, nextTransition := ps.determineMode(scored)

	ps.mu.Lock()
	oldMode := ps.mode
	ps.mode = mode
	ps.schedule = &PassSchedule{
		ComputedAt:     time.Now(),
		LocationName:   loc.Name,
		Lat:            loc.Lat,
		Lon:            loc.Lon,
		AltM:           loc.AltM,
		CurrentMode:    mode,
		NextTransition: nextTransition,
		UpcomingPasses: scored,
	}
	ps.mu.Unlock()

	if oldMode != mode {
		select {
		case ps.modeCh <- mode:
		default:
		}
	}

	log.Debug().
		Str("location", loc.Name).
		Str("mode", mode.String()).
		Int("passes", len(scored)).
		Msg("iridium: scheduler recomputed")
}

// preferredLocation returns the first custom location, or the first builtin.
func (ps *PassScheduler) preferredLocation() *database.IridiumLocation {
	if ps.db == nil {
		return nil
	}

	locs, err := ps.db.GetIridiumLocations()
	if err != nil || len(locs) == 0 {
		return nil
	}

	// Prefer custom locations (non-builtin) over builtin
	for i := range locs {
		if !locs[i].Builtin {
			return &locs[i]
		}
	}
	return &locs[0]
}

// scorePasses evaluates each pass quality 0.0–1.0.
func (ps *PassScheduler) scorePasses(passes []PassPrediction) []ScoredPass {
	scored := make([]ScoredPass, 0, len(passes))

	for _, p := range passes {
		score := ps.scorePass(p)
		priority := "low"
		if score >= 0.7 {
			priority = "high"
		} else if score >= 0.4 {
			priority = "medium"
		}

		scored = append(scored, ScoredPass{
			PassPrediction: p,
			QualityScore:   math.Round(score*100) / 100,
			Priority:       priority,
		})
	}

	return scored
}

// scorePass computes a quality score for a single pass.
func (ps *PassScheduler) scorePass(p PassPrediction) float64 {
	// Elevation score (50%): higher passes are more reliable
	elevScore := math.Min(p.PeakElevDeg/70.0, 1.0)

	// Historical hit rate (40%): signal success at similar elevations
	histScore := ps.historicalHitRate(p.PeakElevDeg)

	// Duration score (10%): longer passes = more transmission time
	durScore := math.Min(p.DurationMin/12.0, 1.0)

	return elevScore*0.5 + histScore*0.4 + durScore*0.1
}

// historicalHitRate queries GSS registration success and pass quality logs for
// the actual success rate at similar elevation bands.
// Prefers GSS data (ground truth) over signal-bar heuristics.
// Falls back to an elevation-based prior for new installations.
func (ps *PassScheduler) historicalHitRate(peakElevDeg float64) float64 {
	if ps.db == nil {
		return elevationPrior(peakElevDeg)
	}

	// 15° elevation bands
	bandLow := math.Floor(peakElevDeg/15.0) * 15.0
	bandHigh := bandLow + 15.0

	// Prefer GSS registration success data (actual SBDIX outcomes)
	gssRate, gssSamples, err := ps.db.GetGSSSuccessRateByElevation(bandLow, bandHigh, 30)
	if err == nil && gssSamples >= 3 {
		return gssRate
	}

	// Fall back to signal bar pass quality
	hitRate, samples, err := ps.db.GetPassQualityByElevation(bandLow, bandHigh, 30)
	if err == nil && samples >= 3 {
		return hitRate
	}

	return elevationPrior(peakElevDeg)
}

// elevationPrior provides a reasonable prior for signal success based on elevation.
func elevationPrior(elevDeg float64) float64 {
	if elevDeg >= 60 {
		return 0.9
	}
	if elevDeg >= 40 {
		return 0.7
	}
	if elevDeg >= 20 {
		return 0.5
	}
	return 0.3
}

// determineMode checks all passes to find the current mode and next transition time.
func (ps *PassScheduler) determineMode(passes []ScoredPass) (ScheduleMode, time.Time) {
	now := time.Now()
	nowUnix := now.Unix()

	preWake := time.Duration(ps.config.PreWakeMinutes) * time.Minute
	if preWake <= 0 {
		preWake = 5 * time.Minute
	}
	postGrace := time.Duration(ps.config.PostPassGraceSec) * time.Second
	if postGrace <= 0 {
		postGrace = 2 * time.Minute
	}

	var (
		activePass   *ScoredPass
		preWakePass  *ScoredPass
		postPassEnd  time.Time
		postPassPass *ScoredPass
	)

	for i := range passes {
		p := &passes[i]
		aos := time.Unix(p.AOS, 0)
		los := time.Unix(p.LOS, 0)

		// Active: AOS <= now <= LOS
		if p.AOS <= nowUnix && p.LOS >= nowUnix {
			activePass = p
			break // Active takes priority over everything
		}

		// PostPass: LOS < now <= LOS + grace
		losGrace := los.Add(postGrace)
		if p.LOS < nowUnix && nowUnix <= losGrace.Unix() {
			if postPassPass == nil || los.After(postPassEnd) {
				postPassEnd = losGrace
				postPassPass = p
			}
		}

		// PreWake: AOS - preWake <= now < AOS
		preWakeStart := aos.Add(-preWake)
		if now.After(preWakeStart) && now.Before(aos) {
			if preWakePass == nil || p.AOS < preWakePass.AOS {
				preWakePass = p
			}
		}
	}

	// Priority: Active > PostPass > PreWake > Idle
	if activePass != nil {
		los := time.Unix(activePass.LOS, 0)
		return ModeActive, los
	}

	if postPassPass != nil {
		return ModePostPass, postPassEnd
	}

	if preWakePass != nil {
		aos := time.Unix(preWakePass.AOS, 0)
		return ModePreWake, aos
	}

	// Idle — find next pre-wake boundary
	nextTransition := now.Add(15 * time.Minute) // default: recompute in 15 min
	for i := range passes {
		p := &passes[i]
		aos := time.Unix(p.AOS, 0)
		preWakeStart := aos.Add(-preWake)
		if preWakeStart.After(now) {
			nextTransition = preWakeStart
			break // passes are sorted by AOS
		}
	}

	return ModeIdle, nextTransition
}

// timingForMode returns the interval set for a given mode.
func (ps *PassScheduler) timingForMode(mode ScheduleMode) TimingParams {
	idlePoll := time.Duration(ps.config.IdlePollSec) * time.Second
	if idlePoll <= 0 {
		idlePoll = 15 * time.Minute
	}
	activePoll := time.Duration(ps.config.ActivePollSec) * time.Second
	if activePoll <= 0 {
		activePoll = 60 * time.Second
	}

	switch mode {
	case ModeActive:
		return TimingParams{
			PollInterval:     activePoll,
			DLQCheckInterval: 30 * time.Second,
			DLQRetryBase:     30 * time.Second,
		}
	case ModePreWake:
		return TimingParams{
			PollInterval:     60 * time.Second,
			DLQCheckInterval: 30 * time.Second,
			DLQRetryBase:     60 * time.Second,
		}
	case ModePostPass:
		return TimingParams{
			PollInterval:     30 * time.Second,
			DLQCheckInterval: 20 * time.Second,
			DLQRetryBase:     30 * time.Second,
		}
	default: // ModeIdle
		return TimingParams{
			PollInterval:     idlePoll,
			DLQCheckInterval: 5 * time.Minute,
			DLQRetryBase:     5 * time.Minute,
		}
	}
}

// nextTransitionTimer returns a timer that fires at the next mode boundary.
func (ps *PassScheduler) nextTransitionTimer() *time.Timer {
	ps.mu.RLock()
	var nextTrans time.Time
	if ps.schedule != nil {
		nextTrans = ps.schedule.NextTransition
	}
	ps.mu.RUnlock()

	if nextTrans.IsZero() || nextTrans.Before(time.Now()) {
		// No transition or already passed — poll again in 1 minute
		return time.NewTimer(1 * time.Minute)
	}

	d := time.Until(nextTrans)
	if d < time.Second {
		d = time.Second // minimum 1s
	}
	return time.NewTimer(d)
}

// logPassQuality records the actual signal quality observed during the most recent pass
// by querying signal_history for the pass window.
func (ps *PassScheduler) logPassQuality() {
	ps.mu.RLock()
	sched := ps.schedule
	ps.mu.RUnlock()

	if sched == nil || ps.db == nil {
		return
	}

	// Find the most recently completed pass (LOS < now)
	now := time.Now().Unix()
	var lastPass *ScoredPass
	for i := len(sched.UpcomingPasses) - 1; i >= 0; i-- {
		p := &sched.UpcomingPasses[i]
		if p.LOS < now {
			lastPass = p
			break
		}
	}
	if lastPass == nil {
		return
	}

	avg, maxBars, count, err := ps.db.GetSignalDuringWindow("iridium", lastPass.AOS, lastPass.LOS)
	if err != nil || count == 0 {
		return
	}

	// Collect MO attempt/success counters from the gateway
	var moAttempts, moSuccesses int
	if ps.counters != nil {
		a, s := ps.counters.ResetPassCounters()
		moAttempts = int(a)
		moSuccesses = int(s)
	}

	if err := ps.db.InsertPassQualityLog(
		lastPass.Satellite,
		lastPass.AOS,
		lastPass.LOS,
		lastPass.PeakElevDeg,
		avg,
		maxBars,
		moAttempts, moSuccesses,
	); err != nil {
		log.Warn().Err(err).Msg("iridium: failed to log pass quality")
	} else {
		log.Info().
			Str("satellite", lastPass.Satellite).
			Float64("peak_elev", lastPass.PeakElevDeg).
			Float64("avg_bars", avg).
			Int("max_bars", maxBars).
			Int("signal_readings", count).
			Int("mo_attempts", moAttempts).
			Int("mo_successes", moSuccesses).
			Msg("iridium: pass quality logged")
	}
}
