// Package hubroute is the Hub app route on the Bridge (hub_msg, HeMB-NG stage 2, MESHSAT-1584):
// a delivery intent's attempt to a contact's "bridge:<id>" address goes to the Hub on this
// bridge's msg/out, and what the Hub then reports on msg/status is the attempt's evidence;
// what the Hub delivers on msg/in is committed in the inbox (C3), acknowledged on msg/ack, and
// handed to the access rules as interface hub_0. The contract is the Hub's
// docs/bridge-hub-protocol.md "Messaging v1".
package hubroute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/gateway"
	"meshsat/internal/hubmsg"
	"meshsat/internal/hubreporter"
)

// InterfaceID is the route's link, and CorrKind the correlation its evidence carries (the
// operation id).
const (
	InterfaceID = "hub_0"
	CorrKind    = "hub_op"
)

// Publisher is what the route needs from the Hub session (hubreporter.HubReporter).
type Publisher interface {
	PublishMsg(kind string, payload []byte, wait time.Duration) error
	IsConnected() bool
	Principal() string
}

// Config are the route's bounds.
type Config struct {
	// ReceiptAllow bounds an operation: the Hub stops delivering it after this long, so a
	// message the Escalator moved to another route does not arrive hours later as well.
	ReceiptAllow time.Duration
	// PublishWait is how long a publish waits for the broker's PUBACK.
	PublishWait time.Duration
	// Capacity is the inbox's (0 = hubmsg.Capacity).
	Capacity int
	// ClockEstablished is the clock guard's word (nil = established).
	ClockEstablished func() bool
	// Now is the clock (nil = time.Now).
	Now func() time.Time
}

// Route is the hub_0 adapter and the receiver of msg/in and msg/status.
type Route struct {
	db  *database.DB
	pub Publisher
	cfg Config

	mu       sync.Mutex
	evidence func(gateway.IntentReceipt) error
	handoff  func(database.HubHandoff) error
	stored   func(from, text string)
	wake     chan struct{}
	// replay holds the operations owed a repeat whose mark could not be kept either.
	replay map[string]bool
	// asks is each operation's repeats in this process: how many, when, and whether the Hub
	// answered since the last one (a Hub that does not answer is asked less often).
	asks map[string]*replayAsk
	// reconciled is set once this process asked for every operation not settled (its first
	// pass connected to the Hub).
	reconciled bool
}

type replayAsk struct {
	n        int
	at       time.Time
	answered bool
}

// New makes the route.
func New(db *database.DB, pub Publisher, cfg Config) *Route {
	if cfg.ReceiptAllow <= 0 {
		cfg.ReceiptAllow = 120 * time.Second
	}
	if cfg.PublishWait <= 0 {
		cfg.PublishWait = 10 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Route{db: db, pub: pub, cfg: cfg, wake: make(chan struct{}, 1), replay: map[string]bool{}, asks: map[string]*replayAsk{}}
}

func (r *Route) now() time.Time { return r.cfg.Now() }

func (r *Route) established() bool { return r.cfg.ClockEstablished == nil || r.cfg.ClockEstablished() }

// SetHandoff sets who takes a stored message on to the access rules. It returns an error
// while any of the work the message makes could not be stored; the hand-off is then tried
// again, and must make only what is missing (engine's Dispatcher.HubHandoff).
func (r *Route) SetHandoff(fn func(database.HubHandoff) error) {
	r.mu.Lock()
	r.handoff = fn
	r.mu.Unlock()
}

// SetStoredNotify sets who hears of a message the moment it is stored (the live view),
// once per message.
func (r *Route) SetStoredNotify(fn func(from, text string)) {
	r.mu.Lock()
	r.stored = fn
	r.mu.Unlock()
}

// ---- gateway.IntentAdapter ----

func (r *Route) IntentCorrKind() string { return CorrKind }

// IntentNewCorr is a fresh operation id.
func (r *Route) IntentNewCorr() (string, error) { return hubmsg.NewV7(r.now()), nil }

// IntentAdmit: a bridge of the tenant other than this one, a body the Hub takes as it is (text
// up to 16 KiB, not encrypted by a chain), and the Hub session up.
func (r *Route) IntentAdmit(dest string, wire []byte, encrypted bool) (string, string) {
	if hubmsg.BridgeOf(dest) == "" {
		return "skipped_bad_address", fmt.Sprintf("%q is not a bridge:<id> address", dest)
	}
	if dest == r.pub.Principal() {
		return "skipped_bad_address", "the address is this bridge"
	}
	if encrypted || !utf8.Valid(wire) {
		return "skipped_encoding", "the Hub route carries text as it is; this interface's chain makes bytes"
	}
	if len(wire) > hubmsg.MaxBody {
		return "skipped_oversize", fmt.Sprintf("%d bytes, the Hub takes %d", len(wire), hubmsg.MaxBody)
	}
	if !r.pub.IsConnected() {
		return "skipped_offline", "not connected to the Hub"
	}
	return "", ""
}

// IntentSend publishes one operation of the message on msg/out. The broker's PUBACK is
// accepted, not confirmed (C4); the evidence that confirms it comes on msg/status.
func (r *Route) IntentSend(_ context.Context, s gateway.IntentSend) gateway.IntentOutcome {
	now := r.now()
	if s.MessageID == "" || s.MessageExpiresAt.IsZero() {
		return gateway.IntentOutcome{Class: gateway.IntentNotSent, Reason: "not_sent", Detail: "no message id or deadline"}
	}
	midExp := s.MessageExpiresAt.UnixMilli()
	exp := min(midExp, now.Add(r.cfg.ReceiptAllow).UnixMilli())
	if exp <= now.UnixMilli() {
		return gateway.IntentOutcome{Class: gateway.IntentNotSent, Reason: "expired", Detail: "the message's deadline has passed"}
	}
	sub, err := hubmsg.NewSubmission(s.CorrValue, s.MessageID, s.Destination, s.Text, midExp, exp)
	if err != nil {
		return gateway.IntentOutcome{Class: gateway.IntentNotSent, Reason: "not_sent", Detail: err.Error()}
	}
	payload, _ := json.Marshal(sub)
	// The operation is kept before it is published, so evidence that names it, however soon
	// and whatever the outcome of this call, can be checked against it; with it the payload,
	// to repeat unchanged when an event is lost.
	if err := r.db.PutHubOp(database.HubOp{OpID: sub.OpID, Mid: sub.Mid, Recipient: sub.To, IntentID: s.MessageID,
		ExpiresAt: exp, CreatedAt: now.UnixMilli(), Submission: string(payload)}); err != nil {
		return gateway.IntentOutcome{Class: gateway.IntentNotSent, Retryable: true, ConsumeRetry: true, Reason: "not_sent",
			Detail: "the operation could not be kept: " + err.Error()}
	}
	switch err := r.pub.PublishMsg("out", payload, r.cfg.PublishWait); {
	case err == nil:
		return gateway.IntentOutcome{Class: gateway.IntentAccepted, AcceptedBy: "hub_broker", Detail: "the Hub's broker took it",
			Receipt: &gateway.IntentReceipt{CorrKind: CorrKind, CorrValue: sub.OpID, Scope: InterfaceID, Peer: sub.To,
				Source: "hub_broker", Event: "hub_broker_puback", Trust: "session"}}
	case errors.Is(err, hubreporter.ErrMsgNotConnected):
		return gateway.IntentOutcome{Class: gateway.IntentNotSent, Retryable: true, Reason: "skipped_offline", Detail: err.Error()}
	default:
		// Handed to the client, which does not cancel it: it may reach the Hub (C4).
		return gateway.IntentOutcome{Class: gateway.IntentUnknown, Detail: err.Error()}
	}
}

// SetIntentReceiptSink is the adapter interface's sink, which cannot say whether the ledger
// took an answer. The route keeps every event before it is applied and applies it until the
// ledger has it, so it takes SetIntentEvidenceSink, which says; this one is not used.
func (r *Route) SetIntentReceiptSink(func(gateway.IntentReceipt)) {}

// SetIntentEvidenceSink takes the evidence that comes after a send: nil when the ledger has
// it (or nothing in the ledger can take it), an error when it must be applied again.
func (r *Route) SetIntentEvidenceSink(fn func(gateway.IntentReceipt) error) {
	r.mu.Lock()
	r.evidence = fn
	r.mu.Unlock()
}

// ---- msg/status ----

// refusalEventID is a refusal's id among the operation's kept events: one per reason, so
// the copy every Hub replica sends is kept once. A Hub event id is a UUID and never this.
func refusalEventID(opID, reason string) string { return "refusal:" + opID + ":" + reason }

func isRefusal(e hubmsg.Event) bool { return strings.HasPrefix(e.EventID, "refusal:") }

// receiptFor is what a kept event is to the intent ledger. Its key is the event id: the
// ledger records each event once however often it is applied.
func receiptFor(op *database.HubOp, e hubmsg.Event) gateway.IntentReceipt {
	rc := gateway.IntentReceipt{CorrKind: CorrKind, CorrValue: op.OpID, Scope: InterfaceID, Peer: op.Recipient, Trust: "session",
		Key: "hub_event:" + e.EventID}
	if isRefusal(e) {
		// The Hub did not take the submission, so nothing left the Hub: the broker's PUBACK
		// accepted a message nobody carried on, as a radio that refused a packet sent nothing.
		// The attempt has no receipt to wait for, and the message is not sent.
		rc.Source, rc.Event = "hub", "dropped:hub_refusal:"+e.Reason
		return rc
	}
	rc.Source = e.Authority
	switch e.Class {
	case "completed":
		rc.Source, rc.Event, rc.Completes = "recipient_app", e.Kind, database.OutcomeDeliveredApp
	case "failed", "expired":
		rc.Event = "nak:" + e.Kind + ":" + e.Reason
	default:
		rc.Event = e.Kind
	}
	return rc
}

// HandleStatus takes one msg/status payload: an event of one of this bridge's operations, or
// the Hub's refusal of a submission. The event is kept first, as received; the ledger gets it
// next, and an event it did not get stays pending until it does (ApplyPending), so a failed
// write loses nothing. An event that could not even be kept makes the operation owed a
// repeat, which the Hub answers with every event again (C4).
func (r *Route) HandleStatus(payload []byte) {
	ev, ref, ok := hubmsg.ParseStatus(payload)
	if !ok {
		log.Warn().Int("bytes", len(payload)).Msg("hub_msg: a status that is neither an event nor a refusal, ignored")
		return
	}
	opID := ""
	if ev != nil {
		opID = ev.OpID
	} else {
		opID = ref.OpID
	}
	r.noteAnswered(opID)
	op, err := r.db.GetHubOp(opID)
	if err != nil {
		log.Warn().Err(err).Str("op_id", opID).Msg("hub_msg: status not read against its operation; a repeat is owed")
		r.oweReplay(opID)
		return
	}
	if op == nil {
		log.Debug().Str("op_id", opID).Msg("hub_msg: status for an operation this bridge does not keep, ignored")
		return
	}
	var e hubmsg.Event
	valid := true
	if ref != nil {
		if ref.Mid != op.Mid {
			return
		}
		e = hubmsg.Event{OpID: op.OpID, EventID: refusalEventID(op.OpID, ref.Reason), Class: "failed", Kind: "hub_refusal",
			Reason: ref.Reason, Authority: "hub", At: r.now().UTC().Format(time.RFC3339)}
	} else {
		e = *ev
		e.Source = "status_topic"
		valid = hubmsg.Valid(hubmsg.Operation{OpID: op.OpID, Mid: op.Mid, Recipient: op.Recipient}, e)
	}
	kept, err := r.db.AddHubOpEvent(e, valid, r.now())
	if err != nil {
		log.Warn().Err(err).Str("event_id", e.EventID).Msg("hub_msg: event not kept; a repeat is owed")
		r.oweReplay(op.OpID)
		return
	}
	if kept.Valid && settles(kept.Event) {
		r.settle(op.OpID)
	}
	switch {
	case !kept.Valid:
		if kept.Added {
			log.Warn().Str("op_id", op.OpID).Str("kind", e.Kind).Str("class", e.Class).Msg("hub_msg: an event that may not join this operation, kept as rejected")
		}
		return
	case kept.Applied:
		return // the union by event id: a copy of what the ledger has changes nothing
	}
	if kept.Added && ref != nil {
		log.Info().Str("op_id", op.OpID).Str("reason", ref.Reason).Msg("hub_msg: the Hub refused the submission")
	}
	r.apply(op, kept.Event)
}

// apply gives a kept event to the ledger and marks it applied once the ledger has it.
func (r *Route) apply(op *database.HubOp, e hubmsg.Event) {
	r.mu.Lock()
	fn := r.evidence
	r.mu.Unlock()
	if fn == nil {
		return // pending until a sink is set
	}
	if err := fn(receiptFor(op, e)); err != nil {
		log.Warn().Err(err).Str("event_id", e.EventID).Msg("hub_msg: event not in the ledger yet, applied again later")
		return
	}
	if err := r.db.MarkHubOpEventApplied(e.EventID, r.now()); err != nil {
		// The ledger has it under its key: applied again, it is not recorded twice.
		log.Warn().Err(err).Str("event_id", e.EventID).Msg("hub_msg: event not marked applied")
	}
}

// ApplyPending gives the ledger every kept event it does not have yet: after a failed write
// and after a restart, without waiting for the Hub.
func (r *Route) ApplyPending() {
	evs, err := r.db.PendingHubOpEvents(100)
	if err != nil {
		log.Warn().Err(err).Msg("hub_msg: pending events not listed")
		return
	}
	for _, k := range evs {
		op, err := r.db.GetHubOp(k.Event.OpID)
		if err != nil || op == nil {
			continue
		}
		r.apply(op, k.Event)
	}
}

// settles is true for an event of a terminal class: the bridge then keeps the operation's
// outcome, and nothing a replay could bring matters to it.
func settles(e hubmsg.Event) bool {
	return e.Class == "completed" || e.Class == "failed" || e.Class == "expired"
}

// settle clears an operation's owed repeat once its outcome is kept. Only this clears one:
// a repeat going out does not, so an event lost while a repeat is in flight stays owed.
func (r *Route) settle(opID string) {
	if err := r.db.SettleHubOpReplay(opID); err != nil {
		log.Warn().Err(err).Str("op_id", opID).Msg("hub_msg: owed repeat not cleared; cleared on the next pass")
	}
	r.mu.Lock()
	delete(r.replay, opID)
	delete(r.asks, opID)
	r.mu.Unlock()
}

// oweReplay records that an operation is owed a repeat, in the database when it can and in
// memory when even that fails (a process start then finds it among the operations not
// settled).
func (r *Route) oweReplay(opID string) {
	if err := r.db.WantHubOpReplay(opID, r.now()); err == nil {
		return
	}
	r.mu.Lock()
	r.replay[opID] = true
	r.mu.Unlock()
}

// noteAnswered records that the Hub sent something about an operation since its last repeat.
func (r *Route) noteAnswered(opID string) {
	r.mu.Lock()
	if a := r.asks[opID]; a != nil {
		a.answered = true
	}
	r.mu.Unlock()
}

// ReplayWithin bounds the repeats: an operation's events are asked for until this long after
// its deadline. The Hub answers a repeat from what it stores for a day past the deadline
// (C4), so a repeat inside this bound is always a replay, never refused as late; a kit that
// was off the Hub for an evening still gets its outcomes.
const ReplayWithin = 12 * time.Hour

// replayDue says whether an operation may be asked again now: at once while the Hub answers
// each repeat (the database may still be failing), otherwise after 30 s doubling to 30 min.
func (r *Route) replayDue(opID string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.asks[opID]
	if a == nil || a.answered {
		return true
	}
	wait := 30 * time.Second
	for i := 1; i < a.n && wait < 30*time.Minute; i++ {
		wait *= 2
	}
	return now.Sub(a.at) >= min(wait, 30*time.Minute)
}

// RequestReplays asks the Hub again, with the kept submission unchanged, for the events of
// every operation that may be missing one, and the Hub answers with every event under the
// same ids (C4):
//   - the operations owed a repeat, in the database or in memory;
//   - once per process start, every operation whose outcome is not kept, inside the bound:
//     an event lost with its mark leaves nothing else to find.
//
// An operation stops being asked for when its outcome is kept (settle) or at the bound,
// where the bridge records that the outcome is unknown and stops: given up, not recovered.
func (r *Route) RequestReplays() {
	now := r.now()
	r.giveUpPastBound(now)
	if n, err := r.db.SettleHubOpReplays(); err != nil {
		log.Warn().Err(err).Msg("hub_msg: settled repeats not cleared")
	} else if n > 0 {
		log.Info().Int("operations", n).Msg("hub_msg: owed repeats of settled operations cleared")
	}
	if !r.pub.IsConnected() {
		return
	}
	ops, err := r.db.HubOpsOwedReplay(now, ReplayWithin, 50)
	if err != nil {
		log.Warn().Err(err).Msg("hub_msg: owed repeats not listed")
	}
	r.mu.Lock()
	reconciled := r.reconciled
	var held []string
	for id := range r.replay {
		held = append(held, id)
	}
	r.mu.Unlock()
	if !reconciled {
		unsettled, err := r.db.UnsettledHubOps(now, ReplayWithin, 200)
		if err != nil {
			log.Warn().Err(err).Msg("hub_msg: operations without their outcome not listed; asked for at the next pass")
		} else {
			if len(unsettled) > 0 {
				log.Info().Int("operations", len(unsettled)).Msg("hub_msg: operations without their outcome at start; asking the Hub for their events")
			}
			ops = append(ops, unsettled...)
			r.mu.Lock()
			r.reconciled = true
			r.mu.Unlock()
		}
	}
	for _, id := range held {
		op, err := r.db.GetHubOp(id)
		if err != nil {
			continue // still owed
		}
		settled, err := r.db.HubOpSettled(id)
		if err != nil {
			continue
		}
		if op == nil || op.Submission == "" || settled || op.ReplayGivenUpAt > 0 || op.ExpiresAt+ReplayWithin.Milliseconds() < now.UnixMilli() {
			r.mu.Lock()
			delete(r.replay, id)
			r.mu.Unlock()
			continue
		}
		ops = append(ops, *op)
	}
	seen := map[string]bool{}
	for _, op := range ops {
		if seen[op.OpID] || !r.replayDue(op.OpID, now) {
			continue
		}
		seen[op.OpID] = true
		r.mu.Lock()
		a := r.asks[op.OpID]
		if a == nil {
			a = &replayAsk{}
			r.asks[op.OpID] = a
		}
		// From here an answer counts for this repeat, one that comes before the publish
		// returns included.
		a.answered = false
		r.mu.Unlock()
		if err := r.pub.PublishMsg("out", []byte(op.Submission), r.cfg.PublishWait); err != nil {
			log.Warn().Err(err).Str("op_id", op.OpID).Msg("hub_msg: repeat not published, tried again later")
			r.oweReplay(op.OpID)
			r.mu.Lock()
			a.answered = true // nothing went out: not a silent Hub
			r.mu.Unlock()
			continue
		}
		r.mu.Lock()
		a.n, a.at = a.n+1, now
		r.mu.Unlock()
		log.Info().Str("op_id", op.OpID).Msg("hub_msg: submission repeated, the Hub replays its events")
	}
}

// giveUpPastBound ends the asking for each operation past the bound whose outcome this
// bridge does not keep: the ledger gets hub_outcome_unknown (evidence only, once), then the
// operation is marked given up. Its owed mark stays, so it never reads as recovered.
func (r *Route) giveUpPastBound(now time.Time) {
	ops, err := r.db.UnsettledHubOpsPastBound(now, ReplayWithin, 50)
	if err != nil {
		log.Warn().Err(err).Msg("hub_msg: operations past the bound not listed")
		return
	}
	r.mu.Lock()
	fn := r.evidence
	r.mu.Unlock()
	for _, op := range ops {
		if fn == nil {
			return
		}
		rc := gateway.IntentReceipt{CorrKind: CorrKind, CorrValue: op.OpID, Scope: InterfaceID, Peer: op.Recipient, Trust: "session",
			Source: "bridge", Event: "hub_outcome_unknown", Key: "hub_outcome_unknown:" + op.OpID}
		if err := fn(rc); err != nil {
			log.Warn().Err(err).Str("op_id", op.OpID).Msg("hub_msg: unknown outcome not in the ledger yet, recorded again later")
			continue
		}
		if err := r.db.GiveUpHubOpReplay(op.OpID, now); err != nil {
			log.Warn().Err(err).Str("op_id", op.OpID).Msg("hub_msg: given up not marked; marked again later")
			continue
		}
		r.mu.Lock()
		delete(r.replay, op.OpID)
		delete(r.asks, op.OpID)
		r.mu.Unlock()
		log.Warn().Str("op_id", op.OpID).Bool("owed", op.ReplayAt > 0).
			Msg("hub_msg: the Hub's outcome of an operation never reached this kit inside the bound; stopped asking")
	}
}

// ---- msg/in ----

// HandleIn takes one delivery: commit (C3), then the answer on msg/ack, then the hand-off.
func (r *Route) HandleIn(payload []byte) {
	var d hubmsg.Delivery
	if err := json.Unmarshal(payload, &d); err != nil || d.V != 1 {
		log.Warn().Int("bytes", len(payload)).Msg("hub_msg: a delivery that does not parse, ignored")
		return
	}
	if d.To != r.pub.Principal() || hubmsg.BridgeOf(d.From) == "" {
		log.Warn().Str("to", d.To).Str("from", d.From).Msg("hub_msg: a delivery not addressed to this bridge, ignored")
		return
	}
	ans, err := r.db.CommitHubDelivery(d, r.now(), r.established(), r.cfg.Capacity)
	if err != nil {
		// Nothing committed, nothing answered: the Hub delivers it again.
		log.Error().Err(err).Str("op_id", d.OpID).Msg("hub_msg: delivery not committed")
		return
	}
	ack, _ := json.Marshal(hubmsg.Ack{V: 1, OpID: d.OpID, Mid: d.Mid, From: d.From, Result: ans.Result, Reason: ans.Reason})
	if err := r.pub.PublishMsg("ack", ack, r.cfg.PublishWait); err != nil {
		// Committed; the Hub's next delivery of it is answered from the inbox.
		log.Warn().Err(err).Str("op_id", d.OpID).Msg("hub_msg: answer not published")
	}
	log.Info().Str("from", d.From).Str("op_id", d.OpID).Str("result", ans.Result).Str("reason", ans.Reason).
		Bool("stored_now", ans.StoredNow).Bool("within_skew", ans.WithinSkew).Bool("unjudged", ans.Unjudged).Msg("hub_msg: delivery answered")
	if ans.StoredNow {
		r.mu.Lock()
		fn := r.stored
		r.mu.Unlock()
		if fn != nil {
			fn(d.From, d.Body)
		}
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
}

// ---- the hand-off worker ----

// Run hands stored messages to the access rules, gives the ledger the events it does not
// have and repeats the submissions owed a repeat (at start, after each commit and every
// interval, so a crash or a failed write loses nothing), and prunes the inbox and the kept
// operations once an hour.
func (r *Route) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	lastPrune := time.Time{}
	tick := true
	for {
		r.HandOff()
		if tick {
			// The evidence passes run at start and on the interval, not on every commit.
			r.ApplyPending()
			r.RequestReplays()
		}
		if now := r.now(); now.Sub(lastPrune) >= time.Hour {
			if n, err := r.db.PruneHubInbox(now); err != nil {
				log.Warn().Err(err).Msg("hub_msg: inbox prune failed")
			} else if n > 0 {
				log.Info().Int("entries", n).Msg("hub_msg: inbox entries past their retention pruned")
			}
			if _, err := r.db.PruneHubOps(now); err != nil {
				log.Warn().Err(err).Msg("hub_msg: operation prune failed")
			}
			lastPrune = now
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick = true
		case <-r.wake:
			tick = false
		}
	}
}

// HandOff takes every pending stored message to the access rules once.
func (r *Route) HandOff() {
	r.mu.Lock()
	fn := r.handoff
	r.mu.Unlock()
	if fn == nil {
		return
	}
	hs, err := r.db.PendingHubHandoffs(50)
	if err != nil {
		log.Warn().Err(err).Msg("hub_msg: pending hand-offs not listed")
		return
	}
	for _, h := range hs {
		if err := fn(h); err != nil {
			log.Warn().Err(err).Int64("message", h.MessageID).Msg("hub_msg: hand-off failed, tried again later")
			continue
		}
		if err := r.db.MarkHubHandoffDone(h.MessageID, r.now()); err != nil {
			log.Warn().Err(err).Int64("message", h.MessageID).Msg("hub_msg: hand-off not marked")
		}
	}
}

// Enabled reads MESHSAT_HUB_MSG_ROUTE (default false): the route is off until it is turned on
// on both ends (the Hub's HUB_MSG_ROUTE_ENABLED), and with it off nothing subscribes, nothing
// plans onto hub_0, and the link is not made.
func Enabled() bool {
	v, _ := strconv.ParseBool(os.Getenv("MESHSAT_HUB_MSG_ROUTE"))
	return v
}

// EnsureInterface makes the route's link, hub_0, once: device-less, online when on, like
// lxmf_0 and cell_0, so an access rule can carry what comes in on it.
func EnsureInterface(db *database.DB) error {
	if _, err := db.GetInterface(InterfaceID); err == nil {
		return nil
	}
	return db.InsertInterface(&database.Interface{ID: InterfaceID, ChannelType: "hub", Label: "MeshSat Hub messages", Enabled: true,
		Config: "{}", IngressTransforms: "[]", EgressTransforms: "[]"})
}
