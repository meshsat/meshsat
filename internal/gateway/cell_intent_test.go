package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"meshsat/internal/smpp"
)

// The 2G cell as a delivery-intent route, over the test SMPP server: a
// phone on the cell and one part whole; the phone's acknowledgement
// (submit_sm answered 0 in forward mode) is the handset's receipt; a phone
// off the cell, a delivery failure, a refusal and an answer that never came
// are each read for what they are. [MESHSAT-1584]

func TestCellIntent_AdmitsAPhoneOnTheCellAndOnePart(t *testing.T) {
	r := startCell(t, nil)
	for _, tc := range []struct {
		dest, text, reason string
	}{
		{"1001", "hello", ""},
		{"cell:1001", "hello", ""},
		{"PA0ABC-10", "hello", "skipped_unsupported"},
		{"1001", strings.Repeat("a", 160), ""},
		{"1001", strings.Repeat("a", 161), "skipped_oversize"},
		{"1001", strings.Repeat("γ", 70), ""},
		{"1001", strings.Repeat("γ", 71), "skipped_oversize"}, // lower-case Greek is UCS-2: 70 a part
		{"1001", strings.Repeat("Γ", 160), ""},                // capital Γ is in GSM 03.38
		{"1001", "", "skipped_encoding"},
	} {
		if got, d := r.gw.IntentAdmit(tc.dest, []byte(tc.text), false); got != tc.reason {
			t.Fatalf("%q, %d chars: %q (%s); want %q", tc.dest, len([]rune(tc.text)), got, d, tc.reason)
		}
	}
}

func TestCellIntent_EachAnswer(t *testing.T) {
	r := startCell(t, nil)
	r.srv.OnSubmit = func(sm smpp.SM) (uint32, time.Duration, string) {
		switch sm.DestAddr {
		case "2002":
			return smpp.StatusInvalidDstAddr, 0, ""
		case "3003":
			return smpp.StatusDeliveryFailure, 0, ""
		case "4004":
			return smpp.StatusSysErr, 0, ""
		case "5005":
			return smpp.StatusOK, 2 * time.Second, "" // the phone answers late
		}
		return smpp.StatusOK, 10 * time.Millisecond, ""
	}
	o := r.gw.IntentSend(context.Background(), IntentSend{Destination: "1001", Text: "as written", CorrValue: "c1"})
	if o.Class != IntentAccepted || o.Receipt == nil || o.Receipt.Completes != "delivered_handset" || o.AcceptedBy != "handset" {
		t.Fatalf("acknowledged: %+v", o)
	}
	if s := r.srv.Submits(); len(s) != 1 || smpp.DecodeGSM7(s[0].ShortMessage) != "as written" || s[0].DestAddr != "1001" {
		t.Fatalf("submits %+v", s)
	}
	for _, tc := range []struct {
		name, dest    string
		class, reason string
		retry, spend  bool
	}{
		{"not on the cell", "2002", IntentNotSent, "not_sent", true, false},
		{"refused", "4004", IntentNotSent, "not_sent", true, true},
	} {
		o := r.gw.IntentSend(context.Background(), IntentSend{Destination: tc.dest, Text: "x", CorrValue: "c2"})
		if o.Class != tc.class || o.Reason != tc.reason || o.Retryable != tc.retry || o.ConsumeRetry != tc.spend || o.Receipt != nil {
			t.Fatalf("%s: %+v", tc.name, o)
		}
	}
	// A delivery failure (0xFE): paging expired, the phone refused it, or
	// the cell lost track after it went (osmo-msc 1.16.0). Not proof that
	// the phone did not get it (review 2026-10-18 13:47, CELL-R1).
	if o := r.gw.IntentSend(context.Background(), IntentSend{Destination: "3003", Text: "x", CorrValue: "c2b"}); o.Class != IntentUnknown || o.Retryable || o.Receipt != nil ||
		!strings.Contains(o.Detail, "may have been sent") {
		t.Fatalf("delivery failure: %+v", o)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if o := r.gw.IntentSend(ctx, IntentSend{Destination: "5005", Text: "x", CorrValue: "c3"}); o.Class != IntentUnknown {
		t.Fatalf("no answer in time: %+v; the phone may have it", o)
	}
	idle := NewCellGateway(DefaultCellConfig(), nil, "cell_0")
	if o := idle.IntentSend(context.Background(), IntentSend{Destination: "1001", Text: "x", CorrValue: "c4"}); o.Class != IntentNotSent || o.Reason != "skipped_offline" || !o.Retryable {
		t.Fatalf("unbound: %+v", o)
	}
}
