package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"

	"meshsat/internal/codec"
	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/gateway"
	"meshsat/internal/rules"
)

// Stage 1b routes carried by a gateway's intent adapter (gateway.IntentAdapter):
// admission against the gateway's own limits, a correlation chosen before
// the send, one submission per call, answers after the call through the
// receipt sink. [MESHSAT-1584]

const (
	modeAPRS = "aprs"
	modeSMS  = "sms"
)

// gatewayModes are the adapter modes a gateway carries.
var gatewayModes = map[string]bool{modeAPRS: true, modeSMS: true}

// billed reports whether an adapter's submissions cost money (B9).
func billed(ad gateway.IntentAdapter) bool {
	b, ok := ad.(gateway.IntentBilled)
	return ok && b.IntentBilled()
}

// intentAdapter is the running gateway of an interface when it can carry
// intents, else nil.
func (d *Dispatcher) intentAdapter(ifaceID string) gateway.IntentAdapter {
	if d.gwProv == nil {
		return nil
	}
	gw := d.gwProv.GatewayByInterfaceID(ifaceID)
	if gw == nil {
		return nil
	}
	ad, _ := gw.(gateway.IntentAdapter)
	return ad
}

// wireBytes is the payload as the interface's egress chain makes it (the
// real chain; its output length does not depend on the key), with the
// version byte the worker adds after a chain.
func (e *Escalator) wireBytes(chain string, payload []byte) ([]byte, string, error) {
	if chain == "" || chain == "[]" {
		return payload, "", nil
	}
	sum := sha256.Sum256([]byte(chain))
	if e.d.transforms == nil {
		return nil, "", errors.New("no transform pipeline")
	}
	out, err := e.d.transforms.ApplyEgress(payload, chain)
	if err != nil {
		return nil, "", err
	}
	return codec.PrependVersionByte(out), hex.EncodeToString(sum[:]), nil
}

// admitGateway admits a route carried by a gateway adapter.
func (e *Escalator) admitGateway(p *database.RoutePlanEntry, kind directory.Kind, payload []byte) {
	ifaceID, err := e.d.interfaceForKind(kind)
	if err != nil {
		p.Reason = reasonOffline
		return
	}
	p.InterfaceID = ifaceID
	iface, err := e.d.db.GetInterface(ifaceID)
	if err != nil || !iface.Enabled {
		p.Reason = reasonOffline
		return
	}
	ad := e.d.intentAdapter(ifaceID)
	if ad == nil {
		p.Reason = reasonOffline // its gateway is not running
		return
	}
	wire, chainSHA, err := e.wireBytes(iface.EgressTransforms, payload)
	if err != nil {
		p.Reason = reasonEncoding
		return
	}
	p.EstWireBytes, p.TransformSHA256 = len(wire), chainSHA
	if reason, _ := ad.IntentAdmit(p.Destination, wire, ChainEncrypts(iface.EgressTransforms)); reason != "" {
		p.Reason = reason
		return
	}
	if billed(ad) {
		// A paid route is priced with its interface's tariff; one without a
		// tariff is never priced at zero (r3 D8). [MESHSAT-1584]
		p.Paid = true
		t, err := e.d.db.CurrentTariff(ifaceID)
		if err != nil || t == nil {
			p.Reason = reasonNoPrice
			return
		}
		// A tariff that cannot price this intent (another currency, out of
		// bounds, a cost past int64) is no price (B9SMS-R3, R5).
		units, minor, err := t.Cost(len(wire))
		if err != nil {
			log.Warn().Err(err).Str("iface", ifaceID).Int64("tariff", t.ID).Msg("intent: the tariff cannot price this route")
			p.Reason = reasonNoPrice
			return
		}
		p.UnitKind, p.EstUnits, p.TariffID, p.TariffVersion, p.EstMinor = t.UnitKind, units, t.ID, t.Version, minor
		p.Paid = !t.Free
	}
	p.Admitted = true
}

// intentPrice is what this attempt costs now (r3 D8 reprice: the current
// tariff, the wire bytes after the chain as it is now). tariff is nil for a
// free route; refuse is set when a paid route has no tariff, or one that
// cannot price this intent: a currency other than the intent's (checked
// again here, at the authorization, B9SMS-R3), or out of bounds (R5).
func (w *DeliveryWorker) intentPrice(ad gateway.IntentAdapter, del database.MessageDelivery, text string) (tariff *database.Tariff, units int, minor int64, refuse *intentRefusal) {
	if ad == nil || !billed(ad) {
		return nil, 0, 0, nil
	}
	t, err := w.db.CurrentTariff(w.channelID)
	if err != nil || t == nil {
		return nil, 0, 0, &intentRefusal{Reason: reasonNoPrice, Detail: "the route has no tariff"}
	}
	in, err := w.db.GetIntent(del.IntentID)
	if err != nil {
		return nil, 0, 0, &intentRefusal{Reason: reasonNoPrice, Detail: "the intent could not be read: " + err.Error()}
	}
	if t.Currency != in.Currency {
		return nil, 0, 0, &intentRefusal{Reason: reasonNoPrice,
			Detail: fmt.Sprintf("the tariff is in %s and the intent in %s; nothing is converted", t.Currency, in.Currency)}
	}
	units, minor, err = t.Cost(len(text))
	if err != nil {
		return nil, 0, 0, &intentRefusal{Reason: reasonNoPrice, Detail: "the tariff cannot price this send: " + err.Error()}
	}
	return t, units, minor, nil
}

// overCapReason says why an authorization over the cap was refused:
// reprice when the figure differs from the one admitted (a changed tariff
// or chain), else skipped_budget.
func (w *DeliveryWorker) overCapReason(del database.MessageDelivery, t *database.Tariff, minor int64) string {
	in, err := w.db.GetIntent(del.IntentID)
	if err != nil || t == nil {
		return "skipped_budget"
	}
	for _, p := range in.RoutePlan {
		if p.RouteIndex == del.RouteIndex && (p.EstMinor != minor || p.TariffVersion != t.Version || p.TariffID != t.ID) {
			return "reprice"
		}
	}
	return "skipped_budget"
}

// intentPreSendGateway is intentPreSend for a gateway route: the egress
// rules, the chain applied now, the gateway's limits checked again (the
// chain or the gateway's mode may have changed since admission).
func (w *DeliveryWorker) intentPreSendGateway(del database.MessageDelivery, ad gateway.IntentAdapter) (string, bool, *intentRefusal) {
	if ad == nil {
		return "", false, &intentRefusal{Reason: reasonOffline, Detail: "the gateway of " + w.channelID + " is not running", Retryable: true}
	}
	if w.access != nil && w.access.HasEgressRules(w.channelID) {
		if len(w.access.EvaluateEgress(w.channelID, rules.RouteMessage{Text: del.TextPreview})) == 0 {
			return "", false, &intentRefusal{Reason: "denied", Detail: "egress rules denied"}
		}
	}
	text, encrypted := string(del.Payload), false
	if iface, err := w.db.GetInterface(w.channelID); err == nil && iface.EgressTransforms != "" && iface.EgressTransforms != "[]" {
		if w.transforms == nil {
			return "", false, &intentRefusal{Reason: reasonEncoding, Detail: "no transform pipeline"}
		}
		out, err := w.transforms.ApplyEgress(del.Payload, iface.EgressTransforms)
		if err != nil {
			return "", false, &intentRefusal{Reason: reasonEncoding, Detail: "egress transform failed: " + err.Error()}
		}
		text, encrypted = string(codec.PrependVersionByte(out)), ChainEncrypts(iface.EgressTransforms)
	}
	if reason, detail := ad.IntentAdmit(del.Destination, []byte(text), encrypted); reason != "" {
		r := reason
		if r == reasonOversize {
			r = "oversize"
		}
		return "", false, &intentRefusal{Reason: r, Detail: detail + " (checked again before the send)"}
	}
	return text, encrypted, nil
}

// reserveCorr picks a correlation value no kept transmission on this
// interface used, from the adapter's generator.
func (w *DeliveryWorker) reserveCorr(kind string, next func() (string, error)) (string, error) {
	for i := 0; i < 8; i++ {
		v, err := next()
		if err != nil {
			return "", err
		}
		inUse, err := w.db.CorrelationInUse(kind, v, w.channelID, 0)
		if err != nil {
			return "", err
		}
		if !inUse {
			return v, nil
		}
	}
	return "", errors.New("every correlation value tried is still kept")
}

// sendIntentGateway makes the submission through the adapter. Answers that
// come after it returns reach the ledger through the receipt sink.
func (w *DeliveryWorker) sendIntentGateway(ctx context.Context, del database.MessageDelivery, ad gateway.IntentAdapter, text string, encrypted bool, corrValue string, tariff *database.Tariff) SendOutcome {
	ad.SetIntentReceiptSink(w.intentGatewayReceipt)
	o := ad.IntentSend(ctx, gateway.IntentSend{Destination: del.Destination, Text: text, Encrypted: encrypted,
		PlainText: string(del.Payload), CorrValue: corrValue, MsgRef: del.MsgRef})
	out := SendOutcome{Class: o.Class, AcceptedBy: o.AcceptedBy, Retryable: o.Retryable, ConsumeRetry: o.ConsumeRetry,
		Detail: o.Detail, Reason: o.Reason}
	if o.Charged && tariff != nil {
		// Billing is a fact of its own (r3 D8): incurred whatever the outcome.
		// An estimate is the tariff's own count for this submission, its
		// minimum included, never fewer units than the adapter saw go (one
		// SMS is one physical submission, not the tariff's units,
		// B9SMS-R4); only the provider's confirmed figure replaces it.
		units := o.ChargedUnits
		if !o.ChargeConfirmed {
			units = max(units, tariff.Units(len(text)))
		}
		out.Charges = append(out.Charges, Charge{UnitKind: tariff.UnitKind, Units: units,
			Minor: tariff.Price(units), Confirmed: o.ChargeConfirmed})
	}
	if o.Class != database.ClassNotSent {
		out.Corr = Correlation{Kind: ad.IntentCorrKind(), Value: corrValue, Scope: w.channelID, Peer: del.Destination}
	}
	if r := o.Receipt; r != nil {
		out.Evidence = append(out.Evidence, database.DeliveryEvidence{Source: r.Source, Event: r.Event, Completes: r.Completes,
			CorrKind: ad.IntentCorrKind(), CorrValue: corrValue, CorrScope: w.channelID, CorrPeer: r.Peer, Trust: r.Trust})
	}
	return out
}

// intentGatewayReceipt records an answer that came after the send returned
// (IntentReceiptRecorder), and tells the Escalator.
func (w *DeliveryWorker) intentGatewayReceipt(r gateway.IntentReceipt) {
	rec := &IntentReceiptRecorder{db: w.db, scope: w.channelID}
	if w.intents != nil {
		rec.notify = w.intents.Notify
	}
	rec.Record(r)
}

// gatewayCorrWindowS bounds how long after its handoff a kept transmission
// takes an answer, per correlation kind (0 = while it is kept). An APRS ack
// is an unauthenticated five-character id: a day covers every retry
// schedule and keeps an id from matching a send nobody waits for any more.
var gatewayCorrWindowS = map[string]int{gateway.APRSIntentCorrKind: 24 * 60 * 60}

// IntentReceiptRecorder takes the answers a gateway's intent adapter
// reports, during or after a send, before or after a restart: the kept
// transmission is the authority (its correlation, interface, peer and
// handoff time), never the gateway's memory (APRS-R2). It is installed as
// the process-wide sink before any gateway starts. [MESHSAT-1584]
type IntentReceiptRecorder struct {
	db     *database.DB
	scope  string
	mu     sync.Mutex
	notify func(string)
}

// NewIntentReceiptRecorder makes a recorder over db.
func NewIntentReceiptRecorder(db *database.DB) *IntentReceiptRecorder {
	return &IntentReceiptRecorder{db: db}
}

// SetNotify sets who hears that an intent got evidence (the Escalator,
// once it exists; until then the intent's dirty flag carries it).
func (rec *IntentReceiptRecorder) SetNotify(fn func(string)) {
	rec.mu.Lock()
	rec.notify = fn
	rec.mu.Unlock()
}

// samePeer compares two addresses as an answer names them.
func samePeer(a, b string) bool {
	return a != "" && strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// Record stores r against the one kept transmission with its correlation
// within the window. What it completes is decided against that
// transmission: CompletesIfPeer, and Completes too, only when the answer
// came from the peer the transmission went to.
func (rec *IntentReceiptRecorder) Record(r gateway.IntentReceipt) {
	if rec == nil || rec.db == nil || r.CorrKind == "" || r.CorrValue == "" {
		return
	}
	if r.Scope == "" {
		r.Scope = rec.scope
	}
	window := gatewayCorrWindowS[r.CorrKind]
	inUse, err := rec.db.CorrelationInUse(r.CorrKind, r.CorrValue, r.Scope, window)
	if err != nil || !inUse {
		return
	}
	e, err := rec.db.RecordEvidence(database.DeliveryEvidence{Source: r.Source, Event: r.Event,
		CorrKind: r.CorrKind, CorrValue: r.CorrValue, CorrScope: r.Scope, CorrPeer: r.Peer, Trust: r.Trust}, window,
		func(t database.DeliveryTransmission) (string, string, string, string) {
			completes := ""
			if samePeer(t.CorrPeer, r.Peer) {
				completes = r.Completes
				if r.CompletesIfPeer != "" {
					completes = r.CompletesIfPeer
				}
			}
			return r.Event, r.Source, r.Trust, completes
		})
	if err != nil {
		log.Warn().Err(err).Str("corr", r.CorrValue).Msg("intent: late answer not recorded")
		return
	}
	rec.mu.Lock()
	notify := rec.notify
	rec.mu.Unlock()
	if e.Matched && notify != nil {
		notify(e.IntentID)
	}
	log.Info().Str("corr", fmt.Sprintf("%s:%s", r.CorrKind, r.CorrValue)).Str("event", r.Event).Bool("matched", e.Matched).
		Str("completes", e.Completes).Msg("intent: late answer recorded")
}
