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
