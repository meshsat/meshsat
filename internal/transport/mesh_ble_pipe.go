package transport

import (
	"context"
	"errors"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/rs/zerolog/log"
	"go.bug.st/serial"
)

// The node link's side of the MeshSat node's modem pipe (ble_pipe.go): the
// pipe runs on the mesh session's device path, STATS is followed whoever holds
// the modem, the modem is claimed while the switch "Use the node's modem" is
// on and handed back once it goes off, as soon as no satellite session of
// this Bridge runs (the SBD transport's Quiesce: the modem is given back,
// and the link to a node that is forgotten or replaced ended, only through
// it), and the SBD transport is told when the node gives it the modem and
// when it does not any more (MESHSAT_IRIDIUM_PORT=ble). As MeshSat Android's
// GatewayService does (observeIridiumPipe, v2.19.4). [MESHSAT-1391]

// satPipeDriver is the SBD transport as the node's pipe drives it.
type satPipeDriver interface {
	// PortReady: the node gives this Bridge its modem now.
	PortReady()
	// PortGone: it does not any more (given back, taken, the link lost).
	PortGone()
	// PortLeaving: the modem goes back to the node once no session runs
	// (the switch went off). No session starts from here.
	PortLeaving()
	// Quiesce runs fn once no satellite session and no modem command runs
	// on the line, and keeps them out until fn returns: the transport's
	// session lock, then its serial lock. The modem is given back, and the
	// link to the node ended, only through it: a release in the middle of
	// an SBDIX cut the session on this side while the node finished it,
	// the send failed and the queue sent the message again.
	Quiesce(fn func())
	// QuiesceAbandoning is Quiesce for a node that is being forgotten or
	// replaced: a send waiting to learn the outcome of a session whose link
	// dropped gives up at once (it waits for that node), its outcome
	// unknown.
	QuiesceAbandoning(fn func())
}

// startPipe opens the pipe of a session that carries one and runs it until
// the session is lost.
func (l *bleLink) startPipe(bus *bluezBus, device dbus.ObjectPath, s *bleGattSession) {
	g, err := bus.openPipe(device, s.lost)
	if err != nil {
		log.Warn().Err(err).Msg("iridium pipe: the node's modem pipe is not usable")
		l.mu.Lock()
		if l.session == s {
			l.pipe = false
		}
		l.mu.Unlock()
		return
	}
	l.runPipeOn(g)
}

// runPipeOn makes g the node's pipe until its link is lost. Tests hand it an
// in-memory node.
func (l *bleLink) runPipeOn(g pipeGATT) *blePipe {
	var p *blePipe
	p = newBLEPipe(g, l.pipeHealth, func(owner string) { l.pipeOwnerChanged(p, owner) }, l.pipeFault)
	l.mu.Lock()
	old := l.pipeSess
	l.pipeSess = p
	l.mu.Unlock()
	if old != nil {
		old.close()
	}
	go p.run()
	go l.claimLoop(p)
	l.syncAvailability()
	return p
}

// claimLoop follows STATS, and keeps the modem claimed while the switch is on
// and the SBD transport rides the pipe: a claim can race the link's
// encryption or the node's config dump and go unanswered, and a node busy with
// its own session hands over later, so the claim is asked again (soon after
// the link came up, then every pipeClaimRetry). The switch off gives the
// modem back. Ends with the link.
func (l *bleLink) claimLoop(p *blePipe) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-p.g.lost():
			cancel()
		case <-ctx.Done():
		}
	}()
	defer l.dropPipe(p)

	sctx, scancel := context.WithTimeout(ctx, 10*time.Second)
	p.watchStats(sctx)
	scancel()

	attempt := 0
	for {
		wait := time.Hour // until the switch moves
		wanted := l.claimWanted()
		if wanted && p.Owner() == PipeOwnerPhone && p.flightStuck() {
			l.unstickModem(ctx, p)
			// The switch may have gone off while the node had its modem
			// back: no claim against it.
			wanted = l.claimWanted()
		}
		if wanted {
			p.keepClaim(ctx)
			wait = pipeClaimRetry
			if attempt == 0 {
				wait = pipeClaimFirstRetry
			}
			attempt++
		} else {
			if p.holds() {
				l.releaseModem(p)
			}
			attempt = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-l.pipeKick:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// claimWanted: the switch is on and the SBD transport rides the pipe. A node
// adopted without MESHSAT_IRIDIUM_PORT=ble keeps its modem.
func (l *bleLink) claimWanted() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	return !l.satOff && l.satDriver != nil
}

// pipeOwnerChanged passes a change of owner of the current pipe to the SBD
// transport. A modem the node gives this Bridge while the switch says to
// leave it to the node (a claim whose subscription landed after it was
// called off, a release the node did not take) wakes the claim loop, which
// gives it back.
func (l *bleLink) pipeOwnerChanged(p *blePipe, owner string) {
	l.mu.Lock()
	current, d, off := l.pipeSess == p, l.satDriver, l.satOff
	l.mu.Unlock()
	if !current {
		return
	}
	if owner == PipeOwnerPhone && (off || d == nil) {
		l.kickClaim()
	}
	if d == nil {
		return
	}
	if owner == PipeOwnerPhone && !off {
		d.PortReady()
		return
	}
	d.PortGone()
}

// releaseModem gives the modem back to the node, through the SBD
// transport's Quiesce when it rides the pipe: a session in flight ends with
// its own answer first (or, its link dropped, its outcome settled), and an
// MT read with its message, and the transport drops the line before the
// locks go. Nothing is given back when the switch went on again meanwhile.
func (l *bleLink) releaseModem(p *blePipe) {
	l.mu.Lock()
	d := l.satDriver
	l.mu.Unlock()
	release := func() {
		if l.claimWanted() || !p.holds() {
			return
		}
		p.release()
		if d != nil && l.isCurrentPipe(p) {
			d.PortGone()
		}
	}
	if d == nil {
		release()
		return
	}
	d.Quiesce(release)
}

// pipeUnstickWait is how long unstickModem leaves the node its modem before
// the claim takes it again: past the node's release debounce (2 s), within
// which a new subscription keeps the modem, and the stuck flag with it.
var pipeUnstickWait = 3 * time.Second

// unstickModem gives the modem back to the node, and lets the claim take it
// again, when the node has said a session is in flight for longer than one
// runs, none started since (blePipe.flightStuckLocked): the modem never
// answered it, and the node clears its flag only once its client lets go
// and its own cap has passed. The release goes through Quiesce as every
// release does; the flag gets another pipeFlightCap, so a node that keeps it
// all the same is not given its modem back more often than that.
func (l *bleLink) unstickModem(ctx context.Context, p *blePipe) {
	log.Warn().Msg("iridium pipe: the node says a satellite session is in flight for longer than one runs; giving it the modem back to clear it, then taking it again")
	l.mu.Lock()
	d := l.satDriver
	l.mu.Unlock()
	release := func() {
		if !p.holds() {
			return
		}
		p.release()
		if d != nil && l.isCurrentPipe(p) {
			d.PortGone()
		}
	}
	if d == nil {
		release()
	} else {
		d.Quiesce(release)
	}
	p.restartFlightClock()
	timer := time.NewTimer(pipeUnstickWait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// endPipe ends the pipe of a node that is being forgotten or replaced,
// through the SBD transport's Quiesce: no session starts any more
// (PortLeaving: a send queued behind the one in flight finds the line gone
// instead of opening another billed session on the node that is going), a
// wait for the outcome of a session whose link already dropped is given up
// (that node is going, and another node's account must never settle it), a
// session in flight ends with its answer, and only then is the modem given
// back and the pipe (the one current then) closed, so its port is dead
// before anything else runs. The link itself is ended by the caller.
func (l *bleLink) endPipe() {
	l.mu.Lock()
	d := l.satDriver
	l.mu.Unlock()
	end := func() {
		l.mu.Lock()
		p := l.pipeSess
		l.mu.Unlock()
		if p == nil {
			return
		}
		if p.holds() {
			p.release()
		}
		p.close()
		l.mu.Lock()
		current := l.pipeSess == p
		if current {
			l.pipeSess = nil
		}
		l.mu.Unlock()
		if current && d != nil {
			d.PortGone()
		}
	}
	if d == nil {
		end()
	} else {
		d.PortLeaving()
		d.QuiesceAbandoning(end)
	}
	l.syncAvailability()
}

func (l *bleLink) isCurrentPipe(p *blePipe) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pipeSess == p
}

// dropPipe ends a pipe with its link.
func (l *bleLink) dropPipe(p *blePipe) {
	p.close()
	l.mu.Lock()
	current := l.pipeSess == p
	if current {
		l.pipeSess = nil
	}
	d := l.satDriver
	l.mu.Unlock()
	if current && d != nil {
		d.PortGone()
	}
	l.syncAvailability()
}

// pipeFault: writes stopped reaching the node's modem. The SBD transport drops
// the line, and the link to the node is dropped and made again, at most once
// every pipeRecoveryCooldown: a wedged pipe never mended itself on Android,
// the claim stayed answered (MESHSAT-1270).
func (l *bleLink) pipeFault() {
	l.mu.Lock()
	d := l.satDriver
	if l.recoverTimer == nil {
		wait := time.Until(l.recoverAt.Add(pipeRecoveryCooldown))
		if wait < 0 {
			wait = 0
		}
		l.recoverTimer = time.AfterFunc(wait, l.recoverPipe)
	}
	l.mu.Unlock()
	if d != nil {
		d.PortGone()
	}
}

func (l *bleLink) recoverPipe() {
	l.mu.Lock()
	l.recoverTimer, l.recoverAt = nil, time.Now()
	s, bus, address := l.session, l.bus, l.address
	l.mu.Unlock()
	if broken, _ := l.pipeHealth.state(); !broken || s == nil || s.isLost() || bus == nil || address == "" {
		return
	}
	log.Warn().Str("address", address).Msg("iridium pipe: the node's modem takes no writes; reconnecting to the node")
	s.markLost()
	bus.disconnect(bus.devicePath(address))
}

func (l *bleLink) kickClaim() {
	select {
	case l.pipeKick <- struct{}{}:
	default:
	}
}

// setSatellite keeps the switch with the node and acts on it at once. Off:
// no satellite session starts from here, a claim on its way stops, and the
// claim loop gives the modem back once the session in flight (if any) has
// ended with its answer (releaseModem). On: the claim loop claims it, and a
// modem the node still gives this Bridge is used again.
func (l *bleLink) setSatellite(enabled bool) {
	l.mu.Lock()
	l.load()
	changed := l.satOff == enabled
	l.satOff = !enabled
	p, d := l.pipeSess, l.satDriver
	l.mu.Unlock()
	l.save()
	switch {
	case !enabled && p != nil:
		if d != nil {
			d.PortLeaving()
		}
		p.cancelClaim()
	case enabled && changed && p != nil && d != nil && p.Owner() == PipeOwnerPhone:
		// Switched back on before the modem went back: no change of owner
		// will say it is this Bridge's.
		d.PortReady()
	}
	l.kickClaim()
	l.syncAvailability()
	if changed {
		log.Info().Bool("enabled", enabled).Msg("iridium pipe: use the node's modem")
	}
}

// satellitePort is the SBD transport's port opener: the pipe while the node
// gives this Bridge its modem.
func (l *bleLink) satellitePort(context.Context) (serial.Port, error) {
	l.mu.Lock()
	p, off := l.pipeSess, l.satOff
	l.mu.Unlock()
	if off {
		return nil, errors.New("the node keeps its modem: the switch is off")
	}
	if p == nil {
		return nil, errors.New("no MeshSat node with a modem pipe is connected")
	}
	return p.port()
}

// nodeStats reads the node's STATS now, through the current link's pipe,
// whoever holds the modem: the SBD transport settles a session whose answer
// the link lost with it, and opens none while one is in flight. A flag the
// node has kept for longer than a session runs, no session started since
// (the modem never answered one), no longer counts as a session in flight
// (flightStuck), with the cap blePipe.port applies: while this Bridge holds
// the modem the node never clears it, and every later session would wait
// for good. The claim loop gives the modem back so the node clears it
// (unstickModem).
func (l *bleLink) nodeStats(ctx context.Context) (*PipeStats, error) {
	l.mu.Lock()
	p := l.pipeSess
	l.mu.Unlock()
	if p == nil {
		return nil, ErrNoPipeStats
	}
	st, err := p.freshStats(ctx)
	if err != nil {
		return nil, err
	}
	if st.Flags.SessionInFlight && p.flightStuck() {
		st.Flags.SessionInFlight = false
		st.flightStuck = true
	}
	return st, nil
}

// satAvailable is the SBD gateway's cue: the link is up, the node carries a
// usable pipe, and the switch is on.
func (l *bleLink) satAvailable() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	return l.pipeSess != nil && !l.satOff && l.session != nil && !l.session.isLost()
}

// onSatAvailable has fn told whether the pipe is there for the SBD gateway,
// now and on every change, one call at a time and in order.
func (l *bleLink) onSatAvailable(fn func(bool)) {
	l.mu.Lock()
	l.availFn = fn
	if l.availKick == nil {
		l.availKick = make(chan struct{}, 1)
		go l.availabilityLoop(l.availKick)
	}
	l.mu.Unlock()
	l.syncAvailability()
}

func (l *bleLink) syncAvailability() {
	l.mu.Lock()
	kick := l.availKick
	l.mu.Unlock()
	if kick == nil {
		return
	}
	select {
	case kick <- struct{}{}:
	default:
	}
}

func (l *bleLink) availabilityLoop(kick <-chan struct{}) {
	applied, told := false, false
	for range kick {
		now := l.satAvailable()
		l.mu.Lock()
		fn := l.availFn
		l.mu.Unlock()
		if fn == nil || (told && now == applied) {
			continue
		}
		fn(now)
		applied, told = now, true
	}
}

// UseSatellitePipe makes the adopted node's modem pipe the line of sat
// (MESHSAT_IRIDIUM_PORT=ble): sat connects over it whenever the node gives
// this Bridge its modem and drops it when it does not. Without it the node
// keeps its modem. ErrMeshNotBLE when the mesh port is not Bluetooth.
func (t *DirectMeshTransport) UseSatellitePipe(sat *DirectSatTransport) error {
	if t.ble == nil {
		return ErrMeshNotBLE
	}
	t.ble.mu.Lock()
	t.ble.satDriver = sat
	t.ble.mu.Unlock()
	sat.SetNodeStats(t.ble.nodeStats)
	sat.SetPortOpener(t.ble.satellitePort)
	t.ble.kickClaim()
	return nil
}

// OnSatellitePipe has fn told whether the node's modem pipe is there for the
// SBD gateway (the link up with a usable pipe, the switch on), now and on
// every change, one call at a time and in order.
func (t *DirectMeshTransport) OnSatellitePipe(fn func(available bool)) {
	if t.ble != nil {
		t.ble.onSatAvailable(fn)
	}
}

// SatellitePipeAvailable reports the same, for the gateway manager's
// reconcile.
func (t *DirectMeshTransport) SatellitePipeAvailable() bool {
	return t.ble != nil && t.ble.satAvailable()
}

// BLESetSatellite sets the switch "Use the node's modem", kept with the
// node, and acts on it at once: off, no satellite session starts any more,
// and the modem goes back to the node as soon as the one in flight (if any)
// has ended with its answer.
func (t *DirectMeshTransport) BLESetSatellite(enabled bool) error {
	if t.ble == nil {
		return ErrMeshNotBLE
	}
	t.ble.setSatellite(enabled)
	return nil
}

// BLESatelliteStats is the node's STATS, read again when older than 10 s.
// ErrNoPipeStats for a node without them (or no node).
func (t *DirectMeshTransport) BLESatelliteStats(ctx context.Context) (*PipeStatsReading, error) {
	if t.ble == nil {
		return nil, ErrMeshNotBLE
	}
	t.ble.mu.Lock()
	p := t.ble.pipeSess
	t.ble.mu.Unlock()
	if p == nil {
		return nil, ErrNoPipeStats
	}
	return p.statsReading(ctx)
}
