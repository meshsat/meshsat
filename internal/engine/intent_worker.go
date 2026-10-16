package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/codec"
	"meshsat/internal/database"
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

	// Pre-send checks: no transmission, no call.
	text, refuse := w.intentPreSend(del)
	if refuse != nil {
		if err := w.db.EndAttemptNotSent(del.ID, gen, refuse.Reason, refuse.Detail, refuse.Retryable, time.Now().Add(intentOfflineRetry)); err != nil {
			log.Error().Err(err).Int64("id", del.ID).Msg("intent attempt: could not record the refusal")
		}
		log.Info().Int64("id", del.ID).Str("intent", del.IntentID).Str("reason", refuse.Reason).Msg("intent attempt not sent")
		return
	}

	// Free routes only in stage 1a: nothing to reserve (B9 prices paid
	// routes and reserves per transmission).
	txID, err := w.db.AuthorizeTransmission(del.ID, gen, "", 0, 0)
	if err != nil {
		if errors.Is(err, database.ErrOverCap) {
			_ = w.db.EndAttemptNotSent(del.ID, gen, "skipped_budget", err.Error(), false, time.Time{})
		}
		log.Error().Err(err).Int64("id", del.ID).Msg("intent attempt: authorization failed; not sent")
		return
	}
	if err := w.db.MarkTransmissionHandedOff(txID); err != nil {
		log.Error().Err(err).Int64("id", del.ID).Msg("intent attempt: handoff not recorded; not sent")
		_, _ = w.db.FinalizeTransmission(txID, database.TransmissionResult{Class: database.ClassNotSent, Retryable: true,
			Detail: "handoff not recorded: " + err.Error()})
		return
	}

	var out SendOutcome
	switch {
	case del.Class == database.DeliveryClassLXMF:
		out = w.sendIntentLXMF(ctx, del)
	case w.channelID == "mesh_0" || w.channelID == "mesh":
		out = w.sendIntentMesh(ctx, del, text)
	default:
		out = SendOutcome{Class: database.ClassNotSent, Detail: "no intent adapter for " + w.channelID, Reason: reasonUnsupported}
	}
	if out.Class == database.ClassNotSent && out.Retryable && !out.ConsumeRetry {
		// A link that was down: try again soon, without spending a retry.
		// Its queue allowance decides when the next route takes over.
		out.Detail = "link down: " + out.Detail
	}
	res := out.transmissionResult()
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

// sendIntentMesh sends a mesh attempt under its own packet id and returns
// the radio's acceptance with the id as the correlation the destination's
// acknowledgement will carry.
func (w *DeliveryWorker) sendIntentMesh(ctx context.Context, del database.MessageDelivery, text string) SendOutcome {
	sender, ok := w.mesh.(meshIDSender)
	if !ok || w.mesh == nil {
		return SendOutcome{Class: database.ClassNotSent, Detail: "the mesh transport cannot send under its own packet id", Reason: reasonUnsupported}
	}
	req := transport.SendRequest{Text: text, To: del.Destination}
	id, err := sender.SendMessageID(ctx, req)
	if err != nil {
		return LegacyOutcome(err)
	}
	w.packets.Add(MeshTXRecord(w.mesh, w.channelID, req, del.MsgRef))
	return SendOutcome{
		Class:      database.ClassAccepted,
		AcceptedBy: "radio",
		Corr:       Correlation{Kind: "mesh_packet_id", Value: fmt.Sprintf("%08x", id), Scope: w.channelID, Peer: del.Destination},
		Detail:     "queued by the radio",
	}
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
