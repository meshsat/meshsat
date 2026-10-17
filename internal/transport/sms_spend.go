package transport

import "errors"

// SMSSpend is the prepaid SMS bundle as the modem's send path holds it
// (MESHSAT-1161; r3 D8, MESHSAT-1584). Each SMS takes its hold on the
// serial I/O loop before its first byte and settles it there from the
// modem's answer, so the bundle check and the use are one step under the
// loop that serializes the sends: a command that outlives its caller still
// owns its share, and the next one is checked after it settled (B9FIX-R1).
type SMSSpend interface {
	// HoldSMS takes the share of one SMS. A delivery intent's SMS
	// (enforce) is refused, with an error wrapping ErrSMSBundle, when the
	// bundle cannot take it, or ErrSMSBundleNotReady when the counter
	// cannot be read; the other send paths are counted only and always
	// get their hold.
	HoldSMS(to, text string, enforce bool) (SMSHold, error)
	// CheckSMS is the same check without a hold, for a caller deciding
	// whether to queue at all: nil, or an error wrapping ErrSMSBundle or
	// ErrSMSBundleNotReady. HoldSMS still decides.
	CheckSMS(segments int) error
}

// SMSHold is one SMS's share of the bundle.
type SMSHold interface {
	// Possible: the SMS goes to the modem now, and from here it may be
	// submitted, so the share is possible use, kept across a restart. For
	// a delivery intent's SMS an error (the share could not be stored,
	// wrapping ErrSMSBundleNotReady) means: do not send it (B9FIX-R4).
	Possible() error
	// Settle ends the hold by the phase reached: accepted (+CMGS) counts
	// it as used, refused or not sent gives it back, handed over (no
	// +CMGS) leaves it possible use (B9FIX-R2).
	Settle(SMSPhase)
}

// ErrSMSBundle is an intent SMS the prepaid bundle cannot take.
var ErrSMSBundle = errors.New("the prepaid SMS bundle cannot take this SMS")

// ErrSMSBundleNotReady is an intent SMS asked for while the bundle counter
// cannot account for it: not set on the transport yet (B9FIX-R3), or its
// state cannot be read or stored (B9FIX-R4). It waits, it is not sent.
var ErrSMSBundleNotReady = errors.New("the SMS bundle counter is not ready")
