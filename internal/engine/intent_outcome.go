package engine

import (
	"context"
	"errors"

	"meshsat/internal/database"
	"meshsat/internal/lxmf"
	"meshsat/internal/transport"
)

// SendOutcome is what one adapter call says about one transmission of a
// delivery intent's attempt (architecture r3 D18): proven not sent,
// accepted by some entity, or unknown. Accepted is not transmitted: a radio
// that queued a packet accepted it. [MESHSAT-1581]
type SendOutcome struct {
	Class      string // database.ClassNotSent, ClassAccepted, ClassUnknown
	AcceptedBy string // local_queue, radio, modem, carrier, satellite_gateway, broker, destination_node
	Corr       Correlation
	// Retryable (not sent only): nothing left the device and a later
	// claim may try again; ConsumeRetry says whether that spends one of
	// the row's retries (a link that is down does not).
	Retryable    bool
	ConsumeRetry bool
	Charges      []Charge
	Detail       string
	Reason       string                      // the attempt's reason when not sent (oversize, denied, ...)
	Evidence     []database.DeliveryEvidence // evidence that came back within the call
}

// Correlation ties later evidence to one transmission: the evidence must
// carry Kind and Value itself, within Scope (the interface); Peer is who the
// transmission went to.
type Correlation struct {
	Kind  string
	Value string
	Scope string
	Peer  string
}

// Charge is what one transmission cost, apart from its outcome (r3 D8).
type Charge struct {
	UnitKind  string
	Units     int
	Minor     int64
	Confirmed bool
}

// OutcomeForwarder is an adapter that reports its own SendOutcome.
type OutcomeForwarder interface {
	ForwardOutcome(ctx context.Context, msg *transport.MeshMessage) SendOutcome
}

// LegacyOutcome reads a Forward-style error by r3 D18's legacy rule: nil is
// accepted by the local queue; an error saying nothing left the device
// (ErrNotSubmitted, ErrNotConnected, ErrPayloadTooLong, ErrNoRecipient) is
// not sent; every other error is unknown, because the adapter may have
// handed the message on before it failed.
func LegacyOutcome(err error) SendOutcome {
	switch {
	case err == nil:
		return SendOutcome{Class: database.ClassAccepted, AcceptedBy: "local_queue"}
	case errors.Is(err, transport.ErrNotConnected):
		return SendOutcome{Class: database.ClassNotSent, Retryable: true, Detail: err.Error(), Reason: "skipped_offline"}
	case errors.Is(err, transport.ErrPayloadTooLong):
		return SendOutcome{Class: database.ClassNotSent, Detail: err.Error(), Reason: "skipped_oversize"}
	case errors.Is(err, transport.ErrNoRecipient):
		return SendOutcome{Class: database.ClassNotSent, Detail: err.Error(), Reason: "not_sent"}
	case errors.Is(err, transport.ErrNotSubmitted):
		return SendOutcome{Class: database.ClassNotSent, Retryable: true, ConsumeRetry: true, Detail: err.Error(), Reason: "not_sent"}
	}
	return SendOutcome{Class: database.ClassUnknown, Detail: err.Error()}
}

// lxmfOutcome reads an LXMF send's error. The router waits for the proof
// within the call, so success is the proof, signed by the destination's
// identity: accepted by the recipient's Reticulum node, which is what a
// proof means (F24), never "the app accepted it". Unknown destination, no
// path and a failed link mean the message never left; a missing proof means
// it may have arrived.
func lxmfOutcome(hash string, err error) SendOutcome {
	if err == nil {
		return SendOutcome{
			Class:      database.ClassAccepted,
			AcceptedBy: "destination_node",
			Evidence: []database.DeliveryEvidence{{
				Source:    "destination_node",
				Event:     "proof",
				Completes: database.OutcomeAcceptedDestinationNode,
				Trust:     "signed",
			}},
		}
	}
	switch {
	case errors.Is(err, lxmf.ErrPaidPath):
		// The free-only send found its path on a paid link and sent
		// nothing; a free path may appear later. [MESHSAT-1584]
		return SendOutcome{Class: database.ClassNotSent, Retryable: true, ConsumeRetry: true, Detail: err.Error(), Reason: "skipped_no_price"}
	case errors.Is(err, lxmf.ErrUnknownDestination), errors.Is(err, lxmf.ErrNoPath), errors.Is(err, lxmf.ErrLinkFailed):
		return SendOutcome{Class: database.ClassNotSent, Retryable: true, ConsumeRetry: true, Detail: err.Error(), Reason: "not_sent"}
	case errors.Is(err, lxmf.ErrNotPrepared):
		// The proof's hash could not be stored first, so nothing was
		// written (red-team A13-R1). [MESHSAT-1584]
		return SendOutcome{Class: database.ClassNotSent, Retryable: true, ConsumeRetry: true, Detail: err.Error(), Reason: "not_sent"}
	case errors.Is(err, lxmf.ErrStampCostTooHigh), errors.Is(err, lxmf.ErrTooLarge):
		return SendOutcome{Class: database.ClassNotSent, Detail: err.Error(), Reason: "not_sent"}
	}
	o := LegacyOutcome(err)
	return o
}

// transmissionResult is a SendOutcome as the ledger records it.
func (o SendOutcome) transmissionResult() database.TransmissionResult {
	r := database.TransmissionResult{
		Class:        o.Class,
		Retryable:    o.Retryable,
		ConsumeRetry: o.ConsumeRetry,
		AcceptedBy:   o.AcceptedBy,
		CorrKind:     o.Corr.Kind,
		CorrValue:    o.Corr.Value,
		CorrScope:    o.Corr.Scope,
		CorrPeer:     o.Corr.Peer,
		Detail:       o.Detail,
		Reason:       o.Reason,
		Evidence:     o.Evidence,
	}
	for _, c := range o.Charges {
		r.ChargedUnits += c.Units
		r.ChargedMinor += c.Minor
		r.Confirmed = r.Confirmed || c.Confirmed
	}
	return r
}
