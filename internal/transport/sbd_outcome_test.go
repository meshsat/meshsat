package transport

import (
	"errors"
	"fmt"
	"testing"
)

// How far an SBD send got, as a delivery intent reads it: the +SBDIX MO
// status table of MAN0009 v2 section 5.144, the errors before a session,
// and the 9603 emulator's sends. [MESHSAT-1584]

func TestSBDMOStatus_TheManualsTable(t *testing.T) {
	type want struct {
		phase SBDPhase
		retry bool
	}
	for mo := 0; mo <= 70; mo++ {
		w := want{SBDUnknown, false} // reserved and undocumented values
		switch {
		case mo <= 4:
			w = want{SBDAccepted, false}
		case mo == 10, mo == 13, mo == 17, mo == 18, mo == 19:
			w = want{SBDUnknown, false} // may have reached the gateway
		case mo >= 5 && mo <= 8, mo == 11:
			w = want{SBDNotSent, true}
		case mo == 12, mo == 14, mo == 15, mo == 16:
			w = want{SBDNotSent, false}
		case mo >= 32 && mo <= 38, mo == 64, mo == 65:
			w = want{SBDNotSent, true}
		}
		if p, r := SBDMOStatus(mo); p != w.phase || r != w.retry {
			t.Errorf("MO status %d: phase %d retry %v; want %d %v", mo, p, r, w.phase, w.retry)
		}
	}
}

func TestSBDOutcome_Errors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		res     *SatResult
		err     error
		phase   SBDPhase
		retry   bool
		offline bool
	}{
		{"line down", nil, fmt.Errorf("sbd: %w", ErrNotConnected), SBDNotSent, true, true},
		{"before the session", nil, fmt.Errorf("%w: AT+SBDWB failed", ErrNotSubmitted), SBDNotSent, true, false},
		{"too long", nil, fmt.Errorf("x: %w", ErrPayloadTooLong), SBDNotSent, false, false},
		{"the node could not settle it", &SatResult{MOStatus: 0}, fmt.Errorf("SBDIX failed: %w", ErrOutcomeUnknown), SBDUnknown, false, false},
		{"an error at or after the SBDIX", nil, errors.New("SBDIX failed: read timeout"), SBDUnknown, false, false},
		{"no result and no error", nil, nil, SBDUnknown, false, false},
		{"accepted", &SatResult{MOStatus: 0, MOMSN: 7}, nil, SBDAccepted, false, false},
		{"no network", &SatResult{MOStatus: 32}, nil, SBDNotSent, true, false},
		{"did not complete", &SatResult{MOStatus: 13}, nil, SBDUnknown, false, false},
	} {
		s := SBDOutcome(tc.res, tc.err)
		if s.Phase != tc.phase || s.Retry != tc.retry || s.Offline != tc.offline {
			t.Errorf("%s: %+v; want phase %d retry %v offline %v", tc.name, s, tc.phase, tc.retry, tc.offline)
		}
	}
}

// Through the 9603 emulator: what each kind of session gives, and that a
// send that never reached its SBDIX says so (ErrNotSubmitted).
func TestSBDOutcome_OverTheEmulator(t *testing.T) {
	for _, tc := range []struct {
		name    string
		binary  bool
		set     func(e *sbdEmu)
		phase   SBDPhase
		retry   bool
		session bool
	}{
		{"taken", true, func(e *sbdEmu) {}, SBDAccepted, false, true},
		{"taken as text", false, func(e *sbdEmu) {}, SBDAccepted, false, true},
		{"the load's answer lost", true, func(e *sbdEmu) { e.loadLost = true }, SBDNotSent, true, false},
		{"the text load's answer lost", false, func(e *sbdEmu) { e.loadLost = true }, SBDNotSent, true, false},
		{"SBDIX refused with ERROR", true, func(e *sbdEmu) { e.sbdixReply = "ERROR" }, SBDNotSent, true, true},
		{"no network service", true, func(e *sbdEmu) { e.sbdixReply = "+SBDIX: 32, 0, 0, 0, 0, 0" }, SBDNotSent, true, true},
		{"the session did not complete", true, func(e *sbdEmu) { e.sbdixReply = "+SBDIX: 13, 0, 0, 0, 0, 0" }, SBDUnknown, false, true},
		{"RF drop", false, func(e *sbdEmu) { e.sbdixReply = "+SBDIX: 18, 0, 0, 0, 0, 0" }, SBDUnknown, false, true},
		{"access denied", true, func(e *sbdEmu) { e.sbdixReply = "+SBDIX: 15, 0, 0, 0, 0, 0" }, SBDNotSent, false, true},
		{"the answer never came", true, func(e *sbdEmu) { e.sbdixSilent = true }, SBDUnknown, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("IRIDIUM_SBDIX_TIMEOUT", "1")
			e := newSBDEmu()
			tc.set(e)
			tr := emuTransport(t, e)
			o := await(t, queuedSend{"intent mo", tc.binary}.start(tr), "the send")
			s := SBDOutcome(o.res, o.err)
			if s.Phase != tc.phase || s.Retry != tc.retry {
				t.Fatalf("%+v (err %v); want phase %d retry %v", s, o.err, tc.phase, tc.retry)
			}
			if got := e.count("AT+SBDIX") > 0; got != tc.session {
				t.Fatalf("a session went: %v; want %v (%q)", got, tc.session, e.sent())
			}
			if tc.phase == SBDNotSent && !tc.session && e.moBuffer() != "" {
				t.Fatalf("not sent, but the MO buffer holds %q", e.moBuffer())
			}
		})
	}
}
