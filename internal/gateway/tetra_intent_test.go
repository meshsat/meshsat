package gateway

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	"meshsat/internal/tetra"
)

// The TETRA radio as a delivery-intent route, over the fake MTP3550: an
// SDS address and one SDS whole, the wire as written, and the radio's
// answers read as taken, not sent or offline; with a MeshSat Bridge on the
// group, its acknowledgement as acked by a station and its silence as
// unknown. [MESHSAT-1584]

func TestTetraIntent_AdmitsAnSDSAddressAndOneSDSWhole(t *testing.T) {
	g := startTetra(t, newTetraFake(1), DefaultTetraConfig())
	for _, tc := range []struct {
		dest, text, reason string
	}{
		{"2000", "hello", ""},
		{"tetra:2000", "hello", ""},
		{"7530236100000100", "hello", ""},
		{"!4370c1d8", "hello", "skipped_unsupported"},
		{"2000", strings.Repeat("e", 126), ""}, // 126 + 4 = 130 bytes, the radio's limit
		{"2000", strings.Repeat("e", 127), "skipped_oversize"},
		{"2000", "", "skipped_encoding"},
	} {
		if r, d := g.IntentAdmit(tc.dest, []byte(tc.text), false); r != tc.reason {
			t.Fatalf("%q, %d chars: %q (%s); want %q", tc.dest, len(tc.text), r, d, tc.reason)
		}
	}
}

func TestTetraIntent_OneSDSAsWritten(t *testing.T) {
	f := newTetraFake(1)
	g := startTetra(t, f, DefaultTetraConfig())
	o := g.IntentSend(context.Background(), IntentSend{Destination: "2000", Text: "status ok", PlainText: "status ok", CorrValue: "c1"})
	if o.Class != IntentAccepted || o.AcceptedBy != "radio" {
		t.Fatalf("%+v", o)
	}
	sent := f.sent()
	if len(sent) != 1 {
		t.Fatalf("%d SDS; want one", len(sent))
	}
	pdu, _ := hex.DecodeString(sent[0])
	if body := string(pdu[4:]); body != "status ok" {
		t.Fatalf("on air %q; want the text as written, no sender prefix", body)
	}
	if !has(f.commands(), "AT+CMGS=2000,"+itoa(len(pdu)*8)) {
		t.Fatalf("commands %q", f.commands())
	}
	if g.IntentCorrKind() != TetraIntentCorrKind {
		t.Fatal("correlation kind")
	}
	if _, billed := interface{}(g).(IntentBilled); billed {
		t.Fatal("a TETRA SDS in Direct Mode is free")
	}
}

func TestTetraIntent_RefusedAndNoRadio(t *testing.T) {
	f := newTetraFake(1)
	g := startTetra(t, f, DefaultTetraConfig())
	f.mu.Lock()
	f.refuse = true
	f.mu.Unlock()
	if o := g.IntentSend(context.Background(), IntentSend{Destination: "2000", Text: "x", CorrValue: "c1"}); o.Class != IntentNotSent || !o.Retryable || !o.ConsumeRetry || o.Reason != "not_sent" {
		t.Fatalf("refused by the radio: %+v", o)
	}
	// The radio takes it, then reports it not sent (a collision): it may
	// have gone on air (another radio can decode it by capture), so unknown,
	// never sent again by itself, the radio's report kept (review
	// 2026-10-18 13:47).
	f.mu.Lock()
	f.refuse, f.collide = false, true
	f.mu.Unlock()
	if o := g.IntentSend(context.Background(), IntentSend{Destination: "2000", Text: "x", CorrValue: "c1b"}); o.Class != IntentUnknown || o.Retryable || o.Receipt != nil ||
		!strings.Contains(o.Detail, "may have been sent") || !strings.Contains(o.Detail, "not sent") {
		t.Fatalf("reported not sent by the radio: %+v", o)
	}
	idle := NewTetraGateway(DefaultTetraConfig(), "tetra_0", func() string { return "" })
	if o := idle.IntentSend(context.Background(), IntentSend{Destination: "2000", Text: "x", CorrValue: "c2"}); o.Class != IntentNotSent || !o.Retryable || o.Reason != "skipped_offline" {
		t.Fatalf("no radio: %+v", o)
	}
}

// With a MeshSat Bridge known on the group (its hello), an SDS it
// acknowledges is acked by a station inside the call (unauthenticated),
// and one it does not is unknown, never not sent: it may have arrived and
// the acknowledgement been lost.
func TestTetraIntent_APeersAckIsAStationAckAndItsSilenceUnknown(t *testing.T) {
	f := newTetraFake(1)
	g := startTetra(t, f, DefaultTetraConfig())
	hello, bits := tetra.EncodeLink(tetra.Link{Kind: tetra.LinkHello, Tag: 7})
	f.incoming("+CTSDSR: 12,100,0,2000,0,"+itoa(bits), hex.EncodeToString(hello))
	waitTetra(t, "a known peer", func() bool { return g.TetraStatus().Peers == 1 })

	f.mu.Lock()
	f.peer = true
	f.mu.Unlock()
	o := g.IntentSend(context.Background(), IntentSend{Destination: "2000", Text: "acked please", CorrValue: "c-ack"})
	r := o.Receipt
	if o.Class != IntentAccepted || r == nil || r.Completes != "acked_station" || r.Trust != "unauthenticated" ||
		r.CorrKind != TetraIntentCorrKind || r.CorrValue != "c-ack" || r.Peer != "2000" {
		t.Fatalf("acknowledged: %+v receipt %+v", o, r)
	}

	f.mu.Lock()
	f.peer = false // both radios keyed up at once, or the ack was lost
	f.mu.Unlock()
	o = g.IntentSend(context.Background(), IntentSend{Destination: "2000", Text: "nobody acks", CorrValue: "c-silent"})
	if o.Class != IntentUnknown || o.Receipt != nil || o.Retryable {
		t.Fatalf("unacknowledged: %+v", o)
	}
	if !strings.Contains(o.Detail, "may have been sent") {
		t.Fatalf("detail %q", o.Detail)
	}
}

// With no MeshSat Bridge known the radio's report is all there is:
// accepted, not confirmed, and no receipt claimed.
func TestTetraIntent_NoPeerKnownIsAcceptedWithoutAReceipt(t *testing.T) {
	f := newTetraFake(1) // no peer: the gateway's hello at start goes unanswered
	g := startTetra(t, f, DefaultTetraConfig())
	o := g.IntentSend(context.Background(), IntentSend{Destination: "2000", Text: "plain handhelds", CorrValue: "c-plain"})
	if o.Class != IntentAccepted || o.Receipt != nil {
		t.Fatalf("%+v", o)
	}
}
