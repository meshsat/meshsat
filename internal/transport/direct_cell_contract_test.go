package transport

import (
	"bytes"
	"context"
	"testing"
)

// B7(l) and B9SMS-R2: a delivery intent's SMS goes only under the text
// contract it was admitted for, and only a +CMGS reference is the
// network's acceptance. [MESHSAT-1584]

func cmgsWritten(m *submitModem) bool { return bytes.Contains(m.wrote(), []byte("AT+CMGS=")) }

// The contract is set and read back before every CMGS: a character set
// kept from earlier is put back to IRA and the SMS goes; a modem that
// refuses IRA, keeps another set, or codes text other than GSM 7-bit gets
// no CMGS at all.
func TestSubmitSMS_TextContract(t *testing.T) {
	for _, tc := range []struct {
		name  string
		m     *submitModem
		phase SMSPhase
	}{
		{"kept_ucs2_is_set_back_to_ira", &submitModem{cscs: "UCS2", afterBody: "\r\n+CMGS: 7\r\n\r\nOK\r\n"}, SMSAccepted},
		{"cscs_refused", &submitModem{cscsRefuse: true, afterBody: "\r\n+CMGS: 7\r\n\r\nOK\r\n"}, SMSNotSent},
		{"cscs_stuck_on_gsm", &submitModem{cscs: "GSM", cscsStuck: true, afterBody: "\r\n+CMGS: 7\r\n\r\nOK\r\n"}, SMSNotSent},
		{"text_coding_ucs2", &submitModem{dcs: 8, afterBody: "\r\n+CMGS: 7\r\n\r\nOK\r\n"}, SMSNotSent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := runSubmitCell(t, tc.m)
			res, err := tr.SubmitSMS(context.Background(), "+31612345678", "intent text")
			if res.Phase != tc.phase {
				t.Fatalf("phase %d (err %v); want %d", res.Phase, err, tc.phase)
			}
			w := tc.m.wrote()
			cs, cm := bytes.Index(w, []byte(`AT+CSCS="IRA"`)), bytes.Index(w, []byte("AT+CMGS="))
			if tc.phase == SMSAccepted && (cs < 0 || cm < cs) {
				t.Fatalf("IRA was not set before the CMGS: %q", w)
			}
			if tc.phase == SMSNotSent && cmgsWritten(tc.m) {
				t.Fatalf("a CMGS went out under a broken contract: %q", w)
			}
		})
	}
}

// Every send sets the contract again: a modem that comes back from a
// reconnect, or had its set changed by something else, is put back.
func TestSubmitSMS_TextContractOnEverySend(t *testing.T) {
	m := &submitModem{afterBody: "\r\n+CMGS: 1\r\n\r\nOK\r\n"}
	tr := runSubmitCell(t, m)
	for i := 0; i < 2; i++ {
		m.mu.Lock()
		m.cscs = "UCS2" // a reconnect or another client left it so
		m.mu.Unlock()
		if res, err := tr.SubmitSMS(context.Background(), "+31612345678", "intent text"); res.Phase != SMSAccepted {
			t.Fatalf("send %d: phase %d err %v", i, res.Phase, err)
		}
	}
	if n := bytes.Count(m.wrote(), []byte(`AT+CSCS="IRA"`)); n != 2 {
		t.Fatalf("IRA set %d times; want once per send", n)
	}
}

// The legacy send path is unchanged: no contract commands.
func TestSendSMS_LegacyPathSetsNoContract(t *testing.T) {
	m := &submitModem{afterBody: "\r\n+CMGS: 1\r\n\r\nOK\r\n"}
	tr := runSubmitCell(t, m)
	if err := tr.SendSMS(context.Background(), "+31612345678", "legacy text"); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(m.wrote(), []byte("AT+CSCS")) || bytes.Contains(m.wrote(), []byte("AT+CSMP")) {
		t.Fatalf("the legacy path sent contract commands: %q", m.wrote())
	}
}

// Acceptance needs a +CMGS reference: 0 is one; an OK with no reference,
// or with only an unrelated line, leaves it handed over (it may have
// gone); an explicit refusal stays refused.
func TestSubmitSMS_AcceptanceEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		phase        SMSPhase
		ref          int64
	}{
		{"reference_0", "\r\n+CMGS: 0\r\n\r\nOK\r\n", SMSAccepted, 0},
		{"reference_255", "\r\n+CMGS: 255\r\n\r\nOK\r\n", SMSAccepted, 255},
		{"bare_ok", "\r\nOK\r\n", SMSHandedOver, 0},
		{"unrelated_line_then_ok", "\r\n+CMTI: \"SM\",3\r\n\r\nOK\r\n", SMSHandedOver, 0},
		{"cms_error", "\r\n+CMS ERROR: 38\r\n", SMSRefused, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := runSubmitCell(t, &submitModem{afterBody: tc.answer})
			res, err := tr.SubmitSMS(context.Background(), "+31612345678", "intent text")
			if res.Phase != tc.phase || res.Ref != tc.ref {
				t.Fatalf("phase %d ref %d (err %v); want %d ref %d", res.Phase, res.Ref, err, tc.phase, tc.ref)
			}
			if tc.phase == SMSHandedOver && err == nil {
				t.Fatal("handed over with no error to say why")
			}
		})
	}
}
