package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A delivery intent's MO through the 9704, and one MO at a time. [MESHSAT-1584]

// The 14 final MO statuses of RockBLOCK-9704 v1.1.2 (jsprFinalMoStatus_t in
// src/jspr.h, wire strings in src/jspr.c), each with what it means here
// once this send's payload went to the modem: only an ack, a cancel before
// transit and a missing segment have a stated basis; the rest may have left
// (review IMT-R4). Before the payload went, nothing can have.
func TestClassifyIMTMO_EveryOfficialFinalStatus(t *testing.T) {
	afterPayload := map[string]IMTPhase{
		"mo_ack_received":                 IMTAccepted,
		"message_discarded_on_overflow":   IMTUnknown,
		"message_expired":                 IMTUnknown,
		"message_transfer_timeout":        IMTUnknown,
		"segment_not_supplied":            IMTNotSent,
		"segment_incorrect":               IMTUnknown,
		"network_error":                   IMTUnknown,
		"message_cancelled_pre_transit":   IMTNotSent,
		"message_cancelled_in_transit":    IMTUnknown,
		"subscription_invalid":            IMTUnknown,
		"protocol_error":                  IMTUnknown,
		"message_dropped_local_crc_error": IMTUnknown,
		"crc_error_in_transfer":           IMTUnknown,
		"user_supplied_crc_error":         IMTUnknown,
	}
	if len(imtStatusBasis) != len(afterPayload) {
		t.Fatalf("the table has %d statuses, the library %d", len(imtStatusBasis), len(afterPayload))
	}
	for status, phase := range afterPayload {
		if e, ok := imtStatusBasis[status]; !ok || e.basis == "" {
			t.Errorf("%s: no recorded basis", status)
		}
		if got := classifyIMTMO(moResult{Status: status, Supplied: moSuppliedYes}); got.Phase != phase {
			t.Errorf("%s after the payload: phase %d, want %d", status, got.Phase, phase)
		}
		before := classifyIMTMO(moResult{Status: status, Supplied: moSuppliedNo})
		switch {
		case status == "mo_ack_received":
			if before.Phase != IMTUnknown || !before.Inconsistent {
				t.Errorf("an ack for a payload never supplied: %+v, want unknown and inconsistent", before)
			}
		case before.Phase != IMTNotSent:
			t.Errorf("%s before the payload: phase %d, want not sent", status, before.Phase)
		}
	}
	if got := classifyIMTMO(moResult{Status: "mo_ack_received"}); got.Phase != IMTUnknown || !got.Inconsistent {
		t.Errorf("an ack without the helper's account of the payload: %+v, want unknown", got)
	}
	if classifyIMTMO(moResult{Status: "subscription_invalid", Supplied: moSuppliedNo}).Retry {
		t.Error("an invalid subscription is retried")
	}
	if !classifyIMTMO(moResult{Status: "message_cancelled_pre_transit", Supplied: moSuppliedYes}).Retry {
		t.Error("a message cancelled before transit (no sky) is not retried")
	}
}

// An MO that ended without a final status from the modem: it cannot have
// left when the modem refused it or never got its payload, and may have
// otherwise.
func TestClassifyIMTMO_EndsWithoutAModemStatus(t *testing.T) {
	for _, tc := range []struct {
		name  string
		r     moResult
		phase IMTPhase
		retry bool
	}{
		{"refused at messageOriginate (a 4xx)", moResult{Supplied: moSuppliedNo}, IMTNotSent, true},
		{"overflow at messageOriginate", moResult{Status: "message_discarded_on_overflow", Supplied: moSuppliedNo}, IMTNotSent, true},
		{"invalid subscription at messageOriginate", moResult{Status: "subscription_invalid", Supplied: moSuppliedNo}, IMTNotSent, false},
		{"no originate reply", moResult{Status: "no_response", Supplied: moSuppliedNo}, IMTNotSent, true},
		{"helper timeout before the segment", moResult{Status: "helper_timeout", Supplied: moSuppliedNo}, IMTNotSent, true},
		{"helper timeout after the segment", moResult{Status: "helper_timeout", Supplied: moSuppliedYes}, IMTUnknown, false},
		{"Go stopped waiting", moResult{}, IMTUnknown, false},
		{"a status nobody documents", moResult{Status: "something_new", Supplied: moSuppliedYes}, IMTUnknown, false},
	} {
		got := classifyIMTMO(tc.r)
		if got.Phase != tc.phase || got.Retry != tc.retry {
			t.Errorf("%s: %+v, want phase %d retry %v", tc.name, got, tc.phase, tc.retry)
		}
	}
}

// fakeIMTHelper sits behind a real jsprHelperPort and jsprConn: it reads the
// commands Go writes to the helper and answers a send_mo with the lines
// answer gives, after delay, as cmd/jspr-helper does.
type fakeIMTHelper struct {
	answer   func(cmd map[string]any) []string
	delay    time.Duration
	active   atomic.Int32
	overlaps atomic.Int32
	mu       sync.Mutex
	commands []map[string]any
	out      *io.PipeWriter
	outMu    sync.Mutex
}

func (f *fakeIMTHelper) cmds(name string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.commands {
		if c["cmd"] == name {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeIMTHelper) write(line string) {
	f.outMu.Lock()
	defer f.outMu.Unlock()
	_, _ = f.out.Write([]byte(line + "\n"))
}

// moLine is one helper output line carrying the result of send_mo ref.
func moLine(code int, target string, fields map[string]any) string {
	b, _ := json.Marshal(map[string]any{"type": "mo_result", "code": code, "target": target, "json": fields})
	return string(b)
}

func newIMTTestTransport(t *testing.T, f *fakeIMTHelper) *DirectIMTTransport {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	f.out = outW
	h := &jsprHelperPort{stdin: inW, stdout: outR, scanner: bufio.NewScanner(outR), done: make(chan struct{}), lastRead: time.Now()}
	h.byteCond = sync.NewCond(&h.mu)
	go h.readLoop()
	c := newJSPRConn(h)
	c.startReader()
	go func() {
		sc := bufio.NewScanner(inR)
		for sc.Scan() {
			var cmd map[string]any
			if json.Unmarshal(sc.Bytes(), &cmd) != nil {
				continue
			}
			f.mu.Lock()
			f.commands = append(f.commands, cmd)
			f.mu.Unlock()
			if cmd["cmd"] != "send_mo" {
				continue
			}
			if f.active.Add(1) > 1 {
				f.overlaps.Add(1)
			}
			go func(cmd map[string]any) {
				time.Sleep(f.delay)
				lines := f.answer(cmd)
				f.active.Add(-1)
				for _, l := range lines {
					f.write(l)
				}
			}(cmd)
		}
	}()
	tr := NewDirectIMTTransport("test")
	tr.conn, tr.file, tr.connected = c, h, true
	t.Cleanup(func() {
		c.stopReader()
		_ = inW.Close()
		_ = outW.Close()
	})
	return tr
}

func refOf(cmd map[string]any) int { v, _ := cmd["request_reference"].(float64); return int(v) }

// ackOrCancel answers odd references with an ack and even ones with a cancel
// before transit, tagged as the helper tags its results.
func ackOrCancel(cmd map[string]any) []string {
	status := "mo_ack_received"
	if refOf(cmd)%2 == 0 {
		status = "message_cancelled_pre_transit"
	}
	return []string{moLine(200, "messageOriginateStatus", map[string]any{"final_mo_status": status,
		"message_id": refOf(cmd), "topic_id": 244, "request_reference": refOf(cmd), "segment_supplied": true})}
}

// Two MOs at once (an intent and the Reticulum satellite interface, or the
// dead-letter retries) go to the modem one after the other, and each gets its
// own result; before, the second could take the first's result or time out
// on a message that went.
func TestIMT_OneMOAtATime(t *testing.T) {
	f := &fakeIMTHelper{answer: ackOrCancel, delay: 80 * time.Millisecond}
	tr := newIMTTestTransport(t, f)
	type got struct {
		status string
		ref    int
	}
	results := make(chan got, 4)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				sub, err := tr.SubmitMO(context.Background(), []byte("intent"))
				if err != nil {
					t.Errorf("intent MO: %v", err)
				}
				results <- got{status: sub.Status}
				return
			}
			res, err := tr.Send(context.Background(), []byte("legacy"))
			if err != nil {
				t.Errorf("legacy MO: %v", err)
				return
			}
			results <- got{status: res.StatusText}
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("an MO did not get its result")
	}
	close(results)
	if n := f.overlaps.Load(); n != 0 {
		t.Fatalf("%d MOs reached the helper while another was still with it", n)
	}
	acks, cancels := 0, 0
	for r := range results {
		switch r.status {
		case "mo_ack_received":
			acks++
		case "message_cancelled_pre_transit":
			cancels++
		default:
			t.Errorf("status %q", r.status)
		}
	}
	// References 1..4: two odd, two even; each send got its own.
	if acks != 2 || cancels != 2 {
		t.Fatalf("acks %d, cancels %d: a send got another's result", acks, cancels)
	}
}

// The MT poll's yield cancels the MO with the modem only when it is a
// legacy send; an intent's MO is never cancelled under it.
func TestIMT_CancelOnlyALegacyMO(t *testing.T) {
	for _, intent := range []bool{true, false} {
		f := &fakeIMTHelper{answer: ackOrCancel, delay: 300 * time.Millisecond}
		tr := newIMTTestTransport(t, f)
		done := make(chan struct{})
		go func() {
			defer close(done)
			if intent {
				_, _ = tr.SubmitMO(context.Background(), []byte("intent"))
			} else {
				_, _ = tr.Send(context.Background(), []byte("poll"))
			}
		}()
		deadline := time.Now().Add(2 * time.Second)
		for len(f.cmds("send_mo")) == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		cancelled := tr.CancelMO()
		<-done
		if cancelled == intent {
			t.Fatalf("intent=%v: CancelMO answered %v", intent, cancelled)
		}
		if got := len(f.cmds("cancel_mo")); (got > 0) == intent {
			t.Fatalf("intent=%v: %d cancel commands reached the helper", intent, got)
		}
	}
}

// An intent's MO takes only the result tagged with its own reference: an
// untagged error reply to another command, which the reader hands to any
// waiting request, is not its result. A legacy send still takes it.
func TestIMT_IntentMOSkipsAnotherCommandsReply(t *testing.T) {
	stray := func(cmd map[string]any) []string {
		b, _ := json.Marshal(map[string]any{"type": "response", "code": 400, "target": "constellationState", "json": map[string]any{}})
		return []string{string(b), moLine(200, "messageOriginateStatus", map[string]any{"final_mo_status": "mo_ack_received",
			"message_id": 5, "topic_id": 244, "request_reference": refOf(cmd), "segment_supplied": true})}
	}
	f := &fakeIMTHelper{answer: stray, delay: 50 * time.Millisecond}
	tr := newIMTTestTransport(t, f)
	sub, err := tr.SubmitMO(context.Background(), []byte("intent"))
	if err != nil || sub.Phase != IMTAccepted || sub.Status != "mo_ack_received" {
		t.Fatalf("intent MO: %+v, %v; want its own ack", sub, err)
	}
}

// What the helper says about a send it ended itself decides whether the MO
// can have left.
func TestIMT_SubmitMOOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		line  func(ref int) string
		phase IMTPhase
		retry bool
	}{
		{"acknowledged", func(r int) string {
			return moLine(200, "messageOriginateStatus", map[string]any{"final_mo_status": "mo_ack_received", "message_id": 3, "request_reference": r, "segment_supplied": true})
		}, IMTAccepted, false},
		{"no sky, cancelled before transit", func(r int) string {
			return moLine(200, "messageOriginateStatus", map[string]any{"final_mo_status": "message_cancelled_pre_transit", "message_id": 3, "request_reference": r})
		}, IMTNotSent, true},
		{"the modem refused messageOriginate", func(r int) string {
			return moLine(410, "messageOriginate", map[string]any{"request_reference": r, "refused_at": "originate", "segment_supplied": false})
		}, IMTNotSent, true},
		{"a 4xx at messageOriginate from a helper that says no more", func(r int) string {
			return moLine(418, "messageOriginate", map[string]any{"request_reference": r})
		}, IMTNotSent, true},
		{"the subscription is invalid", func(r int) string {
			return moLine(200, "messageOriginateStatus", map[string]any{"final_mo_status": "subscription_invalid", "request_reference": r, "refused_at": "originate", "segment_supplied": false})
		}, IMTNotSent, false},
		{"the modem never answered", func(r int) string {
			return moLine(200, "messageOriginateStatus", map[string]any{"final_mo_status": "no_response", "request_reference": r, "segment_supplied": false})
		}, IMTNotSent, true},
		{"an ack for a payload the helper never supplied", func(r int) string {
			return moLine(200, "messageOriginateStatus", map[string]any{"final_mo_status": "mo_ack_received", "message_id": 22, "request_reference": r, "segment_supplied": false})
		}, IMTUnknown, false},
		{"a local CRC error after the payload went", func(r int) string {
			return moLine(200, "messageOriginateStatus", map[string]any{"final_mo_status": "message_dropped_local_crc_error", "request_reference": r, "segment_supplied": true})
		}, IMTUnknown, false},
		{"the helper gave up with the payload on the modem", func(r int) string {
			return moLine(200, "messageOriginateStatus", map[string]any{"final_mo_status": "helper_timeout", "request_reference": r, "segment_supplied": true})
		}, IMTUnknown, false},
		{"cancelled in transit", func(r int) string {
			return moLine(200, "messageOriginateStatus", map[string]any{"final_mo_status": "message_cancelled_in_transit", "request_reference": r, "segment_supplied": true})
		}, IMTUnknown, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeIMTHelper{answer: func(cmd map[string]any) []string { return []string{tc.line(refOf(cmd))} }}
			tr := newIMTTestTransport(t, f)
			sub, _ := tr.SubmitMO(context.Background(), []byte("intent"))
			if sub.Phase != tc.phase || sub.Retry != tc.retry {
				t.Fatalf("%+v, want phase %d retry %v", sub, tc.phase, tc.retry)
			}
		})
	}
}

// Nothing goes, and nothing is booked as possibly sent, without a
// connection, with a payload past one segment, or while another MO holds
// the modem past the caller's deadline.
func TestIMT_SubmitMONotStarted(t *testing.T) {
	tr := NewDirectIMTTransport("test")
	sub, err := tr.SubmitMO(context.Background(), []byte("x"))
	if sub.Phase != IMTNotSent || !sub.Retry || !errors.Is(err, ErrNotConnected) {
		t.Fatalf("not connected: %+v, %v", sub, err)
	}
	sub, err = tr.SubmitMO(context.Background(), []byte(strings.Repeat("x", IMTOneSegmentBytes+1)))
	if sub.Phase != IMTNotSent || sub.Retry || err == nil {
		t.Fatalf("past one segment: %+v, %v", sub, err)
	}
	release, err := tr.acquireMO(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	sub, err = tr.SubmitMO(ctx, []byte("x"))
	release()
	if sub.Phase != IMTNotSent || !sub.Retry || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("modem busy: %+v, %v", sub, err)
	}
}

// instantHelper is a helper's stdin that answers a send_mo before the write
// returns, as a fast modem could: the result is with the reader before the
// sender is back from the write.
type instantHelper struct{ h *jsprHelperPort }

func (w instantHelper) Write(p []byte) (int, error) {
	var cmd map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(string(p))), &cmd) == nil && cmd["cmd"] == "send_mo" {
		line := fmt.Sprintf("200 messageOriginateStatus {\"final_mo_status\": \"mo_ack_received\", \"request_reference\": %d, \"segment_supplied\": true}\r", refOf(cmd))
		w.h.mu.Lock()
		w.h.byteBuf = append(w.h.byteBuf, line...)
		w.h.mu.Unlock()
		w.h.byteCond.Broadcast()
		for i := 0; i < 500; i++ { // until the reader has taken and dispatched it
			w.h.mu.Lock()
			n := len(w.h.byteBuf)
			w.h.mu.Unlock()
			if n == 0 {
				break
			}
			time.Sleep(time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return len(p), nil
}

func (instantHelper) Close() error { return nil }

// A result that comes back before the send is back from writing its command
// is taken: the wait is registered before the command goes. Before, it was
// dropped as unmatched and the send waited three minutes for a message that
// had gone.
func TestIMT_AResultBeforeTheWriteReturnsIsTaken(t *testing.T) {
	h := &jsprHelperPort{done: make(chan struct{}), lastRead: time.Now()}
	h.byteCond = sync.NewCond(&h.mu)
	h.stdin = instantHelper{h}
	c := newJSPRConn(h)
	c.startReader()
	t.Cleanup(c.stopReader)
	tr := NewDirectIMTTransport("test")
	tr.conn, tr.file, tr.connected = c, h, true
	got := make(chan IMTSubmit, 1)
	go func() {
		sub, _ := tr.SubmitMO(context.Background(), []byte("fast"))
		got <- sub
	}()
	select {
	case sub := <-got:
		if sub.Phase != IMTAccepted {
			t.Fatalf("%+v", sub)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the result came before the wait and was lost")
	}
}
