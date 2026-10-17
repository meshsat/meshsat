package engine

// The Escalator owns delivery intents (architecture r3 D4, section 3a; the
// implementation requirements of 16 Oct 2026, G1, G2, C1). It is the only
// code that changes an intent or creates or retires an intent's attempt
// rows. Every decision is made from durable state in one transaction
// (database.ApplyIntentChange), guarded by the intent's version: an event
// only says "look again", so a lost event is late, never lost, and an event
// about an old round schedules nothing (T15). The delivery worker sends one
// row and finalizes its transmission; evidence handlers store evidence.
// Stage 1a: Meshtastic unicast and LXMF, behind MESHSAT_DELIVERY_INTENTS.
// [MESHSAT-1581]

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/transport"
)

// IntentClock is the Escalator's time: Now is read for elapsed time only
// (Go's monotonic clock in production), Wall and WallTrusted for the
// downtime across a restart (r3 D7).
type IntentClock interface {
	Now() time.Time
	Wall() time.Time
	WallTrusted() bool
}

type systemIntentClock struct{ trusted func() bool }

func (systemIntentClock) Now() time.Time  { return time.Now() }
func (systemIntentClock) Wall() time.Time { return time.Now() }
func (c systemIntentClock) WallTrusted() bool {
	return c.trusted == nil || c.trusted()
}

// SystemIntentClock is the production clock; trusted is the clock guard's
// verdict on the wall clock (nil = trusted).
func SystemIntentClock(trusted func() bool) IntentClock { return systemIntentClock{trusted: trusted} }

// EscalatorConfig holds the Escalator's allowances (r3 D7). The defaults
// are a first proposal (r3 open question 2: allowances per precedence are
// the owner's call); every one has an env var.
type EscalatorConfig struct {
	Heartbeat     time.Duration // MESHSAT_INTENT_HEARTBEAT_S, 10
	QueueAllowS   int           // MESHSAT_INTENT_QUEUE_ALLOW_S, 120
	ReceiptAllowS int           // MESHSAT_INTENT_RECEIPT_ALLOW_S, 120
	ExpiryAllowS  int           // MESHSAT_INTENT_EXPIRY_S, 3600
	MeshMTU       int           // MESHSAT_MESH_MTU, 100
	// RestartSlack is what a restart adds to every active intent's
	// elapsed time: the most a missed heartbeat can hide.
	RestartSlack time.Duration
	MaxDowntime  time.Duration // 24 h
}

// DefaultEscalatorConfig reads the env vars, with the defaults above.
func DefaultEscalatorConfig() EscalatorConfig {
	envInt := func(k string, def int) int {
		if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
			return v
		}
		return def
	}
	hb := time.Duration(envInt("MESHSAT_INTENT_HEARTBEAT_S", 10)) * time.Second
	return EscalatorConfig{
		Heartbeat:     hb,
		QueueAllowS:   envInt("MESHSAT_INTENT_QUEUE_ALLOW_S", 120),
		ReceiptAllowS: envInt("MESHSAT_INTENT_RECEIPT_ALLOW_S", 120),
		ExpiryAllowS:  envInt("MESHSAT_INTENT_EXPIRY_S", 3600),
		MeshMTU:       meshBearerMTU(),
		RestartSlack:  hb,
		MaxDowntime:   24 * time.Hour,
	}
}

// DeliveryIntentsEnabled reads MESHSAT_DELIVERY_INTENTS (default false).
func DeliveryIntentsEnabled() bool {
	v, _ := strconv.ParseBool(os.Getenv("MESHSAT_DELIVERY_INTENTS"))
	return v
}

type clockBase struct {
	ms int64
	at time.Time
}

// Escalator owns the delivery intents.
type Escalator struct {
	db        *database.DB
	d         *Dispatcher
	cfg       EscalatorConfig
	clock     IntentClock
	accepting bool
	notify    chan string

	mu    sync.Mutex
	bases map[string]clockBase

	// ready is set once the dispatcher's LXMF sender and recipient
	// resolver are wired: before that a route's usability cannot be
	// judged, so nothing is decided (a notice is picked up later).
	ready atomic.Bool
}

// SetReady lets the Escalator decide, and looks at every active intent.
func (e *Escalator) SetReady() {
	if e == nil {
		return
	}
	e.ready.Store(true)
	e.EvaluateAll()
}

// NewEscalator builds the Escalator. accepting is MESHSAT_DELIVERY_INTENTS:
// false keeps it running only to finish the intents already in the
// database (one owner during a rollback, S10), and send-to-contact uses the
// legacy path.
func NewEscalator(db *database.DB, d *Dispatcher, cfg EscalatorConfig, clock IntentClock, accepting bool) *Escalator {
	if clock == nil {
		clock = SystemIntentClock(nil)
	}
	if cfg.Heartbeat <= 0 {
		cfg = DefaultEscalatorConfig()
	}
	return &Escalator{db: db, d: d, cfg: cfg, clock: clock, accepting: accepting,
		notify: make(chan string, 256), bases: map[string]clockBase{}}
}

// Accepting reports whether new sends become intents.
func (e *Escalator) Accepting() bool { return e != nil && e.accepting }

// Notify asks the Escalator to look at an intent again. Never blocks: a
// dropped notice is picked up by the next heartbeat. Every caller has just
// written about the intent (a new intent, an attempt's end, matched
// evidence), so a status view is told too.
func (e *Escalator) Notify(intentID string) {
	if e == nil || intentID == "" {
		return
	}
	e.Changed(intentID, "written")
	select {
	case e.notify <- intentID:
	default:
	}
}

// Changed tells the event stream that something about an intent was
// written (change: "written", "handed_off"), so a status view reads it
// again now instead of at its next poll; a closed view that stopped polling
// learns of late evidence this way. A hint only: the view's read is the
// authority, and a slow reader that drops the event still has its poll.
// [MESHSAT-1584]
func (e *Escalator) Changed(intentID, change string) {
	if e == nil || intentID == "" || e.d == nil || e.d.emit == nil {
		return
	}
	data, _ := json.Marshal(map[string]string{"intent_id": intentID, "change": change})
	e.d.emit(transport.MeshEvent{
		Type:    "delivery_intent",
		Message: fmt.Sprintf("Delivery intent %s: %s", intentID, change),
		Data:    data,
		Time:    time.Now().UTC().Format(time.RFC3339),
	})
}

// elapsedMs is an intent's elapsed time now: its persisted value carried
// forward on the monotonic clock.
func (e *Escalator) elapsedMs(id string, persisted int64) int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.clock.Now()
	b, ok := e.bases[id]
	if !ok || b.ms < persisted {
		b = clockBase{ms: persisted, at: now}
		e.bases[id] = b
	}
	return b.ms + now.Sub(b.at).Milliseconds()
}

func (e *Escalator) dropBase(id string) {
	e.mu.Lock()
	delete(e.bases, id)
	e.mu.Unlock()
}

// Recover runs once at start, after the legacy recovery and before any
// delivery worker (r3 D5): it settles the attempts a stop left in flight
// (database.RecoverIntentDeliveries, G1), moves every active intent's
// clock across the downtime (r3 D7) and looks at every active intent.
func (e *Escalator) Recover() error {
	st, err := e.db.RecoverIntentDeliveries()
	if err != nil {
		return err
	}
	if st != (database.RecoverStats{}) {
		log.Warn().Int("never_authorized", st.NeverAuthorized).Int("authorized", st.Authorized).
			Int("handed_off", st.HandedOff).Int("replayed", st.Replayed).
			Msg("delivery intents: settled the attempts a stop left in flight")
	}
	clocks, err := e.db.ActiveIntentClocks()
	if err != nil {
		return err
	}
	persist := map[string]int64{}
	for _, c := range clocks {
		ms := c.ElapsedMs + e.cfg.RestartSlack.Milliseconds()
		if c.Trusted && c.HeartbeatWallAt != nil && e.clock.WallTrusted() {
			down := e.clock.Wall().Sub(*c.HeartbeatWallAt)
			if down < 0 {
				down = 0
			}
			if down > e.cfg.MaxDowntime {
				down = e.cfg.MaxDowntime
			}
			ms += down.Milliseconds()
		}
		if err := e.db.SetIntentElapsed(c.ID, ms); err != nil {
			return err
		}
		e.mu.Lock()
		e.bases[c.ID] = clockBase{ms: ms, at: e.clock.Now()}
		e.mu.Unlock()
		persist[c.ID] = ms
	}
	if err := e.db.PersistIntentClocks(persist, e.clock.Wall(), e.clock.WallTrusted()); err != nil {
		return err
	}
	if e.ready.Load() {
		e.EvaluateAll()
	}
	return nil
}

// Run is the Escalator's loop: a heartbeat every cfg.Heartbeat and a look
// at every intent it is notified about.
func (e *Escalator) Run(ctx context.Context) {
	t := time.NewTicker(e.cfg.Heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.Heartbeat()
		case id := <-e.notify:
			e.Evaluate(id)
		}
	}
}

// Heartbeat persists every active intent's clock and looks again at every
// active intent and every closed one something was written about since
// (dirty): a notice is only a hint, durable state is what is acted on, so
// a dropped notice or a restart cannot strand a receipt (red-team F-3).
func (e *Escalator) Heartbeat() {
	ids, err := e.db.ActiveIntentIDs()
	if err != nil {
		log.Error().Err(err).Msg("delivery intents: heartbeat")
		return
	}
	persist := map[string]int64{}
	for _, id := range ids {
		in, err := e.db.GetIntent(id)
		if err != nil {
			continue
		}
		persist[id] = e.elapsedMs(id, in.ElapsedMs)
	}
	if err := e.db.PersistIntentClocks(persist, e.clock.Wall(), e.clock.WallTrusted()); err != nil {
		log.Error().Err(err).Msg("delivery intents: persist clocks")
	}
	e.EvaluateAll()
}

// ElapsedNow is an intent's elapsed time now, on the Escalator's clock: the
// worker stamps an acceptance with it.
func (e *Escalator) ElapsedNow(id string) (int64, bool) {
	in, err := e.db.GetIntent(id)
	if err != nil || in.State != database.IntentActive {
		return 0, false
	}
	return e.elapsedMs(id, in.ElapsedMs), true
}

// EvaluateAll looks at every active intent and every dirty closed one.
func (e *Escalator) EvaluateAll() {
	ids, err := e.db.IntentsToReview()
	if err != nil {
		log.Error().Err(err).Msg("delivery intents: list")
		return
	}
	for _, id := range ids {
		e.Evaluate(id)
	}
}

// Evaluate applies whatever transitions the intent's durable state calls
// for, one transaction each, until none applies.
func (e *Escalator) Evaluate(id string) {
	if !e.ready.Load() {
		return
	}
	for i := 0; i < 8; i++ {
		in, err := e.db.GetIntent(id)
		if err != nil {
			return
		}
		if in.State != database.IntentActive {
			e.evaluateClosed(id)
			e.dropBase(id)
			return
		}
		now := e.elapsedMs(id, in.ElapsedMs)
		elig := e.eligibility(in)
		ch, err := e.db.ApplyIntentChange(id, func(s *database.IntentSnapshot) *database.IntentChange {
			return decideIntent(s, now, elig, e.buildRow)
		})
		if err != nil {
			log.Error().Err(err).Str("intent", id).Msg("delivery intents: transition failed")
			return
		}
		if ch == nil || ch.Transition == "" {
			return
		}
		e.logTransition(id, ch)
		if ch.Close {
			e.dropBase(id)
			return
		}
	}
}

// evaluateClosed applies T14: evidence (or an in-flight attempt's
// outcome) that arrives after an intent closed is recorded and may upgrade
// its outcome by the D11 rule; it never reopens the intent, never sends.
// One transaction, which also clears the intent's dirty flag.
func (e *Escalator) evaluateClosed(id string) {
	up, err := e.db.ReviewClosedIntent(id, func(s *database.IntentSnapshot) (string, bool) {
		return outcomeRule(s), possibleDuplicate(s)
	}, func(a, b string) bool { return outcomeStrength(a) > outcomeStrength(b) })
	if err != nil {
		log.Error().Err(err).Str("intent", id).Msg("delivery intents: closed review failed")
		return
	}
	if up != "" {
		log.Info().Str("intent", id).Str("to", up).Msg("delivery intent: late evidence upgraded the outcome (T14)")
		e.emitIntent(id, "T14", up)
	}
}

// Cancel closes an intent (T13): its unclaimed attempts are retired as
// cancelled; an attempt in flight finishes and is recorded, and the status
// says it may still be sent. The outcome is the D11 rule's, with cancelled
// in place of failed (C1).
func (e *Escalator) Cancel(id, reason string) (*database.DeliveryIntent, error) {
	in, err := e.db.GetIntent(id)
	if err != nil {
		return nil, err
	}
	if in.State != database.IntentActive {
		return in, fmt.Errorf("intent %s is %s", id, in.State)
	}
	if reason == "" {
		reason = "cancelled by the sender"
	}
	now := e.elapsedMs(id, in.ElapsedMs)
	ch, err := e.db.ApplyIntentChange(id, func(s *database.IntentSnapshot) *database.IntentChange {
		if s.Intent.State != database.IntentActive {
			return nil
		}
		out := outcomeRule(s)
		if out == database.OutcomeFailed {
			out = database.OutcomeCancelled
		}
		return &database.IntentChange{Transition: "T13", ExpectVersion: s.Intent.Version, ElapsedMs: now,
			RetireAll: true, RetireStatus: database.AttemptCancelled, RetireReason: "cancelled",
			SetOutcome: out, Close: true, CancelReason: reason}
	})
	if err != nil {
		return nil, err
	}
	if ch == nil || ch.Transition == "" {
		in, _ = e.db.GetIntent(id)
		return in, fmt.Errorf("intent %s changed while it was being cancelled", id)
	}
	e.logTransition(id, ch)
	e.dropBase(id)
	return e.db.GetIntent(id)
}

func (e *Escalator) logTransition(id string, ch *database.IntentChange) {
	ev := log.Info().Str("intent", id).Str("transition", ch.Transition)
	if ch.SetOutcome != "" {
		ev = ev.Str("outcome", ch.SetOutcome)
	}
	if ch.NewRow != nil {
		ev = ev.Str("next_route", ch.NewRow.Channel).Int("round", ch.NewActiveRound)
	}
	ev.Bool("closed", ch.Close).Msg("delivery intent transition")
	e.emitIntent(id, ch.Transition, ch.SetOutcome)
}

func (e *Escalator) emitIntent(id, transition, outcome string) {
	if e.d == nil || e.d.emit == nil {
		return
	}
	e.d.emit(transport.MeshEvent{
		Type:    "delivery_intent",
		Message: fmt.Sprintf("Delivery intent %s: %s", id, transition),
		Data:    []byte(fmt.Sprintf(`{"intent_id":%q,"transition":%q,"outcome":%q}`, id, transition, outcome)),
		Time:    time.Now().UTC().Format(time.RFC3339),
	})
}

// ---- the decision (pure) ----

type rowBuilder func(in database.DeliveryIntent, entry database.RoutePlanEntry, round, attemptNo int) database.MessageDelivery

type rowFacts struct {
	row       *database.IntentAttempt
	entry     database.RoutePlanEntry
	handedOff bool // a transmission of this row reached the adapter
	completed bool // matched completion evidence on one of its transmissions
	noReceipt bool // matched evidence that no receipt will come (a NAK)
	dropped   bool // matched evidence that our own radio sent nothing
}

func planEntry(plan []database.RoutePlanEntry, idx int) database.RoutePlanEntry {
	for _, p := range plan {
		if p.RouteIndex == idx {
			return p
		}
	}
	return database.RoutePlanEntry{RouteIndex: idx}
}

func factsOf(s *database.IntentSnapshot) []rowFacts {
	byRow := map[int64]*rowFacts{}
	out := make([]rowFacts, len(s.Rows))
	for i := range s.Rows {
		out[i] = rowFacts{row: &s.Rows[i], entry: planEntry(s.Intent.RoutePlan, s.Rows[i].RouteIndex)}
		byRow[s.Rows[i].ID] = &out[i]
	}
	for _, t := range s.Transmissions {
		if f := byRow[t.AttemptID]; f != nil && (t.State == database.TxHandedOff ||
			(t.State == database.TxDone && t.OutcomeClass != database.ClassNotSent)) {
			f.handedOff = true
		}
	}
	for _, ev := range s.Evidence {
		f := byRow[ev.AttemptID]
		if f == nil || !ev.Matched {
			continue
		}
		switch {
		case ev.Completes != "":
			f.completed = true
		case strings.HasPrefix(ev.Event, "nak"):
			f.noReceipt = true
		case strings.HasPrefix(ev.Event, "dropped"):
			f.noReceipt, f.dropped = true, true
		}
	}
	return out
}

func unclaimed(status string) bool {
	return status == "queued" || status == "retry" || status == "held"
}

func receiptEnded(f rowFacts, now int64) bool {
	if f.noReceipt {
		return true
	}
	if f.row.AcceptedElapsedMs == nil {
		return false
	}
	return now-*f.row.AcceptedElapsedMs >= int64(f.entry.ReceiptAllowS)*1000
}

// settled is the requirements' G2 definition.
func settled(f rowFacts, now int64) bool {
	switch f.row.Status {
	case database.AttemptNotSent, database.AttemptUnknown, database.AttemptSuperseded, database.AttemptCancelled:
		return true
	case database.AttemptAccepted:
		if f.completed {
			return true
		}
		if f.entry.CompletionRule == "" && !f.entry.EscalateUnconfirmed {
			return true
		}
		return receiptEnded(f, now)
	}
	return false // queued, retry, held, sending
}

// mayEscalateAfterAcceptance is T5's guard: the route's rule expects a
// receipt and the route is free, or the policy opts in (for a paid route,
// or a route without receipts, r3 D6).
func mayEscalateAfterAcceptance(e database.RoutePlanEntry) bool {
	if e.CompletionRule != "" {
		return !e.Paid || e.EscalateUnconfirmed
	}
	return e.EscalateUnconfirmed
}

// nextRoute is the next admitted route after idx that is still usable.
func nextRoute(plan []database.RoutePlanEntry, idx int, eligible map[int]bool) (database.RoutePlanEntry, bool) {
	for _, p := range plan {
		if p.RouteIndex > idx && p.Admitted && eligible[p.RouteIndex] {
			return p, true
		}
	}
	return database.RoutePlanEntry{}, false
}

// outcomeRule is r3 D11's rule: the first matched completion evidence's
// outcome; else accepted_unconfirmed if any transmission was accepted; else
// may_have_been_sent if any is unknown; else failed (no transmission left
// the device).
func outcomeRule(s *database.IntentSnapshot) string {
	for _, ev := range s.Evidence {
		if ev.Matched && ev.Completes != "" {
			return ev.Completes
		}
	}
	dropped := droppedTransmissions(s)
	accepted, unknown := false, false
	for _, t := range s.Transmissions {
		switch {
		case dropped[t.ID]:
			// Our own radio sent nothing: not accepted by anyone.
		case t.State == database.TxDone && t.OutcomeClass == database.ClassAccepted:
			accepted = true
		case t.State == database.TxDone && t.OutcomeClass == database.ClassUnknown, t.State == database.TxHandedOff:
			unknown = true
		}
	}
	switch {
	case accepted:
		return database.OutcomeAcceptedUnconfirmed
	case unknown:
		return database.OutcomeMayHaveBeenSent
	}
	return database.OutcomeFailed
}

// droppedTransmissions are the transmissions our own radio refused and sent
// nothing of (matched "dropped" evidence, classifyMeshReceipt): the serial
// handoff made them accepted, the radio's own NAK says nothing left.
func droppedTransmissions(s *database.IntentSnapshot) map[int64]bool {
	m := map[int64]bool{}
	for _, ev := range s.Evidence {
		if ev.Matched && ev.TransmissionID != 0 && strings.HasPrefix(ev.Event, "dropped") {
			m[ev.TransmissionID] = true
		}
	}
	return m
}

// outcomeStrength orders outcomes for T14: evidence beats acceptance beats
// possibly sent beats the rest.
func outcomeStrength(o string) int {
	switch o {
	case database.OutcomeDeliveredHandset, database.OutcomeDeliveredDevice, database.OutcomeAcceptedDestinationNode, database.OutcomeAckedStation:
		return 4
	case database.OutcomeAcceptedUnconfirmed:
		return 3
	case database.OutcomeMayHaveBeenSent:
		return 2
	case database.OutcomeFailed, database.OutcomeExpired, database.OutcomeCancelled:
		return 1
	}
	return 0
}

// possibleDuplicate: two or more rows of the intent each have a
// transmission that was accepted or whose outcome is unknown, so the
// recipient may get the message twice. Once set it stays set.
func possibleDuplicate(s *database.IntentSnapshot) bool {
	if s.Intent.PossibleDuplicate {
		return true
	}
	rows := map[int64]bool{}
	dropped := droppedTransmissions(s)
	for _, t := range s.Transmissions {
		if dropped[t.ID] {
			continue // our own radio sent nothing of it
		}
		if t.State == database.TxHandedOff || (t.State == database.TxDone && t.OutcomeClass != database.ClassNotSent) {
			rows[t.AttemptID] = true
		}
	}
	return len(rows) >= 2
}

func maxAttemptNo(s *database.IntentSnapshot) int {
	n := 0
	for _, r := range s.Rows {
		if r.AttemptNo > n {
			n = r.AttemptNo
		}
	}
	return n
}

// decideIntent chooses at most one transition of r3 section 3a from an
// intent's durable state, with the bookkeeping (acceptance stamps, the
// possible-duplicate flag) that goes with looking at it.
func decideIntent(s *database.IntentSnapshot, now int64, eligible map[int]bool, build rowBuilder) *database.IntentChange {
	in := &s.Intent
	if in.State != database.IntentActive {
		return nil
	}
	ch := &database.IntentChange{ExpectVersion: in.Version, ElapsedMs: now}
	for i := range s.Rows {
		r := &s.Rows[i]
		if r.Status == database.AttemptAccepted && r.AcceptedElapsedMs == nil {
			if ch.StampAccepted == nil {
				ch.StampAccepted = map[int64]int64{}
			}
			ch.StampAccepted[r.ID] = now
			v := now
			r.AcceptedElapsedMs = &v
		}
	}
	facts := factsOf(s)
	outcome := outcomeRule(s)
	dup := possibleDuplicate(s)
	transition := func(name string) *database.IntentChange {
		ch.Transition = name
		if dup != in.PossibleDuplicate {
			d := dup
			ch.PossibleDuplicate = &d
		}
		return ch
	}
	closeWith := func(name, out, retireReason string) *database.IntentChange {
		ch.Close, ch.SetOutcome = true, out
		ch.RetireAll, ch.RetireStatus, ch.RetireReason = true, database.AttemptSuperseded, retireReason
		return transition(name)
	}
	strategy := directory.Strategy(in.EffectiveStrategy)

	// T9 / T10: matched completion evidence from any transmission the
	// intent owns, in any round.
	for _, f := range facts {
		if !f.completed {
			continue
		}
		if strategy != directory.StrategyAllBearers {
			return closeWith("T9", outcome, "superseded")
		}
		if in.Outcome != outcome {
			ch.SetOutcome = outcome
			return transition("T10")
		}
		break
	}

	// T12: the intent's expiry allowance.
	if in.ExpiryAllowS > 0 && now >= int64(in.ExpiryAllowS)*1000 {
		out := outcome
		if out == database.OutcomeFailed || out == database.OutcomePending {
			out = database.OutcomeExpired
		}
		return closeWith("T12", out, "expired")
	}

	var active []rowFacts
	for _, f := range facts {
		if f.row.Round == in.ActiveRound {
			active = append(active, f)
		}
	}

	// T7: a paid route's outcome is unknown (PRIMARY_ONLY and
	// ORDERED_FALLBACK; in ALL_BEARERS the row is settled and T11 decides).
	if strategy != directory.StrategyAllBearers {
		for _, f := range active {
			if f.row.Status == database.AttemptUnknown && f.entry.Paid {
				return closeWith("T7", outcome, "withheld_outcome_unknown")
			}
		}
	}

	if strategy == directory.StrategyOrderedFallback && len(active) == 1 {
		f := active[0]
		next, hasNext := nextRoute(in.RoutePlan, f.entry.RouteIndex, eligible)
		advance := func(name, retireReason string) *database.IntentChange {
			row := build(*in, next, in.ActiveRound+1, maxAttemptNo(s)+1)
			ch.NewRow = &row
			ch.NewActiveRound = in.ActiveRound + 1
			ch.RoundStartedMs = now
			ch.Retire = []int{in.ActiveRound}
			ch.RetireStatus, ch.RetireReason = database.AttemptSuperseded, retireReason
			return transition(name)
		}
		switch {
		case f.row.Status == database.AttemptNotSent && hasNext:
			return advance("T3", "superseded")
		case f.row.Status == database.AttemptUnknown && !f.entry.Paid && hasNext:
			return advance("T8", "superseded")
		case unclaimed(f.row.Status) && !f.handedOff && hasNext &&
			now-in.RoundStartedMs >= int64(f.entry.QueueAllowS)*1000:
			return advance("T4", "skipped_offline")
		case f.row.Status == database.AttemptAccepted && !f.completed && receiptEnded(f, now):
			if mayEscalateAfterAcceptance(f.entry) && hasNext {
				return advance("T5", "superseded")
			}
			if f.entry.Paid && f.entry.CompletionRule != "" && !f.entry.EscalateUnconfirmed {
				return closeWith("T6", outcome, "superseded")
			}
		}
	}

	// T11: every row of the active round is settled, and there is no next
	// route (any advance above came first).
	if len(active) > 0 {
		all := true
		for _, f := range active {
			if !settled(f, now) {
				all = false
				break
			}
		}
		if all {
			return closeWith("T11", outcome, "superseded")
		}
	}

	if ch.StampAccepted != nil || dup != in.PossibleDuplicate {
		if dup != in.PossibleDuplicate {
			d := dup
			ch.PossibleDuplicate = &d
		}
		return ch
	}
	return nil
}
