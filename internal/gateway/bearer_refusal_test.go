package gateway

import (
	"context"
	"fmt"
	"testing"

	"meshsat/internal/transport"
)

// The native adapters flag only the bearer's own refusal (R50-V1): the
// modem's answer or failure, never a send its caller cancelled, a command
// that never started or a check before the modem. [MESHSAT-1650]

// imt_local_cancellation_is_not_radio_evidence: the IMT adapter on the
// results the real transport gives (TestIMT_LocalCancellationIsNotRadioEvidence
// shows their shape): a cancel by the caller before the handoff is not the
// bearer's refusal; the modem's cancel before transit and its refusal at
// messageOriginate are.
func TestIMTIntent_LocalCancellationIsNotRadioEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		res    transport.IMTSubmit
		err    error
		bearer bool
	}{
		{"cancelled by its caller", transport.IMTSubmit{Phase: transport.IMTNotSent, Retry: true, MOStatus: transport.IMTStatusGenericFail},
			fmt.Errorf("imt: not started: %w", context.Canceled), false},
		{"its deadline passed while another MO held the modem", transport.IMTSubmit{Phase: transport.IMTNotSent, Retry: true, MOStatus: transport.IMTStatusGenericFail},
			fmt.Errorf("imt: not started: %w", context.DeadlineExceeded), false},
		{"the modem cancelled it before transit", transport.IMTSubmit{Phase: transport.IMTNotSent, Retry: true, Status: "message_cancelled_pre_transit"}, nil, true},
		{"the modem refused it at messageOriginate", transport.IMTSubmit{Phase: transport.IMTNotSent, Retry: true}, nil, true},
		{"the modem's cancel, with a read error after it", transport.IMTSubmit{Phase: transport.IMTNotSent, Retry: true, Status: "message_cancelled_pre_transit"},
			fmt.Errorf("helper read: %w", context.Canceled), true},
	} {
		sat := &submitSat{res: tc.res, err: tc.err}
		o := imtIntentGateway(sat, imtFar).IntentSend(context.Background(), IntentSend{Destination: imtFar, Text: "\x01QUJD", Encrypted: true, CorrValue: "c1"})
		if o.Class != IntentNotSent || o.BearerRefused != tc.bearer {
			t.Errorf("%s: %+v, want not sent with bearer refused %v", tc.name, o, tc.bearer)
		}
	}
}

// sms_local_queue_failure_is_not_radio_evidence: the SMS adapter on the
// production transport's local outcomes (the I/O loop ended before the
// command ran, the loop busy with another command, the caller gone): not the
// bearer's refusal; the modem failing the command is, and the network's
// refusal is not.
func TestSMSIntent_LocalQueueFailureIsNotRadioEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		res    transport.SMSSubmit
		err    error
		bearer bool
	}{
		{"the session ended before it ran", transport.SMSSubmit{Phase: transport.SMSNotSent}, transport.ErrCellSessionEnded, false},
		{"behind a busy command", transport.SMSSubmit{Phase: transport.SMSNotSent}, fmt.Errorf("%w: another command is still inside its deadline", transport.ErrCellProbeBusy), false},
		{"the caller gave up first", transport.SMSSubmit{Phase: transport.SMSNotSent}, context.Canceled, false},
		{"the modem failed the command", transport.SMSSubmit{Phase: transport.SMSNotSent}, fmt.Errorf("modem not responding (%w)", transport.ErrModemFailed), true},
		{"the network refused it", transport.SMSSubmit{Phase: transport.SMSRefused}, errTest("+CMS ERROR: 1"), false},
	} {
		cell := &submitCell{fakeCellTransport: newFakeCellTransport(), res: tc.res, err: tc.err}
		gw, _ := smsIntentGateway(t, cell, CellularConfig{})
		o := gw.IntentSend(context.Background(), IntentSend{Destination: "+31612345678", Text: "\x01QUJD", Encrypted: true, PlainText: "x", CorrValue: "c"})
		if o.Class != IntentNotSent || o.BearerRefused != tc.bearer {
			t.Errorf("%s: %+v, want not sent with bearer refused %v", tc.name, o, tc.bearer)
		}
	}
}
