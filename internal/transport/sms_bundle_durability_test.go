package transport_test

// The prepaid SMS bundle when its storage fails, the process dies or the
// operator tops up during a send (B9FIX-R4, R5, R6). The first three tests
// are the reviewer's of 17 Oct 2026 (Appendix A of the B9 bundle
// reassessment), as supplied; the rest cover the acceptance criteria past
// them. [MESHSAT-1584]

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/gateway"
	"meshsat/internal/transport"
)

func TestReviewSMSBundlePersistenceFailure(t *testing.T) {
	for _, failWrite := range []bool{false, true} {
		name := "durable_control"
		if failWrite {
			name = "possible_write_rejected"
		}
		t.Run(name, func(t *testing.T) {
			m := &reviewBundleModem{afterBody: "\r\nOK\r\n"}
			g, tr, b, db := bundleLoop(t, m, 1, 200*time.Millisecond)
			if err := b.Reset(1, 0); err != nil {
				t.Fatal(err)
			}
			if failWrite {
				_, err := db.Exec(`CREATE TRIGGER review_fail_possible BEFORE INSERT ON system_config
                WHEN NEW.key = 'sms_bundle_uncertain' AND CAST(NEW.value AS INTEGER) > 0
                BEGIN SELECT RAISE(ABORT, 'review: possible-use write rejected'); END`)
				if err != nil {
					t.Fatal(err)
				}
			}
			first := reviewBundleSend(g, "review first")
			firstWrites := m.count()
			before := *b.Status()
			if failWrite {
				if _, err := db.Exec("DROP TRIGGER review_fail_possible"); err != nil {
					t.Fatal(err)
				}
			}
			again := gateway.NewSMSBudget(db, "review", gateway.SMSBudgetOptions{}, nil)
			reloaded := *again.Status()
			tr.SetSMSSpend(again)
			m.mu.Lock()
			m.afterBody = "\r\n+CMGS: 42\r\n\r\nOK\r\n"
			m.mu.Unlock()
			second := reviewBundleSend(g, "review second")
			t.Logf("write_fault=%v first=%s first_submissions=%d memory_uncertain=%d reload_uncertain=%d reload_remaining=%d second=%s second_reason=%s total_submissions=%d",
				failWrite, first.Class, firstWrites, before.Uncertain, reloaded.Uncertain, reloaded.Remaining, second.Class, second.Reason, m.count())
			if failWrite && (firstWrites != 0 || first.Class != gateway.IntentNotSent) {
				t.Error("an intent SMS was submitted after its durable possible-use write failed")
			}
			if m.count() != 1 {
				t.Error("one bundle slot authorized two submissions across a counter reload")
			}
			if !failWrite && (first.Class != gateway.IntentUnknown || second.Reason != "skipped_budget") {
				t.Fatal("durable control did not preserve uncertainty")
			}
		})
	}
}

func TestReviewSMSBundleReadFailure(t *testing.T) {
	for _, failRead := range []bool{false, true} {
		name := "spent_bundle_control"
		if failRead {
			name = "config_read_unavailable"
		}
		t.Run(name, func(t *testing.T) {
			m := &reviewBundleModem{afterBody: "\r\n+CMGS: 43\r\n\r\nOK\r\n"}
			g, tr, b, db := bundleLoop(t, m, 1, 200*time.Millisecond)
			if err := b.Reset(1, 1); err != nil {
				t.Fatal(err)
			}
			if failRead {
				if _, err := db.Exec("ALTER TABLE system_config RENAME TO review_saved_config"); err != nil {
					t.Fatal(err)
				}
			}
			again := gateway.NewSMSBudget(db, "review", gateway.SMSBudgetOptions{}, nil)
			if failRead {
				if _, err := db.Exec("ALTER TABLE review_saved_config RENAME TO system_config"); err != nil {
					t.Fatal(err)
				}
			}
			tr.SetSMSSpend(again)
			o := reviewBundleSend(g, "review first")
			size, err := db.GetSystemConfig("sms_bundle_size")
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("read_fault=%v stored_size=%s status_absent=%v outcome=%s reason=%s submissions=%d", failRead, size, again.Status() == nil, o.Class, o.Reason, m.count())
			if o.Class != gateway.IntentNotSent || m.count() != 0 {
				t.Fatal("a config read error converted a configured spent bundle into an unrestricted route")
			}
		})
	}
}

func TestReviewSMSBundleTopUpWhileRunning(t *testing.T) {
	for _, tc := range []struct {
		name, answer, firstClass string
		wantSent, wantUncertain  int
		wantSecond               string
		wantWrites               int
	}{
		{"accepted_control", "\r\n+CMGS: 44\r\n\r\nOK\r\n", gateway.IntentAccepted, 1, 0, gateway.IntentNotSent, 1},
		{"refused_control", "\r\n+CMS ERROR: 38\r\n", gateway.IntentNotSent, 0, 0, gateway.IntentAccepted, 2},
		{"unknown_after_topup", "\r\nOK\r\n", gateway.IntentUnknown, 0, 1, gateway.IntentNotSent, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &reviewBundleModem{afterBody: tc.answer, bodyEntered: make(chan struct{}), bodyRelease: make(chan struct{})}
			g, _, b, _ := bundleLoop(t, m, 1, 200*time.Millisecond)
			firstResult := make(chan gateway.IntentOutcome, 1)
			go func() { firstResult <- reviewBundleSend(g, "review first") }()
			select {
			case <-m.bodyEntered:
			case <-time.After(2 * time.Second):
				t.Fatal("first body write did not start")
			}
			before := *b.Status()
			if before.Uncertain != 1 || m.count() != 0 {
				t.Fatalf("expected one active possible-use share, no Ctrl-Z yet: %+v writes=%d", before, m.count())
			}
			if err := b.Reset(1, 0); err != nil {
				t.Fatal(err)
			}
			atReset := *b.Status()
			m.release()
			var first gateway.IntentOutcome
			select {
			case first = <-firstResult:
			case <-time.After(2 * time.Second):
				t.Fatal("first send did not finish")
			}
			settled := *b.Status()
			m.mu.Lock()
			m.afterBody = "\r\n+CMGS: 45\r\n\r\nOK\r\n"
			m.mu.Unlock()
			second := reviewBundleSend(g, "review second")
			t.Logf("pre_topup_uncertain=%d pre_topup_submissions=0 reset_remaining=%d first=%s settled_sent=%d settled_uncertain=%d settled_remaining=%d second=%s second_reason=%s submissions=%d",
				before.Uncertain, atReset.Remaining, first.Class, settled.Sent, settled.Uncertain, settled.Remaining, second.Class, second.Reason, m.count())
			if first.Class != tc.firstClass {
				t.Fatalf("unexpected first outcome: %+v", first)
			}
			if settled.Sent != tc.wantSent || settled.Uncertain != tc.wantUncertain || second.Class != tc.wantSecond || m.count() != tc.wantWrites {
				t.Fatal("top-up forgot an active submission that reached the modem only after the reset")
			}
		})
	}
}

// B9FIX-R6 past the reviewer's three answers: no answer and a Ctrl-Z
// write that failed after it reached the modem stay possible use under the
// new figures too; a top-up with no command running still clears a
// historical unknown.
func TestSMSBundle_TopUpKeepsTheRunningShare(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		ctrlZFail    bool
	}{{"no_answer", "", false}, {"ctrl_z_then_write_error", "", true}} {
		t.Run(tc.name, func(t *testing.T) {
			m := &reviewBundleModem{afterBody: tc.answer, ctrlZFail: tc.ctrlZFail, bodyEntered: make(chan struct{}), bodyRelease: make(chan struct{})}
			g, _, b, _ := bundleLoop(t, m, 1, 200*time.Millisecond)
			first := make(chan gateway.IntentOutcome, 1)
			go func() { first <- reviewBundleSend(g, "review first") }()
			<-m.bodyEntered
			if err := b.Reset(1, 0); err != nil {
				t.Fatal(err)
			}
			if st := b.Status(); st.Uncertain != 1 || st.Remaining != 0 {
				t.Fatalf("top-up while the command runs: %+v; want its share kept", st)
			}
			m.release()
			if o := <-first; o.Class != gateway.IntentUnknown {
				t.Fatalf("first: %+v; want unknown", o)
			}
			if st := b.Status(); st.Uncertain != 1 || st.Remaining != 0 {
				t.Fatalf("after its unknown end: %+v; want uncertain 1", st)
			}
			if o := reviewBundleSend(g, "review second"); o.Reason != "skipped_budget" || m.count() != 1 {
				t.Fatalf("second: %+v after %d submissions; want skipped_budget, one submission", o, m.count())
			}
			// The command is done: its unknown is history now, and the
			// next top-up reconciles it.
			if err := b.Reset(1, 0); err != nil {
				t.Fatal(err)
			}
			if st := b.Status(); st.Uncertain != 0 || st.Remaining != 1 {
				t.Fatalf("top-up with nothing running: %+v; want the operator's figures", st)
			}
		})
	}
}

// B9FIX-R5: a settlement whose commit fails keeps the share as possible
// use, in memory and on disk; nothing is half written, whichever of its
// two writes is refused.
func TestSMSBundle_SettlementCommitFailureKeepsTheShare(t *testing.T) {
	for _, tc := range []struct{ name, trigger string }{
		{"sent_write_refused", `NEW.key = 'sms_bundle_sent' AND CAST(NEW.value AS INTEGER) > 0`},
		{"uncertain_clear_refused", `NEW.key = 'sms_bundle_uncertain' AND NEW.value = '0'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &reviewBundleModem{afterBody: "\r\n+CMGS: 50\r\n\r\nOK\r\n"}
			g, _, b, db := bundleLoop(t, m, 1, 200*time.Millisecond)
			if err := b.Reset(1, 0); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TRIGGER fail_settle BEFORE INSERT ON system_config WHEN ` + tc.trigger +
				` BEGIN SELECT RAISE(ABORT, 'settlement write rejected'); END`); err != nil {
				t.Fatal(err)
			}
			if o := reviewBundleSend(g, "review first"); o.Class != gateway.IntentAccepted {
				t.Fatalf("first: %+v", o)
			}
			st := b.Status()
			if st.Sent != 0 || st.Uncertain != 1 || st.Remaining != 0 || st.Error == "" {
				t.Fatalf("after a failed settlement: %+v; want the share kept as possible use and the error shown", st)
			}
			if state, _ := b.HealthStatus(); state != gateway.HealthStateFailed {
				t.Fatalf("health %s; want failed (nothing left)", state)
			}
			again := gateway.NewSMSBudget(db, "review", gateway.SMSBudgetOptions{}, nil)
			if st := again.Status(); st.Sent != 0 || st.Uncertain != 1 {
				t.Fatalf("on disk: %+v; want sent 0, uncertain 1 (the earlier state, whole)", st)
			}
			if o := reviewBundleSend(g, "review second"); o.Reason != "skipped_budget" || m.count() != 1 {
				t.Fatalf("second: %+v; want skipped_budget", o)
			}
		})
	}
}

// B9FIX-R4: a failed top-up is reported and changes nothing.
func TestSMSBundle_FailedTopUpChangesNothing(t *testing.T) {
	_, _, b, db := bundleLoop(t, &reviewBundleModem{}, 5, 200*time.Millisecond)
	if err := b.Reset(5, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_reset BEFORE INSERT ON system_config
		WHEN NEW.key = 'sms_bundle_reset_at' BEGIN SELECT RAISE(ABORT, 'reset write rejected'); END`); err != nil {
		t.Fatal(err)
	}
	if err := b.Reset(250, 0); err == nil {
		t.Fatal("a top-up that could not be stored reported success")
	}
	if st := b.Status(); st.Size != 5 || st.Sent != 4 {
		t.Fatalf("after a failed top-up: %+v; want the old figures", st)
	}
	again := gateway.NewSMSBudget(db, "review", gateway.SMSBudgetOptions{}, nil)
	if st := again.Status(); st.Size != 5 || st.Sent != 4 {
		t.Fatalf("on disk after a failed top-up: %+v; want the old figures, whole", st)
	}
}

// B9FIX-R4: while the bundle cannot be read an intent SMS waits and the
// health says so; counted-only SMS still go, and once the read succeeds
// they count as possible use. A missing bundle is not a failed read.
func TestSMSBundle_ReadOutage(t *testing.T) {
	m := &reviewBundleModem{afterBody: "\r\n+CMGS: 51\r\n\r\nOK\r\n"}
	g, tr, b, db := bundleLoop(t, m, 3, 200*time.Millisecond)
	if err := b.Reset(3, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ALTER TABLE system_config RENAME TO saved_config"); err != nil {
		t.Fatal(err)
	}
	down := gateway.NewSMSBudget(db, "review", gateway.SMSBudgetOptions{}, nil)
	tr.SetSMSSpend(down)
	if o := reviewBundleSend(g, "review first"); o.Class != gateway.IntentNotSent || o.Reason != "skipped_offline" || !o.Retryable || m.cmgs() != 0 {
		t.Fatalf("intent SMS while unreadable: %+v, %d CMGS; want a wait and no SMS", o, m.cmgs())
	}
	if st := down.Status(); st == nil || st.Error == "" || !st.Empty {
		t.Fatalf("status while unreadable: %+v; want the error", st)
	}
	if state, _ := down.HealthStatus(); state != gateway.HealthStateFailed {
		t.Fatalf("health while unreadable: %s; want failed", state)
	}
	if err := tr.SendSMS(context.Background(), "+15550000000", "legacy"); err != nil {
		t.Fatalf("a counted-only SMS was refused: %v", err)
	}
	if _, err := db.Exec("ALTER TABLE saved_config RENAME TO system_config"); err != nil {
		t.Fatal(err)
	}
	st := down.Status()
	if st == nil || st.Error != "" || st.Size != 3 || st.Uncertain != 1 || st.Remaining != 2 {
		t.Fatalf("after the read recovered: %+v; want size 3, the legacy SMS as possible use", st)
	}
	if o := reviewBundleSend(g, "review second"); o.Class != gateway.IntentAccepted {
		t.Fatalf("intent SMS after recovery: %+v", o)
	}

	none := gateway.NewSMSBudget(bundleDB(t), "review", gateway.SMSBudgetOptions{}, nil)
	if none.Status() != nil || none.CheckSMS(1) != nil {
		t.Fatal("an empty database read as a failed read")
	}
}

// B9FIX-R5: the process dies at each point of a send. Whatever the point,
// the reloaded bundle never offers the slot again: before AT+CMGS and at
// the Ctrl-Z the share is stored possible use, after the settlement it is
// stored as sent. The settlement is one transaction, so no state between
// its two counts can be committed (see the commit-failure test).
func TestSMSBundle_ProcessExitAtEachPoint(t *testing.T) {
	if mode := os.Getenv("SMS_BUNDLE_CHILD_MODE"); mode != "" {
		db, err := database.New(os.Getenv("SMS_BUNDLE_CHILD_DB"))
		if err != nil {
			t.Fatal(err)
		}
		b := gateway.NewSMSBudget(db, "crash", gateway.SMSBudgetOptions{}, nil)
		if err := b.Reset(1, 0); err != nil {
			t.Fatal(err)
		}
		m := &reviewBundleModem{afterBody: "\r\n+CMGS: 52\r\n\r\nOK\r\n"}
		if mode != "normal" {
			m.exitAt = mode
		}
		tr, _ := transport.StartSMSLoopForTest(m, 200*time.Millisecond)
		tr.SetSMSSpend(b)
		o := gateway.NewCellularGateway(gateway.CellularConfig{}, tr, db).IntentSend(context.Background(),
			gateway.IntentSend{Destination: "+15550000000", Text: "crash first", CorrValue: "c1"})
		if o.Class != gateway.IntentAccepted {
			t.Fatalf("child: %+v", o)
		}
		db.Close()
		os.Exit(0)
	}
	for _, tc := range []struct {
		mode            string
		sent, uncertain int
	}{{"cmgs", 0, 1}, {"ctrlz", 0, 1}, {"normal", 1, 0}} {
		t.Run(tc.mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bundle.db")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSMSBundle_ProcessExitAtEachPoint$")
			cmd.Env = append(os.Environ(), "SMS_BUNDLE_CHILD_MODE="+tc.mode, "SMS_BUNDLE_CHILD_DB="+path)
			out, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			if tc.mode == "normal" && err != nil || tc.mode != "normal" && (!errors.As(err, &exitErr) || exitErr.ExitCode() != 79) {
				t.Fatalf("child %s: %v\n%s", tc.mode, err, out)
			}
			if tc.mode == "normal" && !strings.Contains(string(out), "got +CMGS") {
				t.Fatal("the child never reached the network's acceptance")
			}
			db, err := database.New(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			b := gateway.NewSMSBudget(db, "crash", gateway.SMSBudgetOptions{}, nil)
			st := b.Status()
			if st.Sent != tc.sent || st.Uncertain != tc.uncertain || st.Remaining != 0 {
				t.Fatalf("after an exit at %s: %+v; want sent %d uncertain %d, nothing left", tc.mode, st, tc.sent, tc.uncertain)
			}
			m := &reviewBundleModem{afterBody: "\r\n+CMGS: 53\r\n\r\nOK\r\n"}
			tr, stop := transport.StartSMSLoopForTest(m, 200*time.Millisecond)
			t.Cleanup(func() { stop() })
			tr.SetSMSSpend(b)
			o := gateway.NewCellularGateway(gateway.CellularConfig{}, tr, db).IntentSend(context.Background(),
				gateway.IntentSend{Destination: "+15550000000", Text: "crash second", CorrValue: "c2"})
			if o.Reason != "skipped_budget" || m.cmgs() != 0 {
				t.Fatalf("after an exit at %s the slot was offered again: %+v", tc.mode, o)
			}
		})
	}
}

// B9FIX-R7 for a definite non-send: a counted-only SMS whose share was
// first counted in memory only, stored by a top-up while it ran, then not
// sent at all (the body write failed): released in memory and on disk.
func TestSMSBundle_NotSentAfterTopUpReleasesOnDisk(t *testing.T) {
	m := &reviewBundleModem{bodyFail: true, bodyEntered: make(chan struct{}), bodyRelease: make(chan struct{})}
	_, tr, b, db := bundleLoop(t, m, 1, 200*time.Millisecond)
	if err := b.Reset(1, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_possible BEFORE INSERT ON system_config
		WHEN NEW.key = 'sms_bundle_uncertain' AND CAST(NEW.value AS INTEGER) > 0
		BEGIN SELECT RAISE(ABORT, 'possible-use write rejected'); END`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- tr.SendSMS(context.Background(), "+15550000000", "review first") }()
	<-m.bodyEntered
	if _, err := db.Exec("DROP TRIGGER fail_possible"); err != nil {
		t.Fatal(err)
	}
	if err := b.Reset(1, 0); err != nil {
		t.Fatal(err)
	}
	if v, _ := db.GetSystemConfig("sms_bundle_uncertain"); v != "1" {
		t.Fatalf("the top-up stored uncertain %q; want the running share, 1", v)
	}
	m.release()
	if err := <-done; err == nil {
		t.Fatal("a failed body write was not returned")
	}
	if st := b.Status(); st.Uncertain != 0 || st.Remaining != 1 {
		t.Fatalf("live after the non-send: %+v; want the share back", st)
	}
	if st := gateway.NewSMSBudget(db, "review", gateway.SMSBudgetOptions{}, nil).Status(); st.Uncertain != 0 || st.Remaining != 1 {
		t.Fatalf("reloaded after the non-send: %+v; want the share back on disk too", st)
	}
}

// D8 validity on the loop: an intent SMS that passed the check before the
// bundle's end but waited in the queue past it is refused when its turn
// comes, before any byte. [MESHSAT-1584]
func TestSMSBundle_EndPassesWhileQueued(t *testing.T) {
	m := &reviewBundleModem{afterBody: "\r\n+CMGS: 70\r\n\r\nOK\r\n"}
	g, tr, b, _ := bundleLoop(t, m, 5, 200*time.Millisecond)
	end := time.Now().Add(400 * time.Millisecond)
	size := 5
	if err := b.Update(gateway.SMSBundleUpdate{Size: &size, ExpiresAt: &end}); err != nil {
		t.Fatal(err)
	}
	release, done := transport.HoldSMSLoopForTest(tr)
	res := make(chan gateway.IntentOutcome, 1)
	go func() { res <- reviewBundleSend(g, "review first") }()
	for transport.SMSQueueLenForTest(tr) < 1 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(time.Until(end) + 100*time.Millisecond)
	release()
	<-done
	o := <-res
	st := b.Status()
	if o.Class != gateway.IntentNotSent || o.Reason != "skipped_budget" || m.cmgs() != 0 || !st.Expired || st.Sent != 0 {
		t.Fatalf("queued past the end: %+v, %d CMGS, bundle %+v; want refused on the loop, nothing sent", o, m.cmgs(), st)
	}
}
