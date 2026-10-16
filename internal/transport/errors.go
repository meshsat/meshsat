package transport

import "errors"

// ErrNotConnected is returned by a transport whose device is still present but
// whose link is down: a serial port that closed, a radio mid-handshake, a modem
// that has not answered yet.
//
// It exists so the delivery worker can tell "the bearer is not up at this
// instant" apart from "this message was rejected". The two used to be
// indistinguishable, and a relayed text queued while a kit's mesh radio was
// re-enumerating was marked dead on its first attempt — the radio came back 78
// seconds later and the message was already gone. [MESHSAT-1061]
//
// Wrap it (fmt.Errorf("kiss: %w", transport.ErrNotConnected)) rather than
// returning a fresh error with the same words, so errors.Is keeps working. The
// message is deliberately the same string the transports used before, so
// anything reading logs or last_error sees no change.
var ErrNotConnected = errors.New("not connected")

// ErrNoAck is returned by a bearer that sent a message and never heard the far
// end acknowledge it: an APRS frame can vanish on the air with nothing telling
// the sender. The message may or may not have arrived. The delivery worker
// treats it as not delivered and hands it to the next member of the rule's
// failover group when there is one. [MESHSAT-1021]
var ErrNoAck = errors.New("no acknowledgement from the peer")

// ErrNoRecipient is returned by a bearer asked to send a message that names no
// recipient while no default one is set: an SMS forwarded by a rule that names
// no number, on a gateway with no default number (MeshSat Android: "Leave it
// empty and a text with no recipient is not sent"). No retry can help, so the
// delivery worker gives up at once, with the reason. [MESHSAT-1412]
var ErrNoRecipient = errors.New("not sent: the text has no recipient, and no default number is set")

// ErrPayloadTooLong is returned by a bearer asked to send a message that does
// not fit it, where cutting it would destroy it: ciphertext or a signed frame
// cut short cannot be decrypted or verified at the far end. Nothing is sent.
// No retry can help, so the delivery worker gives up at once, with the
// reason. [MESHSAT-1581]
var ErrPayloadTooLong = errors.New("not sent: the message does not fit, and cutting it would make it unreadable")

// ErrNotSubmitted is wrapped by a send that knows nothing left the device:
// a pre-send refusal, or a write that never started. A delivery intent's
// attempt that ends with it is proven not sent and may be retried or
// escalated; any other error means the outcome is unknown (architecture r3
// D18). ErrNotConnected, ErrPayloadTooLong and ErrNoRecipient say the same
// and are read the same way. [MESHSAT-1581]
var ErrNotSubmitted = errors.New("not submitted: nothing left the device")

// ErrOutcomeUnknown is returned by a satellite send whose session may have
// gone out while its outcome never reached this Bridge: the link to the
// MeshSat node dropped while the node's modem ran the session (or the write
// of AT+SBDIX failed where it may have landed), and the node's own account
// of its sessions could not settle it. A second session could deliver the
// message twice, and every session is billed, so the delivery worker never
// sends it again by itself: the delivery ends marked as possibly sent, and
// only a person's Retry sends it again. [MESHSAT-1391]
var ErrOutcomeUnknown = errors.New("the outcome of the satellite session is unknown: the message may have been sent")

// ErrSubscriberAbsent is returned by the cell gateway when the MeshSat 2G
// cell refused a text because the phone is not registered on it right now
// (SMPP status 0x0B, unknown subscriber) or did not answer its paging
// (0xFE, delivery failure). The cell does not queue it. A phone that comes
// back in range registers at once and its periodic location update is at
// most 30 minutes, so the delivery worker keeps the row and retries on a
// schedule of its own instead of spending the channel's retries.
// [MESHSAT-1557]
var ErrSubscriberAbsent = errors.New("the phone is not on the cell right now")
