package transport_test

// The prepaid SMS bundle on the real send path: the real gateway, the real
// bundle counter and the real serial I/O loop on an injected modem
// (B9FIX-R1, R2, R3). The first two tests are the reviewer's of 17 Oct
// 2026 (sms_fix_boundary_review_test.go), on this package's fixture
// instead of an exported helper in product code, and with the bundle
// installed as it is now (SetSMSSpend). [MESHSAT-1584]

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.bug.st/serial"

	"meshsat/internal/database"
	"meshsat/internal/gateway"
	"meshsat/internal/transport"
)

type reviewBundleModem struct {
	serial.Port
	mu          sync.Mutex
	reply       []byte
	inBody      bool
	writes      int
	afterBody   string
	ctrlZFail   bool   // the Ctrl-Z write reaches the modem and reports an error
	exitAt      string // "cmgs" or "ctrlz": the process exits there (crash tests)
	bodyFail    bool   // the body write fails with nothing written: definitely not sent
	cmgsSeen    int
	bodyEntered chan struct{}
	bodyRelease chan struct{}
	releaseOnce sync.Once
}

func (m *reviewBundleModem) Write(b []byte) (int, error) {
	if bytes.Equal(b, []byte("review first")) && m.bodyEntered != nil {
		close(m.bodyEntered)
		<-m.bodyRelease
	}
	if bytes.Equal(b, []byte("review first")) && m.bodyFail {
		return 0, errors.New("review: body write failed")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case m.inBody:
		if bytes.Contains(b, []byte{0x1a}) && m.exitAt == "ctrlz" {
			os.Exit(79)
		}
		if bytes.Contains(b, []byte{0x1a}) {
			m.inBody = false
			m.writes++
			if m.ctrlZFail {
				return len(b), errors.New("review: the Ctrl-Z went, then the write failed")
			}
			m.reply = append(m.reply, m.afterBody...)
		}
	case bytes.HasPrefix(b, []byte("AT+CMGS=")):
		if m.exitAt == "cmgs" {
			os.Exit(79)
		}
		m.cmgsSeen++
		m.inBody = true
		m.reply = append(m.reply, "\r\n> "...)
	case bytes.HasPrefix(b, []byte("AT+CSCS?")):
		m.reply = append(m.reply, "\r\n+CSCS: \"IRA\"\r\n\r\nOK\r\n"...)
	case bytes.HasPrefix(b, []byte("AT+CSMP?")):
		m.reply = append(m.reply, "\r\n+CSMP: 17,167,0,0\r\n\r\nOK\r\n"...)
	case bytes.HasPrefix(b, []byte("AT")):
		m.reply = append(m.reply, "\r\nOK\r\n"...)
	}
	return len(b), nil
}

func (m *reviewBundleModem) Read(b []byte) (int, error) {
	m.mu.Lock()
	if len(m.reply) > 0 {
		n := copy(b, m.reply)
		m.reply = m.reply[n:]
		m.mu.Unlock()
		return n, nil
	}
	m.mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	return 0, nil
}

func (m *reviewBundleModem) SetReadTimeout(time.Duration) error { return nil }
func (m *reviewBundleModem) Close() error                       { return nil }
func (m *reviewBundleModem) release() {
	if m.bodyRelease != nil {
		m.releaseOnce.Do(func() { close(m.bodyRelease) })
	}
}
func (m *reviewBundleModem) count() int { m.mu.Lock(); defer m.mu.Unlock(); return m.writes }
func (m *reviewBundleModem) cmgs() int  { m.mu.Lock(); defer m.mu.Unlock(); return m.cmgsSeen }

func bundleDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.New(filepath.Join(t.TempDir(), "bundle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// bundleLoop is the real loop on m, the real gateway on it and, when size
// is positive, a bundle of that size set on the transport as main does.
func bundleLoop(t *testing.T, m *reviewBundleModem, size int, answer time.Duration) (*gateway.CellularGateway, *transport.DirectCellTransport, *gateway.SMSBudget, *database.DB) {
	t.Helper()
	tr, stop := transport.StartSMSLoopForTest(m, answer)
	db := bundleDB(t)
	gw := gateway.NewCellularGateway(gateway.CellularConfig{}, tr, db)
	t.Cleanup(func() {
		m.release()
		if !stop() {
			t.Error("serial loop did not stop")
		}
	})
	var b *gateway.SMSBudget
	if size >= 0 {
		b = gateway.NewSMSBudget(db, "review", gateway.SMSBudgetOptions{Size: size}, nil)
		tr.SetSMSSpend(b)
	}
	return gw, tr, b, db
}

func reviewBundleGateway(t *testing.T, m *reviewBundleModem) (*gateway.CellularGateway, *transport.DirectCellTransport, *gateway.SMSBudget) {
	t.Helper()
	g, tr, b, _ := bundleLoop(t, m, 1, time.Second)
	return g, tr, b
}

// waitQueued waits, at most two seconds, until n commands wait for the
// loop.
func waitQueued(t *testing.T, tr *transport.DirectCellTransport, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for transport.SMSQueueLenForTest(tr) < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d commands never queued", n)
		}
		time.Sleep(time.Millisecond)
	}
}

func reviewBundleSend(g *gateway.CellularGateway, text string) gateway.IntentOutcome {
	return g.IntentSend(context.Background(), gateway.IntentSend{Destination: "+15550000000", Text: text, CorrValue: text})
}

func TestReviewSMSFixBundleAfterUnfinished(t *testing.T) {
	t.Run("completed_send_control", func(t *testing.T) {
		m := &reviewBundleModem{afterBody: "\r\n+CMGS: 42\r\n\r\nOK\r\n"}
		g, _, b := reviewBundleGateway(t, m)
		first := reviewBundleSend(g, "review first")
		second := reviewBundleSend(g, "review second")
		t.Logf("first=%s second=%s reason=%s submissions=%d sent=%d remaining=%d", first.Class, second.Class, second.Reason, m.count(), b.Status().Sent, b.Status().Remaining)
		if first.Class != gateway.IntentAccepted || second.Reason != "skipped_budget" || m.count() != 1 {
			t.Fatal("control failed")
		}
	})
	t.Run("unfinished_first_send", func(t *testing.T) {
		m := &reviewBundleModem{afterBody: "\r\n+CMGS: 42\r\n\r\nOK\r\n", bodyEntered: make(chan struct{}), bodyRelease: make(chan struct{})}
		g, tr, b := reviewBundleGateway(t, m)
		releasePrior, priorDone := transport.HoldSMSLoopForTest(tr)
		t.Cleanup(releasePrior)
		firstResult := make(chan gateway.IntentOutcome, 1)
		go func() { firstResult <- reviewBundleSend(g, "review first") }()
		deadline := time.Now().Add(time.Second)
		for transport.SMSQueueLenForTest(tr) == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(300 * time.Millisecond)
		releasePrior()
		<-priorDone
		select {
		case <-m.bodyEntered:
		case <-time.After(2 * time.Second):
			t.Fatal("first command never started")
		}
		var first gateway.IntentOutcome
		select {
		case first = <-firstResult:
		case <-time.After(2 * time.Second):
			t.Fatal("first caller did not return")
		}
		if first.Class != gateway.IntentUnknown || first.Charged || first.Retryable {
			t.Fatalf("first result %+v", first)
		}
		secondResult := make(chan gateway.IntentOutcome, 1)
		go func() { secondResult <- reviewBundleSend(g, "review second") }()
		var second gateway.IntentOutcome
		early := false
		deadline = time.Now().Add(time.Second)
		for transport.SMSQueueLenForTest(tr) == 0 && time.Now().Before(deadline) {
			select {
			case second = <-secondResult:
				early = true
			default:
			}
			if early {
				break
			}
			time.Sleep(time.Millisecond)
		}
		m.release()
		if !early {
			select {
			case second = <-secondResult:
			case <-time.After(2 * time.Second):
				t.Fatal("second caller did not return")
			}
		}
		st := b.Status()
		t.Logf("first=%s second=%s reason=%s submissions=%d sent=%d remaining=%d", first.Class, second.Class, second.Reason, m.count(), st.Sent, st.Remaining)
		if m.count() > 1 || second.Class != gateway.IntentNotSent || second.Reason != "skipped_budget" {
			t.Fatal("two sequential intent calls consumed the last bundle slot twice")
		}
	})
}

func TestReviewSMSFixBundleAcceptanceEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, answer, wantClass string
		wantSent                int
	}{
		{"cmgs_control", "\r\n+CMGS: 42\r\n\r\nOK\r\n", gateway.IntentAccepted, 1},
		{"refusal_control", "\r\n+CMS ERROR: 38\r\n", gateway.IntentNotSent, 0},
		{"bare_ok", "\r\nOK\r\n", gateway.IntentUnknown, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &reviewBundleModem{afterBody: tc.answer}
			g, _, b := reviewBundleGateway(t, m)
			o := reviewBundleSend(g, "review first")
			st := b.Status()
			t.Logf("class=%s charged=%v accepted_by=%q submissions=%d bundle_sent=%d remaining=%d", o.Class, o.Charged, o.AcceptedBy, m.count(), st.Sent, st.Remaining)
			if o.Class != tc.wantClass {
				t.Fatalf("unexpected outcome %+v", o)
			}
			if st.Sent != tc.wantSent {
				t.Fatal("bundle counted carrier acceptance without +CMGS evidence")
			}
		})
	}
}

// B9FIX-R2 acceptance: only a +CMGS reference (0 and 255 too) counts as
// used, once; a refusal gives the share back; every answer without a
// reference, and no answer, keeps it as possible use: visible, and
// enforced against the next intent SMS.
func TestSMSBundle_EvidenceAndPossibleUse(t *testing.T) {
	for _, tc := range []struct {
		name, answer    string
		ctrlZFail       bool
		class           string
		sent, uncertain int
	}{
		{"reference_0", "\r\n+CMGS: 0\r\n\r\nOK\r\n", false, gateway.IntentAccepted, 1, 0},
		{"reference_255", "\r\n+CMGS: 255\r\n\r\nOK\r\n", false, gateway.IntentAccepted, 1, 0},
		{"refused", "\r\n+CMS ERROR: 500\r\n", false, gateway.IntentNotSent, 0, 0},
		{"bare_ok", "\r\nOK\r\n", false, gateway.IntentUnknown, 0, 1},
		{"unrelated_ok", "\r\n+CMTI: \"SM\",3\r\n\r\nOK\r\n", false, gateway.IntentUnknown, 0, 1},
		{"no_answer", "", false, gateway.IntentUnknown, 0, 1},
		{"ctrl_z_then_write_error", "", true, gateway.IntentUnknown, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &reviewBundleModem{afterBody: tc.answer, ctrlZFail: tc.ctrlZFail}
			g, _, b, _ := bundleLoop(t, m, 1, 200*time.Millisecond)
			o := reviewBundleSend(g, "review first")
			st := b.Status()
			t.Logf("class=%s sent=%d uncertain=%d remaining=%d", o.Class, st.Sent, st.Uncertain, st.Remaining)
			if o.Class != tc.class || st.Sent != tc.sent || st.Uncertain != tc.uncertain || st.Remaining != 1-tc.sent-tc.uncertain {
				t.Fatalf("%s: class %s sent %d uncertain %d remaining %d; want %s %d %d %d", tc.name, o.Class, st.Sent, st.Uncertain, st.Remaining,
					tc.class, tc.sent, tc.uncertain, 1-tc.sent-tc.uncertain)
			}
			next := reviewBundleSend(g, "review second")
			if st.Remaining == 0 && (next.Reason != "skipped_budget" || m.count() != 1) {
				t.Fatalf("the next SMS with nothing left: %+v after %d submissions; want skipped_budget, no second submission", next, m.count())
			}
			if st.Remaining == 1 && next.Class == gateway.IntentNotSent && next.Reason == "skipped_budget" {
				t.Fatalf("a refused SMS kept its share: %+v", next)
			}
		})
	}
}

// Possible use outlives a restart and gives way to the next top-up.
func TestSMSBundle_PossibleUseAcrossRestart(t *testing.T) {
	m := &reviewBundleModem{afterBody: "\r\nOK\r\n"}
	g, _, b, db := bundleLoop(t, m, 1, 200*time.Millisecond)
	if o := reviewBundleSend(g, "review first"); o.Class != gateway.IntentUnknown {
		t.Fatalf("bare OK: %+v", o)
	}
	again := gateway.NewSMSBudget(db, "review", gateway.SMSBudgetOptions{Size: 1}, nil)
	if st := again.Status(); st.Uncertain != 1 || st.Remaining != 0 || !st.Empty {
		t.Fatalf("after a restart: %+v; want uncertain 1, nothing left", st)
	}
	if err := again.CheckSMS(1); err == nil {
		t.Fatal("possible use was forgotten across a restart")
	} else {
		t.Log(err)
	}
	if err := again.Reset(1, 0); err != nil {
		t.Fatal(err)
	}
	if st := again.Status(); st.Uncertain != 0 || st.Remaining != 1 {
		t.Fatalf("after a top-up: %+v; want the operator's figures", st)
	}
	_ = b
}

// A command still queued when its caller gives up never starts, takes no
// share and leaves the bundle as it was.
func TestSMSBundle_AbandonedInQueueTakesNothing(t *testing.T) {
	m := &reviewBundleModem{afterBody: "\r\n+CMGS: 7\r\n\r\nOK\r\n"}
	g, tr, b, _ := bundleLoop(t, m, 1, 200*time.Millisecond)
	release, done := transport.HoldSMSLoopForTest(tr)
	o := reviewBundleSend(g, "review first") // gives up after the fixture's 1 s
	release()
	<-done
	time.Sleep(50 * time.Millisecond)
	st := b.Status()
	if o.Class != gateway.IntentNotSent || m.cmgs() != 0 || st.Sent != 0 || st.Uncertain != 0 || st.Remaining != 1 {
		t.Fatalf("abandoned in the queue: %+v, %d CMGS, bundle %+v; want not sent, nothing taken", o, m.cmgs(), st)
	}
	if o := reviewBundleSend(g, "review second"); o.Class != gateway.IntentAccepted {
		t.Fatalf("the share was not given back: %+v", o)
	}
}

// B9FIX-R3: before the counter is set the intent route waits and the modem
// sees no SMS; an explicitly unconfigured bundle (size 0) allows.
func TestSMSBundle_NotSetUpYetWaits(t *testing.T) {
	m := &reviewBundleModem{afterBody: "\r\n+CMGS: 9\r\n\r\nOK\r\n"}
	g, tr, _, db := bundleLoop(t, m, -1, 200*time.Millisecond)
	o := reviewBundleSend(g, "review first")
	if o.Class != gateway.IntentNotSent || o.Reason != "skipped_offline" || !o.Retryable || o.ConsumeRetry || m.cmgs() != 0 {
		t.Fatalf("no counter yet: %+v with %d CMGS; want a wait (skipped_offline, retryable) and no SMS", o, m.cmgs())
	}
	tr.SetSMSSpend(gateway.NewSMSBudget(db, "review", gateway.SMSBudgetOptions{}, nil))
	if o := reviewBundleSend(g, "review first"); o.Class != gateway.IntentAccepted || m.count() != 1 {
		t.Fatalf("no bundle configured: %+v; want accepted", o)
	}
}

// The other send paths are counted, never refused: SendSMS still answers
// nil to a bare OK, the bundle keeps it as possible use, and the sent hook
// and the sent counter move only on +CMGS.
func TestSMSBundle_LegacySendCounted(t *testing.T) {
	for _, tc := range []struct {
		name, answer    string
		sent, uncertain int
		hook            int
	}{
		{"cmgs", "\r\n+CMGS: 3\r\n\r\nOK\r\n", 1, 0, 1},
		{"bare_ok", "\r\nOK\r\n", 0, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &reviewBundleModem{afterBody: tc.answer}
			_, tr, b, _ := bundleLoop(t, m, 1, 200*time.Millisecond)
			hooked := 0
			tr.SetSentHook(func(string, string) { hooked++ })
			if err := tr.SendSMS(context.Background(), "+15550000000", "legacy"); err != nil {
				t.Fatalf("SendSMS: %v", err)
			}
			// Counted only: a spent bundle does not refuse it.
			if err := tr.SendSMS(context.Background(), "+15550000000", "legacy"); err != nil {
				t.Fatalf("SendSMS on a spent bundle: %v", err)
			}
			st := b.Status()
			if st.Sent != 2*tc.sent || st.Uncertain != 2*tc.uncertain || hooked != 2*tc.hook || m.count() != 2 {
				t.Fatalf("bundle %+v, hook %d, submissions %d; want sent %d uncertain %d hook %d, 2 submissions",
					st, hooked, m.count(), 2*tc.sent, 2*tc.uncertain, 2*tc.hook)
			}
		})
	}
}

// Two intent SMS queued while the bundle had one left both pass the
// snapshot check; the loop checks the second after the first settled and
// refuses it before any byte (B9FIX-R1). The first keeps its share until
// its own answer.
func TestSMSBundle_QueuedBehindIsCheckedOnTheLoop(t *testing.T) {
	m := &reviewBundleModem{afterBody: "\r\n+CMGS: 11\r\n\r\nOK\r\n"}
	g, tr, b, _ := bundleLoop(t, m, 1, 200*time.Millisecond)
	release, done := transport.HoldSMSLoopForTest(tr)
	results := make(chan gateway.IntentOutcome, 2)
	go func() { results <- reviewBundleSend(g, "review first") }()
	waitQueued(t, tr, 1)
	go func() { results <- reviewBundleSend(g, "review second") }()
	waitQueued(t, tr, 2)
	release()
	<-done
	a, c := <-results, <-results
	if a.Class == gateway.IntentNotSent {
		a, c = c, a
	}
	st := b.Status()
	if a.Class != gateway.IntentAccepted || c.Class != gateway.IntentNotSent || c.Reason != "skipped_budget" || c.Retryable ||
		m.count() != 1 || m.cmgs() != 1 || st.Sent != 1 || st.Remaining != 0 {
		t.Fatalf("first %+v, second %+v, %d submissions, %d CMGS, bundle %+v; want one accepted, one refused on the loop before AT+CMGS",
			a, c, m.count(), m.cmgs(), st)
	}
}

// A command that outlived its caller settles its own share: once its +CMGS
// comes, the bundle counts it as used, once.
func TestSMSBundle_RunningCommandSettlesItsShare(t *testing.T) {
	m := &reviewBundleModem{afterBody: "\r\n+CMGS: 12\r\n\r\nOK\r\n", bodyEntered: make(chan struct{}), bodyRelease: make(chan struct{})}
	g, _, b, _ := bundleLoop(t, m, 1, 200*time.Millisecond)
	first := make(chan gateway.IntentOutcome, 1)
	go func() { first <- reviewBundleSend(g, "review first") }()
	<-m.bodyEntered
	o := <-first // the fixture's caller gives up after 1 s
	if o.Class != gateway.IntentUnknown {
		t.Fatalf("caller returned %+v while the command ran; want unknown", o)
	}
	if st := b.Status(); st.Remaining != 0 {
		t.Fatalf("while running: %+v; want its share held", st)
	}
	m.release()
	deadline := time.Now().Add(2 * time.Second)
	for b.Status().Sent != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if st := b.Status(); st.Sent != 1 || st.Uncertain != 0 || st.Remaining != 0 || m.count() != 1 {
		t.Fatalf("after its +CMGS: %+v, %d submissions; want sent 1, nothing uncertain", st, m.count())
	}
}
