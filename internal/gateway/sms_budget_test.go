package gateway

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// [MESHSAT-1161] prepaid SMS bundle counter.

func newBudgetDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.New(filepath.Join(t.TempDir(), "budget.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

type fakeSender struct {
	mu    sync.Mutex
	sent  []string // "to|text"
	fail  bool
	calls int
}

func (f *fakeSender) send(_ context.Context, to, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail {
		return errors.New("modem busy")
	}
	f.sent = append(f.sent, to+"|"+text)
	return nil
}

func TestSMSSegments(t *testing.T) {
	for _, tc := range []struct{ n, want int }{{0, 1}, {1, 1}, {160, 1}, {161, 2}, {306, 2}, {307, 3}, {320, 3}} {
		if got := SMSSegments(strings.Repeat("a", tc.n)); got != tc.want {
			t.Errorf("%d chars: segments %d, want %d", tc.n, got, tc.want)
		}
	}
}

func TestSMSBudget_CountsPersistsAndReloads(t *testing.T) {
	db := newBudgetDB(t)
	fs := &fakeSender{}
	b := NewSMSBudget(db, "kitA", SMSBudgetOptions{Size: 250, WarnAt: 50}, fs.send)
	b.Record("+311", "hello")
	b.Record("+312", strings.Repeat("x", 200)) // two segments
	st := b.Status()
	if st == nil || st.Sent != 3 || st.Remaining != 247 || st.Low {
		t.Fatalf("status after 2 sends: %+v", st)
	}
	if state, _ := b.HealthStatus(); state != HealthStateOK {
		t.Fatalf("health %s, want ok", state)
	}
	// Until the API sets a size, the env default applies at every start;
	// the count itself is persisted.
	b2 := NewSMSBudget(db, "kitA", SMSBudgetOptions{Size: 10, WarnAt: 1}, fs.send)
	if st2 := b2.Status(); st2.Size != 10 || st2.Sent != 3 || st2.WarnAt != 1 {
		t.Fatalf("reloaded status with env defaults: %+v", st2)
	}
	// A size set through Reset is persisted and wins over the env default.
	if err := b2.Reset(250, 3); err != nil {
		t.Fatal(err)
	}
	warn := 50
	if err := b2.Configure(&warn, nil); err != nil {
		t.Fatal(err)
	}
	b3 := NewSMSBudget(db, "kitA", SMSBudgetOptions{Size: 10, WarnAt: 1}, fs.send)
	if st3 := b3.Status(); st3.Size != 250 || st3.Sent != 3 || st3.WarnAt != 50 || st3.ResetAt == nil {
		t.Fatalf("reloaded status after reset: %+v", st3)
	}
	if fs.calls != 0 {
		t.Fatalf("no reminder expected, got %d sends", fs.calls)
	}
}

func TestSMSBudget_NoBundleCountsNothing(t *testing.T) {
	b := NewSMSBudget(newBudgetDB(t), "kitA", SMSBudgetOptions{Size: 0, WarnAt: 50}, nil)
	b.Record("+311", "hello")
	if b.Status() != nil {
		t.Fatal("status must be nil without a bundle")
	}
	if state, _ := b.HealthStatus(); state != HealthStateUnknown {
		t.Fatalf("health %s, want unknown", state)
	}
}

func TestSMSBudget_WarnsOnceAndRemindsOnce(t *testing.T) {
	db := newBudgetDB(t)
	fs := &fakeSender{}
	var events []string
	var evMu sync.Mutex
	b := NewSMSBudget(db, "tesseract", SMSBudgetOptions{Size: 100, WarnAt: 50, AlertNumber: "+31600000000"}, fs.send)
	b.SetEventEmitter(func(typ, msg string) { evMu.Lock(); events = append(events, typ); evMu.Unlock() })
	if err := b.Reset(100, 49); err != nil {
		t.Fatal(err)
	}
	b.Record("+311", "one") // 50 left: threshold
	b.Wait()
	if state, detail := b.HealthStatus(); state != HealthStateDegraded || !strings.Contains(detail, "50 of 100 left") {
		t.Fatalf("health %s %q", state, detail)
	}
	if len(fs.sent) != 1 || !strings.HasPrefix(fs.sent[0], "+31600000000|MeshSat tesseract: 50 of 100 SMS left") {
		t.Fatalf("reminder: %v", fs.sent)
	}
	if txt := strings.SplitN(fs.sent[0], "|", 2)[1]; len(txt) > 160 {
		t.Fatalf("reminder is %d chars, over one segment", len(txt))
	}
	b.Record("+311", "two")
	b.Record("+311", "three")
	b.Wait()
	if fs.calls != 1 {
		t.Fatalf("reminder must go once per bundle, sent %d", fs.calls)
	}
	st := b.Status()
	if !st.Low || !st.Alerted || !st.AlertSet || st.Remaining != 48 {
		t.Fatalf("status: %+v", st)
	}
	evMu.Lock()
	joined := strings.Join(events, ",")
	evMu.Unlock()
	if strings.Count(joined, "sms_credit_low") != 1 || strings.Count(joined, "sms_credit_alert") != 1 {
		t.Fatalf("events: %s", joined)
	}
	// The alerted flag survives a restart.
	b2 := NewSMSBudget(db, "tesseract", SMSBudgetOptions{}, fs.send)
	if !b2.Status().Alerted {
		t.Fatal("alerted flag not persisted")
	}
	b2.Record("+311", "four")
	b2.Wait()
	if fs.calls != 1 {
		t.Fatalf("reminder resent after restart: %d", fs.calls)
	}
	// A top-up re-arms both.
	if err := b2.Reset(250, 0); err != nil {
		t.Fatal(err)
	}
	st = b2.Status()
	if st.Low || st.Alerted || st.Sent != 0 || st.Size != 250 || st.ResetAt == nil {
		t.Fatalf("status after top-up: %+v", st)
	}
	if state, _ := b2.HealthStatus(); state != HealthStateOK {
		t.Fatalf("health after top-up %s", state)
	}
}

func TestSMSBudget_ReminderRetriesAfterFailure(t *testing.T) {
	fs := &fakeSender{fail: true}
	b := NewSMSBudget(newBudgetDB(t), "parallax", SMSBudgetOptions{Size: 60, WarnAt: 50, AlertNumber: "+31600000000"}, fs.send)
	now := time.Date(2026, 9, 15, 22, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return now }
	b.Record("+311", "a") // 59 left, no warning yet
	b.Wait()
	if fs.calls != 0 {
		t.Fatalf("premature reminder")
	}
	for i := 0; i < 9; i++ {
		b.Record("+311", "a")
	}
	b.Wait() // 50 left: first attempt fails
	if fs.calls != 1 {
		t.Fatalf("first attempt: %d calls", fs.calls)
	}
	b.Record("+311", "a")
	b.Wait()
	if fs.calls != 1 {
		t.Fatalf("retry inside the gap: %d calls", fs.calls)
	}
	now = now.Add(smsAlertRetry + time.Second)
	fs.fail = false
	b.Record("+311", "a")
	b.Wait()
	if fs.calls != 2 || len(fs.sent) != 1 || !b.Status().Alerted {
		t.Fatalf("retry after the gap: calls %d sent %v alerted %v", fs.calls, fs.sent, b.Status().Alerted)
	}
}

func TestSMSBudget_SpentIsFailed(t *testing.T) {
	b := NewSMSBudget(newBudgetDB(t), "kitA", SMSBudgetOptions{Size: 2, WarnAt: 0}, nil)
	b.Record("+311", "a")
	b.Record("+311", "b")
	if state, detail := b.HealthStatus(); state != HealthStateFailed || !strings.Contains(detail, "spent") {
		t.Fatalf("health %s %q", state, detail)
	}
	if st := b.Status(); !st.Empty || !st.Low || st.Remaining != 0 {
		t.Fatalf("status %+v", st)
	}
}

func TestSMSBudget_ConfigureValidatesAndRearms(t *testing.T) {
	fs := &fakeSender{}
	b := NewSMSBudget(newBudgetDB(t), "kitA", SMSBudgetOptions{Size: 100, WarnAt: 50}, fs.send)
	bad := "0612345678"
	if err := b.Configure(nil, &bad); err == nil {
		t.Fatal("a number without + must be rejected")
	}
	neg := -1
	if err := b.Configure(&neg, nil); err == nil {
		t.Fatal("a negative warn_at must be rejected")
	}
	if err := b.Reset(100, 60); err != nil { // 40 left, low, no number yet
		t.Fatal(err)
	}
	b.Wait()
	if fs.calls != 0 {
		t.Fatal("no number, no reminder")
	}
	num := "+31600000000"
	if err := b.Configure(nil, &num); err != nil { // setting the number while low sends it
		t.Fatal(err)
	}
	b.Wait()
	if fs.calls != 1 || b.AlertNumber() != num {
		t.Fatalf("calls %d number %q", fs.calls, b.AlertNumber())
	}
	warn := 10
	if err := b.Configure(&warn, nil); err != nil {
		t.Fatal(err)
	}
	if state, _ := b.HealthStatus(); state != HealthStateOK {
		t.Fatalf("40 left with warn_at 10 must be ok, got %s", state)
	}
}

// The first PUT on a kit sets size, warn_at and the number together on a
// counter that has no bundle yet; nothing may fire from the in-between
// state (a "0 of 250 left" reminder went out on 15 Sep 2026).
func TestSMSBudget_ApplyOnUnconfiguredBundleDoesNotRemind(t *testing.T) {
	fs := &fakeSender{}
	var events []string
	b := NewSMSBudget(newBudgetDB(t), "kitA", SMSBudgetOptions{}, fs.send)
	b.SetEventEmitter(func(typ, _ string) { events = append(events, typ) })
	num := "+31600000000"
	if err := b.Configure(nil, &num); err != nil { // number before size: nothing to be low on
		t.Fatal(err)
	}
	b.Wait()
	size, sent, warn := 250, 0, 50
	if err := b.Apply(&size, &sent, &warn, &num); err != nil {
		t.Fatal(err)
	}
	b.Wait()
	if fs.calls != 0 {
		t.Fatalf("reminder fired on setup: %v", fs.sent)
	}
	for _, e := range events {
		if e == "sms_credit_low" || e == "sms_credit_alert" {
			t.Fatalf("warning fired on setup: %v", events)
		}
	}
	if state, _ := b.HealthStatus(); state != HealthStateOK {
		t.Fatalf("health %s", state)
	}
	if err := b.Apply(nil, &sent, nil, nil); err == nil {
		t.Fatal("sent without size must be rejected")
	}
}

func TestSMSBudgetOptionsFromEnv(t *testing.T) {
	t.Setenv("MESHSAT_SMS_BUNDLE_SIZE", "250")
	t.Setenv("MESHSAT_SMS_BUNDLE_WARN_AT", "")
	t.Setenv("MESHSAT_SMS_ALERT_NUMBER", " +31600000000 ")
	o := SMSBudgetOptionsFromEnv()
	if o.Size != 250 || o.WarnAt != 50 || o.AlertNumber != "+31600000000" {
		t.Fatalf("%+v", o)
	}
	t.Setenv("MESHSAT_SMS_BUNDLE_SIZE", "-5")
	if SMSBudgetOptionsFromEnv().Size != 0 {
		t.Fatal("negative size must fall back to 0")
	}
}

// The share of one SMS through its life (B9FIX-R1, R2): held while its
// command runs, possible use once its text goes (persisted), then used on
// +CMGS, given back on a refusal or a send that never went, kept on no
// reference. [MESHSAT-1584]
func TestSMSBudget_HoldLifecycle(t *testing.T) {
	db := newBudgetDB(t)
	b := NewSMSBudget(db, "kitA", SMSBudgetOptions{Size: 3, WarnAt: 0}, (&fakeSender{}).send)
	left := func() int { return b.Status().Remaining }

	h1, err := b.HoldSMS("+311", "one", true)
	if err != nil || left() != 2 {
		t.Fatalf("hold: %v, %d left; want 2", err, left())
	}
	h1.Settle(transport.SMSNotSent) // never reached the modem
	if left() != 3 {
		t.Fatalf("a send that never went kept its share: %d left", left())
	}

	h2, _ := b.HoldSMS("+311", "two", true)
	h2.Possible()
	if st := b.Status(); st.Uncertain != 1 || st.Remaining != 2 {
		t.Fatalf("possible use: %+v", st)
	}
	h2.Settle(transport.SMSRefused)
	if st := b.Status(); st.Uncertain != 0 || st.Sent != 0 || st.Remaining != 3 {
		t.Fatalf("a refusal kept its share: %+v", st)
	}

	h3, _ := b.HoldSMS("+311", "three", true)
	h3.Possible()
	h3.Settle(transport.SMSAccepted)
	h3.Settle(transport.SMSAccepted) // once
	if st := b.Status(); st.Sent != 1 || st.Uncertain != 0 || st.Remaining != 2 {
		t.Fatalf("accepted: %+v; want sent 1", st)
	}

	h4, _ := b.HoldSMS("+311", "four", true)
	h4.Possible()
	h4.Settle(transport.SMSHandedOver)
	h5, _ := b.HoldSMS("+311", "five", false) // counted only, never refused
	if st := b.Status(); st.Uncertain != 1 || st.Remaining != 0 {
		t.Fatalf("no reference plus a running send: %+v; want uncertain 1, nothing left", st)
	}
	if _, err := b.HoldSMS("+311", "six", true); !errors.Is(err, transport.ErrSMSBundle) {
		t.Fatalf("intent SMS with nothing left: %v; want ErrSMSBundle", err)
	}
	if b.CheckSMS(1) == nil {
		t.Fatal("the snapshot check ignores the running send")
	}
	if state, detail := b.HealthStatus(); state != HealthStateFailed {
		t.Fatalf("health %s (%s); want failed", state, detail)
	}

	// A top-up while a share is possible use: the operator's figures win,
	// and the late answer never drives a count below zero.
	h5.Possible()
	if err := b.Reset(10, 2); err != nil {
		t.Fatal(err)
	}
	h5.Settle(transport.SMSRefused)
	if st := b.Status(); st.Uncertain != 0 || st.Sent != 2 || st.Remaining != 8 {
		t.Fatalf("after a top-up: %+v; want sent 2, uncertain 0, 8 left", st)
	}
	if v, _ := db.GetSystemConfig("sms_bundle_uncertain"); v != "0" {
		t.Fatalf("stored uncertain %q; want 0", v)
	}
}

// No bundle set: nothing is counted or refused.
func TestSMSBudget_NoBundleHolds(t *testing.T) {
	b := NewSMSBudget(newBudgetDB(t), "kitA", SMSBudgetOptions{}, (&fakeSender{}).send)
	h, err := b.HoldSMS("+311", "x", true)
	if err != nil {
		t.Fatal(err)
	}
	h.Possible()
	h.Settle(transport.SMSAccepted)
	if b.Status() != nil {
		t.Fatal("a status without a bundle")
	}
	if err := b.CheckSMS(5); err != nil {
		t.Fatalf("no bundle refused: %v", err)
	}
}

// A counted-only SMS whose share cannot be stored still goes (the legacy
// paths are never refused) and is counted in memory; an intent SMS does
// not go (B9FIX-R4). The status shows the storage error.
func TestSMSBudget_StoreFailureByPath(t *testing.T) {
	db := newBudgetDB(t)
	b := NewSMSBudget(db, "kitA", SMSBudgetOptions{}, (&fakeSender{}).send)
	if err := b.Reset(3, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_unsure BEFORE INSERT ON system_config
		WHEN NEW.key = 'sms_bundle_uncertain' AND CAST(NEW.value AS INTEGER) > 0
		BEGIN SELECT RAISE(ABORT, 'rejected'); END`); err != nil {
		t.Fatal(err)
	}
	hi, err := b.HoldSMS("+311", "intent", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := hi.Possible(); !errors.Is(err, transport.ErrSMSBundleNotReady) {
		t.Fatalf("intent SMS with an unstored share: %v; want ErrSMSBundleNotReady (do not send)", err)
	}
	hi.Settle(transport.SMSNotSent)
	hl, _ := b.HoldSMS("+311", "legacy", false)
	if err := hl.Possible(); err != nil {
		t.Fatalf("counted-only SMS refused: %v", err)
	}
	if st := b.Status(); st.Uncertain != 1 || st.Remaining != 2 || st.Error == "" {
		t.Fatalf("counted in memory: %+v; want uncertain 1 and the error", st)
	}
	if state, _ := b.HealthStatus(); state != HealthStateDegraded {
		t.Fatalf("health %s; want degraded", state)
	}
	hl.Settle(transport.SMSRefused)
	if st := b.Status(); st.Uncertain != 0 || st.Remaining != 3 {
		t.Fatalf("refused: %+v; want the share back", st)
	}
}

// The bundle's recorded end (D8 validity): intent SMS are refused from the
// end on, before it they go; counts stay as they are; counted-only paths
// still go; the end survives a restart; a renewal keeps a running share;
// a top-up without an end leaves it unknown. [MESHSAT-1584]
func TestSMSBudget_Expiry(t *testing.T) {
	db := newBudgetDB(t)
	t0 := time.Date(2026, 10, 17, 1, 34, 0, 0, time.UTC)
	now := t0
	b := NewSMSBudget(db, "kitA", SMSBudgetOptions{}, (&fakeSender{}).send)
	b.now = func() time.Time { return now }
	end := t0.Add(31 * 24 * time.Hour)
	size, sent := 250, 1
	if err := b.Update(SMSBundleUpdate{Size: &size, Sent: &sent, ExpiresAt: &end}); err != nil {
		t.Fatal(err)
	}
	if st := b.Status(); st.ExpiresAt == nil || !st.ExpiresAt.Equal(end) || st.Expired || st.ExpiringSoon || st.Usable != 249 {
		t.Fatalf("a fresh bundle: %+v", st)
	}
	now = end.Add(-47 * time.Hour)
	if state, _ := b.HealthStatus(); state != HealthStateDegraded || !b.Status().ExpiringSoon {
		t.Fatalf("47 h before the end: health %s; want degraded, ending soon", state)
	}
	now = end.Add(-time.Nanosecond)
	if err := b.CheckSMS(1); err != nil {
		t.Fatalf("just before the end: %v", err)
	}
	h, err := b.HoldSMS("+311", "x", true)
	if err != nil {
		t.Fatalf("hold just before the end: %v", err)
	}
	h.Settle(transport.SMSNotSent)
	for _, at := range []time.Time{end, end.Add(time.Second)} {
		now = at
		if err := b.CheckSMS(1); !errors.Is(err, transport.ErrSMSBundle) {
			t.Fatalf("at %v: check %v; want ErrSMSBundle", at, err)
		}
		if _, err := b.HoldSMS("+311", "x", true); !errors.Is(err, transport.ErrSMSBundle) {
			t.Fatalf("at %v: intent hold %v; want ErrSMSBundle", at, err)
		}
		st := b.Status()
		if !st.Expired || st.Usable != 0 || st.Sent != 1 || st.Remaining != 249 {
			t.Fatalf("at %v: %+v; want ended, nothing usable, the counts as they were", at, st)
		}
		if state, _ := b.HealthStatus(); state != HealthStateFailed {
			t.Fatalf("at %v: health %s; want failed", at, state)
		}
	}
	hl, err := b.HoldSMS("+311", "legacy", false)
	if err != nil {
		t.Fatalf("a counted-only SMS was refused after the end: %v", err)
	}
	if err := hl.Possible(); err != nil {
		t.Fatal(err)
	}

	again := NewSMSBudget(db, "kitA", SMSBudgetOptions{}, (&fakeSender{}).send)
	again.now = b.now
	if st := again.Status(); st.ExpiresAt == nil || !st.ExpiresAt.Equal(end) || !st.Expired {
		t.Fatalf("after a restart: %+v; want the same end, ended", st)
	}

	// Renewal while the counted-only SMS runs: its share stays.
	next := now.Add(31 * 24 * time.Hour)
	zero := 0
	if err := b.Update(SMSBundleUpdate{Size: &size, Sent: &zero, ExpiresAt: &next}); err != nil {
		t.Fatal(err)
	}
	if st := b.Status(); st.Expired || st.Uncertain != 1 || st.Usable != 249 || !st.ExpiresAt.Equal(next) {
		t.Fatalf("renewed during a send: %+v; want the running share kept, the new end", st)
	}
	hl.Settle(transport.SMSAccepted)
	if st := b.Status(); st.Sent != 1 || st.Uncertain != 0 {
		t.Fatalf("the running SMS settled after the renewal: %+v", st)
	}

	// A top-up without an end: not known. An end alone corrects it; a
	// clear forgets it.
	if err := b.Reset(250, 0); err != nil {
		t.Fatal(err)
	}
	if st := b.Status(); st.ExpiresAt != nil || st.Expired {
		t.Fatalf("a top-up without an end: %+v; want it not known", st)
	}
	if err := b.Update(SMSBundleUpdate{ExpiresAt: &end}); err != nil {
		t.Fatal(err)
	}
	if !b.Status().Expired {
		t.Fatal("a corrected end in the past is not ended")
	}
	if err := b.Update(SMSBundleUpdate{ClearExpiry: true}); err != nil {
		t.Fatal(err)
	}
	if st := b.Status(); st.ExpiresAt != nil || st.Expired {
		t.Fatalf("cleared: %+v", st)
	}
	if err := b.Update(SMSBundleUpdate{ExpiresAt: &end, ClearExpiry: true}); err == nil {
		t.Fatal("an end and a clear together were taken")
	}

	// A stored end that is not a time is a failed read, not an unknown end.
	if err := db.SetSystemConfig("sms_bundle_expires_at", "next month"); err != nil {
		t.Fatal(err)
	}
	bad := NewSMSBudget(db, "kitA", SMSBudgetOptions{}, (&fakeSender{}).send)
	if err := bad.CheckSMS(1); !errors.Is(err, transport.ErrSMSBundleNotReady) {
		t.Fatalf("an unreadable end: %v; want ErrSMSBundleNotReady", err)
	}
}
