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
