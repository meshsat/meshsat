package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/types"
)

// A send-to-contact with an idempotency key is one send whatever the mode.
// With delivery intents on, the intent submission holds the key; without
// them the key used to be ignored and a retried request queued the message
// again (review B10-R4). Now a send made outside delivery intents writes its
// delivery rows and a submission (mode legacy, holding what it queued) in
// one transaction, and SendToRecipient looks the key up before it chooses a
// mode, so a retry gets the first send's result in either mode.
// [MESHSAT-1584]

// legacySendResult is what a send outside delivery intents queued, kept
// with its submission so a retry can answer with it.
type legacySendResult struct {
	DeliveryIDs       map[directory.Kind][]int64 `json:"delivery_ids"`
	Errors            map[directory.Kind]string  `json:"errors,omitempty"`
	Strategy          directory.Strategy         `json:"strategy"`
	RequestedStrategy directory.Strategy         `json:"requested_strategy,omitempty"`
	StrategyReason    string                     `json:"strategy_reason,omitempty"`
}

// sendSource is the scope of an idempotency key, as the intent path sets it.
func sendSource(opts SendOptions) string {
	if opts.Source == "" {
		return "api"
	}
	return opts.Source
}

// sendFingerprint is what a key must name again: for a contact the intent
// request's own fingerprint (so a key keeps its meaning across the two
// modes), for a raw send its interface, address, payload and options.
func (d *Dispatcher) sendFingerprint(rcpt RecipientRef, body []byte, opts SendOptions, precedence types.Precedence) string {
	if rcpt.Raw == nil {
		return IntentRequest{ContactID: rcpt.ContactID, Body: body, Precedence: precedence,
			Strategy: opts.Strategy, MaxCostCents: opts.MaxCostCents}.fingerprint()
	}
	sum := sha256.Sum256(body)
	b, _ := json.Marshal(map[string]any{
		"raw_interface_id": rcpt.Raw.InterfaceID, "raw_address": rcpt.Raw.Address,
		"payload_sha256": hex.EncodeToString(sum[:]), "precedence": string(precedence),
		"class": opts.Class, "max_retries": opts.MaxRetries,
	})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// queueLegacyKeyed writes dels (one per kinds entry) and their submission
// in one transaction. When the key was taken meanwhile by the same request,
// that earlier send answers and nothing is queued.
func (d *Dispatcher) queueLegacyKeyed(opts SendOptions, fp string, kinds []directory.Kind, dels []database.MessageDelivery, res SendResult) (SendResult, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return res, err
	}
	sub := database.DeliverySubmission{ID: id.String(), Source: sendSource(opts), IdempotencyKey: opts.IdempotencyKey,
		RequestSHA256: fp, Mode: database.SubmissionLegacy}
	existing, ids, err := d.db.CreateLegacySubmission(sub, dels, func(ids []int64) (string, error) {
		out := legacySendResult{DeliveryIDs: map[directory.Kind][]int64{}, Errors: map[directory.Kind]string{},
			Strategy: res.Strategy, RequestedStrategy: res.RequestedStrategy, StrategyReason: res.StrategyReason}
		for i, id := range ids {
			out.DeliveryIDs[kinds[i]] = append(out.DeliveryIDs[kinds[i]], id)
		}
		// An address that could not be queued keeps its error beside the
		// rows of the same bearer, as an unkeyed send reports it (B10-R6).
		for k, e := range res.Errors {
			out.Errors[k] = e.Error()
		}
		b, err := json.Marshal(out)
		return string(b), err
	})
	if err != nil {
		return res, err
	}
	if existing != nil {
		if existing.Mode != database.SubmissionLegacy {
			return res, errors.New("this key named a delivery intent made meanwhile")
		}
		return replayLegacySend(existing, res)
	}
	for i, id := range ids {
		d.directSendQueued(dels[i], id)
		res.DeliveryIDs[kinds[i]] = append(res.DeliveryIDs[kinds[i]], id)
	}
	res.Legacy = &sub
	return res, nil
}

// replayLegacySend answers a retried request with the result its key's
// first send stored.
func replayLegacySend(prev *database.DeliverySubmission, res SendResult) (SendResult, error) {
	var out legacySendResult
	if err := json.Unmarshal([]byte(prev.LegacyResult), &out); err != nil {
		return res, fmt.Errorf("submission %s: stored result: %w", prev.ID, err)
	}
	res.DeliveryIDs = map[directory.Kind][]int64{}
	res.Errors = map[directory.Kind]error{}
	for k, ids := range out.DeliveryIDs {
		res.DeliveryIDs[k] = append([]int64(nil), ids...)
	}
	for k, e := range out.Errors {
		res.Errors[k] = errors.New(e)
	}
	res.Strategy, res.RequestedStrategy, res.StrategyReason = out.Strategy, out.RequestedStrategy, out.StrategyReason
	res.Legacy, res.Existing = prev, true
	log.Info().Str("submission", prev.ID).Msg("send-to-contact: the key named an earlier send; nothing queued again")
	return res, nil
}
