package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/codec"
	"meshsat/internal/database"
	"meshsat/internal/gateway"
	"meshsat/internal/rules"
	"meshsat/internal/transport"
)

// intentOfflineRetry is how soon an attempt whose link was down is tried
// again; the queue allowance (the Escalator's T4) bounds how long it waits
// before the next route takes over.
const intentOfflineRetry = 15 * time.Second

// deliverIntent sends one attempt of a delivery intent (architecture r3 D5,
// requirements G1): the claim passes the intent's fence and counts
// claim_gen up; pre-send checks that refuse end the attempt with no
// transmission; otherwise transmission gen = claim_gen is authorized,
// marked handed off just before the one adapter call, and finalized in one
// transaction with the outcome. Then the Escalator looks at the intent.
// [MESHSAT-1581]
func (w *DeliveryWorker) deliverIntent(ctx context.Context, del database.MessageDelivery) {
	if w.intents == nil {
		// No owner in this process (cannot happen: main.go runs the
		// Escalator whenever intents exist). The row is left as it is.
		log.Error().Int64("id", del.ID).Str("intent", del.IntentID).Msg("delivery intent attempt with no Escalator; not sent")
		return
	}
	claimed, err := w.db.ClaimDeliveryForSending(del.ID)
	if err != nil {
		log.Error().Err(err).Int64("id", del.ID).Msg("intent attempt: claim failed")
		return
	}
	if !claimed {
		log.Debug().Int64("id", del.ID).Str("intent", del.IntentID).Msg("intent attempt not claimed (retired, closed or another round); not sent")
		return
	}
	fresh, err := w.db.GetDelivery(del.ID)
	if err != nil {
		log.Error().Err(err).Int64("id", del.ID).Msg("intent attempt: re-read failed")
		return
	}
	del = *fresh
	gen := del.ClaimGen
	defer w.intents.Notify(del.IntentID)

	// Pre-send checks: no transmission, no call. A route of a gateway
	// adapter (stage 1b) has its own. [MESHSAT-1584]
	var ad gateway.IntentAdapter
	var text string
	var encrypted bool
	var refuse *intentRefusal
	if del.Class != database.DeliveryClassLXMF && w.channelID != "mesh_0" && w.channelID != "mesh" {
		ad = w.intentAdapterFor()
		text, encrypted, refuse = w.intentPreSendGateway(del, ad)
	} else {
		text, refuse = w.intentPreSend(del)
	}
	if refuse != nil {
		if err := w.db.EndAttemptNotSent(del.ID, gen, refuse.Reason, refuse.Detail, refuse.Retryable, time.Now().Add(intentOfflineRetry)); err != nil {
			log.Error().Err(err).Int64("id", del.ID).Msg("intent attempt: could not record the refusal")
		}
		log.Info().Int64("id", del.ID).Str("intent", del.IntentID).Str("reason", refuse.Reason).Msg("intent attempt not sent")
		return
	}

	// A paid route is priced now, with the current tariff and the wire as
	// it is, and that figure is reserved against the intent's cap with the
	// transmission, before any call (r3 D8, B9). A free route reserves
	// nothing. [MESHSAT-1584]
	tariff, units, minor, refuse := w.intentPrice(ad, del, text)
	if refuse != nil {
		_ = w.db.EndAttemptNotSent(del.ID, gen, refuse.Reason, refuse.Detail, false, time.Time{})
		log.Info().Int64("id", del.ID).Str("intent", del.IntentID).Str("reason", refuse.Reason).Msg("intent attempt not sent")
		return
	}
	unitKind := ""
	if tariff != nil {
		unitKind = tariff.UnitKind
	}
	txID, err := w.db.AuthorizeTransmission(del.ID, gen, unitKind, units, minor)
	if err != nil {
		if errors.Is(err, database.ErrOverCap) {
			_ = w.db.EndAttemptNotSent(del.ID, gen, w.overCapReason(del, tariff, minor), err.Error(), false, time.Time{})
		}
		log.Error().Err(err).Int64("id", del.ID).Msg("intent attempt: authorization failed; not sent")
		return
	}
	// A mesh send under a packet id of our own: the id is reserved (no
	// kept transmission used it) and stored with the handoff, before the
	// radio has it, so an answer that comes back before finalization, or
	// after a stop, names this transmission (red-team F-2, R-3).
	var meshID uint32
	var corr database.Correlation
	if ad != nil {
		v, err := w.reserveCorr(ad.IntentCorrKind(), ad.IntentNewCorr)
		if err != nil {
			_, _ = w.db.FinalizeTransmission(txID, database.TransmissionResult{Class: database.ClassNotSent, Retryable: true,
				ConsumeRetry: true, Detail: "no correlation: " + err.Error()})
			return
		}
		corr = database.Correlation{Kind: ad.IntentCorrKind(), Value: v, Scope: w.channelID, Peer: del.Destination}
	} else if del.Class != database.DeliveryClassLXMF {
		if meshID, err = w.reserveMeshID(); err != nil {
			_, _ = w.db.FinalizeTransmission(txID, database.TransmissionResult{Class: database.ClassNotSent, Retryable: true,
				ConsumeRetry: true, Detail: "no packet id: " + err.Error()})
			return
		}
		corr = database.Correlation{Kind: meshCorrKind, Value: fmt.Sprintf("%08x", meshID), Scope: w.channelID, Peer: del.Destination}
	}
	if err := w.db.MarkTransmissionHandedOffWith(txID, corr); err != nil {
		log.Error().Err(err).Int64("id", del.ID).Msg("intent attempt: handoff not recorded; not sent")
		_, _ = w.db.FinalizeTransmission(txID, database.TransmissionResult{Class: database.ClassNotSent, Retryable: true,
			Detail: "handoff not recorded: " + err.Error()})
		return
	}
	w.intents.Changed(del.IntentID, "handed_off")
	// Authorized and handed off: a real submission attempt from here on, so
	// this is where a paid link feeds the hop set's alarm (R46-V6).
	w.hopPaidAttempt()

	var out SendOutcome
	switch {
	case del.Class == database.DeliveryClassLXMF:
		out = w.sendIntentLXMF(ctx, del)
	case ad != nil:
		out = w.sendIntentGateway(ctx, del, ad, text, encrypted, corr.Value, tariff)
	case w.channelID == "mesh_0" || w.channelID == "mesh":
		out = w.sendIntentMesh(ctx, del, text, meshID)
	default:
		out = SendOutcome{Class: database.ClassNotSent, Detail: "no intent adapter for " + w.channelID, Reason: reasonUnsupported}
	}
	if out.Class == database.ClassNotSent && out.Retryable && !out.ConsumeRetry {
		// A link that was down: try again soon, without spending a retry.
		// Its queue allowance decides when the next route takes over.
		out.Detail = "link down: " + out.Detail
	}
	w.hopIntentOutcome(out)
	res := out.transmissionResult()
	if del.Class != database.DeliveryClassLXMF && ad == nil {
		res.Reconcile, res.MatchWindowS = classifyMeshReceipt, meshCorrWindowS
	}
	if out.Class == database.ClassAccepted {
		if ms, ok := w.intents.ElapsedNow(del.IntentID); ok {
			res.AcceptedElapsedMs = &ms
		}
	}
	if out.Class == database.ClassNotSent && out.Retryable {
		if out.ConsumeRetry {
			res.RetryAt = w.calculateNextRetry(del.Retries + 1)
		} else {
			res.RetryAt = time.Now().Add(intentOfflineRetry)
		}
	}
	if _, err := w.db.FinalizeTransmission(txID, res); err != nil {
		// The transmission stays handed off: the restart recovery reads
		// it as unknown, never as not sent.
		log.Error().Err(err).Int64("id", del.ID).Int64("transmission", txID).Msg("intent attempt: finalization failed")
		return
	}
	log.Info().Int64("id", del.ID).Str("intent", del.IntentID).Int("gen", gen).Str("outcome", out.Class).
		Str("accepted_by", out.AcceptedBy).Str("corr", out.Corr.Value).Msg("intent attempt finalized")
	if w.emit != nil {
		w.emit(transport.MeshEvent{
			Type:    "delivery_attempt",
			Message: fmt.Sprintf("Intent attempt on %s: %s", w.channelID, out.Class),
			Data:    deliveryEventData(del, out.Class, map[string]interface{}{"intent_id": del.IntentID, "gen": gen}),
			Time:    time.Now().UTC().Format(time.RFC3339),
		})
	}
}

// hopIntentOutcome turns an intent send's outcome into the hop set's
// evidence, as hopFailure does for the legacy rows: nothing left the device
// though the bearer was there and a retry is spent (every adapter's
// ErrNotSubmitted) is the radio's sign. A link that was down, an oversize
// or unsupported send, and the pre-send refusals (which never get here)
// say nothing about the band. A missing receipt is the Escalator's to
// report (hopReceiptEnded). [MESHSAT-1650, R46-V3]
func (w *DeliveryWorker) hopIntentOutcome(out SendOutcome) {
	if out.Class != database.ClassNotSent || !out.ConsumeRetry || out.Reason != "not_sent" {
		return
	}
	if h := hopSigns(w.hop); h != nil {
		h.Negative(w.channelID, "radio", "not_submitted", "the radio did not send it")
	}
}

// intentRefusal is a pre-send refusal of an attempt.
type intentRefusal struct {
	Reason    string
	Detail    string
	Retryable bool
}

// intentPreSend re-checks what may have changed since admission (r3 D2):
// the route is still allowed (egress rules), the interface's chain still
// gives a payload that fits, an LXMF path has not moved onto a paid link.
// It returns the text to send on the mesh.
func (w *DeliveryWorker) intentPreSend(del database.MessageDelivery) (string, *intentRefusal) {
	if del.Class == database.DeliveryClassLXMF {
		if pc, ok := w.lxmf.(paidPathChecker); ok && pc.CrossesPaidLink(del.Destination) {
			return "", &intentRefusal{Reason: reasonNoPrice, Detail: "the path to this destination now crosses a paid link, which has no price for intents yet"}
		}
		return "", nil
	}
	// Only a radio that sends under a packet id chosen and stored before
	// the send: with one that picks its own and tells it after, an answer
	// can arrive before the id is known, and a reused id would then name
	// an older transmission (red-team R-3). Refused before any
	// transmission; the kits' radio is such a radio.
	if _, ok := w.mesh.(meshPreparedSender); !ok || w.mesh == nil {
		return "", &intentRefusal{Reason: reasonUnsupported, Detail: "the mesh transport cannot send under a packet id chosen before the send"}
	}
	if w.access != nil && w.access.HasEgressRules(w.channelID) {
		if len(w.access.EvaluateEgress(w.channelID, rules.RouteMessage{Text: del.TextPreview})) == 0 {
			return "", &intentRefusal{Reason: "denied", Detail: "egress rules denied"}
		}
	}
	text := string(del.Payload)
	if w.transforms != nil {
		if iface, err := w.db.GetInterface(w.channelID); err == nil && iface.EgressTransforms != "" && iface.EgressTransforms != "[]" {
			out, err := w.transforms.ApplyEgress(del.Payload, iface.EgressTransforms)
			if err != nil {
				return "", &intentRefusal{Reason: reasonEncoding, Detail: "egress transform failed: " + err.Error()}
			}
			text = string(codec.PrependVersionByte(out))
		}
	}
	if mtu := meshBearerMTU(); len(text) > mtu {
		return "", &intentRefusal{Reason: "oversize", Detail: fmt.Sprintf("%d bytes on the mesh, the limit is %d (the chain changed since admission)", len(text), mtu)}
	}
	return text, nil
}

// meshPreparedSender is a mesh transport that sends under a packet id the
// caller chose (and stored) before the send.
type meshPreparedSender interface {
	SendMessageWithID(ctx context.Context, req transport.SendRequest, id uint32) error
}

// reserveMeshID picks a random packet id that no kept transmission on this
// interface used.
func (w *DeliveryWorker) reserveMeshID() (uint32, error) {
	for i := 0; i < 8; i++ {
		id, err := transport.NewMeshPacketID()
		if err != nil {
			return 0, err
		}
		inUse, err := w.db.CorrelationInUse(meshCorrKind, fmt.Sprintf("%08x", id), w.channelID, meshCorrWindowS)
		if err != nil {
			return 0, err
		}
		if !inUse {
			return id, nil
		}
	}
	return 0, errors.New("every packet id tried is still live")
}

// sendIntentMesh sends a mesh attempt under its prepared packet id. The
// correlation is kept whatever the outcome: a send that failed after the
// radio may have it still names this transmission.
func (w *DeliveryWorker) sendIntentMesh(ctx context.Context, del database.MessageDelivery, text string, meshID uint32) SendOutcome {
	req := transport.SendRequest{Text: text, To: del.Destination}
	sender, ok := w.mesh.(meshPreparedSender)
	if !ok || meshID == 0 {
		return SendOutcome{Class: database.ClassNotSent, Detail: "the mesh transport cannot send under a packet id chosen before the send", Reason: reasonUnsupported}
	}
	err := sender.SendMessageWithID(ctx, req, meshID)
	corr := Correlation{Kind: meshCorrKind, Value: fmt.Sprintf("%08x", meshID), Scope: w.channelID, Peer: del.Destination}
	if err != nil {
		out := LegacyOutcome(err)
		if out.Class != database.ClassNotSent {
			out.Corr = corr
		}
		return out
	}
	w.packets.Add(MeshTXRecord(w.mesh, w.channelID, req, del.MsgRef))
	return SendOutcome{Class: database.ClassAccepted, AcceptedBy: "radio", Corr: corr, Detail: "queued by the radio"}
}

// sendIntentLXMF sends an LXMF attempt and waits for its proof, as the
// legacy LXMF row does, under the intent's fixed timestamp.
func (w *DeliveryWorker) sendIntentLXMF(ctx context.Context, del database.MessageDelivery) SendOutcome {
	if w.lxmf == nil {
		return SendOutcome{Class: database.ClassNotSent, Retryable: true, ConsumeRetry: true, Detail: "lxmf router not running", Reason: "not_sent"}
	}
	timeout := 3 * time.Minute
	if t, ok := w.lxmf.(interface {
		DeliverTimeout(dest string) time.Duration
	}); ok {
		if d := t.DeliverTimeout(del.Destination); d > timeout {
			timeout = d
		}
	}
	sendCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	hash, err := w.lxmf.Deliver(sendCtx, del)
	out := lxmfOutcome(hash, err)
	if hash != "" {
		out.Corr = Correlation{Kind: "lxmf_hash", Value: hash, Scope: "lxmf_0", Peer: del.Destination}
	}
	return out
}

// intentAdapterFor is this worker's gateway when it can carry intents.
func (w *DeliveryWorker) intentAdapterFor() gateway.IntentAdapter {
	if w.channelID == hubRouteInterface {
		return w.hubRoute.get()
	}
	if w.gwProv == nil {
		return nil
	}
	gw := w.gwProv.GatewayByInterfaceID(w.channelID)
	if gw == nil {
		return nil
	}
	ad, _ := gw.(gateway.IntentAdapter)
	return ad
}
