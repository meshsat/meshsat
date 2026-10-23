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
//     packet), or a delivery failure to one destination. Failures to the same
//     destination are one sign.
//   - A suspect link is excluded when, inside TriggerWindow, it holds two
//     distinct signs, no positive sign came after the latest of them, and it
//     has been suspect for DropDwell. Otherwise it goes back to admitted once
//     its signs age out or a positive sign arrives.
//   - An excluded link starts recovering on a positive sign (a frame received
//     on it, a peer's acknowledgement), never on elapsed time alone, and is
//     admitted after ReadmitDwell without a new negative sign; one sends it
//     back to excluded.
//   - The floor: never fewer than MinSet usable links (of those not down),
//     and never the last free one. An exclusion that would break it leaves
//     the link retained (suspect, still tried); a floor broken by a link going
//     down re-admits the least-bad excluded link, by measured evidence.
//   - A deaf receiver or a missing device (the receive checkers) is down: out
//     of use whatever the floor says, as the slice (a) gate had it.

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
	ReadmitDwell  time.Duration // recovering without a negative sign before admitted
	MinSet        int           // never fewer usable links than this
	AlarmEvery    time.Duration // the alarm repeats at most this often per link
	RefreshEvery  time.Duration // how often the list of links is read again
	TickEvery     time.Duration // how often time-driven transitions are checked
}

// DefaultHopSetConfig is the design's proposal (owner to confirm the floor
// of two): seconds to drop, minutes to re-admit.
func DefaultHopSetConfig() HopSetConfig {
	return HopSetConfig{
		TriggerWindow: 120 * time.Second,
		DropDwell:     10 * time.Second,
		ReadmitDwell:  5 * time.Minute,
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
	Down         bool       `json:"down"`
	Retained     bool       `json:"retained"`
	Since        time.Time  `json:"since"`
	Reason       string     `json:"reason,omitempty"`
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
	down            bool
	retained        bool
	reason          string
	negatives       []HopSign
	lastPositive    time.Time
	lastPositiveSeq uint64
	sinceSeq        uint64 // the arrival order when the state was entered
	lastAlarm       time.Time
}

// distinct counts the link's negative signs by (source, key).
func (l *hopLink) distinct() int {
	seen := map[string]bool{}
	for _, s := range l.negatives {
		seen[s.Source+"\x00"+s.Key] = true
	}
	return len(seen)
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
		if f.paid || f.down || f.state != HopExcluded {
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

// RouteExcluded implements RouteGate: a down link and an excluded one are
// not tried; suspect, retained and recovering links are. A link outside the
// hop set is gated by its deaf check only, as the slice (a) gate had it.
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
	case l.down:
		ex, why = true, "deaf"
	case l.state == HopExcluded:
		ex, why = true, "excluded: "+l.reason
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
		"readmit_dwell_s": m.cfg.ReadmitDwell.Seconds(), "min_set": m.cfg.MinSet,
	}, Links: []HopLinkStatus{}}
	for _, l := range m.sortedLocked() {
		ls := HopLinkStatus{InterfaceID: l.id, ChannelType: l.channelType, Paid: l.paid, State: l.state,
			Down: l.down, Retained: l.retained, Since: l.since, Reason: l.reason, Signs: append([]HopSign{}, l.negatives...)}
		if !l.lastPositive.IsZero() {
			t := l.lastPositive
			ls.LastPositive = &t
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

// evaluateLocked applies the rules at now and returns what to emit: the
// transitions first, then the floor, then one audit entry or event per net
// change, so a link the floor keeps never shows an exclusion it didn't have.
func (m *HopSetManager) evaluateLocked(now time.Time) hopOut {
	if m.refreshed.IsZero() || now.Sub(m.refreshed) >= m.cfg.RefreshEvery {
		m.refreshLocked(now)
	}
	links := m.sortedLocked()
	type before struct {
		state    HopState
		retained bool
	}
	prior := make(map[*hopLink]before, len(links))
	for _, l := range links {
		prior[l] = before{l.state, l.retained}
		kept := l.negatives[:0]
		for _, s := range l.negatives {
			// The spectrum sign is read afresh below, never kept.
			if s.Source != "spectrum" && now.Sub(s.At) <= m.cfg.TriggerWindow {
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
		l.down = m.receive != nil && m.receive.ReceiveDeaf(l.id)
	}

	for _, l := range links {
		latest := l.latestNegative()
		cleared := len(l.negatives) == 0 || l.lastPositiveSeq > latest
		switch l.state {
		case HopAdmitted:
			if !cleared {
				m.setLocked(l, HopSuspect, l.summary(), now)
			}
		case HopSuspect:
			if cleared {
				l.retained = false
				m.setLocked(l, HopAdmitted, "evidence cleared", now)
			} else if l.distinct() >= 2 && now.Sub(l.since) >= m.cfg.DropDwell {
				l.retained = false
				m.setLocked(l, HopExcluded, l.summary(), now)
			}
		case HopExcluded:
			if l.lastPositiveSeq > l.sinceSeq && l.lastPositiveSeq > latest {
				m.setLocked(l, HopRecovering, "a positive sign", now)
			}
		case HopRecovering:
			if latest > l.sinceSeq {
				m.setLocked(l, HopExcluded, l.summary(), now)
			} else if now.Sub(l.since) >= m.cfg.ReadmitDwell {
				m.setLocked(l, HopAdmitted, "re-admitted", now)
			}
		}
	}
	m.repairFloorLocked(now)

	var out hopOut
	for _, l := range links {
		was := prior[l]
		switch {
		case l.state == HopExcluded && was.state != HopExcluded:
			out = append(out, m.emitLocked("hopset_excluded", string(hemb.EventBearerDegraded), l, "excluded: "+l.reason)...)
		case l.state == HopRecovering && was.state != HopRecovering:
			out = append(out, m.emitLocked("hopset_recovering", "", l, "recovering: a positive sign")...)
		case l.state == HopAdmitted && was.state == HopRecovering:
			out = append(out, m.emitLocked("hopset_admitted", string(hemb.EventBearerRecovered), l, "re-admitted after "+m.cfg.ReadmitDwell.String()+" without a negative sign")...)
		case l.retained && !was.retained:
			out = append(out, m.emitLocked("hopset_retained", "", l, "retained by the floor: "+l.summary())...)
		}
	}
	return out
}

// countsLocked counts the links not down, the usable ones (not excluded),
// and the free ones of each.
func (m *HopSetManager) countsLocked() (available, usable, freeAvailable, freeUsable int) {
	for _, l := range m.links {
		if l.down {
			continue
		}
		available++
		if !l.paid {
			freeAvailable++
		}
		if l.state == HopExcluded {
			continue
		}
		usable++
		if !l.paid {
			freeUsable++
		}
	}
	return
}

// repairFloorLocked keeps the floor: while fewer than MinSet links (of
// those not down) are usable, or no free link is while one is available,
// the least-bad excluded link is kept in use, retained: the fewest
// distinct signs first, then the most recent positive sign, then the id.
// Never at random, never on elapsed time. One rule for both ways the floor
// breaks: an exclusion, and a link going down.
func (m *HopSetManager) repairFloorLocked(now time.Time) {
	for {
		available, usable, freeAvailable, freeUsable := m.countsLocked()
		needFree := freeAvailable > 0 && freeUsable == 0
		if usable >= min(m.cfg.MinSet, available) && !needFree {
			return
		}
		var cands []*hopLink
		for _, l := range m.sortedLocked() {
			if l.down || l.state != HopExcluded || (needFree && l.paid) {
				continue
			}
			cands = append(cands, l)
		}
		if len(cands) == 0 {
			return
		}
		sort.SliceStable(cands, func(i, j int) bool {
			a, b := cands[i], cands[j]
			if a.distinct() != b.distinct() {
				return a.distinct() < b.distinct()
			}
			return a.lastPositiveSeq > b.lastPositiveSeq
		})
		l := cands[0]
		l.retained = true
		m.setLocked(l, HopSuspect, l.summary(), now)
	}
}

func (m *HopSetManager) setLocked(l *hopLink, s HopState, reason string, now time.Time) {
	log.Debug().Str("link", l.id).Str("from", string(l.state)).Str("to", string(s)).Str("reason", reason).Msg("hopset")
	m.seq++
	l.state, l.reason, l.since, l.sinceSeq = s, reason, now, m.seq
}

// emitLocked prepares an audit entry and, when event is set, a HeMB event.
func (m *HopSetManager) emitLocked(auditType, event string, l *hopLink, detail string) hopOut {
	audit, publish := m.audit, m.publish
	mode := "observing"
	if m.gating {
		mode = "gating"
	}
	id, full := l.id, detail+" ("+mode+")"
	var out hopOut
	if audit != nil {
		out = append(out, func() { audit(auditType, id, full) })
	}
	if event != "" && publish != nil {
		payload, _ := json.Marshal(map[string]any{"interface_id": id, "state": l.state, "reason": l.reason,
			"retained": l.retained, "signs": append([]HopSign{}, l.negatives...), "mode": mode})
		ev := hemb.Event{Type: hemb.EventType(event), Timestamp: m.now(), Payload: payload}
		out = append(out, func() { publish(ev) })
	}
	return out
}
