package transport_test

// The reviewer's tests of 17 Oct 2026 (Appendix A of the B9 bundle
// durability reassessment), as supplied: B9FIX-R7 and the read-recovery
// branch. [MESHSAT-1584]

import (
	"context"
	"testing"
	"time"

	"meshsat/internal/gateway"
)

func TestReviewSMSBundleLegacyRefusalAfterTopUp(t *testing.T) {
	for _, tc := range []struct {
		name                string
		failPossible, topup bool
	}{
		{"healthy_topup_control", false, true},
		{"failed_write_without_topup_control", true, false},
		{"failed_write_then_topup", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &reviewBundleModem{afterBody: "\r\n+CMS ERROR: 38\r\n", bodyEntered: make(chan struct{}), bodyRelease: make(chan struct{})}
			g, tr, b, db := bundleLoop(t, m, 1, 200*time.Millisecond)
			if err := b.Reset(1, 0); err != nil {
				t.Fatal(err)
			}
			if tc.failPossible {
				if _, err := db.Exec(`CREATE TRIGGER review_fail_possible BEFORE INSERT ON system_config
                WHEN NEW.key = 'sms_bundle_uncertain' AND CAST(NEW.value AS INTEGER) > 0
                BEGIN SELECT RAISE(ABORT, 'review: possible-use write rejected'); END`); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() { done <- tr.SendSMS(context.Background(), "+15550000000", "review first") }()
			select {
			case <-m.bodyEntered:
			case <-time.After(2 * time.Second):
				t.Fatal("legacy SMS did not reach its body write")
			}
			if st := b.Status(); st.Uncertain != 1 || m.count() != 0 {
				t.Fatalf("expected one running share before submission, status=%+v submissions=%d", st, m.count())
			}
			if tc.failPossible {
				if _, err := db.Exec("DROP TRIGGER review_fail_possible"); err != nil {
					t.Fatal(err)
				}
			}
			if tc.topup {
				if err := b.Reset(1, 0); err != nil {
					t.Fatal(err)
				}
			}
			persistedWhileRunning, err := db.GetSystemConfig("sms_bundle_uncertain")
			if err != nil {
				t.Fatal(err)
			}
			m.release()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("explicit modem rejection was not returned")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("legacy SMS did not finish")
			}
			live := *b.Status()
			again := gateway.NewSMSBudget(db, "review", gateway.SMSBudgetOptions{}, nil)
			reloaded := *again.Status()
			tr.SetSMSSpend(again)
			m.mu.Lock()
			m.afterBody = "\r\n+CMGS: 61\r\n\r\nOK\r\n"
			m.mu.Unlock()
			next := reviewBundleSend(g, "review second")
			t.Logf("initial_write_failed=%v topup=%v persisted_while_running=%s live_uncertain=%d live_remaining=%d reload_uncertain=%d reload_remaining=%d next=%s reason=%s",
				tc.failPossible, tc.topup, persistedWhileRunning, live.Uncertain, live.Remaining, reloaded.Uncertain, reloaded.Remaining, next.Class, next.Reason)
			if live.Uncertain != 0 || reloaded.Uncertain != 0 || next.Class != gateway.IntentAccepted {
				t.Fatal("a refused legacy SMS became phantom persisted usage after its active share was stored by a top-up")
			}
		})
	}
}

func TestReviewSMSBundleReadRecoveryWriteFailure(t *testing.T) {
	for _, failFold := range []bool{false, true} {
		name := "successful_fold_control"
		if failFold {
			name = "fold_write_rejected"
		}
		t.Run(name, func(t *testing.T) {
			m := &reviewBundleModem{afterBody: "\r\n+CMGS: 62\r\n\r\nOK\r\n"}
			g, tr, b, db := bundleLoop(t, m, 3, 200*time.Millisecond)
			if err := b.Reset(3, 0); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("ALTER TABLE system_config RENAME TO review_saved_config"); err != nil {
				t.Fatal(err)
			}
			down := gateway.NewSMSBudget(db, "review", gateway.SMSBudgetOptions{}, nil)
			tr.SetSMSSpend(down)
			if err := tr.SendSMS(context.Background(), "+15550000000", "legacy"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("ALTER TABLE review_saved_config RENAME TO system_config"); err != nil {
				t.Fatal(err)
			}
			if failFold {
				if _, err := db.Exec(`CREATE TRIGGER review_fail_fold BEFORE INSERT ON system_config
                WHEN NEW.key = 'sms_bundle_uncertain' AND CAST(NEW.value AS INTEGER) > 0
                BEGIN SELECT RAISE(ABORT, 'review: recovery write rejected'); END`); err != nil {
					t.Fatal(err)
				}
			}
			recovered := *down.Status()
			if recovered.Uncertain != 1 || recovered.Remaining != 2 || (recovered.Error != "") != failFold {
				t.Fatalf("lost legacy exposure after read recovery: %+v", recovered)
			}
			if failFold {
				blocked := reviewBundleSend(g, "review blocked")
				if blocked.Class != gateway.IntentNotSent || blocked.Reason != "skipped_offline" || m.count() != 1 {
					t.Fatalf("intent send while accounting still unwritable: %+v submissions=%d", blocked, m.count())
				}
				if _, err := db.Exec("DROP TRIGGER review_fail_fold"); err != nil {
					t.Fatal(err)
				}
			}
			next := reviewBundleSend(g, "review second")
			again := gateway.NewSMSBudget(db, "review", gateway.SMSBudgetOptions{}, nil)
			st := again.Status()
			t.Logf("fold_failed=%v retained_uncertain=%d retained_error=%v next=%s reload_sent=%d reload_uncertain=%d remaining=%d submissions=%d", failFold, recovered.Uncertain, recovered.Error != "", next.Class, st.Sent, st.Uncertain, st.Remaining, m.count())
			if next.Class != gateway.IntentAccepted || st.Sent != 1 || st.Uncertain != 1 || st.Remaining != 1 || m.count() != 2 {
				t.Fatal("the next successful intent reservation did not persist the earlier legacy exposure")
			}
		})
	}
}
