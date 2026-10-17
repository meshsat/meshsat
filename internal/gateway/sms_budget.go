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
// (parallax, 15 Sep 2026). The serial modem holds every SMS's share on its
// I/O loop (transport.SMSSpend, HoldSMS), so every send path is counted:
// gateway relays, OOB frames, the API test send and the Reticulum SMS
// interface; the ModemManager modem reports accepted SMS to Record. State
// lives in system_config and survives restarts; a top-up is recorded with
// Reset. [MESHSAT-1161]
//
// Three counts (B9FIX-R1, R2): sent is what the network accepted (+CMGS);
// uncertain is what went to the modem without a +CMGS (a bare OK, no
// answer), which may have been billed, kept across a restart until the
// next top-up replaces the figures; held is the share of the SMS command
// running now. What is left is the size less all three, and a delivery
// intent's SMS goes only while that covers it (r3 D8). [MESHSAT-1584]
//
// Durability (B9FIX-R4, R5, R6): an intent SMS goes only once its share is
// stored as possible use, and every change of the counts is one
// transaction, published in memory only after it committed; a failed
// write keeps the earlier, larger exposure. A counter whose state cannot
// be read refuses intent SMS until a read succeeds (retried at the next
// check). A top-up replaces the historical counts but keeps the share of a
// command still running, which may submit after the balance was read.
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
	uncertain    int // went to the modem without a +CMGS; persisted
	held         int // the SMS command running now; in memory only
	running      int // the part of uncertain whose command still runs
	loadErr      error
	storeErr     error
	lost         int // segments counted-only SMS took while loadErr was set
	lostActive   int // counted-only commands running while loadErr was set
	opts         SMSBudgetOptions
	warnAt       int
	alertNumber  string
	resetAt      time.Time
	expiresAt    time.Time // the carrier bundle's end, as the operator recorded it; zero = not known
	alerted      bool      // the reminder SMS was accepted by the network for this bundle
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
	cfgSMSBundleUnsure  = "sms_bundle_uncertain"  // [MESHSAT-1584]
	cfgSMSBundleExpires = "sms_bundle_expires_at" // RFC 3339, empty = not known [MESHSAT-1584]
	cfgSMSBundleResetAt = "sms_bundle_reset_at"
	cfgSMSBundleWarnAt  = "sms_bundle_warn_at"
	cfgSMSAlertNumber   = "sms_alert_number"
	cfgSMSBundleAlerted = "sms_bundle_alerted"
)

// ErrSMSBundleStorage is a change to the bundle refused because its state
// could not be read or stored (B9FIX-R4). [MESHSAT-1584]
var ErrSMSBundleStorage = errors.New("the SMS bundle is unavailable")

// smsExpiryWarn is how long before its recorded end a bundle reports
// degraded. [MESHSAT-1584]
const smsExpiryWarn = 48 * time.Hour

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
// names this bridge in the reminder text. A read that fails (other than a
// key never written) leaves the counter unusable for intent SMS until a
// later read succeeds (B9FIX-R4).
func NewSMSBudget(db *database.DB, kit string, opts SMSBudgetOptions, send func(ctx context.Context, to, text string) error) *SMSBudget {
	b := &SMSBudget{db: db, kit: kit, send: send, now: time.Now, opts: opts}
	b.mu.Lock()
	b.loadErr = b.loadLocked()
	b.mu.Unlock()
	if b.loadErr != nil {
		log.Error().Err(b.loadErr).Msg("sms budget: the bundle could not be read; intent SMS wait until it can")
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

// loadLocked reads the stored counter into b, all fields or none. A key
// never written takes its default; any other read error, or a stored
// count that is not a number, is an error.
func (b *SMSBudget) loadLocked() error {
	if b.db == nil {
		b.size, b.warnAt, b.alertNumber = b.opts.Size, b.opts.WarnAt, b.opts.AlertNumber
		return nil
	}
	var errs []error
	str := func(key, fallback string) string {
		v, err := b.db.GetSystemConfig(key)
		if errors.Is(err, sql.ErrNoRows) {
			return fallback
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
			return fallback
		}
		return v
	}
	num := func(key string, fallback int) int {
		v := strings.TrimSpace(str(key, ""))
		if v == "" {
			return fallback
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %q is not a number", key, v))
			return fallback
		}
		return n
	}
	size, sent, uncertain := num(cfgSMSBundleSize, b.opts.Size), num(cfgSMSBundleSent, 0), num(cfgSMSBundleUnsure, 0)
	warnAt, alertNumber, alerted := num(cfgSMSBundleWarnAt, b.opts.WarnAt), str(cfgSMSAlertNumber, b.opts.AlertNumber), num(cfgSMSBundleAlerted, 0) == 1
	var resetAt, expiresAt time.Time
	if v := str(cfgSMSBundleResetAt, ""); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			resetAt = t
		}
	}
	if v := strings.TrimSpace(str(cfgSMSBundleExpires, "")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %q is not a time", cfgSMSBundleExpires, v))
		}
		expiresAt = t
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	b.size, b.sent, b.uncertain = size, sent, max(uncertain, 0)
	b.warnAt, b.alertNumber, b.alerted, b.resetAt, b.expiresAt = warnAt, alertNumber, alerted, resetAt, expiresAt
	return nil
}

// readyLocked retries the read of a counter that could not be read, when
// no command it counts for runs, and folds in what counted-only SMS took
// meanwhile as possible use. It returns the read error that remains.
func (b *SMSBudget) readyLocked() error {
	if b.loadErr == nil {
		return nil
	}
	if b.lostActive > 0 {
		return b.loadErr
	}
	if err := b.loadLocked(); err != nil {
		b.loadErr = err
		return err
	}
	b.loadErr = nil
	log.Info().Int("size", b.size).Int("sent", b.sent).Int("uncertain", b.uncertain).Msg("sms budget: the bundle was read again")
	if b.lost > 0 && b.size > 0 {
		lost := b.lost
		if err := b.commitLocked(b.sent, b.uncertain+lost); err != nil {
			b.uncertain += lost // not stored: counted in memory
		}
		log.Warn().Int("segments", lost).Msg("sms budget: SMS sent while the bundle could not be read, counted as possible use")
	}
	b.lost = 0
	return nil
}

// commitLocked stores both counts in one transaction and, only once it
// committed, publishes them (B9FIX-R5). On failure nothing changes. The
// size is the operator's (Apply), never written here, so an unset size
// keeps following its first-boot default.
func (b *SMSBudget) commitLocked(sent, uncertain int) error {
	if b.db != nil {
		if err := b.db.SetSystemConfigs(
			[2]string{cfgSMSBundleSent, strconv.Itoa(sent)},
			[2]string{cfgSMSBundleUnsure, strconv.Itoa(uncertain)},
		); err != nil {
			b.storeErr = err
			log.Error().Err(err).Int("sent", sent).Int("uncertain", uncertain).Msg("sms budget: the bundle count could not be stored")
			return err
		}
	}
	b.sent, b.uncertain, b.storeErr = sent, uncertain, nil
	return nil
}

// store writes one key that is not a count (the reminder flag), best
// effort.
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

// Record is the ModemManager modem's sent hook: one accepted SMS to one
// destination, counted after the fact (that modem holds no share). It runs
// inside the transport's send critical section, so the reminder goes out
// from its own goroutine.
func (b *SMSBudget) Record(to, text string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	segs := SMSSegments(text)
	if b.readyLocked() != nil {
		b.lost += segs
		return
	}
	if b.size <= 0 {
		return
	}
	if b.commitLocked(b.sent+segs, b.uncertain) != nil {
		b.uncertain += segs // not stored: counted in memory, as possible use
	}
	log.Info().Str("to", to).Int("segments", segs).Int("sent", b.sent).Int("size", b.size).Int("remaining", b.leftLocked()).
		Msg("sms budget: segment counted")
	b.checkLocked()
}

// leftLocked is what the bundle still covers: the size less what was
// accepted, what may have gone and what the running command holds.
func (b *SMSBudget) leftLocked() int { return b.size - b.sent - b.uncertain - b.held }

// expiredLocked: the bundle has a recorded end and it has come (at the
// end itself the bundle is over).
func (b *SMSBudget) expiredLocked() bool {
	return !b.expiresAt.IsZero() && !b.now().Before(b.expiresAt)
}

func (b *SMSBudget) expiredText() string {
	return fmt.Sprintf("the prepaid SMS bundle ended at %s; renew it and record the new one", b.expiresAt.UTC().Format(time.RFC3339))
}

// HoldSMS implements transport.SMSSpend: the share of one SMS, taken on
// the modem's I/O loop before its first byte. A delivery intent's SMS
// (enforce) is refused when what is left does not cover it (r3 D8,
// B9FIX-R1), and while the counter cannot be read (B9FIX-R4); every other
// send path is counted only. Without a bundle nothing is counted or
// refused.
func (b *SMSBudget) HoldSMS(to, text string, enforce bool) (transport.SMSHold, error) {
	segs := SMSSegments(text)
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.readyLocked(); err != nil {
		if enforce {
			return nil, fmt.Errorf("%w: the SMS bundle could not be read: %v", transport.ErrSMSBundleNotReady, err)
		}
		b.lostActive++
		return &smsHold{b: b, to: to, segs: segs, outage: true}, nil
	}
	if b.size <= 0 {
		return smsNoHold{}, nil
	}
	if enforce && b.expiredLocked() {
		// Checked here, on the loop, so a command that waited in the
		// queue past the end is refused too. Counted-only paths go.
		return nil, fmt.Errorf("%w: %s", transport.ErrSMSBundle, b.expiredText())
	}
	if left := b.leftLocked(); enforce && left < segs {
		return nil, fmt.Errorf("%w: %s", transport.ErrSMSBundle, b.shortLocked(left, segs))
	}
	b.held += segs
	return &smsHold{b: b, to: to, segs: segs, enforce: enforce}, nil
}

func (b *SMSBudget) shortLocked(left, segs int) string {
	s := fmt.Sprintf("the prepaid SMS bundle has %d of %d left, this SMS takes %d", max(left, 0), b.size, segs)
	if b.uncertain > 0 {
		s += fmt.Sprintf(" (%d went without a network reference and count as used until the next top-up)", b.uncertain)
	}
	return s
}

// smsNoHold is the share of an SMS sent with no bundle set.
type smsNoHold struct{}

func (smsNoHold) Possible() error           { return nil }
func (smsNoHold) Settle(transport.SMSPhase) {}

// smsHold is one SMS's share: held while its command runs, possible use
// from the moment it goes to the modem, then used or given back. An
// outage hold is a counted-only SMS sent while the counter could not be
// read: it is tallied apart and folded in once a read succeeds.
type smsHold struct {
	b               *SMSBudget
	to              string
	segs            int
	enforce, outage bool
	possible, done  bool
}

// Possible implements transport.SMSHold: the SMS goes to the modem now.
// The share is stored as possible use first; an intent SMS whose share
// could not be stored does not go (B9FIX-R4). A counted-only SMS goes
// anyway, its share counted in memory.
func (h *smsHold) Possible() error {
	b := h.b
	b.mu.Lock()
	defer b.mu.Unlock()
	if h.possible || h.done {
		return nil
	}
	if h.outage {
		h.possible = true
		b.lost += h.segs
		return nil
	}
	if err := b.commitLocked(b.sent, b.uncertain+h.segs); err != nil {
		if h.enforce {
			return fmt.Errorf("%w: its share could not be stored: %v", transport.ErrSMSBundleNotReady, err)
		}
		// Counted in memory; the next commit of the counts, whichever it
		// is, stores it with them.
		b.uncertain += h.segs
	}
	h.possible = true
	b.held = max(b.held-h.segs, 0)
	b.running += h.segs
	return nil
}

// Settle implements transport.SMSHold. Each outcome is one stored change;
// when the store fails the share stays possible use (B9FIX-R5).
func (h *smsHold) Settle(p transport.SMSPhase) {
	b := h.b
	b.mu.Lock()
	defer b.mu.Unlock()
	if h.done {
		return
	}
	h.done = true
	given := p == transport.SMSRefused || p == transport.SMSNotSent
	if h.outage {
		b.lostActive = max(b.lostActive-1, 0)
		if h.possible && given {
			b.lost = max(b.lost-h.segs, 0)
		}
		return
	}
	if !h.possible {
		b.held = max(b.held-h.segs, 0)
		return
	}
	b.running = max(b.running-h.segs, 0)
	switch {
	case p == transport.SMSAccepted:
		if b.commitLocked(b.sent+h.segs, max(b.uncertain-h.segs, 0)) != nil {
			log.Warn().Str("to", h.to).Int("segments", h.segs).Msg("sms budget: accepted, but not stored; kept as possible use")
			b.checkLocked()
			return
		}
		log.Info().Str("to", h.to).Int("segments", h.segs).Int("sent", b.sent).Int("size", b.size).Int("remaining", b.leftLocked()).
			Msg("sms budget: segment counted")
	case given:
		// Always through a commit of the counts: a share first counted in
		// memory only may be on disk by now (a later commit or a top-up
		// stored it), so a release in memory alone could leave it spent
		// there (B9FIX-R7). When the commit fails the share stays.
		if b.commitLocked(b.sent, max(b.uncertain-h.segs, 0)) != nil {
			log.Warn().Str("to", h.to).Int("segments", h.segs).Msg("sms budget: not sent, but not stored; kept as possible use")
		}
		return
	default:
		// No +CMGS and no refusal: it may have been billed. It stays
		// possible use (B9FIX-R2).
		log.Warn().Str("to", h.to).Int("segments", h.segs).Int("uncertain", b.uncertain).Int("remaining", b.leftLocked()).
			Msg("sms budget: no network reference, counted as possible use until the next top-up")
	}
	b.checkLocked()
}

// checkLocked raises the warning once per bundle and starts the reminder.
// Without a bundle there is nothing to be low on: the first PUT that set
// the reminder number before the size fired a "0 of 250 left" reminder
// (tesseract, 15 Sep 2026 22:20).
func (b *SMSBudget) checkLocked() {
	if b.size <= 0 {
		return
	}
	remaining := b.leftLocked()
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

// SMSBundleUpdate is one change to the bundle; a nil leaves that field
// alone. Sent needs Size. A new Size records a top-up: its ExpiresAt is the
// new bundle's end, or not known when none is given (the old end belonged
// to the old bundle). ExpiresAt without Size corrects the end;
// ClearExpiry forgets it. [MESHSAT-1161, MESHSAT-1584]
type SMSBundleUpdate struct {
	Size, Sent, WarnAt *int
	AlertNumber        *string
	ExpiresAt          *time.Time
	ClearExpiry        bool
}

// Apply is Update for the counts, the threshold and the reminder number.
func (b *SMSBudget) Apply(size, sent, warnAt *int, alertNumber *string) error {
	return b.Update(SMSBundleUpdate{Size: size, Sent: sent, WarnAt: warnAt, AlertNumber: alertNumber})
}

// Update changes any of the fields under one lock and evaluates the
// thresholds once at the end, so a PUT that sets size, warn_at and the
// reminder number together never fires on a half-configured state. A
// changed number re-arms the reminder; a new size records a top-up and
// re-arms warning and reminder, and its figures replace the uncertain
// count (the carrier's balance is what the operator read), except the
// share of a command still running, which may submit after that balance
// was read and stays possible use (B9FIX-R6). Every field is stored in one
// transaction; when it fails nothing changes and the error is returned
// (B9FIX-R4).
func (b *SMSBudget) Update(u SMSBundleUpdate) error {
	size, sent, warnAt, alertNumber := u.Size, u.Sent, u.WarnAt, u.AlertNumber
	if size != nil && *size <= 0 {
		return errors.New("bundle size must be positive")
	}
	if sent != nil && *sent < 0 {
		return errors.New("sent must not be negative")
	}
	if sent != nil && size == nil {
		return errors.New("sent needs size")
	}
	if warnAt != nil && *warnAt < 0 {
		return errors.New("warn_at must not be negative")
	}
	if u.ExpiresAt != nil && u.ClearExpiry {
		return errors.New("give an end or clear it, not both")
	}
	var number string
	if alertNumber != nil {
		number = strings.TrimSpace(*alertNumber)
		if number != "" && !strings.HasPrefix(number, "+") {
			return errors.New("alert_number must be E.164 (+country...)")
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.readyLocked(); err != nil {
		return fmt.Errorf("%w: it could not be read: %v", ErrSMSBundleStorage, err)
	}
	var pairs [][2]string
	newNumber, rearmAlert := b.alertNumber, false
	if alertNumber != nil {
		if number != b.alertNumber {
			rearmAlert = true
		}
		newNumber = number
		pairs = append(pairs, [2]string{cfgSMSAlertNumber, number})
	}
	newWarn := b.warnAt
	if warnAt != nil {
		newWarn = *warnAt
		pairs = append(pairs, [2]string{cfgSMSBundleWarnAt, strconv.Itoa(*warnAt)})
	}
	newSize, newSent, newUnsure, newReset := b.size, b.sent, b.uncertain, b.resetAt
	if size != nil {
		newSize, newSent, newUnsure, newReset = *size, 0, b.running, b.now()
		if sent != nil {
			newSent = *sent
		}
		pairs = append(pairs,
			[2]string{cfgSMSBundleSize, strconv.Itoa(newSize)},
			[2]string{cfgSMSBundleSent, strconv.Itoa(newSent)},
			[2]string{cfgSMSBundleUnsure, strconv.Itoa(newUnsure)},
			[2]string{cfgSMSBundleResetAt, newReset.UTC().Format(time.RFC3339)})
		rearmAlert = true
	}
	newExpires, expiryChanged := b.expiresAt, false
	switch {
	case u.ExpiresAt != nil:
		newExpires, expiryChanged = u.ExpiresAt.UTC(), true
	case u.ClearExpiry || size != nil:
		newExpires, expiryChanged = time.Time{}, true
	}
	if expiryChanged {
		v := ""
		if !newExpires.IsZero() {
			v = newExpires.Format(time.RFC3339)
		}
		pairs = append(pairs, [2]string{cfgSMSBundleExpires, v})
	}
	if rearmAlert {
		pairs = append(pairs, [2]string{cfgSMSBundleAlerted, "0"})
	}
	if b.db != nil && len(pairs) > 0 {
		if err := b.db.SetSystemConfigs(pairs...); err != nil {
			return fmt.Errorf("%w: it was not changed, it could not be stored: %v", ErrSMSBundleStorage, err)
		}
	}
	if rearmAlert {
		b.alerted, b.alertTriedAt = false, time.Time{}
	}
	b.alertNumber = newNumber
	if warnAt != nil {
		b.warnAt, b.warned = newWarn, false
	}
	b.expiresAt = newExpires
	if size != nil {
		b.size, b.sent, b.uncertain, b.resetAt, b.storeErr = newSize, newSent, newUnsure, newReset, nil
		b.warned = false
		log.Info().Int("size", newSize).Int("sent", newSent).Int("uncertain", newUnsure).Msg("sms budget: bundle reset")
		if b.emit != nil {
			b.emit("sms_credit_reset", fmt.Sprintf("SMS bundle set to %d, %d used", newSize, newSent))
		}
	}
	b.checkLocked()
	return nil
}

// Reset records a top-up: a new bundle of size segments with sent already
// used (0 for a fresh card). The warning and the reminder re-arm.
func (b *SMSBudget) Reset(size, sent int) error { return b.Apply(&size, &sent, nil, nil) }

// Configure changes the warning threshold and the reminder number; a nil
// leaves that field alone. A changed number re-arms the reminder.
func (b *SMSBudget) Configure(warnAt *int, alertNumber *string) error {
	return b.Apply(nil, nil, warnAt, alertNumber)
}

// AlertNumber is the configured reminder number ("" when none).
func (b *SMSBudget) AlertNumber() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.alertNumber
}

// Status is the counter for the status API; nil when no bundle is set.
// While the stored bundle cannot be read it says so (Error) instead.
func (b *SMSBudget) Status() *transport.SMSBundleStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	_ = b.readyLocked()
	if b.loadErr != nil {
		return &transport.SMSBundleStatus{Empty: true, Error: "the SMS bundle could not be read: " + b.loadErr.Error()}
	}
	if b.size <= 0 {
		return nil
	}
	remaining, expired := b.leftLocked(), b.expiredLocked()
	usable := max(remaining, 0)
	if expired {
		usable = 0
	}
	st := &transport.SMSBundleStatus{
		Size: b.size, Sent: b.sent, Uncertain: b.uncertain, Remaining: remaining, Usable: usable, WarnAt: b.warnAt,
		Low: remaining <= b.warnAt, Empty: remaining <= 0, Expired: expired, Alerted: b.alerted, AlertSet: b.alertNumber != "",
	}
	if !b.expiresAt.IsZero() {
		t := b.expiresAt
		st.ExpiresAt = &t
		st.ExpiringSoon = !expired && b.expiresAt.Sub(b.now()) <= smsExpiryWarn
	}
	if b.storeErr != nil {
		st.Error = "the SMS bundle count could not be stored: " + b.storeErr.Error()
	}
	if !b.resetAt.IsZero() {
		t := b.resetAt
		st.ResetAt = &t
	}
	return st
}

// HealthStatus is the device-health external target: degraded while the
// bundle is low or its count could not be stored, failed once it is spent
// or cannot be read, so the panel chip and the dashboard turn amber or red
// without any heal rung running (the modem is fine, the card is not). It
// carries no interface ids on purpose: a low bundle must not score the SMS
// lane 0 in failover.
func (b *SMSBudget) HealthStatus() (state, detail string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_ = b.readyLocked()
	if b.loadErr != nil {
		return HealthStateFailed, "the SMS bundle could not be read, intent SMS wait: " + b.loadErr.Error()
	}
	if b.size <= 0 {
		return HealthStateUnknown, "no SMS bundle configured"
	}
	remaining := b.leftLocked()
	switch {
	case b.expiredLocked():
		return HealthStateFailed, "SMS bundle ended: " + b.expiredText()
	case remaining <= 0:
		return HealthStateFailed, fmt.Sprintf("SMS credit spent: %d of %d used, top up the prepaid SIM", b.size-remaining, b.size)
	case !b.expiresAt.IsZero() && b.expiresAt.Sub(b.now()) <= smsExpiryWarn:
		return HealthStateDegraded, fmt.Sprintf("SMS bundle ends at %s, %d of %d left: renew it", b.expiresAt.UTC().Format(time.RFC3339), remaining, b.size)
	case b.storeErr != nil:
		return HealthStateDegraded, fmt.Sprintf("SMS credit %d of %d left, but the count could not be stored: %v", remaining, b.size, b.storeErr)
	case remaining <= b.warnAt:
		return HealthStateDegraded, fmt.Sprintf("SMS credit low: %d of %d left, top up the prepaid SIM", remaining, b.size)
	default:
		return HealthStateOK, fmt.Sprintf("SMS credit %d of %d left", remaining, b.size)
	}
}

// CheckSMS implements transport.SMSSpend: whether a delivery intent may
// send segments more SMS now, the HoldSMS check without a hold. Without a
// bundle it allows: nothing is counted against. [MESHSAT-1584]
func (b *SMSBudget) CheckSMS(segments int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.readyLocked(); err != nil {
		return fmt.Errorf("%w: the SMS bundle could not be read: %v", transport.ErrSMSBundleNotReady, err)
	}
	if b.size <= 0 {
		return nil
	}
	if b.expiredLocked() {
		return fmt.Errorf("%w: %s", transport.ErrSMSBundle, b.expiredText())
	}
	if left := b.leftLocked(); left < segments {
		return fmt.Errorf("%w: %s", transport.ErrSMSBundle, b.shortLocked(left, segments))
	}
	return nil
}

// Wait blocks until an in-flight reminder send has finished (tests).
func (b *SMSBudget) Wait() { b.wg.Wait() }
