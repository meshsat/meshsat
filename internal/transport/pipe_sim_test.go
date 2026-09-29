package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// simNode is a MeshSat node as its firmware runs the modem pipe
// (meshsat-firmware src/meshsat/IridiumPipe.cpp: updateOwner, the command
// and answer line readers, publishStatus and publishStats), outliving the
// Bluetooth links made to it, which are pipeGATTs (simLink):
//
//   - the modem is taken by a TX subscription when nobody owns it, and given
//     back after the release debounce once the link unsubscribes or goes;
//     a session in flight holds it for its answer, up to the session cap;
//   - an AT+SBDIX that reaches the modem counts a session and sets the
//     session-in-flight flag, the modem's +SBDIX answer (or ERROR) clears it
//     and becomes the last result, whoever owned the modem or none;
//   - the modem's output reaches the link only while it holds the modem;
//     bytes written without holding it are discarded;
//   - STATUS (v2, or v1 without the flags byte) is notified on a change of
//     owner or flags, STATS (52 bytes) on a change other than its ages;
//   - a write can land and fail (its acknowledgement lost), fail without
//     landing, or land and take the link with it.
//
// The node's clock (uptime) starts well past boot and can be moved on.
type simNode struct {
	t     *testing.T
	modem *nodeModem

	mu           sync.Mutex
	link         *simLink
	owner        byte // 0 none, 1 the client, 2 the node
	subscribed   bool
	lastUnsub    time.Time
	debounce     time.Duration
	sessionCap   time.Duration
	inFlight     bool
	sessionStart time.Time
	sessions     uint32
	nodeSessions uint32
	lastMO       int
	lastMOMSN    uint32
	lastMT       int
	lastMTQueued uint32
	lastSession  time.Time
	boot         time.Time
	skew         time.Duration // the node's clock runs this far ahead (advance)
	statusV1     bool
	cmd          []byte
	resp         []byte
	lastStatus   []byte
	lastStats    []byte
	events       []string
	ackLost      string // a write of exactly this lands, then fails
	notLanded    string // a write of exactly this fails, never landing
	dropAfter    string // a write of exactly this lands, and the link goes with it
	// mangleTX changes what reaches the link of the modem's output (the
	// node reads it intact off its UART); dropTX drops it altogether (a
	// notification lost on its way).
	mangleTX func([]byte) []byte
	dropTX   bool

	stop chan struct{}
}

func newSimNode(t *testing.T, m *nodeModem) *simNode {
	t.Helper()
	n := &simNode{
		t:          t,
		modem:      m,
		debounce:   50 * time.Millisecond,
		sessionCap: 5 * time.Second,
		lastMO:     -1,
		lastMT:     -1,
		boot:       time.Now().Add(-1000 * time.Second),
		stop:       make(chan struct{}),
	}
	m.set(func(m *nodeModem) { m.out = n.fromModem })
	go n.run()
	t.Cleanup(func() {
		close(n.stop)
		n.mu.Lock()
		l := n.link
		n.mu.Unlock()
		if l != nil {
			l.drop()
		}
	})
	return n
}

// run is the firmware's runOnce: the owner and the notifications follow the
// clock (the debounce, the session cap).
func (n *simNode) run() {
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-n.stop:
			return
		case <-tick.C:
			n.step()
		}
	}
}

func (n *simNode) set(fn func(n *simNode)) {
	n.mu.Lock()
	fn(n)
	n.mu.Unlock()
	n.step()
}

// clockLocked is the node's own clock (its millis): wall time, moved on by
// advance. Everything the node times goes by it. Caller holds n.mu.
func (n *simNode) clockLocked() time.Time { return time.Now().Add(n.skew) }

// advance moves the node's clock on by d: its uptime and the ages of what
// happened before grow by d, and what happens from now on is stamped d
// later.
func (n *simNode) advance(d time.Duration) {
	n.mu.Lock()
	n.skew += d
	n.mu.Unlock()
	n.step()
}

func (n *simNode) note(event string) {
	n.events = append(n.events, event)
}

// eventLog is what happened on the node, in order: "take", "sbdix",
// "answer", "release", "unsubscribe", "lost".
func (n *simNode) eventLog() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.events...)
}

func (n *simNode) ownerNow() byte {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.owner
}

// connect makes a new link to the node; a link still up goes first.
func (n *simNode) connect() *simLink {
	n.mu.Lock()
	old := n.link
	n.mu.Unlock()
	if old != nil {
		old.drop()
	}
	l := &simLink{n: n, vals: make(chan pipeValue, 4096), lostCh: make(chan struct{})}
	n.mu.Lock()
	n.link = l
	n.mu.Unlock()
	return l
}

// step is the firmware's updateOwner, publishStatus and publishStats.
func (n *simNode) step() {
	n.mu.Lock()
	now := n.clockLocked()
	if n.subscribed {
		if n.owner == 0 {
			n.owner = 1
			n.note("take")
		}
	} else if n.owner == 1 {
		hold := n.inFlight && now.Sub(n.sessionStart) < n.sessionCap
		if !hold && now.Sub(n.lastUnsub) >= n.debounce {
			n.inFlight = false // an answer never came within the cap
			n.owner = 0
			n.note("release")
		}
	}
	status := n.statusLocked()
	notifyStatus := n.lastStatus == nil || status[1] != n.lastStatus[1] || (len(status) > 2 && status[2] != n.lastStatus[2])
	n.lastStatus = status
	stats := n.statsLocked()
	notifyStats := n.lastStats == nil || !bytes.Equal(statsSansAges(stats), statsSansAges(n.lastStats))
	n.lastStats = stats
	l := n.link
	n.mu.Unlock()
	if l == nil {
		return
	}
	if notifyStatus {
		l.notify(pipeStatusUUID, status)
	}
	if notifyStats {
		l.notify(pipeStatsUUID, stats)
	}
}

func statsSansAges(v []byte) []byte {
	c := append([]byte(nil), v...)
	for _, off := range []int{4, 20, 24} {
		copy(c[off:off+4], []byte{0, 0, 0, 0})
	}
	return c
}

func (n *simNode) flagsLocked() byte {
	f := byte(0x04) // the modem answers
	if n.inFlight {
		f |= 0x01
	}
	return f
}

func (n *simNode) statusLocked() []byte {
	if n.statusV1 {
		return []byte{1, n.owner}
	}
	return []byte{2, n.owner, n.flagsLocked(), 0xFF}
}

func (n *simNode) uptimeLocked() uint32 { return uint32(n.clockLocked().Sub(n.boot) / time.Second) }

func (n *simNode) statsLocked() []byte {
	v := make([]byte, 52)
	v[0], v[1], v[2], v[3] = 2, n.owner, n.flagsLocked(), 0xFF
	binary.LittleEndian.PutUint32(v[4:], 0xFFFFFFFF)
	binary.LittleEndian.PutUint32(v[8:], n.sessions)
	binary.LittleEndian.PutUint16(v[12:], uint16(int16(n.lastMO)))
	binary.LittleEndian.PutUint16(v[14:], uint16(n.lastMOMSN))
	binary.LittleEndian.PutUint16(v[16:], uint16(int16(n.lastMT)))
	binary.LittleEndian.PutUint16(v[18:], uint16(n.lastMTQueued))
	age := uint32(0xFFFFFFFF)
	if !n.lastSession.IsZero() {
		age = uint32(n.clockLocked().Sub(n.lastSession) / time.Second)
	}
	binary.LittleEndian.PutUint32(v[20:], age)
	binary.LittleEndian.PutUint32(v[24:], n.uptimeLocked())
	binary.LittleEndian.PutUint32(v[36:], n.nodeSessions)
	return v
}

// clientWrite is RX: bytes pass to the modem while the link holds it.
func (n *simNode) clientWrite(l *simLink, data []byte) error {
	n.mu.Lock()
	if l.isLost() {
		n.mu.Unlock()
		return errors.New("org.bluez.Error.Failed: Not connected")
	}
	s := string(data)
	if s == n.notLanded {
		n.notLanded = ""
		n.mu.Unlock()
		return errors.New("org.bluez.Error.Failed: Operation failed with ATT error: 0x0e")
	}
	ackLost := s == n.ackLost
	if ackLost {
		n.ackLost = ""
	}
	drop := s == n.dropAfter
	if drop {
		n.dropAfter = ""
	}
	owned := n.owner == 1 && n.subscribed && n.link == l
	if owned {
		n.clientBytesLocked(data)
	}
	n.mu.Unlock()
	if owned {
		n.modem.feed(data)
	}
	n.step()
	if drop {
		l.drop()
		return errors.New("org.bluez.Error.Failed: Not connected")
	}
	if ackLost {
		return errors.New("org.bluez.Error.Failed: Operation failed with ATT error: 0x0e (acknowledgement lost)")
	}
	return nil
}

// clientBytesLocked is notePhoneBytes: an AT+SBDIX that reaches the modem is
// a session.
func (n *simNode) clientBytesLocked(data []byte) {
	for _, b := range data {
		if b == '\r' || b == '\n' {
			if strings.HasPrefix(strings.ToUpper(string(n.cmd)), "AT+SBDIX") {
				n.sessions++
				n.inFlight = true
				n.sessionStart = n.clockLocked()
				n.note("sbdix")
			}
			n.cmd = n.cmd[:0]
			continue
		}
		if len(n.cmd) >= 63 {
			n.cmd = n.cmd[:0]
			continue
		}
		n.cmd = append(n.cmd, b)
	}
}

// fromModem is the modem's UART: its answers are read whoever owns it, and
// reach the link only while it holds the modem.
func (n *simNode) fromModem(data []byte) {
	n.mu.Lock()
	for _, b := range data {
		if b == '\r' || b == '\n' {
			if len(n.resp) > 0 {
				n.responseLineLocked(string(n.resp))
			}
			n.resp = n.resp[:0]
			continue
		}
		if len(n.resp) >= 95 {
			n.resp = n.resp[:0]
			continue
		}
		n.resp = append(n.resp, b)
	}
	l := n.link
	forward := n.owner == 1 && n.subscribed && l != nil && !n.dropTX
	mangle := n.mangleTX
	n.mu.Unlock()
	if forward {
		if mangle != nil {
			data = mangle(data)
		}
		l.tx(data)
	}
	n.step()
}

func (n *simNode) responseLineLocked(line string) {
	switch {
	case strings.HasPrefix(line, "+SBDIX:"):
		fields := strings.Split(strings.TrimPrefix(line, "+SBDIX:"), ",")
		num := func(i int) int {
			if i >= len(fields) {
				return 0
			}
			v, _ := strconv.Atoi(strings.TrimSpace(fields[i]))
			return v
		}
		n.lastMO, n.lastMOMSN, n.lastMT, n.lastMTQueued = num(0), uint32(num(1)), num(2), uint32(num(5))
		n.lastSession = n.clockLocked()
		n.inFlight = false
		n.note("answer")
	case line == "ERROR" && n.inFlight:
		n.inFlight = false
		n.lastSession = n.clockLocked()
		n.note("error")
	}
}

func (n *simNode) subscribe(l *simLink) {
	n.mu.Lock()
	if n.link == l && !l.isLost() {
		n.subscribed = true
	}
	n.mu.Unlock()
	n.step()
}

func (n *simNode) unsubscribe(l *simLink) {
	n.mu.Lock()
	if n.link == l && n.subscribed {
		n.subscribed = false
		n.lastUnsub = n.clockLocked()
		n.note("unsubscribe")
	}
	n.mu.Unlock()
	n.step()
}

func (n *simNode) linkLost(l *simLink) {
	n.mu.Lock()
	if n.link == l {
		n.link = nil
		if n.subscribed {
			n.subscribed = false
			n.lastUnsub = n.clockLocked()
		}
		n.note("lost")
	}
	n.mu.Unlock()
	n.step()
}

// nodeSession is a session of the node's own routing, once no client holds
// the modem.
func (n *simNode) nodeSession(mo int, momsn uint32) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.owner != 0 {
		n.t.Fatalf("the node cannot take its modem: owner %d", n.owner)
	}
	n.sessions++
	n.nodeSessions++
	n.lastMO, n.lastMOMSN, n.lastMT, n.lastMTQueued = mo, momsn, 0, 0
	n.lastSession = n.clockLocked()
	n.note("node session")
}

// reboot restarts the node: its counters and clock start again.
func (n *simNode) reboot() {
	n.mu.Lock()
	n.boot = n.clockLocked()
	n.sessions, n.nodeSessions = 0, 0
	n.lastMO, n.lastMT, n.lastMOMSN, n.lastMTQueued = -1, -1, 0, 0
	n.lastSession = time.Time{}
	n.inFlight = false
	n.owner = 0
	n.note("reboot")
	n.mu.Unlock()
}

// simLink is one Bluetooth link to a simNode, as the pipe sees it.
type simLink struct {
	n      *simNode
	vals   chan pipeValue
	lostCh chan struct{}
	once   sync.Once
}

func (l *simLink) has(uuid string) bool {
	switch uuid {
	case pipeRXUUID, pipeTXUUID, pipeStatusUUID, pipeStatsUUID:
		return true
	}
	return false
}

func (l *simLink) startNotify(_ context.Context, uuid string) error {
	if l.isLost() {
		return errors.New("org.bluez.Error.Failed: Not connected")
	}
	if uuid == pipeTXUUID {
		l.n.subscribe(l)
	}
	return nil
}

func (l *simLink) stopNotify(uuid string) {
	if uuid == pipeTXUUID && !l.isLost() {
		l.n.unsubscribe(l)
	}
}

func (l *simLink) read(_ context.Context, uuid string) ([]byte, error) {
	if l.isLost() {
		return nil, errors.New("org.bluez.Error.Failed: Not connected")
	}
	l.n.mu.Lock()
	defer l.n.mu.Unlock()
	switch uuid {
	case pipeStatusUUID:
		return l.n.statusLocked(), nil
	case pipeStatsUUID:
		return l.n.statsLocked(), nil
	}
	return nil, errors.New("org.bluez.Error.NotPermitted")
}

func (l *simLink) write(_ context.Context, uuid string, data []byte) error {
	if uuid != pipeRXUUID {
		return errors.New("org.bluez.Error.NotPermitted")
	}
	return l.n.clientWrite(l, data)
}

func (l *simLink) mtu() int                 { return 185 }
func (l *simLink) values() <-chan pipeValue { return l.vals }
func (l *simLink) lost() <-chan struct{}    { return l.lostCh }

func (l *simLink) isLost() bool {
	select {
	case <-l.lostCh:
		return true
	default:
		return false
	}
}

func (l *simLink) drop() {
	l.once.Do(func() {
		close(l.lostCh)
		l.n.linkLost(l)
	})
}

func (l *simLink) notify(uuid string, v []byte) {
	select {
	case l.vals <- pipeValue{uuid: uuid, value: append([]byte(nil), v...)}:
	case <-l.lostCh:
	}
}

// tx notifies the modem's output in chunks of 20 bytes.
func (l *simLink) tx(data []byte) {
	for len(data) > 0 {
		k := min(20, len(data))
		l.notify(pipeTXUUID, data[:k])
		data = data[k:]
	}
}

// simBridge is the SBD transport riding a node link over a simNode's pipe,
// wired as main.go wires it (UseSatellitePipe), with the link's own claim
// loop and switch.
type simBridge struct {
	t      *testing.T
	mesh   *DirectMeshTransport
	sat    *DirectSatTransport
	node   *simNode
	modem  *nodeModem
	events <-chan SatEvent
}

func newSimBridge(t *testing.T, m *nodeModem) *simBridge {
	t.Helper()
	mesh, _ := pipeLink(t, nil)
	sat := NewDirectSatTransport("ble")
	sat.pipeWake = fastWake
	if err := mesh.UseSatellitePipe(sat); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	events, err := sat.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b := &simBridge{t: t, mesh: mesh, sat: sat, node: newSimNode(t, m), modem: m, events: events}
	t.Cleanup(func() {
		cancel()
		sat.Close()
		b.end()
	})
	return b
}

// end takes the node's link down and waits for the pipe's claim loop to be
// over, observed under the link's lock the loop takes last (dropPipe):
// a test that shortened the pipe's timings puts them back only after, as
// the loop reads them.
func (b *simBridge) end() {
	b.node.mu.Lock()
	l := b.node.link
	b.node.mu.Unlock()
	if l != nil {
		l.drop()
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b.mesh.ble.mu.Lock()
		p := b.mesh.ble.pipeSess
		b.mesh.ble.mu.Unlock()
		if p == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	// A pipe the test let go of (forgotten, replaced) has its loop end with
	// the link as well, without being the link's current one; the status
	// read takes the lock after it.
	time.Sleep(50 * time.Millisecond)
	_ = b.mesh.BLEStatus()
}

// connect brings a link to the node up and runs the pipe on it, as the link
// coming up does.
func (b *simBridge) connect() *simLink {
	b.t.Helper()
	l := b.node.connect()
	b.mesh.ble.runPipeOn(l)
	return l
}

// ready waits for the SBD transport to be up on the node's modem, and lets
// its first session go without the 10 s between sessions.
func (b *simBridge) ready() {
	b.t.Helper()
	pipeEventually(b.t, 10*time.Second, "the modem connected over the pipe", b.sat.IsConnected)
	b.sat.mu.Lock()
	b.sat.lastSBDIX = time.Time{}
	b.sat.mu.Unlock()
}

// shortOutcome shortens the wait for a cut session's outcome for one test.
func shortOutcome(t *testing.T, wait time.Duration) {
	t.Helper()
	w, p := pipeOutcomeWait, pipeOutcomePoll
	pipeOutcomeWait, pipeOutcomePoll = wait, 20*time.Millisecond
	t.Cleanup(func() { pipeOutcomeWait, pipeOutcomePoll = w, p })
}

// between reports whether any command went to the modem between the first
// "from" and the first "to" after it.
func between(cmds []string, from, to string) []string {
	start := pipeCmdPos(cmds, from)
	if start < 0 {
		return nil
	}
	var in []string
	for _, c := range cmds[start+1:] {
		if c == to {
			return in
		}
		in = append(in, c)
	}
	return in
}
