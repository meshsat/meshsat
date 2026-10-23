package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/hemb"
)

// The hop set is slice (b) of media hopping (MEDIA_HOP_DESIGN section 4.3):
// per link a state built from contact-independent evidence only, so one
// silent contact never takes a bearer out of use. Behind MESHSAT_HOPSET the
// manager is the route gate (design D-7); without it it only observes, and
// GET /api/hopset shows what it would decide. [MESHSAT-1650]
//
// The rules, in order:
//   - A negative sign makes an admitted link suspect. A sign is RF-side (a
//     known spectrum jam), the radio's own (it refused or never sent the
//     packet), a delivery failure to one destination, or silence (a link that
//     usually hears traffic every minute has heard nothing for SilenceAfter).
//     Failures to the same destination are one sign; the radio's own
//     refusals at least RadioRepeat apart are separate signs (D-10).
//   - A positive sign answers every negative sign that came before it: those
//     are cleared and never count again (R46-V1).
//   - A suspect link is excluded when, inside TriggerWindow, it holds two
//     independent unanswered signs and has been suspect for DropDwell. It goes
//     back to admitted once its signs age out or a positive sign answers them.
//   - An excluded link starts recovering on a positive sign (a frame received
//     on it, a peer's acknowledgement), never on elapsed time alone, and is
//     admitted after its re-admission dwell without a new negative sign; one
//     sends it back to excluded. The dwell is ReadmitDwell, doubled for each
//     exclusion within FlapMemory of the one before, up to ReadmitMax (D-10).
//     A recovering link carries no ordinary traffic until it is admitted,
//     unless the floor keeps it (R46-V2).
//   - The floor: never fewer than MinSet usable links (of those not down),
//     and never the last free one. While it is broken, the least-bad link out
//     of use (excluded or recovering) is kept in use, retained. Retention is
//     worked out afresh at each evaluation.
//   - A link is down when its receiver is deaf (the receive checkers) or its
//     device is known to be absent (the availability the failover groups
//     use; a device-less link is never absent): out of use whatever the floor
//     says, and not counted for it (R46-V4).
//   - Every net change of a link's state, retention or down is audited once
//     per evaluation; a link going out of use or back into use is a HeMB
//     bearer event (R46-V5).

// HopState is a link's place in the hop set.
type HopState string

// The hop set's states.
const (
	HopAdmitted   HopState = "admitted"
	HopSuspect    HopState = "suspect"
	HopExcluded   HopState = "excluded"
	HopRecovering HopState = "recovering"
)

// HopSigns takes the evidence the hop set decides on.
type HopSigns interface {
	// Negative records a sign against a link: source is "radio" or
	// "delivery" (the spectrum is read, not told), key the destination of
	// a delivery sign or the radio's reason.
	Negative(ifaceID, source, key, reason string)
	// Positive records a sign the link carries traffic.
	Positive(ifaceID, source string)
	// PaidAttempt notes a send on a paid link, for the alarm.
	PaidAttempt(ifaceID string)
}

// hopSink holds a HopSigns behind an atomic pointer, so the workers made
// before main sets it still see it.
type hopSink struct{ HopSigns }

// JamVerdicter is the spectrum monitor's verdict on a link (D-8 ages it).
type JamVerdicter interface {
	JamVerdict(interfaceID string) (jammed, known bool)
}

// HopSetConfig holds the hop set's timings and its floor.
type HopSetConfig struct {
	TriggerWindow time.Duration // signs older than this are forgotten
	DropDwell     time.Duration // least time suspect before excluded
	ReadmitDwell  time.Duration // recovering without a negative sign before admitted (first exclusion)
	ReadmitMax    time.Duration // the re-admission dwell doubles up to this
	FlapMemory    time.Duration // an exclusion this soon after the last doubles the dwell
	RadioRepeat   time.Duration // the radio's own refusals this far apart are separate signs
	SilenceAfter  time.Duration // a busy link quiet this long has a silence sign
	MinSet        int           // never fewer usable links than this
	AlarmEvery    time.Duration // the alarm repeats at most this often per link
	RefreshEvery  time.Duration // how often the list of links is read again
	TickEvery     time.Duration // how often time-driven transitions are checked
}

// DefaultHopSetConfig is the owner's ruling (design D-10, 23 Oct 2026),
// from the kits' own traffic: a link sends every 15 to 55 min, so the
// window is 15 min; links come back slowly and more slowly each time they
// fail again within an hour (as route flap damping does). [MESHSAT-1650]
func DefaultHopSetConfig() HopSetConfig {
	return HopSetConfig{
		TriggerWindow: 15 * time.Minute,
		DropDwell:     10 * time.Second,
		ReadmitDwell:  10 * time.Minute,
		ReadmitMax:    60 * time.Minute,
		FlapMemory:    time.Hour,
		RadioRepeat:   60 * time.Second,
		SilenceAfter:  10 * time.Minute,
		MinSet:        2,
		AlarmEvery:    10 * time.Minute,
		RefreshEvery:  30 * time.Second,
		TickEvery:     5 * time.Second,
	}
}

// HopSetGating reports MESHSAT_HOPSET (default false): true makes the hop
// set the route gate; false leaves the slice (a) gate in place and the hop
// set observing.
func HopSetGating() bool {
	v, _ := strconv.ParseBool(os.Getenv("MESHSAT_HOPSET"))
	return v
}

// hopPaid lists the channel types the hop set covers and whether each is
// paid (the three the intent engine bills). Any other type is outside it.
// hopDeviceless lists the covered channel types with no device of their own,
// which are never down for absence.
var hopDeviceless = map[string]bool{"lxmf": true, "hub": true}

var hopPaid = map[string]bool{
	"mesh": false, "aprs": false, "tetra": false, "lxmf": false, "cell": false,
	"zigbee": false, "hub": false,
	"cellular": true, "sms": true, "iridium": true, "iridium_imt": true,
}

// HopSign is one piece of evidence against a link.
type HopSign struct {
	Source string    `json:"source"`
	Key    string    `json:"key"`
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
	seq    uint64    // arrival order: two signs in one instant still have an order
}

// HopLinkStatus is one link as GET /api/hopset shows it.
type HopLinkStatus struct {
	InterfaceID  string     `json:"interface_id"`
	ChannelType  string     `json:"channel_type"`
	Paid         bool       `json:"paid"`
	State        HopState   `json:"state"`
	Usable       bool       `json:"usable"` // ordinary traffic may use it now
	Down         bool       `json:"down"`
	Deaf         bool       `json:"deaf"`
	Absent       bool       `json:"absent"`
	Retained     bool       `json:"retained"`
	Since        time.Time  `json:"since"`
	Reason       string     `json:"reason,omitempty"`
	ReadmitAfter string     `json:"readmit_after,omitempty"` // the re-admission dwell of its last exclusion
	Flaps        int        `json:"flaps"`                   // exclusions within an hour of the one before
	Signs        []HopSign  `json:"signs"`
	LastPositive *time.Time `json:"last_positive,omitempty"`
}

// HopSetStatus is GET /api/hopset.
type HopSetStatus struct {
	Gating bool            `json:"gating"`
	Config map[string]any  `json:"config"`
	Links  []HopLinkStatus `json:"links"`
}

type hopLink struct {
	id, channelType string
	paid            bool
	state           HopState
	since           time.Time
	deaf, absent    bool
	down            bool // deaf or absent
	retained        bool
	reason          string
	negatives       []HopSign
	lastPositive    time.Time
	lastPositiveSeq uint64
	rxTimes         []time.Time   // frames heard, the last 90 min, for the silence sign
	lastExcluded    time.Time     // when the link was last excluded
	flaps           int           // exclusions within FlapMemory of the one before
	readmit         time.Duration // this exclusion's re-admission dwell
	sinceSeq        uint64        // the arrival order when the state was entered
	lastAlarm       time.Time
}

// distinct counts the link's independent signs. A delivery sign counts once
// per destination, so one silent contact is one sign however often it
// stays silent. The radio's own refusals are contact-independent: per
// reason, refusals at least repeat apart are separate signs (the owner's
// ruling, D-10). The spectrum and silence signs count once each.
func (l *hopLink) distinct(repeat time.Duration) int {
	seen := map[string]bool{}
	radio := map[string][]time.Time{}
	for _, s := range l.negatives {
		if s.Source == "radio" {
			radio[s.Key] = append(radio[s.Key], s.At)
			continue
		}
		seen[s.Source+"\x00"+s.Key] = true
	}
	n := len(seen)
	for _, ts := range radio {
		sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
		var last time.Time
		for i, t := range ts {
			if i == 0 || t.Sub(last) >= repeat {
				n++
				last = t
			}
		}
	}
	return n
}

// busy reports whether the link usually hears traffic about every
// minute: at least 30 frames in the hour before its last one, in at least
// 5 of that hour's 10-minute stretches. A short burst followed by its usual
// quiet is not busy.
func (l *hopLink) busy() bool {
	if len(l.rxTimes) == 0 {
		return false
	}
	last := l.rxTimes[len(l.rxTimes)-1]
	from := last.Add(-time.Hour)
	n := 0
	buckets := map[int]bool{}
	for _, t := range l.rxTimes {
		if t.Before(from) {
			continue
		}
		n++
		buckets[int(last.Sub(t)/(10*time.Minute))] = true
	}
	return n >= 30 && len(buckets) >= 5
}

// latestNegative is the arrival order of the link's newest negative sign
// (0 for none). Signs are ordered by arrival, not by clock time, so a
// positive sign in the same instant as a negative one is still after it.
func (l *hopLink) latestNegative() uint64 {
	var n uint64
	for _, s := range l.negatives {
		if s.seq > n {
			n = s.seq
		}
	}
	return n
}

// summary names the signs a decision rests on.
func (l *hopLink) summary() string {
	parts := make([]string, 0, len(l.negatives))
	seen := map[string]bool{}
	for _, s := range l.negatives {
		k := s.Source + ":" + s.Key
		if !seen[k] {
			seen[k] = true
			parts = append(parts, k)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// HopSetManager keeps the hop set.
type HopSetManager struct {
	mu        sync.Mutex
	cfg       HopSetConfig
	gating    bool
	clock     func() time.Time
	list      func() ([]database.Interface, error)
	spectrum  JamVerdicter
	receive   ReceiveChecker
	avail     func(ifaceID string) (up, known bool)
	audit     func(eventType, ifaceID, detail string)
	publish   func(hemb.Event)
	links     map[string]*hopLink
	refreshed time.Time
	seq       uint64 // the arrival order of signs and state changes
}

// NewHopSetManager makes a hop set over the links list returns.
func NewHopSetManager(cfg HopSetConfig, list func() ([]database.Interface, error), gating bool) *HopSetManager {
	return &HopSetManager{cfg: cfg, gating: gating, list: list, links: map[string]*hopLink{}}
}

// Gating reports whether the hop set is the route gate.
func (m *HopSetManager) Gating() bool { return m.gating }

// SetSpectrum sets the jam verdicts the hop set reads.
func (m *HopSetManager) SetSpectrum(sv JamVerdicter) {
	m.mu.Lock()
	m.spectrum = sv
	m.mu.Unlock()
}

// SetReceiveChecker sets the deaf checkers (a deaf link is down).
func (m *HopSetManager) SetReceiveChecker(rc ReceiveChecker) {
	m.mu.Lock()
	m.receive = rc
	m.mu.Unlock()
}

// SetAvailability sets where the hop set learns that a link's device is
// known to be absent (the failover groups' online check). An unknown answer
// never makes a link down.
func (m *HopSetManager) SetAvailability(f func(ifaceID string) (up, known bool)) {
	m.mu.Lock()
	m.avail = f
	m.mu.Unlock()
}

// SetAudit sets where state changes are audited.
func (m *HopSetManager) SetAudit(f func(eventType, ifaceID, detail string)) {
	m.mu.Lock()
	m.audit = f
	m.mu.Unlock()
}

// SetPublisher sets where the HeMB bearer events go.
func (m *HopSetManager) SetPublisher(f func(hemb.Event)) {
	m.mu.Lock()
	m.publish = f
	m.mu.Unlock()
}

func (m *HopSetManager) now() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return time.Now()
}

// Start checks the time-driven transitions every TickEvery until ctx ends.
func (m *HopSetManager) Start(ctx context.Context) {
	t := time.NewTicker(m.cfg.TickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.mu.Lock()
			out := m.evaluateLocked(m.now())
			m.mu.Unlock()
			out.flush()
		}
	}
}

// hopOut is what an evaluation emits, run after the lock is released.
type hopOut []func()

func (o hopOut) flush() {
	for _, f := range o {
		f()
	}
}

// Negative implements HopSigns.
func (m *HopSetManager) Negative(ifaceID, source, key, reason string) {
	m.mu.Lock()
	now := m.now()
	l := m.linkLocked(ifaceID, now)
	if l == nil {
		m.mu.Unlock()
		return
	}
	if key == "" {
		key = "-"
	}
	m.seq++
	l.negatives = append(l.negatives, HopSign{Source: source, Key: key, Reason: reason, At: now, seq: m.seq})
	out := m.evaluateLocked(now)
	m.mu.Unlock()
	out.flush()
}

// Positive implements HopSigns.
func (m *HopSetManager) Positive(ifaceID, source string) {
	m.mu.Lock()
	now := m.now()
	l := m.linkLocked(ifaceID, now)
	if l == nil {
		m.mu.Unlock()
		return
	}
	m.seq++
	l.lastPositive, l.lastPositiveSeq = now, m.seq
	if source == "rx" {
		l.rxTimes = append(l.rxTimes, now)
		cut := 0
		for cut < len(l.rxTimes) && now.Sub(l.rxTimes[cut]) > 90*time.Minute {
			cut++
		}
		if len(l.rxTimes)-cut > 512 {
			cut = len(l.rxTimes) - 512
		}
		l.rxTimes = l.rxTimes[cut:]
	}
	out := m.evaluateLocked(now)
	m.mu.Unlock()
	out.flush()
}

// PaidAttempt implements HopSigns: a send on a paid link while a free link
// is excluded raises the alarm "free bearers excluded while paid attempts
// rise", once per AlarmEvery per free link.
func (m *HopSetManager) PaidAttempt(ifaceID string) {
	m.mu.Lock()
	now := m.now()
	l := m.linkLocked(ifaceID, now)
	if l == nil || !l.paid {
		m.mu.Unlock()
		return
	}
	var out hopOut
	for _, f := range m.sortedLocked() {
		if f.paid || f.down || usable(f) {
			continue
		}
		if !f.lastAlarm.IsZero() && now.Sub(f.lastAlarm) < m.cfg.AlarmEvery {
			continue
		}
		f.lastAlarm = now
		detail := fmt.Sprintf("free bearer %s excluded (%s) while a paid attempt goes out on %s", f.id, f.reason, ifaceID)
		out = append(out, m.emitLocked("hopset_alarm", "HOPSET_ALARM", f, detail)...)
	}
	m.mu.Unlock()
	out.flush()
}

// RouteExcluded implements RouteGate: a link is tried when it is usable
// (admitted, suspect, or kept by the floor) and not down. A recovering link
// waits for its re-admission. A link outside the hop set is gated by its
// deaf check only, as the slice (a) gate had it.
func (m *HopSetManager) RouteExcluded(interfaceID string) (bool, string) {
	m.mu.Lock()
	out := m.evaluateLocked(m.now())
	l := m.links[interfaceID]
	var ex bool
	var why string
	switch {
	case l == nil:
		if m.receive != nil && m.receive.ReceiveDeaf(interfaceID) {
			ex, why = true, "deaf"
		}
	case l.deaf:
		ex, why = true, "deaf"
	case l.absent:
		ex, why = true, "absent"
	case !usable(l):
		ex, why = true, string(l.state)+": "+l.reason
	}
	m.mu.Unlock()
	out.flush()
	return ex, why
}

// Status is GET /api/hopset.
func (m *HopSetManager) Status() HopSetStatus {
	m.mu.Lock()
	out := m.evaluateLocked(m.now())
	st := HopSetStatus{Gating: m.gating, Config: map[string]any{
		"trigger_window_s": m.cfg.TriggerWindow.Seconds(), "drop_dwell_s": m.cfg.DropDwell.Seconds(),
		"readmit_dwell_s": m.cfg.ReadmitDwell.Seconds(), "readmit_max_s": m.cfg.ReadmitMax.Seconds(),
		"flap_memory_s": m.cfg.FlapMemory.Seconds(), "radio_repeat_s": m.cfg.RadioRepeat.Seconds(),
		"silence_after_s": m.cfg.SilenceAfter.Seconds(), "min_set": m.cfg.MinSet,
	}, Links: []HopLinkStatus{}}
	for _, l := range m.sortedLocked() {
		ls := HopLinkStatus{InterfaceID: l.id, ChannelType: l.channelType, Paid: l.paid, State: l.state, Usable: usable(l),
			Down: l.down, Deaf: l.deaf, Absent: l.absent, Retained: l.retained, Since: l.since, Reason: l.reason,
			Flaps: l.flaps,
			Signs: append([]HopSign{}, l.negatives...)}
		if !l.lastPositive.IsZero() {
			t := l.lastPositive
			ls.LastPositive = &t
		}
		if l.readmit > 0 {
			ls.ReadmitAfter = l.readmit.String()
		}
		st.Links = append(st.Links, ls)
	}
	m.mu.Unlock()
	out.flush()
	return st
}

// linkLocked is the link for an interface, reading the list again when the
// id is not known yet; nil for a link outside the hop set.
func (m *HopSetManager) linkLocked(id string, now time.Time) *hopLink {
	if l := m.links[id]; l != nil {
		return l
	}
	m.refreshLocked(now)
	return m.links[id]
}

// refreshLocked reads the enabled links of the covered channel types. A new
// link starts admitted; one that is gone or switched off leaves the set.
func (m *HopSetManager) refreshLocked(now time.Time) {
	m.refreshed = now
	if m.list == nil {
		return
	}
	ifaces, err := m.list()
	if err != nil {
		log.Debug().Err(err).Msg("hopset: list links")
		return
	}
	seen := map[string]bool{}
	for _, i := range ifaces {
		paid, ok := hopPaid[i.ChannelType]
		if !ok || !i.Enabled {
			continue
		}
		seen[i.ID] = true
		if m.links[i.ID] == nil {
			m.links[i.ID] = &hopLink{id: i.ID, channelType: i.ChannelType, paid: paid, state: HopAdmitted, since: now}
		}
	}
	for id := range m.links {
		if !seen[id] {
			delete(m.links, id)
		}
	}
}

func (m *HopSetManager) sortedLocked() []*hopLink {
	out := make([]*hopLink, 0, len(m.links))
	for _, l := range m.links {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// usable reports whether ordinary traffic may use the link now.
func usable(l *hopLink) bool {
	if l.down {
		return false
	}
	return l.state == HopAdmitted || l.state == HopSuspect || l.retained
}

// hopSnap is what a link was before an evaluation.
type hopSnap struct {
	state                  HopState
	retained, down, usable bool
}

// evaluateLocked applies the rules at now and returns what to emit: the
// transitions first, then the floor, then one audit entry per net change
// and one event per link going out of use or back into use, so a link the
// floor keeps never shows an exclusion it didn't have.
func (m *HopSetManager) evaluateLocked(now time.Time) hopOut {
	if m.refreshed.IsZero() || now.Sub(m.refreshed) >= m.cfg.RefreshEvery {
		m.refreshLocked(now)
	}
	links := m.sortedLocked()
	prior := make(map[*hopLink]hopSnap, len(links))
	for _, l := range links {
		prior[l] = hopSnap{l.state, l.retained, l.down, usable(l)}
		kept := l.negatives[:0]
		for _, s := range l.negatives {
			// The spectrum and silence signs are read afresh below, never
			// kept; a sign a positive sign answered is cleared (R46-V1).
			if s.Source != "spectrum" && s.Source != "silence" && now.Sub(s.At) <= m.cfg.TriggerWindow && s.seq > l.lastPositiveSeq {
				kept = append(kept, s)
			}
		}
		l.negatives = kept
		// A known jam is a sign for as long as the monitor says so (D-8 ages
		// the verdict; the hop set keeps no clock of its own for it).
		if m.spectrum != nil {
			if jammed, known := m.spectrum.JamVerdict(l.id); jammed && known {
				m.seq++
				l.negatives = append(l.negatives, HopSign{Source: "spectrum", Key: "jam", Reason: "spectrum jam verdict", At: now, seq: m.seq})
			}
		}
		// A link that usually hears traffic every minute and has heard
		// nothing for SilenceAfter has a silence sign, read afresh like
		// the spectrum's: the next frame heard ends it (D-10).
		if l.busy() && now.Sub(l.rxTimes[len(l.rxTimes)-1]) >= m.cfg.SilenceAfter {
			m.seq++
			l.negatives = append(l.negatives, HopSign{Source: "silence", Key: "rx",
				Reason: "nothing heard for " + now.Sub(l.rxTimes[len(l.rxTimes)-1]).Round(time.Second).String(), At: now, seq: m.seq})
		}
		l.deaf = m.receive != nil && m.receive.ReceiveDeaf(l.id)
		l.absent = false
		if m.avail != nil && !hopDeviceless[l.channelType] {
			if up, known := m.avail(l.id); known && !up {
				l.absent = true
			}
		}
		l.down = l.deaf || l.absent
		l.retained = false // worked out afresh by the floor below
	}

	for _, l := range links {
		latest := l.latestNegative()
		cleared := len(l.negatives) == 0
		switch l.state {
		case HopAdmitted:
			if !cleared {
				m.setLocked(l, HopSuspect, l.summary(), now)
			}
		case HopSuspect:
			if cleared {
				m.setLocked(l, HopAdmitted, "evidence cleared", now)
			} else if l.distinct(m.cfg.RadioRepeat) >= 2 && now.Sub(l.since) >= m.cfg.DropDwell {
				m.excludeLocked(l, now)
			}
		case HopExcluded:
			if l.lastPositiveSeq > l.sinceSeq && cleared {
				m.setLocked(l, HopRecovering, "a positive sign", now)
			}
		case HopRecovering:
			if latest > l.sinceSeq {
				m.excludeLocked(l, now)
			} else if now.Sub(l.since) >= l.readmitDwell(m.cfg) {
				m.setLocked(l, HopAdmitted, "re-admitted", now)
			}
		}
	}
	m.repairFloorLocked()

	var out hopOut
	for _, l := range links {
		was := prior[l]
		if was.state != l.state || was.retained != l.retained || was.down != l.down {
			out = append(out, m.auditLocked(l)...)
		}
		switch now := usable(l); {
		case was.usable && !now:
			out = append(out, m.eventLocked(string(hemb.EventBearerDegraded), l)...)
		case !was.usable && now:
			out = append(out, m.eventLocked(string(hemb.EventBearerRecovered), l)...)
		}
	}
	return out
}

// countsLocked counts the links not down, the usable ones, and the free
// ones of each.
func (m *HopSetManager) countsLocked() (available, usableN, freeAvailable, freeUsable int) {
	for _, l := range m.links {
		if l.down {
			continue
		}
		available++
		if !l.paid {
			freeAvailable++
		}
		if !usable(l) {
			continue
		}
		usableN++
		if !l.paid {
			freeUsable++
		}
	}
	return
}

// repairFloorLocked keeps the floor: while fewer than MinSet links (of
// those not down) are usable, or no free link is while one is available,
// the least-bad link out of use (excluded or recovering) is kept in use,
// retained: the fewest distinct signs first, then a recovering one, then
// the most recent positive sign, then the id. Never at random, never on
// elapsed time. One rule for every way the floor breaks.
func (m *HopSetManager) repairFloorLocked() {
	for {
		available, usableN, freeAvailable, freeUsable := m.countsLocked()
		needFree := freeAvailable > 0 && freeUsable == 0
		if usableN >= min(m.cfg.MinSet, available) && !needFree {
			return
		}
		var cands []*hopLink
		for _, l := range m.sortedLocked() {
			if l.down || usable(l) || (needFree && l.paid) {
				continue
			}
			cands = append(cands, l)
		}
		if len(cands) == 0 {
			return
		}
		sort.SliceStable(cands, func(i, j int) bool {
			a, b := cands[i], cands[j]
			if a.distinct(m.cfg.RadioRepeat) != b.distinct(m.cfg.RadioRepeat) {
				return a.distinct(m.cfg.RadioRepeat) < b.distinct(m.cfg.RadioRepeat)
			}
			if ra, rb := a.state == HopRecovering, b.state == HopRecovering; ra != rb {
				return ra
			}
			return a.lastPositiveSeq > b.lastPositiveSeq
		})
		cands[0].retained = true
	}
}

// excludeLocked excludes a link and sets how long it must recover before
// it is admitted again: ReadmitDwell, doubled for each exclusion within
// FlapMemory of the one before, up to ReadmitMax, as route flap damping
// does, so a link that keeps failing comes back more slowly each time.
func (m *HopSetManager) excludeLocked(l *hopLink, now time.Time) {
	if !l.lastExcluded.IsZero() && now.Sub(l.lastExcluded) <= m.cfg.FlapMemory {
		l.flaps++
	} else {
		l.flaps = 0
	}
	l.lastExcluded = now
	d := m.cfg.ReadmitDwell
	for i := 0; i < l.flaps && d < m.cfg.ReadmitMax; i++ {
		d *= 2
	}
	if m.cfg.ReadmitMax > 0 && d > m.cfg.ReadmitMax {
		d = m.cfg.ReadmitMax
	}
	l.readmit = d
	m.setLocked(l, HopExcluded, l.summary(), now)
}

// readmitDwell is how long the link must recover, by its last exclusion.
func (l *hopLink) readmitDwell(cfg HopSetConfig) time.Duration {
	if l.readmit > 0 {
		return l.readmit
	}
	return cfg.ReadmitDwell
}

func (m *HopSetManager) setLocked(l *hopLink, s HopState, reason string, now time.Time) {
	log.Debug().Str("link", l.id).Str("from", string(l.state)).Str("to", string(s)).Str("reason", reason).Msg("hopset")
	m.seq++
	l.state, l.reason, l.since, l.sinceSeq = s, reason, now, m.seq
}

// label is the link's state as the audit names it: down and retained
// first, since they decide its use.
func (l *hopLink) label() string {
	switch {
	case l.deaf:
		return "down"
	case l.absent:
		return "down"
	case l.retained:
		return "retained"
	}
	return string(l.state)
}

func (m *HopSetManager) mode() string {
	if m.gating {
		return "gating"
	}
	return "observing"
}

// auditLocked prepares the audit entry for a link's net change.
func (m *HopSetManager) auditLocked(l *hopLink) hopOut {
	audit := m.audit
	if audit == nil {
		return nil
	}
	detail := fmt.Sprintf("%s: state %s, retained %v, deaf %v, absent %v: %s (%s)", l.label(), l.state, l.retained, l.deaf, l.absent, l.reason, m.mode())
	typ, id := "hopset_"+l.label(), l.id
	return hopOut{func() { audit(typ, id, detail) }}
}

// eventLocked prepares a HeMB bearer event for a link going out of use or
// back into use.
func (m *HopSetManager) eventLocked(event string, l *hopLink) hopOut {
	publish := m.publish
	if publish == nil {
		return nil
	}
	payload, _ := json.Marshal(map[string]any{"interface_id": l.id, "state": l.state, "reason": l.reason,
		"retained": l.retained, "deaf": l.deaf, "absent": l.absent, "signs": append([]HopSign{}, l.negatives...), "mode": m.mode()})
	ev := hemb.Event{Type: hemb.EventType(event), Timestamp: m.now(), Payload: payload}
	return hopOut{func() { publish(ev) }}
}

// emitLocked prepares an audit entry and, when event is set, a HeMB event
// (the alarm's).
func (m *HopSetManager) emitLocked(auditType, event string, l *hopLink, detail string) hopOut {
	audit, publish := m.audit, m.publish
	id, full := l.id, detail+" ("+m.mode()+")"
	var out hopOut
	if audit != nil {
		out = append(out, func() { audit(auditType, id, full) })
	}
	if event != "" && publish != nil {
		payload, _ := json.Marshal(map[string]any{"interface_id": id, "state": l.state, "reason": l.reason, "mode": m.mode()})
		ev := hemb.Event{Type: hemb.EventType(event), Timestamp: m.now(), Payload: payload}
		out = append(out, func() { publish(ev) })
	}
	return out
}
