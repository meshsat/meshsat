package transport

import (
	"context"
	"fmt"
)

// A delivery intent's MO through the 9704 (HeMB-NG, the IMT route), and the
// one-MO-at-a-time rule every MO follows. [MESHSAT-1584]
//
// The helper sends one MO at a time but Go waits for each result in one
// slot, so two senders at once (the delivery worker, the Reticulum satellite
// interface, the gateway's dead-letter retries) could take each other's
// result or time out on a message that went. Every MO now holds the modem
// until its own result is in.

// IMTPhase is how far an MO got.
type IMTPhase int

const (
	// IMTNotSent: the modem never had the message to send, or the network
	// refused it; it went nowhere.
	IMTNotSent IMTPhase = iota
	// IMTAccepted: mo_ack_received, the network acknowledged the MO.
	IMTAccepted
	// IMTUnknown: the message may have left.
	IMTUnknown
)

// IMTOneSegmentBytes is the largest payload that goes in one segment with
// its CRC (the modem asks for at most 1446 bytes a segment, RockBLOCK-9704
// v1.1.2 jspr.c). The helper answers one segment request per MO, and a
// one-segment MO the modem never got the segment of cannot have left.
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
}

// imtFinalStatus classifies the 14 final MO statuses of RockBLOCK-9704
// v1.1.2 (jsprFinalMoStatus_t, jspr.h). The library itself only tells
// mo_ack_received from the rest; this table says which of the rest leave
// nothing behind, from what each names, and keeps every one that may come
// after the message started to leave as unknown.
var imtFinalStatus = map[string]struct {
	phase IMTPhase
	retry bool
}{
	"mo_ack_received": {IMTAccepted, false},
	// Ended before transit, or the modem never had valid data for it.
	"message_cancelled_pre_transit":   {IMTNotSent, true},
	"message_discarded_on_overflow":   {IMTNotSent, true},
	"segment_not_supplied":            {IMTNotSent, true}, // one segment only (IMTOneSegmentBytes)
	"segment_incorrect":               {IMTNotSent, true},
	"user_supplied_crc_error":         {IMTNotSent, true},
	"message_dropped_local_crc_error": {IMTNotSent, true},
	// Refused by the network: delivered nowhere, and a retry meets the same.
	"subscription_invalid": {IMTNotSent, false},
	// May have left in part or whole before it ended.
	"message_expired":              {IMTUnknown, false},
	"message_transfer_timeout":     {IMTUnknown, false},
	"network_error":                {IMTUnknown, false},
	"message_cancelled_in_transit": {IMTUnknown, false},
	"protocol_error":               {IMTUnknown, false},
	"crc_error_in_transfer":        {IMTUnknown, false},
}

// classifyIMTMO says how far an MO got. The modem's final status decides
// when it gave one; otherwise a message the modem never got the payload of
// (refused at messageOriginate, or never asked for the segment) cannot have
// left, and one whose payload went, or when this side cannot tell, may have.
// A status nobody documents is unknown.
func classifyIMTMO(r moResult) IMTSubmit {
	if e, ok := imtFinalStatus[r.Status]; ok {
		return IMTSubmit{Phase: e.phase, Retry: e.retry}
	}
	if r.Supplied == moSuppliedNo {
		return IMTSubmit{Phase: IMTNotSent, Retry: true}
	}
	return IMTSubmit{Phase: IMTUnknown}
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

// acquireMO takes the modem for one MO, or gives up with ctx.
func (t *DirectIMTTransport) acquireMO(ctx context.Context) (func(), error) {
	t.moSemOnce.Do(func() { t.moSem = make(chan struct{}, 1) })
	select {
	case t.moSem <- struct{}{}:
		return func() { <-t.moSem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// SubmitMO sends one MO for a delivery intent on the raw topic and says how
// far it got. It makes one submission and no retry, takes only a result
// tagged with its own reference, and cannot be cancelled by the MT poll's
// yield (only a Deferred send gives way). The payload must fit one segment.
func (t *DirectIMTTransport) SubmitMO(ctx context.Context, data []byte) (IMTSubmit, error) {
	if len(data) == 0 || len(data) > IMTOneSegmentBytes {
		return IMTSubmit{Phase: IMTNotSent, MOStatus: IMTStatusGenericFail},
			fmt.Errorf("imt: %d bytes, an intent's MO takes 1 to %d", len(data), IMTOneSegmentBytes)
	}
	release, err := t.acquireMO(ctx)
	if err != nil {
		return IMTSubmit{Phase: IMTNotSent, Retry: true, MOStatus: IMTStatusGenericFail},
			fmt.Errorf("imt: not started, the modem was busy with another message: %w", err)
	}
	defer release()

	t.mu.Lock()
	if !t.connected || t.conn == nil {
		t.mu.Unlock()
		return IMTSubmit{Phase: IMTNotSent, Retry: true, MOStatus: IMTStatusGenericFail}, fmt.Errorf("imt: %w", ErrNotConnected)
	}
	conn := t.conn
	t.moNoCancel = true
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.moNoCancel = false
		t.mu.Unlock()
	}()

	r, err := conn.jsprSendMOResult(jsprRawTopic, data, true)
	sub := classifyIMTMO(r)
	sub.Status, sub.MOStatus = r.Status, imtMOStatusCode(r.Status)
	return sub, err
}
