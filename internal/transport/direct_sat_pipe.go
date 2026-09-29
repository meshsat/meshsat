package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"go.bug.st/serial"
)

// The SBD transport over the adopted MeshSat node's modem pipe
// (MESHSAT_IRIDIUM_PORT=ble; the pipe itself is ble_pipe.go). What differs
// from a RockBLOCK on USB, all of it from MeshSat Android v2.19.4
// (bt/IridiumSpp.kt, service/GatewayService.kt):
//
//   - The line comes and goes with the node: PortReady when the node gives
//     this Bridge its modem, PortGone when it does not any more. Subscribe
//     never connects; the pipe loop does.
//   - A node that has just powered its modem gives it about 10 s before it
//     answers, and one without a modem never does: AT&K0 is asked again
//     until an answer, and after a minute of silence the status says so.
//   - The set-up never empties the MT buffer blind (AT+SBDD1 would delete a
//     message waiting in the modem, and a reconnect must not): AT+SBDSX says
//     what the buffers hold, and a waiting message is read at once, for
//     free, before anything can bill a session, then cleared as every read
//     clears it and held for Receive (mtHeld). A message read on an earlier
//     link whose clear did not go through (mtStale) is cleared again, never
//     handed over twice, but only while it is that very message in that very
//     modem: the node runs sessions of its own while the Bridge is away, and
//     another node may be chosen.
//   - A link lost during AT+SBDIX ends the command at once (the pipe's port
//     fails every read the moment the link goes), and so does a write of
//     AT+SBDIX that failed where it may have landed. Such a session starts
//     the 3-minute hold of the mailbox check a person asks for (SBDIXHold),
//     from the session's start, as over a cable. The node finishes it and
//     keeps its result: a send settles it from the node's account (STATS)
//     once the link is back (settleCutSession), its real result or
//     ErrOutcomeUnknown, never a failure the queue retries; a mailbox check
//     answers MO status -1 (MOStatusUnknown) beside the error.
//   - No port is handed out while the node says a session is in flight
//     (blePipe.port): the set-up would write into it.
//   - The modem is given back only through Quiesce (the node link's
//     releases), never in the middle of a session or an MT read.
//
// A USB 9603 never takes any of these paths. [MESHSAT-1391]

// pipeResync is how often the pipe loop checks that a modem the node gives
// this Bridge is connected (after a failed set-up, or a connection that
// ended on its own).
const pipeResync = 10 * time.Second

// A send whose session the link lost waits at most pipeOutcomeWait after its
// AT+SBDIX for the node's account (the link back, no session in flight),
// asking every pipeOutcomePoll. The node holds a session's result for up to
// 95 s. Variables so tests can shorten them.
var (
	pipeOutcomeWait = 3 * time.Minute
	pipeOutcomePoll = time.Second
)

// IsSatPipe reports whether an Iridium port setting means the adopted
// Bluetooth node's modem pipe: `ble`.
func IsSatPipe(port string) bool {
	return strings.EqualFold(strings.TrimSpace(port), "ble")
}

// pipeWakeTimings pace the wake-up probe over the pipe: AT&K0 with an answer
// timeout of at, asked again every retry, and after grace without an answer
// the modem is reported silent and asked every silentRetry.
type pipeWakeTimings struct {
	at, retry, grace, silentRetry time.Duration
}

var defaultPipeWake = pipeWakeTimings{
	at:          iridiumReadTimeout,
	retry:       2 * time.Second,
	grace:       60 * time.Second,
	silentRetry: 30 * time.Second,
}

// pipeCurPort is the pipe port of the current connection, for PortGone.
type pipeCurPort struct{ port serial.Port }

func mtHeldEvent() SatEvent {
	return SatEvent{
		Type:    "mt_received",
		Message: "a message waits in the modem's MT buffer",
		Time:    time.Now().UTC().Format(time.RFC3339),
	}
}

// SetPortOpener makes the transport reach its modem through open instead of
// a device path: the adopted node's modem pipe. From then on it connects in
// the background after PortReady and drops the line on PortGone.
func (t *DirectSatTransport) SetPortOpener(open func(ctx context.Context) (serial.Port, error)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	first := t.opener == nil
	t.opener = open
	if first && open != nil {
		go t.pipeLoop()
	}
}

// SetNodeStats gives the transport the node's account of its modem (STATS,
// read now), which settles a session whose answer the link lost.
func (t *DirectSatTransport) SetNodeStats(read func(ctx context.Context) (*PipeStats, error)) {
	t.mu.Lock()
	t.nodeStats = read
	t.mu.Unlock()
}

// Quiesce runs fn with no satellite session and no modem command in flight,
// and none starting until fn returns: it holds the session lock, then the
// serial lock, as the sessions do. A session in flight ends first with its
// answer or, over the node's pipe, with its outcome settled; an MT read
// with its message. The node link gives the modem back through it.
func (t *DirectSatTransport) Quiesce(fn func()) {
	t.sessionMu.Lock()
	defer t.sessionMu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	fn()
}

// QuiesceAbandoning is Quiesce for a node that is being forgotten or
// replaced: a send waiting to settle a session whose link dropped gives up
// meanwhile (settleCutSession), its outcome unknown, rather than hold the
// session lock for a node that is going.
func (t *DirectSatTransport) QuiesceAbandoning(fn func()) {
	t.abandoning.Add(1)
	defer t.abandoning.Add(-1)
	t.Quiesce(fn)
}

// PortLeaving: the node gets its modem back once no session runs (the
// switch went off). No session starts from here, a connect under way stops,
// and the line is dropped as soon as nothing runs on it (the pipe loop
// takes the serial lock for it); PortGone follows the release. Unlike
// PortGone it never closes the port under a session.
func (t *DirectSatTransport) PortLeaving() {
	t.pipeWant.Store(false)
	t.pipeGen.Add(1)
	t.kickPipe()
}

// OverPipe reports whether the modem is reached through the node's pipe.
func (t *DirectSatTransport) OverPipe() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.opener != nil
}

func (t *DirectSatTransport) isPipe() bool { return t.OverPipe() }

// PortReady: the node gives this Bridge its modem now. The transport probes
// it over the pipe in the background and says "connected" once it answered.
func (t *DirectSatTransport) PortReady() {
	t.pipeWant.Store(true)
	t.pipeGen.Add(1)
	t.kickPipe()
}

// PortGone: the modem is not this Bridge's any more (given back, taken by the
// node, the link lost). A command in flight ends at once, and nothing more is
// sent until the next PortReady.
func (t *DirectSatTransport) PortGone() {
	t.pipeWant.Store(false)
	t.pipeGen.Add(1)
	t.silent.Store(false)
	if cur := t.pipeCur.Load(); cur != nil {
		cur.port.Close()
	}
	t.kickPipe()
}

func (t *DirectSatTransport) kickPipe() {
	select {
	case t.pipeKick <- struct{}{}:
	default:
	}
}

// stopPipe ends the pipe loop and whatever runs over the pipe (Close).
func (t *DirectSatTransport) stopPipe() {
	t.pipeStopOnce.Do(func() { close(t.pipeStop) })
	t.pipeGen.Add(1)
	if cur := t.pipeCur.Load(); cur != nil {
		cur.port.Close()
	}
}

// reprobePipe is Reconnect over the pipe: the line is dropped and the modem
// probed again in the background, as long as the node gives it to this Bridge.
func (t *DirectSatTransport) reprobePipe() error {
	t.mu.Lock()
	if t.connected {
		t.disconnectLocked()
	}
	t.mu.Unlock()
	t.pipeGen.Add(1)
	t.kickPipe()
	if !t.pipeWant.Load() {
		return fmt.Errorf("the node has not given this Bridge its modem: %w", ErrNotConnected)
	}
	return nil
}

func (t *DirectSatTransport) pipeLoop() {
	tick := time.NewTicker(pipeResync)
	defer tick.Stop()
	for {
		select {
		case <-t.pipeStop:
			return
		case <-t.pipeKick:
		case <-tick.C:
		}
		t.syncPipe()
	}
}

// syncPipe brings the connection in line with the node: dropped when the
// modem is not this Bridge's, probed when it is and nothing is connected;
// again when the node changed its mind meanwhile.
func (t *DirectSatTransport) syncPipe() {
	for {
		select {
		case <-t.pipeStop:
			return
		default:
		}
		gen, want := t.pipeGen.Load(), t.pipeWant.Load()
		t.mu.Lock()
		if t.connected && (!want || t.pipeConnGen != gen) {
			t.disconnectLocked()
		}
		connected := t.connected
		t.mu.Unlock()
		if want && !connected {
			t.connectPipe(gen)
		}
		if t.pipeGen.Load() == gen {
			return
		}
	}
}

// connectPipe opens the pipe's port, repeats AT&K0 until the modem answers,
// then runs the set-up. It gives up as soon as the node changes its mind.
func (t *DirectSatTransport) connectPipe(gen uint64) {
	t.mu.Lock()
	open, w := t.opener, t.pipeWake
	t.mu.Unlock()
	if open == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	port, err := open(ctx)
	cancel()
	if err != nil {
		log.Debug().Err(err).Msg("iridium: the node's modem is not this Bridge's yet")
		return
	}
	stale := func() bool {
		select {
		case <-t.pipeStop:
			return true
		default:
		}
		return t.pipeGen.Load() != gen
	}

	start := time.Now()
	for {
		if stale() {
			port.Close()
			return
		}
		// Flow control off first: the node wires TX, RX and GND only.
		resp, err := sendAT(port, "AT&K0", w.at)
		if strings.Contains(resp, "OK") || strings.Contains(resp, "ERROR") {
			break
		}
		if errors.Is(err, errPipeLinkLost) {
			port.Close()
			return
		}
		silent := time.Since(start) >= w.grace
		if silent && !t.silent.Swap(true) {
			log.Warn().Dur("for", time.Since(start).Truncate(time.Second)).Msg("iridium: the node's modem does not answer AT; still trying")
		}
		wait := w.retry
		if silent {
			wait = w.silentRetry
		}
		if !t.pipeSleep(wait, gen) {
			port.Close()
			return
		}
	}
	t.silent.Store(false)

	t.mu.Lock()
	if stale() || t.connected {
		t.mu.Unlock()
		port.Close()
		return
	}
	held, err := t.initPipeModemLocked(port)
	if err == nil {
		t.pipeConnGen = gen
	}
	t.mu.Unlock()
	if err != nil {
		port.Close()
		log.Warn().Err(err).Msg("iridium: the node's modem did not finish its set-up; asking again")
		return
	}
	if held {
		t.emitEvent(mtHeldEvent())
	}
}

// pipeSleep waits d; false when the node changed its mind or the transport
// closed. A kick it takes is seen again by syncPipe, which re-reads the state.
func (t *DirectSatTransport) pipeSleep(d time.Duration, gen uint64) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case <-t.pipeStop:
			return false
		case <-timer.C:
			return t.pipeGen.Load() == gen
		case <-t.pipeKick:
			if t.pipeGen.Load() != gen {
				return false
			}
		}
	}
}

// initPipeModemLocked is the set-up over the pipe once the modem answered
// AT&K0: the USB set-up's commands with AT+CGMI added and AT+SBDD1 left out,
// since it deletes a message waiting in the modem. AT+SBDSX says what the
// buffers hold, and a message in the MT buffer is read now, for free, before
// anything can open a billed session (a session empties the buffer), then
// cleared as every read clears it and held for Receive. Reports whether a
// message is held. Ring alerts on and the MO buffer emptied are what the
// link is taken to be from here on (connGen), so a set-up whose AT+SBDMTA=1
// or AT+SBDD0 the modem did not take fails and is asked again. Caller holds
// t.mu, which it keeps throughout: nobody sees the link up before the
// set-up ends.
func (t *DirectSatTransport) initPipeModemLocked(port serial.Port) (bool, error) {
	drainPort(port)
	// ATE0 lasts until the modem next loses power; the node powers it.
	sendAT(port, "ATE0", iridiumReadTimeout)
	sendAT(port, "AT&D0", iridiumReadTimeout)
	resp, err := sendAT(port, "AT", iridiumReadTimeout)
	if err != nil || !strings.Contains(resp, "OK") {
		return false, fmt.Errorf("AT check failed")
	}
	t.connIMEI = ""
	if resp, err := sendAT(port, "AT+CGSN", iridiumReadTimeout); err == nil {
		t.imei = parseATValue(resp)
		t.connIMEI = t.imei
	}
	if resp, err := sendAT(port, "AT+CGMM", iridiumReadTimeout); err == nil {
		t.model = parseATValue(resp)
	}
	if resp, err := sendAT(port, "AT+CGMR", iridiumReadTimeout); err == nil {
		t.firmware = parseATValue(resp)
		t.needsMSSTMWorkaround = strings.Contains(t.firmware, "TA16005")
		if t.needsMSSTMWorkaround {
			log.Warn().Str("firmware", t.firmware).Msg("iridium: TA16005 detected, enabling MSSTM workaround")
		}
	}
	if resp, err := sendAT(port, "AT+CGMI", iridiumReadTimeout); err == nil {
		t.manufacturer = parseATValue(resp)
	}
	if resp, err := sendAT(port, "AT+SBDMTA=1", iridiumReadTimeout); err != nil || !strings.Contains(resp, "OK") ||
		strings.Contains(resp, "ERROR") {
		return false, fmt.Errorf("AT+SBDMTA=1 not taken: %q, %v", strings.TrimSpace(resp), err)
	}
	// Outgoing messages are the delivery queue's, which keeps them for its
	// retries: one left in the modem would go out with the next session.
	if resp, err := sendAT(port, "AT+SBDD0", iridiumReadTimeout); !sbddCleared(resp, err) {
		return false, fmt.Errorf("AT+SBDD0 not taken: %q, %v", strings.TrimSpace(resp), err)
	}

	t.file = port
	t.connGen++ // a new link: the SBDD0 above emptied the MO buffer (sbdixLocked)
	if rs, ok := port.(interface{ SetRingHandler(func()) }); ok {
		rs.SetRingHandler(t.pipeRing)
		t.portRings = true
	}
	now := time.Now()
	t.connected = true
	t.awake = true
	t.lastWakeTime = now
	t.lastSBDIX = now // prevent a stale SBDIX from firing right after connect
	t.lastReply.Store(now.UnixNano())
	t.pipeCur.Store(&pipeCurPort{port: port})

	// The link is up (for the reads below, under this lock hold) before
	// the monitor starts.
	if resp, err := sendAT(port, "AT+SBDSX", 5*time.Second); err == nil {
		if st, err := parseSBDSX(resp); err == nil {
			log.Info().Bool("mt", st.MTFlag).Bool("ra", st.RAFlag).Int("waiting", st.MTWaiting).
				Msg("iridium: the node's modem holds (SBDSX)")
			if !st.MTFlag {
				// The MT buffer is empty, so no message read before is
				// still there (the node may have run sessions of its own).
				t.mtStale = nil
			} else {
				t.setupReadMTLocked(st.MTMSN)
			}
		}
	}
	log.Info().Str("imei", t.imei).Str("model", t.model).Str("firmware", t.firmware).Int("mt_waiting", len(t.mtHeld)).
		Msg("iridium modem connected over the MeshSat node's pipe")
	t.emitEvent(SatEvent{
		Type:    "connected",
		Message: fmt.Sprintf("Connected to %s through the MeshSat node (IMEI: %s, FW: %s)", t.model, t.imei, t.firmware),
		Time:    now.UTC().Format(time.RFC3339),
	})
	t.startMonitor()
	return len(t.mtHeld) > 0, nil
}

// pipeRing is an SBDRING the pipe saw in band: the only ring alert a node
// without a ring-indicator wire has.
func (t *DirectSatTransport) pipeRing() {
	now := time.Now()
	t.lastRingAlert.Store(&now)
	log.Info().Msg("iridium SBDRING received over the node's pipe")
	t.emitEvent(SatEvent{
		Type:    "ring_alert",
		Message: "MT message waiting at gateway",
		Time:    now.UTC().Format(time.RFC3339),
	})
	select {
	case t.ringCh <- struct{}{}:
	default:
	}
}

// pipeSBDRBLocked reads the MT buffer (AT+SBDRB) over the pipe: the echo
// when echo is on, [length 2B BE][message][checksum 2B BE], then OK. The
// pipe's notifications split the frame anywhere, so it is read until whole.
// An empty buffer gives an empty message. Caller holds t.mu; the monitor
// must not be reading.
func (t *DirectSatTransport) pipeSBDRBLocked() ([]byte, error) {
	port := t.file
	drainPort(port)
	cmd := []byte("AT+SBDRB\r")
	if _, err := port.Write(cmd); err != nil {
		return nil, fmt.Errorf("write failed: %w", err)
	}
	port.SetReadTimeout(50 * time.Millisecond)
	deadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 512)
	var raw []byte
	start, need := -1, -1
	for time.Now().Before(deadline) {
		n, err := port.Read(buf)
		if err != nil {
			return nil, err
		}
		raw = append(raw, buf[:n]...)
		if start < 0 {
			switch {
			case len(raw) < len(cmd) && bytes.Equal(raw, cmd[:len(raw)]):
				continue // the echo (or nothing) so far
			case bytes.HasPrefix(raw, cmd):
				start = len(cmd)
			default:
				start = 0
			}
		}
		if need < 0 && len(raw) >= start+2 {
			length := int(binary.BigEndian.Uint16(raw[start:]))
			if length > 270 {
				return nil, fmt.Errorf("SBDRB length %d is more than the 9603 holds", length)
			}
			need = start + 2 + length + 2
		}
		if need < 0 || len(raw) < need {
			continue
		}
		data := raw[start+2 : need-2]
		var sum uint16
		for _, b := range data {
			sum += uint16(b)
		}
		if !bytes.Contains(raw[need:], []byte("OK")) {
			readATResponse(port, 2*time.Second) // the OK after the frame
		}
		if sum != binary.BigEndian.Uint16(raw[need-2:need]) {
			return nil, errors.New("SBDRB checksum mismatch")
		}
		return append([]byte(nil), data...), nil
	}
	return nil, errors.New("SBDRB timed out")
}

// pipeReadMTLocked is readMTLocked's read over the node's pipe, and the
// set-up's free read: the MT buffer read whole (pipeSBDRBLocked), cleared
// (AT+SBDD1) after a good read, and a message the clear did not take marked
// already read (mtStale: its modem, MTMSN and hash), so it is never handed
// over twice (MESHSAT-1266, MESHSAT-1427). The buffer is read before
// anything is cleared, whatever the mark says: the mark outlives the link,
// and the node runs sessions of its own while the Bridge is away (and gives
// the modem back before it reads what they brought in), another node may be
// chosen, and two messages can be alike in every byte. Only the very
// message marked, in the very modem, is cleared again unread; anything else
// is new and handed over. mtmsn is the message's MTMSN when the caller
// knows it, else it is asked of the modem (AT+SBDSX, free). An empty buffer
// gives nil. Caller holds t.mu with the link up, and the monitor is not
// reading.
func (t *DirectSatTransport) pipeReadMTLocked(mtmsn int) ([]byte, error) {
	if mtmsn < 0 {
		mtmsn = t.pipeMTMSNLocked()
	}
	data, err := t.pipeSBDRBLocked()
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		// Nothing in the buffer: whatever a mark was set for is gone.
		t.mtStale = nil
		return nil, nil
	}
	if t.mtStale.matches(t.connIMEI, mtmsn, data) {
		// Handed over already, its clear did not go through: cleared again
		// now, never handed over twice (MESHSAT-1427).
		log.Info().Int("mtmsn", mtmsn).Msg("iridium: the modem still holds a message already handed over; cleared again")
		if t.clearMTLocked() {
			t.mtStale = nil
		}
		return nil, nil
	}
	t.mtStale = nil
	if !t.clearMTLocked() {
		t.mtStale = newMTReadMark(t.connIMEI, mtmsn, data)
	}
	return data, nil
}

// pipeMTMSNLocked asks the modem the MTMSN of the message in its MT buffer
// (AT+SBDSX, free); -1 when it does not say. Caller holds t.mu with the link
// up.
func (t *DirectSatTransport) pipeMTMSNLocked() int {
	resp, err := sendAT(t.file, "AT+SBDSX", 5*time.Second)
	if err != nil {
		return -1
	}
	st, err := parseSBDSX(resp)
	if err != nil {
		return -1
	}
	return st.MTMSN
}

// setupReadMTLocked is the set-up's free read of a message waiting in the
// MT buffer (mtmsn: its MTMSN as the set-up's AT+SBDSX gave it): read and
// matched against the already-read mark as every read over the pipe is
// (pipeReadMTLocked), and a message this Bridge has not handed over is held
// for Receive. A read that fails leaves the message in the modem, and the
// mark with it: the next read compares again. Caller holds t.mu with the
// link up.
func (t *DirectSatTransport) setupReadMTLocked(mtmsn int) {
	data, err := t.pipeReadMTLocked(mtmsn)
	if err != nil {
		log.Warn().Err(err).Msg("iridium: the message in the modem could not be read yet; it stays there")
		return
	}
	if len(data) > 0 {
		t.mtHeld = append(t.mtHeld, data)
	}
}

// sbddCleared reads an AT+SBDD answer: 0 (cleared) then OK. 1 is the modem
// failing to clear.
func sbddCleared(resp string, err error) bool {
	if err != nil || strings.Contains(resp, "ERROR") || !strings.Contains(resp, "OK") {
		return false
	}
	for _, line := range strings.Split(resp, "\n") {
		if strings.TrimSpace(line) == "1" {
			return false
		}
	}
	return true
}

// pipeDisconnectedLocked forgets what belonged to the connection that ended:
// the port PortGone would close, and the ring handler. Messages read out of
// the modem stay held (mtHeld) for Receive whatever the link does: each was
// cleared from the modem after its read, or is marked already read there
// (mtStale), which the next set-up honours. Nothing to forget for a serial
// modem. Caller holds t.mu.
func (t *DirectSatTransport) pipeDisconnectedLocked() {
	t.pipeCur.Store(nil)
	t.portRings = false
	if t.opener != nil {
		// A line that ended while the node still gives this Bridge its
		// modem (a session cut, a port error) is probed again at once, not
		// at the next resync.
		t.kickPipe()
	}
}

// pipeCutResult is what a session over the node's pipe whose answer never
// came back (the link went under it, or the write of AT+SBDIX failed where
// it may have landed) returns beside its error while its outcome is not
// known: MO status MOStatusUnknown. The node finishes such a session by
// itself and keeps its result.
func pipeCutResult() *SBDResult {
	return &SBDResult{
		MOStatus:   MOStatusUnknown,
		MOMSN:      -1,
		MTStatus:   -1,
		MTMSN:      -1,
		StatusText: "the session's answer never reached this Bridge",
	}
}

// pipeSessionMark is the node's account of its modem (STATS) as a session
// over its pipe starts: what settles the session should its answer never
// reach this Bridge.
type pipeSessionMark struct {
	before    PipeStats
	readAt    time.Time // when the account was read
	writtenAt time.Time // when AT+SBDIX was written, or its write failed
}

// pipeSessionMarkLocked reads the node's account before an SBDIX over its
// pipe. Nil when the node gives none (no STATS, a read that failed): a
// session cut then ends with its outcome unknown. An error, which wraps
// ErrNotConnected, when the node says a session is in flight: none is
// opened into it. A flag the node has kept past pipeFlightCap with no
// session started since holds nothing back (bleLink.nodeStats: the node
// never clears it while this Bridge holds the modem). Caller holds t.mu.
func (t *DirectSatTransport) pipeSessionMarkLocked() (*pipeSessionMark, error) {
	if t.nodeStats == nil {
		return nil, nil
	}
	// The session goes out whatever the caller's context says from here, so
	// the read has a deadline of its own.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	st, err := t.nodeStats(ctx)
	cancel()
	if err != nil {
		log.Debug().Err(err).Msg("iridium: the node's account of its modem could not be read before the session")
		return nil, nil
	}
	if st.Flags.SessionInFlight {
		return nil, fmt.Errorf("no session: the node says one is in flight on its modem: %w", ErrNotConnected)
	}
	return &pipeSessionMark{before: *st, readAt: time.Now()}, nil
}

// SetLifetime gives the transport the Bridge's own lifetime: a send waiting
// to settle a cut session from the node's account gives up when it ends
// (settleCutSession), so its delivery ends given up before the process
// exits, never 'sending' for the next start. Not the send's own context:
// the delivery worker's is cancelled by the very detach a dropped link
// causes (the worker is stopped while the node's modem is away), and the
// settle is for exactly that link coming back.
func (t *DirectSatTransport) SetLifetime(ctx context.Context) {
	t.mu.Lock()
	t.lifetime = ctx
	t.mu.Unlock()
}

// settleLocked settles a send's session from the node's account
// (settleCutSession) with mu let go and the session lock kept. A new +SBDIX
// answer in the account means the session ran, which emptied the MT buffer
// as it started, so an already-read mark goes, unless a set-up looked at
// the buffer meanwhile (a later link): the mark it left is newer. Caller
// holds t.sessionMu and t.mu.
func (t *DirectSatTransport) settleLocked(mark *pipeSessionMark, cause error) (*SBDResult, error) {
	read, life, gen := t.nodeStats, t.lifetime, t.connGen
	t.mu.Unlock()
	res, ran, err := t.settleCutSession(mark, read, life, cause)
	t.mu.Lock()
	if ran && t.connGen == gen {
		t.mtStale = nil
	}
	return res, err
}

// settleCutSession learns the outcome of a session over the node's pipe
// whose answer never reached this Bridge, from the node's own account: the
// node reads the answer off its modem whether or not it reaches the Bridge,
// holds the modem for it when the link dropped, and keeps the result
// (STATS). It waits, at most pipeOutcomeWait from the AT+SBDIX, for an
// account that says no session is in flight, and compares it with the one
// read before the session (judgeCutSession): the session's own answer
// (ran: a new +SBDIX, MO status 0-4 sent exactly once), a session that sent
// nothing (a failure, or errSBDIXRefused: the queue may try again), or
// ErrOutcomeUnknown beside MO status MOStatusUnknown: the message may have
// been sent, and the queue never sends it again by itself. It gives up,
// unknown, when the node is being forgotten or replaced, the transport
// closes, or life ends (the Bridge stops).
//
// Called with the session lock held and mu let go, so the pipe loop brings
// the line back meanwhile. cause is the error that cut the session. The
// settle assumes this Bridge is the node's one client: another phone's
// session on the same node would count as this one's (the firmware has no
// per-client session count).
func (t *DirectSatTransport) settleCutSession(mark *pipeSessionMark, read func(context.Context) (*PipeStats, error), life context.Context, cause error) (*SBDResult, bool, error) {
	unknown := func(why string) (*SBDResult, bool, error) {
		log.Warn().Str("why", why).Msg("iridium: the outcome of the satellite session is unknown; the message may have been sent and is not sent again by itself")
		return pipeCutResult(), false, fmt.Errorf("SBDIX failed: %w (%s): %w", ErrOutcomeUnknown, why, cause)
	}
	if mark == nil || read == nil {
		return unknown("the node gave no account of its sessions before this one")
	}
	var stopping <-chan struct{}
	if life != nil {
		stopping = life.Done()
	}
	deadline := mark.writtenAt.Add(pipeOutcomeWait)
	for {
		if t.abandoning.Load() > 0 {
			return unknown("the node is being forgotten or replaced")
		}
		select {
		case <-stopping:
			return unknown("the Bridge is stopping")
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		after, err := read(ctx)
		cancel()
		if err == nil {
			v := judgeCutSession(mark, after)
			switch {
			case v.res != nil:
				t.mu.Lock()
				if v.res.MOStatus == 32 || v.res.MOStatus == 36 {
					t.holdSBDIXUntil(time.Now().Add(SBDIXHold))
				}
				t.mu.Unlock()
				log.Info().Int("mo_status", v.res.MOStatus).Int("momsn", v.res.MOMSN).Bool("new_answer", v.ran).
					Msg("iridium: the outcome of the satellite session, read from the node")
				return v.res, v.ran, nil
			case v.notSent != nil:
				log.Info().Err(v.notSent).Msg("iridium: the satellite session sent nothing, by the node's account")
				return nil, false, fmt.Errorf("SBDIX failed: %w", v.notSent)
			case v.final:
				return unknown(v.why)
			}
		}
		if time.Now().After(deadline) {
			return unknown(fmt.Sprintf("no account of the session from the node within %s", pipeOutcomeWait))
		}
		timer := time.NewTimer(pipeOutcomePoll)
		select {
		case <-t.pipeStop:
			timer.Stop()
			return unknown("the transport closed")
		case <-stopping:
			timer.Stop()
			return unknown("the Bridge is stopping")
		case <-timer.C:
		}
	}
}

// cutVerdict is what the node's accounts say of a session whose answer
// never reached this Bridge.
type cutVerdict struct {
	// res is the session's outcome as the node read it; ran when it is the
	// session's own new +SBDIX answer (the session ran).
	res *SBDResult
	ran bool
	// notSent: the session sent nothing and has no MO status to show (the
	// modem answered ERROR).
	notSent error
	// final: settled (res, notSent), or the outcome can no longer be
	// learnt (why); else a later account is waited for.
	final bool
	why   string
}

// judgeCutSession compares the node's account after a cut session with the
// one before it. With exactly one session more, none of them the node's
// own, and no restart:
//
//   - a last result that is not the one before is the session's answer;
//   - no new result, but a session event on the node's clock after the one
//     before (the ERROR that ends a session, or an answer the same as the
//     one before, which only a failure can be: a failure keeps the MOMSN):
//     nothing was sent, a failure the queue may retry;
//   - no new result and no such event (the node's own cap ended the session
//     once its client was gone, or the flag is stuck: the modem never
//     answered): unknown.
//
// Anything else is unknown, or not known yet (a session in flight, an
// account that may predate the session).
func judgeCutSession(m *pipeSessionMark, a *PipeStats) cutVerdict {
	b := &m.before
	unknown := func(why string) cutVerdict { return cutVerdict{final: true, why: why} }
	switch {
	case a.UptimeS < b.UptimeS || a.Sessions < b.Sessions:
		return unknown("the node restarted")
	case a.Flags.SessionInFlight:
		return cutVerdict{}
	case a.NodeSessions != b.NodeSessions:
		return unknown("the node ran sessions of its own meanwhile")
	}
	switch a.Sessions - b.Sessions {
	case 0:
		// The node counts a session as its AT+SBDIX reaches the modem, and
		// its account is rebuilt every 2 s: only one built surely after the
		// write says the session never started, and then its bytes may
		// still land late. Unknown, either way, once it is that late.
		if !pipeAccountAfter(m, a) {
			return cutVerdict{}
		}
		return unknown("the node saw no session start")
	case 1:
	default:
		return unknown("other sessions ran on the node's modem meanwhile")
	}
	if a.LastMOStatus != nil && pipeResultChanged(b, a) {
		mo := *a.LastMOStatus
		mt := -1
		if a.LastMTStatus != nil {
			mt = *a.LastMTStatus
		}
		return cutVerdict{res: &SBDResult{
			MOStatus:   mo,
			MOMSN:      a.LastMOMSN,
			MTReceived: mt == 1,
			MTStatus:   mt,
			MTMSN:      -1,
			MTQueued:   a.LastMTQueued,
			StatusText: sbdixResult{moStatus: mo}.statusText() + " (read from the node)",
		}, ran: true, final: true}
	}
	// One session more, and no new answer to show for it.
	switch {
	case a.flightStuck:
		return unknown("the session never answered")
	case !pipeNewerEvent(b, a):
		// The flag went with no answer and no ERROR: the node gave the
		// session up at its own cap once its client was gone.
		return unknown("the node gave up waiting for the session's answer")
	case a.LastMOStatus != nil && *a.LastMOStatus > 4:
		// The answer before, a failure, answered again (or an ERROR after
		// it): nothing went out.
		mo := *a.LastMOStatus
		return cutVerdict{res: &SBDResult{
			MOStatus:   mo,
			MOMSN:      a.LastMOMSN,
			MTStatus:   -1,
			MTMSN:      -1,
			StatusText: fmt.Sprintf("MO status %d, as the session before (read from the node)", mo),
		}, final: true}
	default:
		// A success always answers a new MOMSN, and there was none: the
		// modem answered ERROR, which runs no session.
		return cutVerdict{notSent: errSBDIXRefused, final: true}
	}
}

// pipeNewerEvent reports whether the node's last session event (an answer,
// or the ERROR that ends a session; the node's cap ends one without either)
// is surely later in the account after than in the one before: its time on
// the node's clock (uptime less the event's age) two seconds later or more,
// past the truncation of both.
func pipeNewerEvent(b, a *PipeStats) bool {
	if a.LastSessionAgeS == nil {
		return false
	}
	if b.LastSessionAgeS == nil {
		return true
	}
	at := int64(a.UptimeS) - int64(*a.LastSessionAgeS)
	bt := int64(b.UptimeS) - int64(*b.LastSessionAgeS)
	return at >= bt+2
}

// pipeAccountAfter reports whether an account was surely built after the
// session's AT+SBDIX reached the node: its uptime past the one before by
// the time between the read and the write (whole seconds, rounded up), the
// 2 s the node may have taken to rebuild the account before, a second for
// the truncation of its uptime, and a second for the node to count the
// command.
func pipeAccountAfter(m *pipeSessionMark, a *PipeStats) bool {
	gap := m.writtenAt.Sub(m.readAt)
	if gap < 0 {
		gap = 0
	}
	need := uint64(m.before.UptimeS) + uint64((gap+time.Second-1)/time.Second) + 4
	return uint64(a.UptimeS) >= need
}

// pipeResultChanged reports whether the node's last session result differs
// from the one before: only an +SBDIX answer sets it, and a successful
// session's answer always differs from the one before it (a new MOMSN after
// a success, a new MO status after a failure), so a result that did not
// change is no answer of this session (an SBDIX refused with ERROR, one the
// node gave up on at its cap), or a failure answered as the one before.
func pipeResultChanged(b, a *PipeStats) bool {
	eq := func(x, y *int) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	return !eq(b.LastMOStatus, a.LastMOStatus) || b.LastMOMSN != a.LastMOMSN ||
		!eq(b.LastMTStatus, a.LastMTStatus) || b.LastMTQueued != a.LastMTQueued
}
