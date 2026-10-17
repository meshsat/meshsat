package gateway

// The reviewer's tests of 17 Oct 2026 (Appendix A of the R7 closure and
// recorded bundle expiry reassessment), as supplied: the expiry write in
// the top-up transaction, a failed R7 release, and the end's precision
// across a reload (B9EXP-R1). [MESHSAT-1584]

import (
	"errors"
	"testing"
	"time"

	"meshsat/internal/transport"
)

func TestReviewSMSBundleExpiryUpdateAtomic(t *testing.T) {
	for _, clear := range []bool{false, true} {
		name := "new_expiry"
		if clear {
			name = "topup_clears_expiry"
		}
		t.Run(name, func(t *testing.T) {
			db := newBudgetDB(t)
			b := NewSMSBudget(db, "review", SMSBudgetOptions{}, nil)
			now := time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC)
			b.now = func() time.Time { return now }
			oldEnd, newEnd := now.Add(24*time.Hour), now.Add(31*24*time.Hour)
			size, sent := 5, 2
			if err := b.Update(SMSBundleUpdate{Size: &size, Sent: &sent, ExpiresAt: &oldEnd}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TRIGGER review_fail_expiry BEFORE INSERT ON system_config
WHEN NEW.key = 'sms_bundle_expires_at'
BEGIN SELECT RAISE(ABORT, 'review expiry write rejected'); END`); err != nil {
				t.Fatal(err)
			}
			size, sent = 250, 0
			u := SMSBundleUpdate{Size: &size, Sent: &sent, ExpiresAt: &newEnd}
			if clear {
				u.ExpiresAt = nil
			}
			if err := b.Update(u); !errors.Is(err, ErrSMSBundleStorage) {
				t.Fatalf("failed expiry write returned %v", err)
			}
			for label, got := range map[string]*SMSBudget{"live": b, "reloaded": NewSMSBudget(db, "review", SMSBudgetOptions{}, nil)} {
				st := got.Status()
				if st.Size != 5 || st.Sent != 2 || st.Uncertain != 0 || st.ExpiresAt == nil || !st.ExpiresAt.Equal(oldEnd) {
					t.Fatalf("%s changed after rollback: %+v", label, st)
				}
			}
			if _, err := db.Exec("DROP TRIGGER review_fail_expiry"); err != nil {
				t.Fatal(err)
			}
			if err := b.Update(u); err != nil {
				t.Fatal(err)
			}
			for label, got := range map[string]*SMSBudget{"live": b, "reloaded": NewSMSBudget(db, "review", SMSBudgetOptions{}, nil)} {
				st := got.Status()
				if st.Size != 250 || st.Sent != 0 || st.Uncertain != 0 {
					t.Fatalf("%s counts after recovery: %+v", label, st)
				}
				if clear && st.ExpiresAt != nil || !clear && (st.ExpiresAt == nil || !st.ExpiresAt.Equal(newEnd)) {
					t.Fatalf("%s expiry after recovery: %+v", label, st)
				}
			}
			t.Log("expiry-write failure rolled back the counts and deadline; recovery persisted both")
		})
	}
}

func TestReviewSMSBundleR7ReleaseCommitFailure(t *testing.T) {
	for _, phase := range []transport.SMSPhase{transport.SMSRefused, transport.SMSNotSent} {
		name := "refused"
		if phase == transport.SMSNotSent {
			name = "not_sent"
		}
		t.Run(name, func(t *testing.T) {
			db := newBudgetDB(t)
			b := NewSMSBudget(db, "review", SMSBudgetOptions{}, nil)
			if err := b.Reset(1, 0); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TRIGGER review_fail_possible BEFORE INSERT ON system_config
WHEN NEW.key = 'sms_bundle_uncertain' AND NEW.value = '1'
BEGIN SELECT RAISE(ABORT, 'review possible write rejected'); END`); err != nil {
				t.Fatal(err)
			}
			h, err := b.HoldSMS("+15550000000", "review", false)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.Possible(); err != nil {
				t.Fatalf("legacy path was refused: %v", err)
			}
			if _, err := db.Exec("DROP TRIGGER review_fail_possible"); err != nil {
				t.Fatal(err)
			}
			if err := b.Reset(1, 0); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TRIGGER review_fail_release BEFORE INSERT ON system_config
WHEN NEW.key = 'sms_bundle_uncertain' AND NEW.value = '0'
BEGIN SELECT RAISE(ABORT, 'review release write rejected'); END`); err != nil {
				t.Fatal(err)
			}
			h.Settle(phase)
			st := b.Status()
			reloaded := NewSMSBudget(db, "review", SMSBudgetOptions{}, nil).Status()
			if st.Sent != 0 || st.Uncertain != 1 || st.Remaining != 0 || st.Error == "" || reloaded.Sent != 0 || reloaded.Uncertain != 1 {
				t.Fatalf("failed release lost exposure: live=%+v reloaded=%+v", st, reloaded)
			}
			if err := b.CheckSMS(1); !errors.Is(err, transport.ErrSMSBundle) {
				t.Fatalf("failed release allowed another share: %v", err)
			}
			t.Logf("release failed: live_uncertain=%d reload_uncertain=%d error_visible=%v next_refused=true", st.Uncertain, reloaded.Uncertain, st.Error != "")
		})
	}
}

func TestReviewSMSBundleExpiryPrecisionSurvivesReload(t *testing.T) {
	db := newBudgetDB(t)
	b := NewSMSBudget(db, "review", SMSBudgetOptions{}, nil)
	end := time.Date(2026, 11, 17, 1, 33, 59, 900000000, time.UTC)
	now := end.Add(-time.Hour)
	b.now = func() time.Time { return now }
	size := 250
	if err := b.Update(SMSBundleUpdate{Size: &size, ExpiresAt: &end}); err != nil {
		t.Fatal(err)
	}
	again := NewSMSBudget(db, "review", SMSBudgetOptions{}, nil)
	again.now = b.now
	now = end.Add(-400 * time.Millisecond)
	live, reloaded := b.Status(), again.Status()
	liveErr, reloadErr := b.CheckSMS(1), again.CheckSMS(1)
	t.Logf("live_end=%s reload_end=%s now=%s live_expired=%v reload_expired=%v live_allowed=%v reload_allowed=%v", live.ExpiresAt.Format(time.RFC3339Nano), reloaded.ExpiresAt.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), live.Expired, reloaded.Expired, liveErr == nil, reloadErr == nil)
	if !live.ExpiresAt.Equal(*reloaded.ExpiresAt) || live.Expired != reloaded.Expired || (liveErr == nil) != (reloadErr == nil) {
		t.Fatal("reloading changed the expiry timestamp and eligibility at the same instant")
	}
}
