package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// SMSBudget counts the SMS segments the network accepted (+CMGS) against a
// prepaid bundle, so an empty prepaid card is announced before it happens
// instead of showing up as a lane that silently stopped delivering
// (parallax, 15 Sep 2026). The count hangs off the transport's sent hook,
// so every send path is counted: gateway relays, OOB frames, the API test
// send and the Reticulum SMS interface. State lives in system_config and
// survives restarts; a top-up is recorded with Reset. [MESHSAT-1161]
//
// Thresholds: at or below WarnAt segments left the target reports degraded,
// a warning is logged, an SSE event goes out and, when an alert number is
// configured, one reminder SMS goes to it (once per bundle, retried after
// smsAlertRetry when the send fails). At zero left the target reports failed.
type SMSBudget struct {
	mu           sync.Mutex
	db           *database.DB
	kit          string
	size         int
	sent         int
	warnAt       int
	alertNumber  string
	resetAt      time.Time
	alerted      bool // the reminder SMS was accepted by the network for this bundle
	alertTriedAt time.Time
	warned       bool // warning logged and emitted for this bundle (in memory)

	send func(ctx context.Context, to, text string) error
	emit EventEmitFunc
	now  func() time.Time
	wg   sync.WaitGroup
}

// system_config keys. Values are plain integers, RFC 3339 times or a phone
// number; nothing here is a secret. [MESHSAT-1161]
const (
	cfgSMSBundleSize    = "sms_bundle_size"
	cfgSMSBundleSent    = "sms_bundle_sent"
	cfgSMSBundleResetAt = "sms_bundle_reset_at"
	cfgSMSBundleWarnAt  = "sms_bundle_warn_at"
	cfgSMSAlertNumber   = "sms_alert_number"
	cfgSMSBundleAlerted = "sms_bundle_alerted"
)

// smsAlertRetry is how long a failed reminder send blocks the next attempt.
const smsAlertRetry = 10 * time.Minute

// smsAlertTimeout bounds one reminder send (the modem itself allows 2 min).
const smsAlertTimeout = 3 * time.Minute

// SMSBudgetOptions are the first-boot defaults, used only for a key that has
// no system_config row yet; the API (PUT /api/cellular/bundle) overrides
// them and the override persists.
type SMSBudgetOptions struct {
	Size        int    // bundle size in segments, 0 = no counter
	WarnAt      int    // segments left that trip the warning
	AlertNumber string // E.164 number for the top-up reminder, empty = none
}

// SMSBudgetOptionsFromEnv reads MESHSAT_SMS_BUNDLE_SIZE (default 0, off),
// MESHSAT_SMS_BUNDLE_WARN_AT (default 50) and MESHSAT_SMS_ALERT_NUMBER
// (default empty).
func SMSBudgetOptionsFromEnv() SMSBudgetOptions {
	o := SMSBudgetOptions{Size: 0, WarnAt: 50}
	if v, err := strconv.Atoi(os.Getenv("MESHSAT_SMS_BUNDLE_SIZE")); err == nil && v >= 0 {
		o.Size = v
	}
	if v, err := strconv.Atoi(os.Getenv("MESHSAT_SMS_BUNDLE_WARN_AT")); err == nil && v >= 0 {
		o.WarnAt = v
	}
	o.AlertNumber = strings.TrimSpace(os.Getenv("MESHSAT_SMS_ALERT_NUMBER"))
	return o
}

// NewSMSBudget loads the counter from system_config, falling back to opts
// for keys that were never written. send is the transport's SendSMS; kit
// names this bridge in the reminder text.
func NewSMSBudget(db *database.DB, kit string, opts SMSBudgetOptions, send func(ctx context.Context, to, text string) error) *SMSBudget {
	b := &SMSBudget{db: db, kit: kit, send: send, now: time.Now}
	b.size = b.loadInt(cfgSMSBundleSize, opts.Size)
	b.sent = b.loadInt(cfgSMSBundleSent, 0)
	b.warnAt = b.loadInt(cfgSMSBundleWarnAt, opts.WarnAt)
	b.alertNumber = b.loadStr(cfgSMSAlertNumber, opts.AlertNumber)
	b.alerted = b.loadInt(cfgSMSBundleAlerted, 0) == 1
	if v := b.loadStr(cfgSMSBundleResetAt, ""); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			b.resetAt = t
		}
	}
	return b
}

// SetEventEmitter installs the SSE emitter (sms_credit_low, sms_credit_alert,
// sms_credit_reset events).
func (b *SMSBudget) SetEventEmitter(fn EventEmitFunc) {
	b.mu.Lock()
	b.emit = fn
	b.mu.Unlock()
}

func (b *SMSBudget) loadStr(key, fallback string) string {
	if b.db == nil {
		return fallback
	}
	v, err := b.db.GetSystemConfig(key)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Warn().Err(err).Str("key", key).Msg("sms budget: config read failed")
		}
		return fallback
	}
	return v
}

func (b *SMSBudget) loadInt(key string, fallback int) int {
	v := b.loadStr(key, "")
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fallback
	}
	return n
}

func (b *SMSBudget) store(key, value string) {
	if b.db == nil {
		return
	}
	if err := b.db.SetSystemConfig(key, value); err != nil {
		log.Warn().Err(err).Str("key", key).Msg("sms budget: config write failed")
	}
}

// SMSSegments is how many segments the network bills for one text: a
// single SMS up to 160 GSM-7 characters, concatenated parts of 153 above
// that. The bridge sanitises to GSM-7 before sending, so 7-bit sizes apply.
func SMSSegments(text string) int {
	n := len(text)
	if n <= 160 {
		return 1
	}
	return (n + 152) / 153
}

// Record is the transport's sent hook: one accepted SMS to one destination.
// It runs inside the transport's send critical section, so the reminder
// goes out from its own goroutine.
func (b *SMSBudget) Record(to, text string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.size <= 0 {
		return
	}
	segs := SMSSegments(text)
	b.sent += segs
	b.store(cfgSMSBundleSent, strconv.Itoa(b.sent))
	remaining := b.size - b.sent
	log.Info().Str("to", to).Int("segments", segs).Int("sent", b.sent).Int("size", b.size).Int("remaining", remaining).
		Msg("sms budget: segment counted")
	b.checkLocked()
}

// checkLocked raises the warning once per bundle and starts the reminder.
func (b *SMSBudget) checkLocked() {
	remaining := b.size - b.sent
	if remaining > b.warnAt {
		return
	}
	if !b.warned {
		b.warned = true
		msg := fmt.Sprintf("SMS credit low on %s: %d of %d left, top up the prepaid SIM", b.kit, max(remaining, 0), b.size)
		log.Warn().Int("remaining", remaining).Int("size", b.size).Int("warn_at", b.warnAt).Msg("sms budget: " + msg)
		if b.emit != nil {
			b.emit("sms_credit_low", msg)
		}
	}
	if b.alertNumber == "" || b.alerted || b.send == nil {
		return
	}
	now := b.now()
	if !b.alertTriedAt.IsZero() && now.Sub(b.alertTriedAt) < smsAlertRetry {
		return
	}
	b.alertTriedAt = now
	b.wg.Add(1)
	go b.sendAlert(remaining)
}

// alertText is the reminder, kept inside one GSM-7 segment.
func (b *SMSBudget) alertText(remaining int) string {
	return fmt.Sprintf("MeshSat %s: %d of %d SMS left on the kit SIM. Top up: kpn.com/prepaid/opwaarderen", b.kit, max(remaining, 0), b.size)
}

func (b *SMSBudget) sendAlert(remaining int) {
	defer b.wg.Done()
	b.mu.Lock()
	to, text, emit, send := b.alertNumber, b.alertText(remaining), b.emit, b.send
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), smsAlertTimeout)
	defer cancel()
	if err := send(ctx, to, text); err != nil {
		log.Error().Err(err).Str("to", to).Msg("sms budget: top-up reminder failed, retry in 10 min")
		if b.db != nil {
			b.db.InsertSMSMessage("tx", to, text, "failed", time.Now().Unix())
		}
		return
	}
	b.mu.Lock()
	b.alerted = true
	b.store(cfgSMSBundleAlerted, "1")
	b.mu.Unlock()
	if b.db != nil {
		b.db.InsertSMSMessage("tx", to, text, "sent", time.Now().Unix())
	}
	log.Warn().Str("to", to).Int("remaining", remaining).Msg("sms budget: top-up reminder sent")
	if emit != nil {
		emit("sms_credit_alert", fmt.Sprintf("top-up reminder sent to %s (%d left)", to, max(remaining, 0)))
	}
}

// Reset records a top-up: a new bundle of size segments with sent already
// used (0 for a fresh card). The warning and the reminder re-arm.
func (b *SMSBudget) Reset(size, sent int) error {
	if size <= 0 {
		return errors.New("bundle size must be positive")
	}
	if sent < 0 {
		return errors.New("sent must not be negative")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.size, b.sent = size, sent
	b.resetAt = b.now()
	b.alerted, b.warned, b.alertTriedAt = false, false, time.Time{}
	b.store(cfgSMSBundleSize, strconv.Itoa(size))
	b.store(cfgSMSBundleSent, strconv.Itoa(sent))
	b.store(cfgSMSBundleResetAt, b.resetAt.UTC().Format(time.RFC3339))
	b.store(cfgSMSBundleAlerted, "0")
	log.Info().Int("size", size).Int("sent", sent).Msg("sms budget: bundle reset")
	if b.emit != nil {
		b.emit("sms_credit_reset", fmt.Sprintf("SMS bundle set to %d, %d used", size, sent))
	}
	b.checkLocked()
	return nil
}

// Configure changes the warning threshold and the reminder number; a nil
// leaves that field alone. A changed number re-arms the reminder.
func (b *SMSBudget) Configure(warnAt *int, alertNumber *string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if warnAt != nil {
		if *warnAt < 0 {
			return errors.New("warn_at must not be negative")
		}
		b.warnAt = *warnAt
		b.warned = false
		b.store(cfgSMSBundleWarnAt, strconv.Itoa(*warnAt))
	}
	if alertNumber != nil {
		n := strings.TrimSpace(*alertNumber)
		if n != "" && !strings.HasPrefix(n, "+") {
			return errors.New("alert_number must be E.164 (+country...)")
		}
		if n != b.alertNumber {
			b.alerted, b.alertTriedAt = false, time.Time{}
			b.store(cfgSMSBundleAlerted, "0")
		}
		b.alertNumber = n
		b.store(cfgSMSAlertNumber, n)
	}
	b.checkLocked()
	return nil
}

// AlertNumber is the configured reminder number ("" when none).
func (b *SMSBudget) AlertNumber() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.alertNumber
}

// Status is the counter for the status API; nil when no bundle is set.
func (b *SMSBudget) Status() *transport.SMSBundleStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.size <= 0 {
		return nil
	}
	remaining := b.size - b.sent
	st := &transport.SMSBundleStatus{
		Size: b.size, Sent: b.sent, Remaining: remaining, WarnAt: b.warnAt,
		Low: remaining <= b.warnAt, Empty: remaining <= 0, Alerted: b.alerted, AlertSet: b.alertNumber != "",
	}
	if !b.resetAt.IsZero() {
		t := b.resetAt
		st.ResetAt = &t
	}
	return st
}

// HealthStatus is the device-health external target: degraded while the
// bundle is low, failed once it is spent, so the panel chip and the
// dashboard turn amber or red without any heal rung running (the modem
// is fine, the card is not). It carries no interface ids on purpose:
// a low bundle must not score the SMS lane 0 in failover.
func (b *SMSBudget) HealthStatus() (state, detail string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.size <= 0 {
		return HealthStateUnknown, "no SMS bundle configured"
	}
	remaining := b.size - b.sent
	switch {
	case remaining <= 0:
		return HealthStateFailed, fmt.Sprintf("SMS credit spent: %d of %d used, top up the prepaid SIM", b.sent, b.size)
	case remaining <= b.warnAt:
		return HealthStateDegraded, fmt.Sprintf("SMS credit low: %d of %d left, top up the prepaid SIM", remaining, b.size)
	default:
		return HealthStateOK, fmt.Sprintf("SMS credit %d of %d left", remaining, b.size)
	}
}

// Wait blocks until an in-flight reminder send has finished (tests).
func (b *SMSBudget) Wait() { b.wg.Wait() }
