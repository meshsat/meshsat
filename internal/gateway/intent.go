package gateway

import "context"

// A gateway that can carry a delivery intent's attempt (HeMB-NG r3 D18,
// stage 1b). The engine imports this package, so the contract lives here
// and the engine converts it to its own types. [MESHSAT-1584]
//
// The contract: one submission per IntentSend (the gateway's own protocol
// retries, such as an APRS message sent again until acked, belong to that
// submission), nothing queued silently (a call returns once the gateway
// knows whether anything left the device), and every answer that can come
// after the call names the correlation the engine chose and stored before
// the send.
type IntentAdapter interface {
	// IntentCorrKind is the correlation kind this gateway's answers carry.
	IntentCorrKind() string
	// IntentNewCorr picks a fresh correlation value; the engine checks no
	// kept transmission used it and stores it with the handoff.
	IntentNewCorr() (string, error)
	// IntentAdmit checks, at submission and before each send, that the
	// wire text (after the interface's egress transforms) can go to dest
	// in this gateway's current mode as it is: never cut, never changed.
	// It returns "" or a reason (skipped_oversize, skipped_encoding,
	// skipped_unsupported) with a detail.
	IntentAdmit(dest string, wire []byte, encrypted bool) (reason, detail string)
	// IntentSend makes the submission.
	IntentSend(ctx context.Context, s IntentSend) IntentOutcome
	// SetIntentReceiptSink takes the answers that come after IntentSend
	// returned.
	SetIntentReceiptSink(fn func(IntentReceipt))
}

// IntentSend is one submission of an intent's attempt.
type IntentSend struct {
	Destination string // the route's address, as admitted
	Text        string // the wire text after the egress transforms
	Encrypted   bool   // the egress chain encrypted it
	CorrValue   string // chosen before the send, stored with the handoff
	MsgRef      string
}

// Outcome classes, as database.Class* (not imported here).
const (
	IntentNotSent  = "not_sent"
	IntentAccepted = "accepted"
	IntentUnknown  = "unknown"
)

// IntentOutcome is what one IntentSend knows.
type IntentOutcome struct {
	Class      string
	AcceptedBy string
	// Retryable (not sent only): nothing left the device and a later claim
	// may try again; ConsumeRetry says whether that spends a retry.
	Retryable    bool
	ConsumeRetry bool
	Detail       string
	Reason       string
	// Receipt is an answer that came within the call.
	Receipt *IntentReceipt
}

// IntentReceipt is one answer about a submission, inside or after the call.
type IntentReceipt struct {
	CorrKind  string
	CorrValue string
	Scope     string // the interface
	Peer      string // who answered
	Source    string // e.g. "station"
	Event     string // e.g. "ack", "rej"
	Completes string // the outcome it completes, "" when it completes nothing
	Trust     string // "unauthenticated", "signed", ...
}
