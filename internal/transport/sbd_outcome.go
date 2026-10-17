package transport

import (
	"errors"
	"fmt"
)

// How far an SBD send got, for a delivery intent (HeMB-NG r3 D18, the SBD
// route). The 9603 says it in its +SBDIX MO status (ISU AT Command
// Reference MAN0009 v2, section 5.144); before the SBDIX a send knows
// nothing left (ErrNotSubmitted, ErrNotConnected). Accepted is the Iridium
// gateway (GSS) taking the MO: the provider's delivery to its destinations
// is never seen at the device. [MESHSAT-1584]

// SBDPhase is how far a submission got.
type SBDPhase int

const (
	// SBDNotSent: nothing left the modem, or the gateway said it did not
	// take the MO.
	SBDNotSent SBDPhase = iota
	// SBDAccepted: MO status 0 to 4, the gateway took the MO.
	SBDAccepted
	// SBDUnknown: a session that may have delivered the MO without the
	// modem saying so (a session that did not complete, a lost answer, an
	// RF drop), or a status this table does not know.
	SBDUnknown
)

// SBDSubmit is one submission's account.
type SBDSubmit struct {
	Phase SBDPhase
	// Retry (not sent only): a later session may succeed (no service, a
	// busy modem, the 3-minute wait); false when trying again cannot help
	// (the modem locked, access denied, a segment the gateway refuses).
	Retry    bool
	Offline  bool // not sent because the line is down (ErrNotConnected)
	MOStatus int  // -1 when no session answered
	MOMSN    int
	Status   string // what the modem or the error said
}

// SBDMOStatus classifies an +SBDIX MO status (section 5.144):
//
//   - 0 to 4: transferred (3 and 4 reserved, "indicate MO session success").
//   - 10 and 13, the gateway reporting the call or session did not complete,
//     and 17, 18 and 19, the ISU losing the gateway, the RF link or the
//     protocol mid-call: the MO may have reached the gateway. Unknown (r3:
//     13 and 18; 10, 17 and 19 added for the same reason).
//   - 5 to 8, 11, 12, 14, 15: the gateway reported it did not take the MO
//     (failure, queue full, too many segments, bad segment size, access
//     denied). Not sent.
//   - 16 locked; 32 to 36 unable to initiate the call (no service,
//     antenna, radio off, busy, the 3-minute wait); 37 SBD service
//     temporarily disabled; 38 refused for a traffic management period
//     (+SBDLOE). The modem reports none of them as a transferred MO. Not
//     sent, retryable.
//   - 64 band violation, 65 PLL lock failure: the transmitter refused or
//     failed before anything went out. Not sent.
//   - Anything else, the reserved ranges included, is unknown.
func SBDMOStatus(mo int) (phase SBDPhase, retry bool) {
	switch {
	case mo >= 0 && mo <= 4:
		return SBDAccepted, false
	case mo == 10 || mo == 13 || mo == 17 || mo == 18 || mo == 19:
		return SBDUnknown, false
	case mo >= 5 && mo <= 8, mo == 11:
		return SBDNotSent, true
	case mo == 12, mo == 14, mo == 15, mo == 16:
		return SBDNotSent, false
	case mo >= 32 && mo <= 38, mo == 64, mo == 65:
		return SBDNotSent, true
	}
	return SBDUnknown, false
}

// SBDOutcome reads a satellite send's result and error as a submission.
// An error that says nothing left (ErrNotSubmitted, ErrNotConnected,
// ErrPayloadTooLong) is not sent; ErrOutcomeUnknown, and any other error,
// since it came at or after the SBDIX, is unknown.
func SBDOutcome(res *SatResult, err error) SBDSubmit {
	out := SBDSubmit{MOStatus: -1}
	if res != nil {
		out.MOStatus, out.MOMSN, out.Status = res.MOStatus, res.MOMSN, res.StatusText
	}
	if err != nil {
		if out.Status == "" {
			out.Status = err.Error()
		} else {
			out.Status = fmt.Sprintf("%s: %v", out.Status, err)
		}
		switch {
		case errors.Is(err, ErrOutcomeUnknown):
			out.Phase = SBDUnknown
		case errors.Is(err, ErrNotConnected):
			out.Phase, out.Retry, out.Offline = SBDNotSent, true, true
		case errors.Is(err, ErrPayloadTooLong):
			out.Phase = SBDNotSent
		case errors.Is(err, ErrNotSubmitted):
			out.Phase, out.Retry = SBDNotSent, true
		default:
			out.Phase = SBDUnknown
		}
		return out
	}
	if res == nil {
		out.Phase, out.Status = SBDUnknown, "no result from the modem"
		return out
	}
	out.Phase, out.Retry = SBDMOStatus(res.MOStatus)
	return out
}
