package transport

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"
)

// A delivery intent's MO through the 9704 (HeMB-NG, the IMT route), and the
// one-MO-at-a-time rule every MO follows. [MESHSAT-1584]
//
// The helper sends one MO at a time but Go waits for each result in one
// slot, so two senders at once (the delivery worker, the Reticulum satellite
// interface, the gateway's dead-letter retries) could take each other's
// result or time out on a message that went. Every MO now holds the modem
// until its own result is in, as one operation with its own reference.

// IMTPhase is how far an MO got.
type IMTPhase int

const (
	// IMTNotSent: the modem never had the message to send, or the modem
	// reported it ended before transit; it went nowhere.
	IMTNotSent IMTPhase = iota
	// IMTAccepted: mo_ack_received for this send's own payload, the network
	// acknowledged the MO.
	IMTAccepted
	// IMTUnknown: the message may have left.
	IMTUnknown
)

// IMTOneSegmentBytes is the largest payload that goes in one segment with
// its CRC (the modem asks for at most 1446 bytes a segment, RockBLOCK-9704
// v1.1.2 jspr.c). The helper answers one segment prompt per MO, the whole
// payload at offset 0, and nothing else.
const IMTOneSegmentBytes = jsprMaxSegmentData - jsprIMTCRCSize

// IMTSubmit is what one SubmitMO knows.
type IMTSubmit struct {
	Phase IMTPhase
	// Retry (not sent only): another try may go; false when the refusal
	// would be the same (an invalid subscription, a payload that does not
	// fit).
	Retry bool
	// Status is the modem's final_mo_status, or the helper's marker when it
	// ended the send itself (no_response, helper_timeout); "" when neither
	// came.
	Status string
	// MOStatus is Status as the gateway's pass statistics count it.
	MOStatus int
	// Inconsistent: the result contradicts itself (an ack for a payload the
	// helper says it never supplied). It is never taken as acceptance.
	Inconsistent bool
}

// moOp is the MO with the modem: its reference and whether a cancel may
// touch it.
type moOp struct {
	ref         int
	cancellable bool
}

// imtStatusBasis is the 14 final MO statuses of RockBLOCK-9704 v1.1.2
// (jsprFinalMoStatus_t, jspr.h; the wire strings in jspr.c) and what each
// means here AFTER this send's payload went to the modem. No vendor text
// defines them (the library only tells mo_ack_received from the rest; its
// README: "You're only charged for successful transmissions, so failed
// attempts cost nothing"). So only what has a stated basis releases the
// message as not sent; every other status that comes after the payload
// went may have left, keeps its price as possible and is never resent
// (review IMT-R4). Before the payload went, any status is not sent: the
// modem transmits only data it was given (classifyIMTMO). [MESHSAT-1584]
var imtStatusBasis = map[string]struct {
	phase IMTPhase
	basis string
}{
	"mo_ack_received": {IMTAccepted, "the network acknowledged this message (library: success)"},
	"message_cancelled_pre_transit": {IMTNotSent, "cancelled before transit: the official cancel of a message " +
		"the modem holds (rbCancelMessage); after the cancel-and-settle fix the 20 message soak of 21 Sep 2026 " +
		"(MESHSAT-1282) delivered 20 of 20 with 0 duplicates across cancels and retries"},
	"segment_not_supplied": {IMTNotSent, "the modem states it has no segment for the message, so it has " +
		"nothing to transmit (assumption from the name and the JSPR flow)"},
	"message_discarded_on_overflow":   {IMTUnknown, "no vendor definition after the payload went"},
	"message_expired":                 {IMTUnknown, "no vendor definition; may end after transit began"},
	"message_transfer_timeout":        {IMTUnknown, "no vendor definition; a transfer had begun"},
	"segment_incorrect":               {IMTUnknown, "no vendor definition after the payload went"},
	"network_error":                   {IMTUnknown, "no vendor definition; the network was involved"},
	"message_cancelled_in_transit":    {IMTUnknown, "cancelled after transit began"},
	"subscription_invalid":            {IMTUnknown, "no vendor definition after the payload went"},
	"protocol_error":                  {IMTUnknown, "no vendor definition"},
	"message_dropped_local_crc_error": {IMTUnknown, "no vendor definition after the payload went"},
	"crc_error_in_transfer":           {IMTUnknown, "no vendor definition; a transfer had begun"},
	"user_supplied_crc_error":         {IMTUnknown, "no vendor definition after the payload went"},
}

// classifyIMTMO says how far an MO got.
//
// Before the payload went to the modem (refused at messageOriginate, never
// prompted, or the helper never established that the prompt was this
// send's), nothing can have left whatever the status says, and the send may
// be tried again, except for an invalid subscription. An ack then is a
// contradiction, never acceptance. After the payload went (or when this
// side cannot tell), imtStatusBasis decides; acceptance needs the helper's
// word that it supplied this payload, and an end without a modem status, or
// one nobody documents, may have left.
func classifyIMTMO(r moResult) IMTSubmit {
	if r.Supplied == moSuppliedNo {
		if r.Status == "mo_ack_received" {
			return IMTSubmit{Phase: IMTUnknown, Inconsistent: true}
		}
		return IMTSubmit{Phase: IMTNotSent, Retry: r.Status != "subscription_invalid"}
	}
	e, ok := imtStatusBasis[r.Status]
	switch {
	case !ok:
		return IMTSubmit{Phase: IMTUnknown}
	case e.phase == IMTAccepted && r.Supplied != moSuppliedYes:
		return IMTSubmit{Phase: IMTUnknown, Inconsistent: true}
	case e.phase == IMTNotSent:
		return IMTSubmit{Phase: IMTNotSent, Retry: true}
	}
	return IMTSubmit{Phase: e.phase}
}

// imtMOStatusCode maps a final status to the gateway's IMT result codes.
func imtMOStatusCode(status string) int {
	switch status {
	case "mo_ack_received":
		return IMTStatusSuccess
	case "message_expired":
		return IMTStatusExpired
	case "network_error":
		return IMTStatusNoNetwork
	case "message_discarded_on_overflow":
		return IMTStatusOverflow
	}
	return IMTStatusGenericFail
}

// acquireMO takes the modem for one MO, or gives up with ctx: a caller
// already cancelled never takes it, even from an idle modem, and one
// cancelled while it waited gives it straight back (review IMT-R3).
func (t *DirectIMTTransport) acquireMO(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.moSemOnce.Do(func() { t.moSem = make(chan struct{}, 1) })
	select {
	case t.moSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-t.moSem
		return nil, err
	}
	return func() { <-t.moSem }, nil
}

// endMO ends the MO with the modem, for the cancel's snapshot.
func (t *DirectIMTTransport) endMO() {
	t.mu.Lock()
	t.moCur = nil
	t.mu.Unlock()
}

// SubmitMO sends one MO for a delivery intent on the raw topic and says how
// far it got. It makes one submission and no retry, takes only a result
// tagged with its own reference, and cannot be cancelled by the MT poll's
// yield (only a Deferred send gives way). The payload must fit one segment.
// A caller cancelled before the handoff sends nothing; once the command is
// with the helper the send runs to its own result.
func (t *DirectIMTTransport) SubmitMO(ctx context.Context, data []byte) (IMTSubmit, error) {
	if len(data) == 0 || len(data) > IMTOneSegmentBytes {
		return IMTSubmit{Phase: IMTNotSent, MOStatus: IMTStatusGenericFail},
			fmt.Errorf("imt: %d bytes, an intent's MO takes 1 to %d", len(data), IMTOneSegmentBytes)
	}
	release, err := t.acquireMO(ctx)
	if err != nil {
		return IMTSubmit{Phase: IMTNotSent, Retry: true, MOStatus: IMTStatusGenericFail},
			fmt.Errorf("imt: not started: %w", err)
	}
	defer release()

	t.mu.Lock()
	if !t.connected || t.conn == nil {
		t.mu.Unlock()
		return IMTSubmit{Phase: IMTNotSent, Retry: true, MOStatus: IMTStatusGenericFail}, fmt.Errorf("imt: %w", ErrNotConnected)
	}
	if err := ctx.Err(); err != nil {
		t.mu.Unlock()
		return IMTSubmit{Phase: IMTNotSent, Retry: true, MOStatus: IMTStatusGenericFail}, fmt.Errorf("imt: not started: %w", err)
	}
	conn := t.conn
	ref := conn.nextRef()
	t.moCur = &moOp{ref: ref, cancellable: false}
	t.mu.Unlock()
	defer t.endMO()

	r, err := conn.jsprSendMOResult(jsprRawTopic, data, ref, true)
	sub := classifyIMTMO(r)
	sub.Status, sub.MOStatus = r.Status, imtMOStatusCode(r.Status)
	if sub.Inconsistent {
		log.Error().Int("ref", ref).Str("status", r.Status).Int8("supplied", int8(r.Supplied)).
			Msg("imt: the helper's result contradicts itself (an ack for a payload it says it never supplied); taken as may have left")
	}
	return sub, err
}
