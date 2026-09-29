package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"
)

// pipeNode is a MeshSat node's modem pipe in memory: it hands its modem to
// a TX subscriber as the firmware does (unless told to keep it), notifies
// STATUS on every change of owner, acknowledges RX writes or fails them, and
// passes what RX carries to its modem while the client owns it.
type pipeNode struct {
	mu         sync.Mutex
	vals       chan pipeValue
	lostCh     chan struct{}
	lostOnce   sync.Once
	chars      map[string]bool
	mtuVal     int
	owner      byte // 0 none, 1 the client, 2 the node
	flags, csq byte
	keep       bool // the node keeps its modem: a TX subscription does not take it
	failWrites bool
	rx         [][]byte
	stats      []byte
	statsReads int
	txChunk    int
	modem      func([]byte)
}

func newPipeNode() *pipeNode {
	return &pipeNode{
		vals:    make(chan pipeValue, 4096),
		lostCh:  make(chan struct{}),
		chars:   map[string]bool{pipeRXUUID: true, pipeTXUUID: true, pipeStatusUUID: true, pipeStatsUUID: true},
		mtuVal:  23,
		csq:     0xFF,
		txChunk: 20,
	}
}

func (n *pipeNode) has(uuid string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.chars[uuid]
}

func (n *pipeNode) startNotify(_ context.Context, uuid string) error {
	if uuid != pipeTXUUID {
		return nil
	}
	n.mu.Lock()
	take := !n.keep && n.owner != 1
	if take {
		n.owner = 1
	}
	n.mu.Unlock()
	if take {
		n.notifyStatus()
	}
	return nil
}

func (n *pipeNode) stopNotify(uuid string) {
	if uuid != pipeTXUUID {
		return
	}
	n.mu.Lock()
	give := n.owner == 1
	if give {
		n.owner = 0
	}
	n.mu.Unlock()
	if give {
		n.notifyStatus()
	}
}

func (n *pipeNode) read(_ context.Context, uuid string) ([]byte, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch uuid {
	case pipeStatusUUID:
		return n.statusLocked(), nil
	case pipeStatsUUID:
		n.statsReads++
		if n.stats == nil {
			return nil, errors.New("org.bluez.Error.Failed")
		}
		return append([]byte(nil), n.stats...), nil
	}
	return nil, errors.New("org.bluez.Error.NotPermitted")
}

func (n *pipeNode) write(_ context.Context, uuid string, data []byte) error {
	n.mu.Lock()
	if n.failWrites {
		n.mu.Unlock()
		return errors.New("org.bluez.Error.Failed: Operation failed with ATT error: 0x0e")
	}
	n.rx = append(n.rx, append([]byte(nil), data...))
	owned, modem := n.owner == 1, n.modem
	n.mu.Unlock()
	if owned && modem != nil {
		modem(data)
	}
	return nil
}

func (n *pipeNode) mtu() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.mtuVal
}

func (n *pipeNode) values() <-chan pipeValue { return n.vals }
func (n *pipeNode) lost() <-chan struct{}    { return n.lostCh }

func (n *pipeNode) statusLocked() []byte { return []byte{2, n.owner, n.flags, n.csq} }

func (n *pipeNode) notify(uuid string, v []byte) {
	select {
	case n.vals <- pipeValue{uuid: uuid, value: v}:
	case <-n.lostCh:
	}
}

func (n *pipeNode) notifyStatus() {
	n.mu.Lock()
	v := n.statusLocked()
	n.mu.Unlock()
	n.notify(pipeStatusUUID, v)
}

// tx notifies what the modem says, cut into notifications of txChunk bytes.
func (n *pipeNode) tx(data []byte) {
	n.mu.Lock()
	size := n.txChunk
	n.mu.Unlock()
	for len(data) > 0 {
		k := min(size, len(data))
		n.notify(pipeTXUUID, append([]byte(nil), data[:k]...))
		data = data[k:]
	}
}

func (n *pipeNode) setOwner(o byte) {
	n.mu.Lock()
	n.owner = o
	n.mu.Unlock()
	n.notifyStatus()
}

func (n *pipeNode) set(fn func(n *pipeNode)) {
	n.mu.Lock()
	fn(n)
	n.mu.Unlock()
}

func (n *pipeNode) written() [][]byte {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([][]byte(nil), n.rx...)
}

func (n *pipeNode) drop() { n.lostOnce.Do(func() { close(n.lostCh) }) }

// pipeEventually waits for cond, failing the test after d.
func pipeEventually(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("not within %s: %s", d, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// claimedPipe runs a pipe on n and has it take the modem.
func claimedPipe(t *testing.T, n *pipeNode, health *pipeHealth, onFault func()) *blePipe {
	t.Helper()
	p := newBLEPipe(n, health, nil, onFault)
	go p.run()
	t.Cleanup(n.drop)
	if !p.claim(context.Background(), 2*time.Second) {
		t.Fatal("the node did not hand its modem over")
	}
	return p
}

func pipeLE16(v uint16) []byte { return binary.LittleEndian.AppendUint16(nil, v) }
func pipeLE32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

func pipeIntIs(p *int, want int) bool { return p != nil && *p == want }

// STATUS: the owner of a v1 or v2 value, flags and signal only when there;
// an unknown version, a short value or none reads as unknown.
func TestPipeStatus_OwnerFlagsAndSignal(t *testing.T) {
	for _, c := range []struct {
		v     []byte
		owner string
	}{
		{[]byte{1, 0}, PipeOwnerNone},
		{[]byte{1, 1}, PipeOwnerPhone},
		{[]byte{1, 2}, PipeOwnerNode},
		{[]byte{2, 1, 0, 0}, PipeOwnerPhone},
	} {
		st := parsePipeStatus(c.v)
		if st == nil || st.owner != c.owner {
			t.Errorf("% x: %+v, want owner %q", c.v, st, c.owner)
		}
	}
	for _, v := range [][]byte{{3, 1}, {1}, nil} {
		if st := parsePipeStatus(v); st != nil {
			t.Errorf("% x read as %+v", v, st)
		}
	}

	v1 := parsePipeStatus([]byte{1, 2})
	if v1.owner != PipeOwnerNode || v1.flags != nil || v1.csq != nil {
		t.Fatalf("v1: %+v", v1)
	}
	v2 := parsePipeStatus([]byte{2, 1, 0x0B, 4})
	if v2.owner != PipeOwnerPhone || v2.flags == nil ||
		*v2.flags != (PipeFlags{SessionInFlight: true, MessageWaiting: true, ModemAnswers: false, BufferCongested: true}) ||
		!pipeIntIs(v2.csq, 4) {
		t.Fatalf("v2: %+v %+v", v2, v2.flags)
	}
	// A v2 node still sending 2 bytes, and 0xFF for "never read".
	if st := parsePipeStatus([]byte{2, 1}); st.owner != PipeOwnerPhone || st.flags != nil {
		t.Fatalf("2-byte v2: %+v", st)
	}
	if st := parsePipeStatus([]byte{2, 1, 4, 0xFF}); st.csq != nil {
		t.Fatalf("csq 0xFF: %d", *st.csq)
	}
}

// STATS: the 52 little-endian bytes by offset, the node's "unknown"
// markers, a longer value still parsed, a shorter one or version 1 refused.
func TestPipeStats_The52BytesByOffset(t *testing.T) {
	var v []byte
	v = append(v, 2, 2, 0x06, 3)
	v = append(v, pipeLE32(12)...)
	v = append(v, pipeLE32(7)...)
	v = append(v, pipeLE16(32)...)
	v = append(v, pipeLE16(250)...)
	v = append(v, pipeLE16(2)...)
	v = append(v, pipeLE16(1)...)
	v = append(v, pipeLE32(240)...)
	v = append(v, pipeLE32(8040)...)
	v = append(v, pipeLE32(3)...)
	v = append(v, pipeLE32(17)...)
	v = append(v, pipeLE32(5)...)
	v = append(v, pipeLE32(4)...)
	v = append(v, pipeLE32(1)...)
	v = append(v, 4, 10, 0, 0)
	if len(v) != 52 {
		t.Fatalf("vector of %d bytes", len(v))
	}
	s := parsePipeStats(v)
	if s == nil {
		t.Fatal("nil")
	}
	if s.Owner != PipeOwnerNode || s.Flags != (PipeFlags{MessageWaiting: true, ModemAnswers: true}) || !pipeIntIs(s.CSQ, 3) ||
		s.CSQAgeS == nil || *s.CSQAgeS != 12 || s.Sessions != 7 || !pipeIntIs(s.LastMOStatus, 32) || s.LastMOMSN != 250 ||
		!pipeIntIs(s.LastMTStatus, 2) || s.LastMTQueued != 1 || s.LastSessionAgeS == nil || *s.LastSessionAgeS != 240 ||
		s.UptimeS != 8040 || s.WatchdogReboots != 3 || s.ClientBytesDropped != 17 || s.NodeSessions != 5 ||
		s.NodeSent != 4 || s.NodeReceived != 1 || s.DaySessionsUsed != 4 || s.DaySessionsCap != 10 {
		t.Fatalf("stats %+v", s)
	}

	var fresh []byte
	fresh = append(fresh, 2, 0, 0, 0xFF)
	fresh = append(fresh, pipeLE32(0xFFFFFFFF)...)
	fresh = append(fresh, pipeLE32(0)...)
	fresh = append(fresh, pipeLE16(0xFFFF)...)
	fresh = append(fresh, pipeLE16(0)...)
	fresh = append(fresh, pipeLE16(0xFFFF)...)
	fresh = append(fresh, pipeLE16(0)...)
	fresh = append(fresh, pipeLE32(0xFFFFFFFF)...)
	fresh = append(fresh, make([]byte, 28)...)
	fresh = append(fresh, 9, 9, 9)
	f := parsePipeStats(fresh)
	if f == nil || f.Owner != PipeOwnerNone || f.CSQ != nil || f.CSQAgeS != nil || f.LastMOStatus != nil ||
		f.LastMTStatus != nil || f.LastSessionAgeS != nil {
		t.Fatalf("fresh %+v", f)
	}
	if parsePipeStats(v[:51]) != nil {
		t.Fatal("51 bytes parsed")
	}
	if parsePipeStats(append([]byte{1}, v[1:]...)) != nil {
		t.Fatal("version 1 parsed")
	}
}

// A write goes out in chunks of the link's ATT payload, each acknowledged
// before the next: 342 bytes on an MTU of 23 are 18 writes of at most 20.
func TestPipePort_ChunksOfTheLinkEachAcknowledged(t *testing.T) {
	payload := make([]byte, 342)
	for i := range payload {
		payload[i] = byte(i)
	}
	for _, c := range []struct{ mtu, writes, chunk int }{{23, 18, 20}, {517, 2, 244}, {0, 18, 20}, {100, 4, 97}} {
		n := newPipeNode()
		n.mtuVal = c.mtu
		p := claimedPipe(t, n, nil, nil)
		port, err := p.port()
		if err != nil {
			t.Fatal(err)
		}
		if k, err := port.Write(payload); err != nil || k != len(payload) {
			t.Fatalf("mtu %d: wrote %d, %v", c.mtu, k, err)
		}
		chunks := n.written()
		if len(chunks) != c.writes {
			t.Fatalf("mtu %d: %d writes, want %d", c.mtu, len(chunks), c.writes)
		}
		for _, ch := range chunks {
			if len(ch) > c.chunk {
				t.Fatalf("mtu %d: a chunk of %d", c.mtu, len(ch))
			}
		}
		if !bytes.Equal(bytes.Join(chunks, nil), payload) {
			t.Fatalf("mtu %d: the chunks are not the payload", c.mtu)
		}
	}
}

// While the node does not give this Bridge its modem, a write is refused with
// its own error, sends nothing and never counts towards a broken link.
func TestPipePort_NotOwnerIsRefusedAndNotCounted(t *testing.T) {
	n := newPipeNode()
	health := &pipeHealth{}
	p := claimedPipe(t, n, health, nil)
	port, err := p.port()
	if err != nil {
		t.Fatal(err)
	}
	n.setOwner(2)
	pipeEventually(t, time.Second, "owner node", func() bool { return p.Owner() == PipeOwnerNode })
	for i := 0; i < pipeBrokenWrites+2; i++ {
		if _, err := port.Write([]byte("AT+SBDIX\r")); !errors.Is(err, errPipeNotOwned) {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if len(n.written()) != 0 {
		t.Fatal("bytes went out to a node that holds its modem")
	}
	if broken, _ := health.state(); broken {
		t.Fatal("a handover counted as a broken link")
	}
	if _, err := p.port(); !errors.Is(err, errPipeNotOwned) {
		t.Fatalf("a port while the node holds the modem: %v", err)
	}
}

// A chunk the node does not acknowledge fails the write; three in a row
// break the link (the port dies, the fault is reported), and the first write
// that lands on a new port mends it.
func TestPipePort_FailedChunksBreakTheLinkAndALandedWriteMendsIt(t *testing.T) {
	n := newPipeNode()
	health := &pipeHealth{}
	faults := make(chan struct{}, 4)
	p := claimedPipe(t, n, health, func() { faults <- struct{}{} })
	port, _ := p.port()
	n.set(func(n *pipeNode) { n.failWrites = true })
	for i := 0; i < pipeBrokenWrites; i++ {
		if _, err := port.Write([]byte("AT\r")); !errors.Is(err, errPipeWriteFailed) {
			t.Fatalf("write %d: %v", i, err)
		}
		if broken, _ := health.state(); broken != (i == pipeBrokenWrites-1) {
			t.Fatalf("after %d failures broken=%v", i+1, broken)
		}
	}
	select {
	case <-faults:
	case <-time.After(time.Second):
		t.Fatal("no fault reported")
	}
	if _, err := port.Read(make([]byte, 8)); !errors.Is(err, errPipeLinkLost) {
		t.Fatalf("the port of a broken link still reads: %v", err)
	}
	if _, err := port.Write([]byte("AT\r")); !errors.Is(err, errPipeLinkLost) {
		t.Fatalf("the port of a broken link still writes: %v", err)
	}

	n.set(func(n *pipeNode) { n.failWrites = false })
	again, err := p.port()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := again.Write([]byte("AT\r")); err != nil {
		t.Fatal(err)
	}
	if broken, _ := health.state(); broken {
		t.Fatal("a write that landed did not mend the link")
	}
}

// SBDRING is seen even when a line is split across notifications, once per
// line, and only as a line of its own.
func TestPipe_SBDRINGSplitAcrossNotificationsIsOneRing(t *testing.T) {
	w := newLineWatcher("SBDRING")
	rings := w.feed([]byte("\r\nSBD")) + w.feed([]byte("RI")) + w.feed([]byte("NG\r\n"))
	if rings != 1 {
		t.Fatalf("split: %d rings", rings)
	}
	if got := w.feed([]byte("OK\r\nSSBDRING\r\n")); got != 1 {
		t.Fatalf("SSBDRING: %d", got)
	}
	if got := w.feed([]byte("+SBDRING: no\r\nSBDRIN\r\n")); got != 0 {
		t.Fatalf("not a ring line: %d", got)
	}

	// Through the pipe, whoever reads the bytes.
	n := newPipeNode()
	p := claimedPipe(t, n, nil, nil)
	port, _ := p.port()
	ring := make(chan struct{}, 4)
	port.(*pipePort).SetRingHandler(func() { ring <- struct{}{} })
	n.notify(pipeTXUUID, []byte("\r\nSBD"))
	n.notify(pipeTXUUID, []byte("RI"))
	n.notify(pipeTXUUID, []byte("NG\r\n"))
	select {
	case <-ring:
	case <-time.After(time.Second):
		t.Fatal("no ring through the pipe")
	}
	select {
	case <-ring:
		t.Fatal("a second ring")
	case <-time.After(100 * time.Millisecond):
	}
}

// The modem's output is kept in order, binary-safe, up to 8 KiB; the excess
// is dropped and counted rather than stalling the Bluetooth side.
func TestPipe_InputKeepsBinaryInOrderAndCountsTheExcess(t *testing.T) {
	n := newPipeNode()
	p := claimedPipe(t, n, nil, nil)
	port, _ := p.port()
	first := []byte{0x00, 0x0D, 0x0A, 0xFF, 0x94, 0xC3}
	n.notify(pipeTXUUID, first)
	n.notify(pipeTXUUID, make([]byte, pipeInputBytes))
	pipeEventually(t, time.Second, "the excess counted", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.dropped == uint64(len(first))
	})
	buf := make([]byte, 4)
	if k, err := port.Read(buf); err != nil || !bytes.Equal(buf[:k], first[:4]) {
		t.Fatalf("read % x, %v", buf[:k], err)
	}
	total := 4
	big := make([]byte, 16384)
	for {
		k, err := port.Read(big)
		if err != nil {
			t.Fatal(err)
		}
		if k == 0 {
			break
		}
		total += k
	}
	if total != pipeInputBytes {
		t.Fatalf("kept %d bytes", total)
	}
}

// Read answers (0, nil) at its timeout, as a serial port does (the AT code
// relies on it), and an error at once when the link goes, even mid-wait.
func TestPipePort_ReadTimesOutAndFailsAtOnceWhenTheLinkGoes(t *testing.T) {
	n := newPipeNode()
	p := claimedPipe(t, n, nil, nil)
	port, _ := p.port()
	port.SetReadTimeout(50 * time.Millisecond)
	start := time.Now()
	if k, err := port.Read(make([]byte, 8)); k != 0 || err != nil {
		t.Fatalf("timeout read: %d, %v", k, err)
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Fatal("the read did not wait for its timeout")
	}

	port.SetReadTimeout(-1) // serial.NoTimeout
	done := make(chan error, 1)
	go func() {
		_, err := port.Read(make([]byte, 8))
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	n.drop()
	select {
	case err := <-done:
		if !errors.Is(err, errPipeLinkLost) {
			t.Fatalf("read after the link went: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("a waiting read did not end with the link")
	}
	if p.Owner() != "" {
		t.Fatalf("owner %q after the link went", p.Owner())
	}
}

// The claim waits for STATUS to say 01 (the node finishing its own session
// is a wait, never a failure); release gives the modem back and kills the
// port of that ownership. A node without STATUS is owned once subscribed.
func TestPipe_ClaimWaitsForTheNodeAndReleaseGivesItBack(t *testing.T) {
	n := newPipeNode()
	n.keep, n.owner = true, 2
	var owners []string
	var mu sync.Mutex
	p := newBLEPipe(n, nil, func(o string) { mu.Lock(); owners = append(owners, o); mu.Unlock() }, nil)
	go p.run()
	t.Cleanup(n.drop)
	if p.claim(context.Background(), 150*time.Millisecond) {
		t.Fatal("claimed a modem the node keeps")
	}
	if p.Owner() != PipeOwnerNode || !p.holds() {
		t.Fatalf("owner %q holds %v", p.Owner(), p.holds())
	}
	p.keepClaim(context.Background()) // reads STATUS again while the node holds it
	n.set(func(n *pipeNode) { n.keep = false })
	n.setOwner(1) // the node's session ended: it hands over
	pipeEventually(t, time.Second, "owner phone", func() bool { return p.Owner() == PipeOwnerPhone })
	port, err := p.port()
	if err != nil {
		t.Fatal(err)
	}

	p.release()
	if p.Owner() != PipeOwnerNone || p.holds() {
		t.Fatalf("after release: owner %q holds %v", p.Owner(), p.holds())
	}
	if _, err := port.Write([]byte("AT\r")); !errors.Is(err, errPipeLinkLost) {
		t.Fatalf("the released port writes: %v", err)
	}
	// onOwner hears them on a goroutine of its own, in order.
	told := func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), owners...) }
	pipeEventually(t, time.Second, "three owners told", func() bool { return len(told()) >= 3 })
	if got := told(); got[0] != PipeOwnerNode || got[1] != PipeOwnerPhone || got[2] != PipeOwnerNone {
		t.Fatalf("owners told: %v", got)
	}

	old := newPipeNode()
	delete(old.chars, pipeStatusUUID)
	delete(old.chars, pipeStatsUUID)
	q := claimedPipe(t, old, nil, nil)
	if q.Owner() != PipeOwnerPhone {
		t.Fatalf("a node without STATUS: owner %q", q.Owner())
	}
	if _, err := q.statsReading(context.Background()); !errors.Is(err, ErrNoPipeStats) {
		t.Fatalf("stats of a node without STATS: %v", err)
	}
}

// STATS is read once when the pipe comes up (and by the claim, for the
// session-in-flight flag), served from memory while fresh and read again
// once older than 10 s.
func TestPipe_StatsAreReadAgainWhenOld(t *testing.T) {
	n := newPipeNode()
	n.stats = append([]byte{2, 1, 0x04, 5}, make([]byte, 48)...)
	p := claimedPipe(t, n, nil, nil)
	reads := func() int { n.mu.Lock(); defer n.mu.Unlock(); return n.statsReads }
	claimed := reads()
	if claimed != 1 {
		t.Fatalf("the claim read STATS %d times", claimed)
	}
	p.watchStats(context.Background())
	r, err := p.statsReading(context.Background())
	if err != nil || r.Owner != PipeOwnerPhone || !pipeIntIs(r.CSQ, 5) || r.ReadAt.IsZero() {
		t.Fatalf("reading %+v, %v", r, err)
	}
	if reads() != claimed+1 {
		t.Fatalf("%d reads", reads())
	}
	p.mu.Lock()
	p.statsAt = time.Now().Add(-pipeStatsFresh - time.Second)
	p.mu.Unlock()
	if _, err := p.statsReading(context.Background()); err != nil || reads() != claimed+2 {
		t.Fatalf("stale: %v, %d reads", err, reads())
	}
}

// pipeDriver records what the node link tells the SBD transport.
type pipeDriver struct {
	mu    sync.Mutex
	calls []string
}

func (d *pipeDriver) PortReady()   { d.mu.Lock(); d.calls = append(d.calls, "ready"); d.mu.Unlock() }
func (d *pipeDriver) PortGone()    { d.mu.Lock(); d.calls = append(d.calls, "gone"); d.mu.Unlock() }
func (d *pipeDriver) PortLeaving() { d.mu.Lock(); d.calls = append(d.calls, "leaving"); d.mu.Unlock() }

// Quiesce has no session to wait for.
func (d *pipeDriver) Quiesce(fn func())           { fn() }
func (d *pipeDriver) QuiesceAbandoning(fn func()) { fn() }

func (d *pipeDriver) last() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.calls) == 0 {
		return ""
	}
	return d.calls[len(d.calls)-1]
}

// pipeLink is a node link that is up (no BlueZ behind it) with driver as its
// SBD transport, and what it tells the SBD gateway.
func pipeLink(t *testing.T, driver satPipeDriver) (*DirectMeshTransport, func() []bool) {
	t.Helper()
	tr := NewDirectMeshTransport("ble")
	tr.SetBLEStateDir(t.TempDir())
	l := tr.ble
	l.mu.Lock()
	l.address, l.loaded = "E0:72:A1:B3:C2:ED", true
	l.session = &bleGattSession{lost: make(chan struct{}), notif: make(chan struct{}, 1)}
	if driver != nil {
		l.satDriver = driver
	}
	l.mu.Unlock()
	var mu sync.Mutex
	var told []bool
	tr.OnSatellitePipe(func(up bool) { mu.Lock(); told = append(told, up); mu.Unlock() })
	return tr, func() []bool { mu.Lock(); defer mu.Unlock(); return append([]bool(nil), told...) }
}

func lastTold(told []bool) (bool, bool) {
	if len(told) == 0 {
		return false, false
	}
	return told[len(told)-1], true
}

// The switch is kept with the node (default on), read even when the port
// names the node, and outlives forgetting the node while it is off.
func TestBLELink_TheSatelliteSwitchIsKeptWithTheNode(t *testing.T) {
	dir := t.TempDir()
	tr := NewDirectMeshTransport("ble")
	tr.SetBLEStateDir(dir)
	tr.ble.address, tr.ble.loaded = "E0:72:A1:B3:C2:ED", true
	if !tr.BLEStatus().SatelliteEnabled {
		t.Fatal("the switch is off by default")
	}
	if err := tr.BLESetSatellite(false); err != nil {
		t.Fatal(err)
	}

	again := NewDirectMeshTransport("ble")
	again.SetBLEStateDir(dir)
	if st := again.BLEStatus(); st.SatelliteEnabled || st.Address != "E0:72:A1:B3:C2:ED" || st.SatelliteOwner != "" || st.SatelliteLinkBroken {
		t.Fatalf("after a restart: %+v", st)
	}
	named := NewDirectMeshTransport("ble:E0:72:A1:B3:C2:ED")
	named.SetBLEStateDir(dir)
	if named.BLEStatus().SatelliteEnabled {
		t.Fatal("a port naming the node lost the switch")
	}

	if err := again.BLEForget(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	third := NewDirectMeshTransport("ble")
	third.SetBLEStateDir(dir)
	if st := third.BLEStatus(); st.Address != "" || st.SatelliteEnabled {
		t.Fatalf("after forgetting the node: %+v", st)
	}
	third.BLESetSatellite(true)
	fourth := NewDirectMeshTransport("ble")
	fourth.SetBLEStateDir(dir)
	if !fourth.BLEStatus().SatelliteEnabled {
		t.Fatal("switched on again, it read off")
	}

	serial := NewDirectMeshTransport("/dev/ttyACM0")
	if err := serial.BLESetSatellite(true); !errors.Is(err, ErrMeshNotBLE) {
		t.Fatalf("switch on a serial node: %v", err)
	}
	if _, err := serial.BLESatelliteStats(context.Background()); !errors.Is(err, ErrMeshNotBLE) {
		t.Fatalf("stats on a serial node: %v", err)
	}
}

// With the SBD transport on the pipe, the node's modem is claimed while the
// switch is on and handed back the moment it goes off; the SBD transport and
// gateway hear each change, and the link going ends it all.
func TestBLELink_TheClaimFollowsTheSwitch(t *testing.T) {
	d := &pipeDriver{}
	tr, told := pipeLink(t, d)
	n := newPipeNode()
	n.stats = append([]byte{2, 1, 0x04, 3}, make([]byte, 48)...)
	t.Cleanup(n.drop)
	p := tr.ble.runPipeOn(n)
	pipeEventually(t, 2*time.Second, "claimed", func() bool { return p.Owner() == PipeOwnerPhone && d.last() == "ready" })
	pipeEventually(t, time.Second, "the gateway told", func() bool { up, ok := lastTold(told()); return ok && up })
	st := tr.BLEStatus()
	if !st.SatelliteEnabled || st.SatelliteOwner != PipeOwnerPhone || st.SatelliteLinkBroken || !st.Connected {
		t.Fatalf("status %+v", st)
	}
	if r, err := tr.BLESatelliteStats(context.Background()); err != nil || !pipeIntIs(r.CSQ, 3) || r.Owner != PipeOwnerPhone {
		t.Fatalf("stats %+v, %v", r, err)
	}
	if !tr.SatellitePipeAvailable() {
		t.Fatal("not available with the link up and the switch on")
	}

	// Off: no session starts from here (PortLeaving) and the claim loop
	// gives the modem back through the transport's Quiesce, on its own
	// goroutine.
	tr.BLESetSatellite(false)
	pipeEventually(t, 2*time.Second, "given back", func() bool { return p.Owner() == PipeOwnerNone && d.last() == "gone" })
	d.mu.Lock()
	leaving := pipeCmdPos(d.calls, "leaving")
	d.mu.Unlock()
	if leaving < 0 {
		t.Fatalf("the transport was not told to start no session: %v", d.calls)
	}
	n.mu.Lock()
	nodeOwner := n.owner
	n.mu.Unlock()
	if nodeOwner != 0 {
		t.Fatalf("the node still gives this Bridge its modem: %d", nodeOwner)
	}
	pipeEventually(t, time.Second, "the gateway detached", func() bool { up, _ := lastTold(told()); return !up })
	if tr.SatellitePipeAvailable() {
		t.Fatal("available with the switch off")
	}

	tr.BLESetSatellite(true)
	pipeEventually(t, 2*time.Second, "claimed again", func() bool { return p.Owner() == PipeOwnerPhone && d.last() == "ready" })
	pipeEventually(t, time.Second, "the gateway attached again", func() bool { up, _ := lastTold(told()); return up })

	n.drop()
	pipeEventually(t, 2*time.Second, "the link gone", func() bool {
		up, _ := lastTold(told())
		return d.last() == "gone" && !up && tr.BLEStatus().SatelliteOwner == ""
	})
	if _, err := tr.BLESatelliteStats(context.Background()); !errors.Is(err, ErrNoPipeStats) {
		t.Fatalf("stats without a node: %v", err)
	}
}

// Writes that stop reaching the node's modem mark the link broken in the
// status and drop the SBD transport's line; a landed write clears it.
func TestBLELink_ABrokenPipeDropsTheModem(t *testing.T) {
	d := &pipeDriver{}
	tr, _ := pipeLink(t, d)
	n := newPipeNode()
	t.Cleanup(n.drop)
	p := tr.ble.runPipeOn(n)
	pipeEventually(t, 2*time.Second, "claimed", func() bool { return d.last() == "ready" })
	port, err := tr.ble.satellitePort(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n.set(func(n *pipeNode) { n.failWrites = true })
	for i := 0; i < pipeBrokenWrites; i++ {
		port.Write([]byte("AT\r"))
	}
	pipeEventually(t, time.Second, "the line dropped", func() bool { return d.last() == "gone" })
	if !tr.BLEStatus().SatelliteLinkBroken {
		t.Fatal("the status does not say the link is broken")
	}
	n.set(func(n *pipeNode) { n.failWrites = false })
	again, err := p.port()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := again.Write([]byte("AT\r")); err != nil {
		t.Fatal(err)
	}
	if tr.BLEStatus().SatelliteLinkBroken {
		t.Fatal("a landed write did not clear it")
	}
}

// A node adopted without MESHSAT_IRIDIUM_PORT=ble keeps its modem: nothing
// claims it, and its report is still read.
func TestBLELink_WithoutTheSBDTransportTheNodeKeepsItsModem(t *testing.T) {
	tr, _ := pipeLink(t, nil)
	n := newPipeNode()
	n.stats = append([]byte{2, 2, 0x04, 1}, make([]byte, 48)...)
	t.Cleanup(n.drop)
	tr.ble.runPipeOn(n)
	pipeEventually(t, 2*time.Second, "the report read", func() bool { n.mu.Lock(); defer n.mu.Unlock(); return n.statsReads > 0 })
	time.Sleep(100 * time.Millisecond)
	n.mu.Lock()
	owner := n.owner
	n.mu.Unlock()
	if owner != 0 {
		t.Fatalf("the modem was claimed: owner %d", owner)
	}
	if r, err := tr.BLESatelliteStats(context.Background()); err != nil || r.Owner != PipeOwnerNode {
		t.Fatalf("stats %+v, %v", r, err)
	}
}
