package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

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

const modeAPRS = "aprs"

// gatewayModes are the adapter modes a gateway carries.
var gatewayModes = map[string]bool{modeAPRS: true}

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
	p.Admitted = true
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
func (w *DeliveryWorker) sendIntentGateway(ctx context.Context, del database.MessageDelivery, ad gateway.IntentAdapter, text string, encrypted bool, corrValue string) SendOutcome {
	ad.SetIntentReceiptSink(w.intentGatewayReceipt)
	o := ad.IntentSend(ctx, gateway.IntentSend{Destination: del.Destination, Text: text, Encrypted: encrypted,
		CorrValue: corrValue, MsgRef: del.MsgRef})
	out := SendOutcome{Class: o.Class, AcceptedBy: o.AcceptedBy, Retryable: o.Retryable, ConsumeRetry: o.ConsumeRetry,
		Detail: o.Detail, Reason: o.Reason}
	if o.Class != database.ClassNotSent {
		out.Corr = Correlation{Kind: ad.IntentCorrKind(), Value: corrValue, Scope: w.channelID, Peer: del.Destination}
	}
	if r := o.Receipt; r != nil {
		out.Evidence = append(out.Evidence, database.DeliveryEvidence{Source: r.Source, Event: r.Event, Completes: r.Completes,
			CorrKind: ad.IntentCorrKind(), CorrValue: corrValue, CorrScope: w.channelID, CorrPeer: r.Peer, Trust: r.Trust})
	}
	return out
}

// intentGatewayReceipt records an answer that came after the send returned,
// when a kept transmission carries its correlation (stored at the handoff),
// and tells the Escalator.
func (w *DeliveryWorker) intentGatewayReceipt(r gateway.IntentReceipt) {
	if r.Scope == "" {
		r.Scope = w.channelID
	}
	inUse, err := w.db.CorrelationInUse(r.CorrKind, r.CorrValue, r.Scope, 0)
	if err != nil || !inUse {
		return
	}
	e, err := w.db.RecordEvidence(database.DeliveryEvidence{Source: r.Source, Event: r.Event, Completes: r.Completes,
		CorrKind: r.CorrKind, CorrValue: r.CorrValue, CorrScope: r.Scope, CorrPeer: r.Peer, Trust: r.Trust}, 0, nil)
	if err != nil {
		log.Warn().Err(err).Str("corr", r.CorrValue).Msg("intent: late answer not recorded")
		return
	}
	if e.Matched && w.intents != nil {
		w.intents.Notify(e.IntentID)
	}
	log.Info().Str("corr", fmt.Sprintf("%s:%s", r.CorrKind, r.CorrValue)).Str("event", r.Event).Bool("matched", e.Matched).
		Msg("intent: late answer recorded")
}
