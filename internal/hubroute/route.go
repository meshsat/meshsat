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

	mu      sync.Mutex
	sink    func(gateway.IntentReceipt)
	handoff func(database.HubHandoff) error
	wake    chan struct{}
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
	return &Route{db: db, pub: pub, cfg: cfg, wake: make(chan struct{}, 1)}
}

func (r *Route) now() time.Time { return r.cfg.Now() }

func (r *Route) established() bool { return r.cfg.ClockEstablished == nil || r.cfg.ClockEstablished() }

// SetHandoff sets who takes a stored message on to the access rules.
func (r *Route) SetHandoff(fn func(database.HubHandoff) error) {
	r.mu.Lock()
	r.handoff = fn
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
	// and whatever the outcome of this call, can be checked against it.
	if err := r.db.PutHubOp(database.HubOp{OpID: sub.OpID, Mid: sub.Mid, Recipient: sub.To, IntentID: s.MessageID,
		ExpiresAt: exp, CreatedAt: now.UnixMilli()}); err != nil {
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

// SetIntentReceiptSink takes the evidence that comes after a send.
func (r *Route) SetIntentReceiptSink(fn func(gateway.IntentReceipt)) {
	r.mu.Lock()
	r.sink = fn
	r.mu.Unlock()
}

func (r *Route) record(rc gateway.IntentReceipt) {
	r.mu.Lock()
	fn := r.sink
	r.mu.Unlock()
	if fn != nil {
		fn(rc)
	}
}

// ---- msg/status ----

// HandleStatus takes one msg/status payload: an event of one of this bridge's operations, or
// the Hub's refusal of a submission.
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
	op, err := r.db.GetHubOp(opID)
	if err != nil || op == nil {
		log.Debug().Str("op_id", opID).Msg("hub_msg: status for an operation this bridge does not keep, ignored")
		return
	}
	rc := gateway.IntentReceipt{CorrKind: CorrKind, CorrValue: op.OpID, Scope: InterfaceID, Peer: op.Recipient, Trust: "session"}
	if ref != nil {
		if ref.Mid != op.Mid {
			return
		}
		// The Hub did not take this submission: the attempt has no receipt to wait for.
		rc.Source, rc.Event = "hub", "nak:hub_refusal:"+ref.Reason
		log.Info().Str("op_id", op.OpID).Str("reason", ref.Reason).Msg("hub_msg: the Hub refused the submission")
		r.record(rc)
		return
	}
	ev.Source = "status_topic"
	valid := hubmsg.Valid(hubmsg.Operation{OpID: op.OpID, Mid: op.Mid, Recipient: op.Recipient}, *ev)
	added, err := r.db.AddHubOpEvent(*ev, valid, r.now())
	if err != nil {
		log.Warn().Err(err).Str("event_id", ev.EventID).Msg("hub_msg: event not kept")
		return
	}
	if !valid {
		log.Warn().Str("op_id", op.OpID).Str("kind", ev.Kind).Str("class", ev.Class).Msg("hub_msg: an event that may not join this operation, kept as rejected")
		return
	}
	if !added {
		return // the union by event id: a copy changes nothing
	}
	rc.Source = ev.Authority
	switch ev.Class {
	case "completed":
		rc.Source, rc.Event, rc.Completes = "recipient_app", ev.Kind, database.OutcomeDeliveredApp
	case "failed", "expired":
		rc.Event = "nak:" + ev.Kind + ":" + ev.Reason
	default:
		rc.Event = ev.Kind
	}
	r.record(rc)
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
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
}

// ---- the hand-off worker ----

// Run hands stored messages to the access rules (at start, after each commit and every
// interval, so a crash between commit and hand-off loses nothing) and prunes the inbox and
// the kept operations once an hour.
func (r *Route) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	lastPrune := time.Time{}
	for {
		r.HandOff()
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
		case <-r.wake:
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
