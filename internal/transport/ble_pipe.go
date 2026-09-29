package transport

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/rs/zerolog/log"
	"go.bug.st/serial"
)

// The MeshSat node's Iridium modem pipe (meshsat-esp32/docs/IRIDIUM-BLE.md,
// contract v2): a binary-safe serial line to the node's RockBLOCK 9603, on
// the same Bluetooth LE link as its Meshtastic service. Subscribing to TX
// takes the modem, STATUS says who holds it, STATS is the node's account of
// its modem. The SBD transport speaks AT through a pipePort exactly as over a
// cable. Ported from MeshSat Android v2.19.4 (ble/IridiumPipeStreams.kt,
// ble/IridiumBlePipe.kt, the claim loop of service/GatewayService.kt).
// [MESHSAT-1391]
const (
	pipeRXUUID     = "b9e2d4ba-f386-4728-b77a-7df7121db7a9" // Bridge -> modem, write with response
	pipeTXUUID     = "469354dc-4c89-41ed-b939-d707c7a11f49" // modem -> Bridge, notify; subscribing takes the modem
	pipeStatusUUID = "69a4064d-78b9-46e5-a30a-1862e553245a" // version, owner[, flags, csq]
	pipeStatsUUID  = "9c22cf07-2256-4fc2-b6ee-ab0ceb12198d" // 52 bytes of node health (v2)

	pipeStatusV1     = 1
	pipeStatusV2     = 2
	pipeStatsVersion = 2
	pipeStatsBytes   = 52

	// Writes to RX go out acknowledged, in chunks of the link's ATT payload
	// (MTU - 3), at most what the node notifies in one value and never less
	// than the smallest payload any link allows (MTU 23 - 3).
	pipeMaxChunk     = 244
	pipeMinChunk     = 20
	pipeChunkTimeout = 5 * time.Second
	// pipeInputBytes is what the pipe keeps of the modem's output for the
	// reader. The excess is dropped and counted: a stalled reader must never
	// stall the Bluetooth side.
	pipeInputBytes = 8192
	// pipeBrokenWrites failed writes in a row mark the link broken. A wedged
	// pipe answers every write with a failure and stays attached; on Android
	// nothing noticed for thirteen minutes (MESHSAT-1270).
	pipeBrokenWrites = 3
	// pipeStatsFresh: a STATS value older than this is read again on request.
	// The node notifies only a change of substance, never the moving ages.
	pipeStatsFresh = 10 * time.Second
	// pipeRecoveryCooldown spaces the reconnects a broken pipe causes.
	pipeRecoveryCooldown = 60 * time.Second
)

// The claim: STATUS must say owner 01 within pipeClaimWait of the TX
// subscription. While it does not, the claim is asked again, soon after the
// link comes up (the first one is often refused while the link settles) and
// then every pipeClaimRetry. Variables so tests can shorten them.
var (
	pipeClaimWait       = 8 * time.Second
	pipeClaimFirstRetry = 3 * time.Second
	pipeClaimRetry      = 15 * time.Second
	// pipeFlightCap bounds how long a port is refused for a session in
	// flight: the node clears the flag with the session's answer, or at its
	// own cap (95 s) once the client is gone, but never while a client stays
	// subscribed and the modem stays silent.
	pipeFlightCap = 100 * time.Second
)

// Who holds the node's modem, as STATUS says and the API answers. The
// contract's "phone" is whichever client holds it: here, this Bridge. An
// empty owner is unknown (no pipe, no STATUS read yet, a version this Bridge
// does not know).
const (
	PipeOwnerNone  = "none"
	PipeOwnerPhone = "phone"
	PipeOwnerNode  = "node"
)

var (
	// errPipeNotOwned: the node does not give this Bridge its modem now, so it
	// would discard the bytes. A handover, never a fault of the link: telling
	// the two apart is what keeps a handover from counting towards a broken
	// link (MESHSAT-1270).
	errPipeNotOwned = errors.New("the node has not given this Bridge its modem")
	// errPipeWriteFailed: a write did not get through to the node. Counted
	// towards a broken link.
	errPipeWriteFailed = errors.New("a write to the node's modem pipe failed")
	// errPipeLinkLost: the link behind the port is gone, or the modem was
	// given back, or the pipe counts as broken: nothing more goes through
	// this port, and a command waiting on it ends at once.
	errPipeLinkLost = errors.New("the link to the node's modem is gone")
	// errPipeSessionInFlight: the node gives this Bridge its modem, but a
	// satellite session still runs on it: one a dropped link left behind,
	// which the node holds the modem for until its answer (up to 95 s). No
	// port is handed out meanwhile: a set-up written into the session
	// swallowed its +SBDIX answer.
	errPipeSessionInFlight = errors.New("the node's modem is still in a satellite session")

	// ErrNoPipeStats: no connected node reports its modem's health (no
	// MeshSat node, or firmware older than contract v2).
	ErrNoPipeStats = errors.New("no connected MeshSat node reports its modem's health")
)

// PipeFlags is the flags byte of STATUS v2 and STATS.
type PipeFlags struct {
	SessionInFlight bool `json:"session_in_flight"`
	MessageWaiting  bool `json:"message_waiting"`
	ModemAnswers    bool `json:"modem_answers"`
	BufferCongested bool `json:"buffer_congested"`
}

func pipeFlagsOf(b byte) PipeFlags {
	return PipeFlags{
		SessionInFlight: b&0x01 != 0,
		MessageWaiting:  b&0x02 != 0,
		ModemAnswers:    b&0x04 != 0,
		BufferCongested: b&0x08 != 0,
	}
}

func pipeOwnerOf(b byte) string {
	switch b {
	case 0:
		return PipeOwnerNone
	case 1:
		return PipeOwnerPhone
	case 2:
		return PipeOwnerNode
	}
	return ""
}

// pipeCSQ is the signal byte: 0-5, nil for the node's "never read" (0xFF).
func pipeCSQ(b byte) *int {
	if b > 5 {
		return nil
	}
	n := int(b)
	return &n
}

// pipeStatus is one STATUS value; flags and csq are nil on a version 1
// (2-byte) value.
type pipeStatus struct {
	owner string
	flags *PipeFlags
	csq   *int
}

// parsePipeStatus decodes STATUS of contract v1 (2 bytes) or v2 (4 bytes),
// reading only what is there. Nil for a value too short or of a version this
// Bridge does not know.
func parsePipeStatus(v []byte) *pipeStatus {
	if len(v) < 2 || (v[0] != pipeStatusV1 && v[0] != pipeStatusV2) {
		return nil
	}
	st := &pipeStatus{owner: pipeOwnerOf(v[1])}
	if len(v) >= 4 {
		f := pipeFlagsOf(v[2])
		st.flags, st.csq = &f, pipeCSQ(v[3])
	}
	return st
}

// PipeStats is contract v2's STATS value, the node's own account of its
// modem whoever holds it. Nil fields are the node's "unknown" markers. The
// signal is information for a screen, never a reason to hold a send.
type PipeStats struct {
	Owner string    `json:"owner"`
	Flags PipeFlags `json:"flags"`
	// CSQ is the signal 0-5 as the modem last reported it; nil when never read.
	CSQ     *int    `json:"csq"`
	CSQAgeS *uint32 `json:"csq_age_s"`
	// Sessions are the satellite sessions since the node booted, by any owner.
	Sessions        uint32  `json:"sessions"`
	LastMOStatus    *int    `json:"last_mo_status"`
	LastMOMSN       int     `json:"last_momsn"`
	LastMTStatus    *int    `json:"last_mt_status"`
	LastMTQueued    int     `json:"last_mt_queued"`
	LastSessionAgeS *uint32 `json:"last_session_age_s"`
	UptimeS         uint32  `json:"uptime_s"`
	// WatchdogReboots are the node's Bluetooth watchdog reboots, lifetime.
	WatchdogReboots uint32 `json:"watchdog_reboots"`
	// ClientBytesDropped are bytes a client wrote faster than the modem took them.
	ClientBytesDropped uint32 `json:"client_bytes_dropped"`
	NodeSessions       uint32 `json:"node_sessions"`
	NodeSent           uint32 `json:"node_sent"`
	NodeReceived       uint32 `json:"node_received"`
	DaySessionsUsed    int    `json:"day_sessions_used"`
	DaySessionsCap     int    `json:"day_sessions_cap"`

	// flightStuck: the account is the node link's for the SBD transport
	// (bleLink.nodeStats), and Flags.SessionInFlight was cleared there
	// because the node has said a session is in flight for longer than one
	// runs, none started since (blePipe.flightStuckLocked): the session
	// never answered. Never on the wire.
	flightStuck bool
}

// PipeStatsReading is a STATS value and when it was read.
type PipeStatsReading struct {
	PipeStats
	ReadAt time.Time `json:"read_at"`
}

// parsePipeStats decodes the 52 little-endian bytes of STATS by offset. Nil
// when shorter or of another version; a longer value still parses.
func parsePipeStats(v []byte) *PipeStats {
	if len(v) < pipeStatsBytes || v[0] != pipeStatsVersion {
		return nil
	}
	u16 := func(o int) uint16 { return binary.LittleEndian.Uint16(v[o:]) }
	u32 := func(o int) uint32 { return binary.LittleEndian.Uint32(v[o:]) }
	age := func(o int) *uint32 {
		n := u32(o)
		if n == 0xFFFFFFFF {
			return nil
		}
		return &n
	}
	status := func(o int) *int {
		n := int(int16(u16(o)))
		if n < 0 {
			return nil
		}
		return &n
	}
	return &PipeStats{
		Owner:              pipeOwnerOf(v[1]),
		Flags:              pipeFlagsOf(v[2]),
		CSQ:                pipeCSQ(v[3]),
		CSQAgeS:            age(4),
		Sessions:           u32(8),
		LastMOStatus:       status(12),
		LastMOMSN:          int(u16(14)),
		LastMTStatus:       status(16),
		LastMTQueued:       int(u16(18)),
		LastSessionAgeS:    age(20),
		UptimeS:            u32(24),
		WatchdogReboots:    u32(28),
		ClientBytesDropped: u32(32),
		NodeSessions:       u32(36),
		NodeSent:           u32(40),
		NodeReceived:       u32(44),
		DaySessionsUsed:    int(v[48]),
		DaySessionsCap:     int(v[49]),
	}
}

// lineWatcher spots one unsolicited line in the modem's output: SBDRING,
// which the 9603 sends in band when a message waits at the gateway
// (AT+SBDMTA=1). The node has no ring-indicator wire, so this is the only
// ring alert there is. The match survives a line split across notifications
// and sees bytes that a command's drain discards later.
type lineWatcher struct {
	token   []byte
	matched int
}

func newLineWatcher(line string) *lineWatcher {
	return &lineWatcher{token: []byte(line + "\r")}
}

// feed reports how many times the line completed in data.
func (w *lineWatcher) feed(data []byte) int {
	hits := 0
	for _, b := range data {
		switch {
		case b == w.token[w.matched]:
			w.matched++
		case b == w.token[0]:
			w.matched = 1
		default:
			w.matched = 0
		}
		if w.matched == len(w.token) {
			w.matched = 0
			hits++
		}
	}
	return hits
}

// pipeValue is one value the node notified on a pipe characteristic.
type pipeValue struct {
	uuid  string
	value []byte
}

// pipeGATT is the node's pipe service as the pipe sees it: BlueZ in the
// field, an in-memory node in tests.
type pipeGATT interface {
	// has reports whether the node serves the characteristic.
	has(uuid string) bool
	startNotify(ctx context.Context, uuid string) error
	stopNotify(uuid string)
	read(ctx context.Context, uuid string) ([]byte, error)
	// write writes with response: it returns once the node acknowledged.
	write(ctx context.Context, uuid string, data []byte) error
	// mtu is the link's ATT MTU, 0 when unknown.
	mtu() int
	// values delivers the values notified on TX, STATUS and STATS.
	values() <-chan pipeValue
	// lost is closed when the LE link is gone.
	lost() <-chan struct{}
}

// pipeHealth counts failed writes to the pipe across its links: three in a
// row mark it broken, and the first write that lands clears that. It outlives
// a reconnect as the driver's count does on Android, so the screens keep
// saying the modem is out of reach until bytes go through again.
type pipeHealth struct {
	mu       sync.Mutex
	failures int
	broken   bool
	since    time.Time
}

// failed notes a write that did not get through; true when it completes a
// run of pipeBrokenWrites.
func (h *pipeHealth) failed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failures++
	if h.failures < pipeBrokenWrites {
		return false
	}
	h.failures = 0
	if !h.broken {
		h.broken, h.since = true, time.Now()
	}
	return true
}

// landed notes a write that got through: whatever was wrong is over.
func (h *pipeHealth) landed() {
	h.mu.Lock()
	h.failures, h.broken, h.since = 0, false, time.Time{}
	h.mu.Unlock()
}

// state reports whether the link counts as broken, and since when.
func (h *pipeHealth) state() (bool, time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.broken, h.since
}

func (h *pipeHealth) reset() { h.landed() }

// blePipe is the node's modem pipe on one link. It follows STATUS (who holds
// the modem), keeps what the modem says for the reader, and hands out a
// pipePort while this Bridge holds the modem.
type blePipe struct {
	g         pipeGATT
	health    *pipeHealth
	hasStatus bool
	hasStats  bool
	// onOwner hears of every change of owner, and of the modem becoming
	// usable again once a session the node held it for ended; onFault of a
	// link that takes no writes. Both run on goroutines of their own:
	// onOwner in order (queueOwnerLocked), never on the notification reader's,
	// since it takes the node link's lock, which is held across D-Bus calls
	// of seconds, and a reader stalled on it backed BlueZ's signals up until
	// they were dropped, the modem's bytes with them.
	onOwner func(owner string)
	onFault func()

	// writeMu keeps the chunks of two writes from interleaving.
	writeMu sync.Mutex

	mu sync.Mutex
	// changed is closed and replaced on every change a reader or a claim
	// waits for.
	changed chan struct{}
	owner   string
	status  *pipeStatus
	// statusSeq counts the STATUS values applied: a read that a notification
	// overtook on its way is not applied over it (refreshStatus).
	statusSeq uint64
	// inFlight is the node's session-in-flight flag as it last said it
	// (STATUS flags, or the STATS flags of a node whose STATUS carries none),
	// since flightSince; flightKnown once it has said. refusedInFlight: a
	// port was refused for it, so onOwner hears "phone" again once the
	// session ended (noteFlightLocked).
	flightKnown     bool
	inFlight        bool
	flightSince     time.Time
	refusedInFlight bool
	stats           *PipeStats
	statsAt         time.Time
	in              []byte
	dropped         uint64
	droppedLog      time.Time
	closed          bool
	// lease is bumped whenever the modem is given back, the link goes or the
	// pipe counts as broken: a port of an older lease is dead.
	lease uint64
	// subscribed: TX notifications are on (this Bridge asked for the modem).
	subscribed  bool
	claimCancel context.CancelFunc
	ring        *lineWatcher
	onRing      func()
	ringLease   uint64

	// The owners onOwner is still to hear, oldest first (queueOwnerLocked).
	cbMu      sync.Mutex
	cbQueue   []string
	cbRunning bool
}

func newBLEPipe(g pipeGATT, health *pipeHealth, onOwner func(string), onFault func()) *blePipe {
	if health == nil {
		health = &pipeHealth{}
	}
	return &blePipe{
		g:         g,
		health:    health,
		hasStatus: g.has(pipeStatusUUID),
		hasStats:  g.has(pipeStatsUUID),
		onOwner:   onOwner,
		onFault:   onFault,
		changed:   make(chan struct{}),
		ring:      newLineWatcher("SBDRING"),
	}
}

// signalLocked wakes everyone waiting on the pipe. Caller holds p.mu.
func (p *blePipe) signalLocked() {
	close(p.changed)
	p.changed = make(chan struct{})
}

// run takes the node's notifications until the link is gone.
func (p *blePipe) run() {
	for {
		select {
		case v := <-p.g.values():
			p.onValue(v)
		case <-p.g.lost():
			p.close()
			return
		}
	}
}

func (p *blePipe) onValue(v pipeValue) {
	switch v.uuid {
	case pipeTXUUID:
		p.mu.Lock()
		keep := v.value
		if room := pipeInputBytes - len(p.in); len(keep) > room {
			p.dropped += uint64(len(keep) - room)
			keep = keep[:room]
			if time.Since(p.droppedLog) > time.Minute {
				p.droppedLog = time.Now()
				log.Warn().Uint64("dropped", p.dropped).Msg("iridium pipe: the reader falls behind the modem; bytes dropped")
			}
		}
		p.in = append(p.in, keep...)
		hits := p.ring.feed(v.value)
		ring := p.onRing
		if p.ringLease != p.lease {
			ring = nil
		}
		p.signalLocked()
		p.mu.Unlock()
		for ; hits > 0 && ring != nil; hits-- {
			ring()
		}
	case pipeStatusUUID:
		p.applyStatus(v.value, nil)
	case pipeStatsUUID:
		if st := parsePipeStats(v.value); st != nil {
			p.applyStats(st)
		} else {
			log.Warn().Int("bytes", len(v.value)).Msg("iridium pipe: STATS value not understood")
		}
	}
}

// applyStatus applies a STATUS value: notified, or read (readSeq: statusSeq
// when the read began), which is dropped when a notification was applied
// while it was on its way, as that one is at least as new.
func (p *blePipe) applyStatus(value []byte, readSeq *uint64) {
	st := parsePipeStatus(value)
	owner := ""
	if st != nil {
		owner = st.owner
	}
	p.setOwnerAt(owner, st, readSeq)
}

// applyStats records a STATS value, and for a node whose STATUS carries no
// flags (contract v1 STATUS, the firmware's default build) its
// session-in-flight flag.
func (p *blePipe) applyStats(st *PipeStats) {
	p.mu.Lock()
	if prev := p.stats; prev != nil && st.Sessions != prev.Sessions && p.flightKnown && p.inFlight {
		// A session started under a flag that stayed set (the one before
		// it never answered): its clock starts again.
		p.flightSince = time.Now()
	}
	p.stats, p.statsAt = st, time.Now()
	start := false
	if !p.closed && (p.status == nil || p.status.flags == nil) && p.noteFlightLocked(st.Flags.SessionInFlight) {
		start = p.queueOwnerLocked(p.owner)
	}
	p.mu.Unlock()
	p.startOwnerCallbacks(start)
}

// setOwner records the owner and tells onOwner when it changed. Bytes kept
// from the modem are dropped when the modem changes hands.
func (p *blePipe) setOwner(owner string, st *pipeStatus) { p.setOwnerAt(owner, st, nil) }

func (p *blePipe) setOwnerAt(owner string, st *pipeStatus, readSeq *uint64) {
	p.mu.Lock()
	if p.closed || (readSeq != nil && *readSeq != p.statusSeq) {
		p.mu.Unlock()
		return
	}
	p.statusSeq++
	prev := p.owner
	p.owner, p.status = owner, st
	if prev == PipeOwnerPhone && owner != PipeOwnerPhone {
		p.in = p.in[:0]
	}
	if owner != PipeOwnerPhone {
		// The next handover is told as a change of owner anyway.
		p.refusedInFlight = false
	}
	again := false
	if st != nil && st.flags != nil {
		again = p.noteFlightLocked(st.flags.SessionInFlight)
	}
	start := false
	if prev != owner || again {
		start = p.queueOwnerLocked(owner)
	}
	p.signalLocked()
	p.mu.Unlock()
	p.startOwnerCallbacks(start)
}

// noteFlightLocked records the node's session-in-flight flag, and reports
// whether onOwner is to hear "phone" again: a port was refused while the
// flag was set (the SBD transport waits for one), and the session has
// ended. Caller holds p.mu.
func (p *blePipe) noteFlightLocked(inFlight bool) bool {
	if inFlight && !(p.flightKnown && p.inFlight) {
		p.flightSince = time.Now()
	}
	p.flightKnown, p.inFlight = true, inFlight
	if inFlight || !p.refusedInFlight || p.owner != PipeOwnerPhone {
		return false
	}
	p.refusedInFlight = false
	return true
}

// flightLocked reports whether the node says a satellite session is in
// flight on its modem, within pipeFlightCap of first saying so. Caller
// holds p.mu.
func (p *blePipe) flightLocked() bool {
	return p.flightKnown && p.inFlight && time.Since(p.flightSince) < pipeFlightCap
}

// flightStuckLocked reports whether the node has said a session is in
// flight for pipeFlightCap and more, no session started since: a session
// the modem never answered, whose flag the node clears only once its client
// lets go and its own cap (95 s) has passed (IridiumPipe.cpp updateOwner).
// Caller holds p.mu.
func (p *blePipe) flightStuckLocked() bool {
	return p.flightKnown && p.inFlight && time.Since(p.flightSince) >= pipeFlightCap
}

func (p *blePipe) flightStuck() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.flightStuckLocked()
}

// restartFlightClock gives a flag that stayed set another pipeFlightCap,
// once the modem was given back to have the node clear it.
func (p *blePipe) restartFlightClock() {
	p.mu.Lock()
	if p.flightKnown && p.inFlight {
		p.flightSince = time.Now()
	}
	p.mu.Unlock()
}

// queueOwnerLocked queues an owner for onOwner in the order the pipe applied
// the changes (the caller holds p.mu, so two changes applied on different
// goroutines are queued as they were applied), and reports whether a runner
// is to be started once p.mu is let go (startOwnerCallbacks).
func (p *blePipe) queueOwnerLocked(owner string) bool {
	if p.onOwner == nil {
		return false
	}
	p.cbMu.Lock()
	defer p.cbMu.Unlock()
	p.cbQueue = append(p.cbQueue, owner)
	if p.cbRunning {
		return false
	}
	p.cbRunning = true
	return true
}

// startOwnerCallbacks starts the goroutine that hands the queued owners to
// onOwner, in order, never on the notification reader's goroutine.
func (p *blePipe) startOwnerCallbacks(start bool) {
	if start {
		go p.runOwnerCallbacks()
	}
}

func (p *blePipe) runOwnerCallbacks() {
	for {
		p.cbMu.Lock()
		if len(p.cbQueue) == 0 {
			p.cbRunning = false
			p.cbMu.Unlock()
			return
		}
		owner := p.cbQueue[0]
		p.cbQueue = p.cbQueue[1:]
		p.cbMu.Unlock()
		p.onOwner(owner)
	}
}

// Owner is who holds the modem by the last STATUS.
func (p *blePipe) Owner() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.owner
}

// waitOwner waits until the owner is want, the pipe closes or ctx ends.
func (p *blePipe) waitOwner(ctx context.Context, want string) bool {
	for {
		p.mu.Lock()
		if p.owner == want {
			p.mu.Unlock()
			return true
		}
		if p.closed {
			p.mu.Unlock()
			return false
		}
		ch := p.changed
		p.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return false
		}
	}
}

// subscribe enables one characteristic's notifications, with one retry: the
// first CCCD write can fail while the link is still being encrypted.
func (p *blePipe) subscribe(ctx context.Context, uuid string) bool {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if err = p.g.startNotify(ctx, uuid); err == nil {
			return true
		}
	}
	log.Warn().Err(err).Str("characteristic", uuid).Msg("iridium pipe: notifications could not be enabled")
	return false
}

// claim takes the modem: STATUS and TX notifications on, STATUS read back
// (its notification can be lost), then a wait of up to wait for owner 01.
// Owner 02 is the node finishing its own work, which can take 90 s: wait, never
// a failure. A node without STATUS counts as owned once TX is subscribed.
// STATS is read too, for the session-in-flight flag of a node whose STATUS
// carries none: owner 01 with a session in flight is the node holding the
// modem for a session a dropped link left behind, and port waits for it.
func (p *blePipe) claim(ctx context.Context, wait time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return false
	}
	p.claimCancel = cancel
	p.mu.Unlock()
	if p.hasStatus && !p.subscribe(ctx, pipeStatusUUID) {
		log.Warn().Msg("iridium pipe: reading STATUS instead of being told")
	}
	if !p.subscribe(ctx, pipeTXUUID) {
		if ctx.Err() != nil {
			// Released, or gone, while the subscription was on its way: its
			// CCCD write may have landed all the same (the D-Bus call ends,
			// bluetoothd goes on) and given this Bridge the modem with
			// nobody to use it. Undone.
			p.g.stopNotify(pipeTXUUID)
		}
		return false
	}
	p.mu.Lock()
	if ctx.Err() != nil || p.closed {
		// Released (or gone) while this claim ran: undo the subscription,
		// which would otherwise take the modem after all.
		p.mu.Unlock()
		p.g.stopNotify(pipeTXUUID)
		return false
	}
	p.subscribed = true
	p.mu.Unlock()
	if p.hasStats {
		if err := p.readStats(ctx); err != nil {
			log.Debug().Err(err).Msg("iridium pipe: STATS read failed")
		}
	}
	if !p.hasStatus {
		p.setOwner(PipeOwnerPhone, nil)
	} else {
		p.refreshStatus(ctx)
	}
	if p.waitOwner(ctx, PipeOwnerPhone) {
		log.Info().Msg("iridium pipe: this Bridge holds the node's modem")
		return true
	}
	if owner := p.Owner(); owner == PipeOwnerNode {
		log.Info().Msg("iridium pipe: the node is using its modem; waiting for its release")
	} else {
		log.Info().Str("owner", owner).Msg("iridium pipe: no handover from the node yet")
	}
	return false
}

// release gives the modem back to the node (TX notifications off). The port
// of that ownership is dead from here, and a claim still waiting ends.
func (p *blePipe) release() {
	p.mu.Lock()
	if p.claimCancel != nil {
		p.claimCancel()
	}
	wasSubscribed, owner := p.subscribed, p.owner
	p.subscribed = false
	p.lease++
	p.signalLocked()
	p.mu.Unlock()
	p.g.stopNotify(pipeTXUUID)
	if wasSubscribed {
		log.Info().Msg("iridium pipe: the modem is given back to the node")
	}
	// STATUS is notified as 02 00 (or 02 02 once the node takes it); until
	// then this Bridge knows it does not hold the modem.
	if owner == PipeOwnerPhone || !p.hasStatus {
		p.setOwner(PipeOwnerNone, nil)
	}
}

// cancelClaim ends a claim on its way, without giving anything back.
func (p *blePipe) cancelClaim() {
	p.mu.Lock()
	if p.claimCancel != nil {
		p.claimCancel()
	}
	p.mu.Unlock()
}

// holds reports whether this Bridge holds the modem or has asked for it.
func (p *blePipe) holds() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.subscribed || p.owner == PipeOwnerPhone
}

// keepClaim is one round of the claim loop: nothing while this Bridge holds
// the modem, STATUS read again while the node does or holds it for a
// session in flight (a notification may have been lost), a claim otherwise.
func (p *blePipe) keepClaim(ctx context.Context) {
	p.mu.Lock()
	owner, subscribed, flight := p.owner, p.subscribed, p.flightLocked()
	statusFlags := p.status != nil && p.status.flags != nil
	p.mu.Unlock()
	switch {
	case owner == PipeOwnerPhone && subscribed:
		if flight {
			if p.hasStats && !statusFlags {
				if err := p.readStats(ctx); err != nil {
					log.Debug().Err(err).Msg("iridium pipe: STATS read failed")
				}
			}
			p.refreshStatus(ctx)
		}
	case owner == PipeOwnerNode && subscribed:
		p.refreshStatus(ctx)
	default:
		p.claim(ctx, pipeClaimWait)
	}
}

// refreshStatus reads STATUS again. A notification applied while the read
// was on its way wins over it.
func (p *blePipe) refreshStatus(ctx context.Context) {
	if !p.hasStatus {
		return
	}
	p.mu.Lock()
	seq := p.statusSeq
	p.mu.Unlock()
	v, err := p.g.read(ctx, pipeStatusUUID)
	if err != nil {
		log.Debug().Err(err).Msg("iridium pipe: STATUS read failed")
		return
	}
	p.applyStatus(v, &seq)
}

// watchStats subscribes to STATS and reads it once: the node's health is worth
// showing whoever holds the modem. A node without STATS is left alone.
func (p *blePipe) watchStats(ctx context.Context) {
	if !p.hasStats {
		return
	}
	if !p.subscribe(ctx, pipeStatsUUID) {
		log.Warn().Msg("iridium pipe: reading STATS instead of being told")
	}
	if err := p.readStats(ctx); err != nil {
		log.Debug().Err(err).Msg("iridium pipe: STATS read failed")
	}
}

func (p *blePipe) readStats(ctx context.Context) error {
	v, err := p.g.read(ctx, pipeStatsUUID)
	if err != nil {
		return err
	}
	st := parsePipeStats(v)
	if st == nil {
		return fmt.Errorf("STATS value of %d bytes not understood", len(v))
	}
	p.applyStats(st)
	return nil
}

// freshStats reads STATS now and answers it: the node's account of its
// sessions, which settles one whose answer never reached this Bridge.
func (p *blePipe) freshStats(ctx context.Context) (*PipeStats, error) {
	if !p.hasStats {
		return nil, ErrNoPipeStats
	}
	if err := p.readStats(ctx); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	st := *p.stats
	return &st, nil
}

// statsReading is the node's STATS, read again when older than pipeStatsFresh.
// A failed re-read answers the last value with its own time.
func (p *blePipe) statsReading(ctx context.Context) (*PipeStatsReading, error) {
	if !p.hasStats {
		return nil, ErrNoPipeStats
	}
	p.mu.Lock()
	st, at := p.stats, p.statsAt
	p.mu.Unlock()
	if st == nil || time.Since(at) >= pipeStatsFresh {
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := p.readStats(rctx)
		cancel()
		if err != nil && st == nil {
			return nil, fmt.Errorf("the node did not report its modem's health: %w", err)
		}
		p.mu.Lock()
		st, at = p.stats, p.statsAt
		p.mu.Unlock()
	}
	return &PipeStatsReading{PipeStats: *st, ReadAt: at.UTC()}, nil
}

// port gives the SBD transport a serial port on the pipe while this Bridge
// holds the modem and no satellite session runs on it: after a link dropped
// mid-session the node holds the modem for the session's answer and says
// 01 at once, and a set-up written into the session swallowed that answer.
// Once the node says the session ended, onOwner hears "phone" again.
func (p *blePipe) port() (serial.Port, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, errPipeLinkLost
	}
	if p.owner != PipeOwnerPhone {
		return nil, errPipeNotOwned
	}
	if p.flightLocked() {
		p.refusedInFlight = true
		return nil, errPipeSessionInFlight
	}
	p.refusedInFlight = false
	// A new port starts clean: nothing pending is meant for it.
	p.in = p.in[:0]
	return &pipePort{p: p, lease: p.lease, timeout: 100 * time.Millisecond}, nil
}

// writeFailed counts a write that did not get through; the third in a row
// makes the link broken, kills the port and tells onFault.
func (p *blePipe) writeFailed() {
	select {
	case <-p.g.lost():
		return // the link went: not a wedged pipe
	default:
	}
	if !p.health.failed() {
		return
	}
	log.Warn().Int("writes", pipeBrokenWrites).Msg("iridium pipe: writes in a row did not get through; the link to the node's modem counts as broken")
	p.mu.Lock()
	p.lease++
	p.signalLocked()
	cb := p.onFault
	p.mu.Unlock()
	if cb != nil {
		go cb()
	}
}

// chunkSize is what one acknowledged write to RX carries.
func (p *blePipe) chunkSize() int {
	n := p.g.mtu() - 3
	if n > pipeMaxChunk {
		n = pipeMaxChunk
	}
	if n < pipeMinChunk {
		n = pipeMinChunk
	}
	return n
}

// close ends the pipe with its link: owner unknown, every port dead.
func (p *blePipe) close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.lease++
	prev := p.owner
	p.owner, p.status = "", nil
	p.in = p.in[:0]
	if p.claimCancel != nil {
		p.claimCancel()
	}
	start := false
	if prev != "" {
		start = p.queueOwnerLocked("")
	}
	p.signalLocked()
	p.mu.Unlock()
	p.startOwnerCallbacks(start)
}

// pipePort is the node's modem pipe as a serial port for the SBD transport's
// AT code: Read gives (0, nil) at its timeout as a serial port does, and an
// error at once when the link is gone; Write sends the bytes in acknowledged
// chunks and fails with a reason of its own when the node does not give this
// Bridge the modem.
type pipePort struct {
	p     *blePipe
	lease uint64
	dead  atomic.Bool

	mu      sync.Mutex
	timeout time.Duration
}

var _ serial.Port = (*pipePort)(nil)

// goneLocked: nothing more goes through this port. Caller holds p.mu.
func (pp *pipePort) goneLocked() bool {
	return pp.dead.Load() || pp.p.closed || pp.p.lease != pp.lease
}

func (pp *pipePort) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	pp.mu.Lock()
	timeout := pp.timeout
	pp.mu.Unlock()
	var expired <-chan time.Time
	if timeout >= 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		expired = timer.C
	}
	p := pp.p
	for {
		p.mu.Lock()
		if pp.goneLocked() {
			p.mu.Unlock()
			return 0, errPipeLinkLost
		}
		if len(p.in) > 0 {
			n := copy(b, p.in)
			p.in = p.in[:copy(p.in, p.in[n:])]
			p.mu.Unlock()
			return n, nil
		}
		changed := p.changed
		p.mu.Unlock()
		select {
		case <-changed:
		case <-expired:
			return 0, nil
		}
	}
}

func (pp *pipePort) Write(b []byte) (int, error) {
	p := pp.p
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	p.mu.Lock()
	gone, owner := pp.goneLocked(), p.owner
	p.mu.Unlock()
	if gone {
		return 0, errPipeLinkLost
	}
	if owner != PipeOwnerPhone {
		return 0, errPipeNotOwned
	}
	if len(b) == 0 {
		return 0, nil
	}
	size := p.chunkSize()
	for off := 0; off < len(b); {
		if off > 0 {
			// The modem may have been given back, or the link lost, since
			// the last chunk: the rest would be discarded by the node, or go
			// to whoever holds the modem now.
			p.mu.Lock()
			gone, owner := pp.goneLocked(), p.owner
			p.mu.Unlock()
			if gone {
				return off, errPipeLinkLost
			}
			if owner != PipeOwnerPhone {
				return off, errPipeNotOwned
			}
		}
		end := min(off+size, len(b))
		ctx, cancel := context.WithTimeout(context.Background(), pipeChunkTimeout)
		err := p.g.write(ctx, pipeRXUUID, b[off:end])
		cancel()
		if err != nil {
			p.writeFailed()
			return off, fmt.Errorf("%w: %v", errPipeWriteFailed, err)
		}
		off = end
	}
	p.health.landed()
	return len(b), nil
}

// abort kills the port: a read waiting on it ends at once.
func (pp *pipePort) abort() {
	pp.dead.Store(true)
	pp.p.mu.Lock()
	pp.p.signalLocked()
	pp.p.mu.Unlock()
}

// SetRingHandler has fn called for every SBDRING the modem sends while this
// port is the live one, whoever reads the bytes.
func (pp *pipePort) SetRingHandler(fn func()) {
	pp.p.mu.Lock()
	pp.p.onRing, pp.p.ringLease = fn, pp.lease
	pp.p.mu.Unlock()
}

func (pp *pipePort) SetReadTimeout(t time.Duration) error {
	pp.mu.Lock()
	pp.timeout = t
	pp.mu.Unlock()
	return nil
}

func (pp *pipePort) ResetInputBuffer() error {
	pp.p.mu.Lock()
	if !pp.goneLocked() {
		pp.p.in = pp.p.in[:0]
	}
	pp.p.mu.Unlock()
	return nil
}

// Close ends this port; the modem stays this Bridge's until released.
func (pp *pipePort) Close() error {
	pp.abort()
	return nil
}

// The line has no modem-control wires and no settings: the node's UART to the
// RockBLOCK is 19200 8N1, and a write returns once acknowledged.
func (pp *pipePort) SetMode(*serial.Mode) error { return nil }
func (pp *pipePort) Drain() error               { return nil }
func (pp *pipePort) ResetOutputBuffer() error   { return nil }
func (pp *pipePort) SetDTR(bool) error          { return nil }
func (pp *pipePort) SetRTS(bool) error          { return nil }
func (pp *pipePort) Break(time.Duration) error  { return nil }
func (pp *pipePort) GetModemStatusBits() (*serial.ModemStatusBits, error) {
	return &serial.ModemStatusBits{}, nil
}

// bluezPipe is pipeGATT over BlueZ: the pipe's characteristics under the
// node's device object, their Value changes as notifications.
type bluezPipe struct {
	bus    *bluezBus
	chars  map[string]dbus.ObjectPath
	vals   chan pipeValue
	lostCh <-chan struct{}
	mtuVal int
}

// openPipe finds the node's modem pipe on a connected device and starts
// following its notifications until lost closes. Nil with an error when the
// node has none, or one without RX or TX.
func (b *bluezBus) openPipe(device dbus.ObjectPath, lost <-chan struct{}) (*bluezPipe, error) {
	chars, found, err := b.characteristics(device, meshsatPipeServiceUUID, pipeRXUUID, pipeTXUUID, pipeStatusUUID, pipeStatsUUID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("the node has no modem pipe")
	}
	if chars[pipeRXUUID] == "" || chars[pipeTXUUID] == "" {
		return nil, errors.New("the node's modem pipe has no RX or TX")
	}
	g := &bluezPipe{bus: b, chars: chars, vals: make(chan pipeValue, 256), lostCh: lost}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if v, err := b.getProp(ctx, chars[pipeRXUUID], bluezGattChrIf, "MTU"); err == nil {
		if n, ok := v.Value().(uint16); ok {
			g.mtuVal = int(n)
		}
	}
	cancel()
	// Watched before anything is subscribed, so no value is missed.
	for _, uuid := range []string{pipeTXUUID, pipeStatusUUID, pipeStatsUUID} {
		path, ok := chars[uuid]
		if !ok {
			continue
		}
		ch, stop := b.watch(path)
		go g.forward(uuid, ch, stop)
	}
	return g, nil
}

func (g *bluezPipe) forward(uuid string, ch <-chan propsChange, stop func()) {
	defer stop()
	for {
		select {
		case c := <-ch:
			v, ok := c.Changed["Value"]
			if !ok {
				continue
			}
			value, ok := v.Value().([]byte)
			if !ok {
				continue
			}
			select {
			case g.vals <- pipeValue{uuid: uuid, value: value}:
			case <-g.lostCh:
				return
			}
		case <-g.lostCh:
			return
		}
	}
}

func (g *bluezPipe) has(uuid string) bool { return g.chars[uuid] != "" }

func (g *bluezPipe) path(uuid string) (dbus.ObjectPath, error) {
	path := g.chars[uuid]
	if path == "" {
		return "", fmt.Errorf("the node's modem pipe has no characteristic %s", uuid)
	}
	return path, nil
}

func (g *bluezPipe) startNotify(ctx context.Context, uuid string) error {
	path, err := g.path(uuid)
	if err != nil {
		return err
	}
	return g.bus.startNotify(ctx, path)
}

func (g *bluezPipe) stopNotify(uuid string) {
	if path, err := g.path(uuid); err == nil {
		g.bus.stopNotify(path)
	}
}

func (g *bluezPipe) read(ctx context.Context, uuid string) ([]byte, error) {
	path, err := g.path(uuid)
	if err != nil {
		return nil, err
	}
	return g.bus.readValue(ctx, path)
}

func (g *bluezPipe) write(ctx context.Context, uuid string, data []byte) error {
	path, err := g.path(uuid)
	if err != nil {
		return err
	}
	return g.bus.writeValue(ctx, path, data)
}

func (g *bluezPipe) mtu() int                 { return g.mtuVal }
func (g *bluezPipe) values() <-chan pipeValue { return g.vals }
func (g *bluezPipe) lost() <-chan struct{}    { return g.lostCh }
