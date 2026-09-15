package gateway

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// aprsEncryptedPrefix is the APRS-spec-compliant User-Defined Data
// Type used to wrap encrypted AX.25 payloads. `{` = APRS 1.0.1
// user-defined; `E1` = "Encrypted, format v1" (reserved for future
// rekey / envelope format changes). An unrelated igate sees an
// opaque user-defined blob and ignores it instead of choking on
// binary. Peers that recognise the prefix route the payload into
// the ingress transform chain for decryption.
const aprsEncryptedPrefix = "{E1}"

// APRSGateway bridges MeshSat messages to/from APRS via Direwolf KISS TCP.
type APRSGateway struct {
	config APRSConfig
	db     *database.DB
	kiss   *KISSConn
	inCh   chan InboundMessage
	outCh  chan *aprsOutbound
	rawOut chan []byte // ready AX.25 frames (status beacon, acks) sent by the write worker [MESHSAT-857]

	// Nil when APRSConfig.ExternalDirewolf is true — caller is responsible
	// for running Direwolf out-of-band. [MESHSAT-516]
	supervisor *DirewolfSupervisor

	connected atomic.Bool
	msgsIn    atomic.Int64
	msgsOut   atomic.Int64
	errors    atomic.Int64
	badFrames atomic.Int64 // KISS frames from the TNC that did not decode; dropped, link kept
	// Decoded frames from other stations that were not for this bridge and
	// stayed in the heard list instead of entering the message pipeline.
	// [MESHSAT-1128]
	thirdPartyDropped atomic.Int64
	lastActive        atomic.Int64
	startTime         time.Time

	tracker *APRSTracker

	// receiveState is set by the RxWatchdog (ok, quiet, deaf); empty
	// until the watchdog has judged. [MESHSAT-814]
	receiveState atomic.Value

	// Serial-TNC receive signals (no Direwolf to report an audio level):
	// every decoded frame stamps lastFrameAt. [MESHSAT-821]
	lastFrameAt atomic.Int64

	// Software listen-before-talk (Unix nanoseconds). lastPeerRX is the last
	// AX.25 frame decoded from any station but this one, so a TNC echo of our
	// own transmission never holds us; lastTX is the last frame handed to the
	// TNC. ctsDeferred counts transmissions the gate held back.
	// [MESHSAT-1021, MESHSAT-1069]
	lastPeerRX  atomic.Int64
	lastTX      atomic.Int64
	ctsDeferred atomic.Int64

	// Frame fan-out: a hardware TNC is one file handle, so the Reticulum
	// ax25_0 interface receives raw AX.25 payloads from this gateway's
	// reader instead of opening its own KISS connection. [MESHSAT-821]
	subMu sync.Mutex
	subs  map[uint64]chan []byte
	subID uint64

	// Live packet feed: every frame through the KISS link, both directions,
	// goes to the sink as a PacketRecord tagged with this gateway's
	// interface id. Nil sink = feed off. [MESHSAT-826]
	packetMu    sync.RWMutex
	packetSink  PacketSink
	packetIface string

	// Per-message acks for encrypted frames. done is closed when the running
	// gateway stops, so a Forward waiting for an ack returns. [MESHSAT-1021]
	acks         aprsAckWaiters
	ackLedger    aprsAckLedger
	acksSent     atomic.Int64
	acksReceived atomic.Int64
	ackRetries   atomic.Int64
	ackFailures  atomic.Int64
	lifeMu       sync.Mutex
	done         chan struct{}

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// SetPacketSink installs the live packet feed sink and the interface id
// (aprs_0) its records carry. Safe to call while the gateway runs. [MESHSAT-826]
func (g *APRSGateway) SetPacketSink(sink PacketSink, iface string) {
	g.packetMu.Lock()
	g.packetSink = sink
	g.packetIface = iface
	g.packetMu.Unlock()
}

// recordFrame hands one AX.25 frame to the packet feed. Never blocks the
// reader or writer beyond the sink's own ring insert.
func (g *APRSGateway) recordFrame(dir string, payload []byte, msgRef string) {
	g.packetMu.RLock()
	sink, iface := g.packetSink, g.packetIface
	g.packetMu.RUnlock()
	if sink == nil {
		return
	}
	sink(aprsPacketRecord(dir, iface, payload, msgRef))
}

// SubscribeFrames hands out a channel that receives every AX.25 payload the
// gateway reads from its TNC. The channel is closed when the gateway stops,
// which tells the subscriber to re-subscribe to the next gateway instance.
// Slow subscribers drop frames rather than stall the reader. [MESHSAT-821]
func (g *APRSGateway) SubscribeFrames() (<-chan []byte, func()) {
	ch := make(chan []byte, 32)
	g.subMu.Lock()
	if g.subs == nil {
		g.subs = make(map[uint64]chan []byte)
	}
	g.subID++
	id := g.subID
	g.subs[id] = ch
	g.subMu.Unlock()
	return ch, func() {
		g.subMu.Lock()
		if c, ok := g.subs[id]; ok {
			delete(g.subs, id)
			close(c)
		}
		g.subMu.Unlock()
	}
}

func (g *APRSGateway) fanOut(payload []byte) {
	g.subMu.Lock()
	defer g.subMu.Unlock()
	for _, ch := range g.subs {
		cp := make([]byte, len(payload))
		copy(cp, payload)
		select {
		case ch <- cp:
		default:
		}
	}
}

func (g *APRSGateway) closeSubscribers() {
	g.subMu.Lock()
	defer g.subMu.Unlock()
	for id, ch := range g.subs {
		delete(g.subs, id)
		close(ch)
	}
}

// SerialTNC reports whether this gateway drives a hardware TNC over serial.
func (g *APRSGateway) SerialTNC() bool { return g.kiss.Serial() }

// ReopenTNC closes and reopens the TNC link (serial or TCP). The receive
// watchdog's second rung and OOB RESET aprs level 3 for hardware-TNC kits,
// where a hub-port VBUS cut would not even reboot a PicoAPRS running on its
// own battery. [MESHSAT-821]
func (g *APRSGateway) ReopenTNC(ctx context.Context) error {
	_ = g.kiss.Close()
	g.connected.Store(false)
	if err := g.dialWithRetry(ctx, 20*time.Second); err != nil {
		return fmt.Errorf("aprs: reopen %s: %w", g.kiss.Target(), err)
	}
	g.connected.Store(true)
	log.Info().Str("kiss", g.kiss.Target()).Msg("aprs: TNC link reopened")
	return nil
}

// ReceiveHealth exposes the bundled supervisor's receive-side signals. The
// second value is false for an external Direwolf, where nothing is known.
func (g *APRSGateway) ReceiveHealth() (ReceiveHealth, bool) {
	if g.supervisor != nil {
		return g.supervisor.ReceiveHealth(), true
	}
	if !g.kiss.Serial() {
		return ReceiveHealth{}, false
	}
	// Hardware TNC: no audio level exists, only frames. Level -1 and a
	// zero LevelAt keep the watchdog's "hung Direwolf" branch off.
	h := ReceiveHealth{Running: g.connected.Load(), Level: -1, RxFrames: g.kiss.RX.Load(),
		Serial: true, BytesIn: g.kiss.BytesIn.Load(), LinkOpenedAt: g.kiss.OpenedAt()}
	if ts := g.lastFrameAt.Load(); ts > 0 {
		h.LastDecodeAt = time.Unix(0, ts)
	}
	return h, true
}

// SetReceiveState records the watchdog's verdict for the status endpoints.
func (g *APRSGateway) SetReceiveState(state string) { g.receiveState.Store(state) }

func (g *APRSGateway) currentReceiveState() string {
	if v, ok := g.receiveState.Load().(string); ok && v != "" {
		return v
	}
	if g.supervisor == nil && !g.kiss.Serial() {
		return ReceiveStateUnknown
	}
	return ""
}

// NewAPRSGateway creates a new APRS gateway.
func NewAPRSGateway(cfg APRSConfig, db *database.DB) *APRSGateway {
	var kiss *KISSConn
	if cfg.SerialTNC() {
		kiss = NewKISSSerialConn(cfg.KISSDevice, cfg.KISSBaud)
		cfg.ExternalDirewolf = true
	} else {
		kiss = NewKISSConn(fmt.Sprintf("%s:%d", cfg.KISSHost, cfg.KISSPort))
	}
	g := &APRSGateway{
		config:  cfg,
		db:      db,
		kiss:    kiss,
		inCh:    make(chan InboundMessage, 32),
		outCh:   make(chan *aprsOutbound, 10),
		rawOut:  make(chan []byte, 4),
		tracker: NewAPRSTracker(),
	}
	if !cfg.ExternalDirewolf {
		g.supervisor = NewDirewolfSupervisor(cfg)
	}
	return g
}

// KISSSendFrame sends a raw AX.25 frame via the APRS gateway's KISS connection.
// Used by the AX25 Reticulum interface to route TX through the same pipeline
// node, so all TX is counted by the KISSConn's atomic counter. [MESHSAT-403]
func (g *APRSGateway) KISSSendFrame(payload []byte) error {
	if err := g.kiss.SendFrame(payload); err != nil {
		return err
	}
	g.recordFrame(DirTX, payload, "")
	return nil
}

// Tracker returns the APRS heard station and activity tracker.
func (g *APRSGateway) Tracker() *APRSTracker {
	return g.tracker
}

// GetAPRSStatus returns aggregated status for the dashboard.
//
// `connected` is the operator-facing health signal: for bundled-Direwolf
// kits, both the supervisor and the KISS-TCP link must be up. For the
// legacy external-daemon mode, only KISS-TCP is checked.
//
// `rx`/`tx` are over-the-air frame counts sourced from the Direwolf
// supervisor's log parser when bundled (so direct KISS-injection tests
// and Dispatcher-originated frames are both visible). When direwolf is
// external, we fall back to the meshsat-level KISS counters — they miss
// externally injected frames but are the best proxy available.
// [MESHSAT-514]
func (g *APRSGateway) GetAPRSStatus() map[string]interface{} {
	kissUp := g.connected.Load()
	connected := kissUp
	if g.supervisor != nil {
		connected = kissUp && g.supervisor.Running()
	}
	uptime := ""
	if connected {
		uptime = time.Since(g.startTime).Round(time.Second).String()
	}
	var rx, tx int64
	if g.supervisor != nil {
		rx = g.supervisor.RxFrames()
		tx = g.supervisor.TxFrames()
	} else {
		rx = g.kiss.RX.Load()
		tx = g.kiss.TX.Load()
	}
	status := map[string]interface{}{
		"connected":           connected,
		"kiss_up":             kissUp,
		"callsign":            FormatCallsign(AX25Address{Call: g.config.Callsign, SSID: g.config.SSID}),
		"frequency_mhz":       g.config.FrequencyMHz,
		"uptime":              uptime,
		"rx":                  rx,
		"tx":                  tx,
		"errors":              g.errors.Load(),
		"bad_frames":          g.badFrames.Load(),
		"repaired_frames":     g.kiss.Repaired.Load(),
		"third_party_dropped": g.thirdPartyDropped.Load(),
		"cts_deferred":        g.ctsDeferred.Load(),
		"acks_sent":           g.acksSent.Load(),
		"acks_received":       g.acksReceived.Load(),
		"ack_retries":         g.ackRetries.Load(),
		"ack_failures":        g.ackFailures.Load(),
		"heard_count":         len(g.tracker.GetHeardStations()),
		"packet_types":        g.tracker.GetPacketTypeBreakdown(),
		"kiss_addr":           g.kiss.Target(),
		"tnc_serial":          g.kiss.Serial(),
	}
	if g.kiss.Serial() {
		if ts := g.lastFrameAt.Load(); ts > 0 {
			status["last_decode_at"] = time.Unix(0, ts).UTC().Format(time.RFC3339)
		}
		status["tnc_bytes_in"] = g.kiss.BytesIn.Load()
		if at := g.kiss.OpenedAt(); !at.IsZero() {
			status["link_opened_at"] = at.UTC().Format(time.RFC3339)
		}
	}
	if g.supervisor != nil {
		status["direwolf_bundled"] = true
		status["direwolf_running"] = g.supervisor.Running()
		status["direwolf_restarts"] = g.supervisor.RestartCount()
		h := g.supervisor.ReceiveHealth()
		status["receive_level"] = h.Level
		if !h.LevelAt.IsZero() {
			status["receive_level_at"] = h.LevelAt.UTC().Format(time.RFC3339)
		}
		if !h.LastDecodeAt.IsZero() {
			status["last_decode_at"] = h.LastDecodeAt.UTC().Format(time.RFC3339)
		}
		status["audio_errors"] = h.AudioErrors
	} else {
		status["direwolf_bundled"] = false
	}
	if st := g.currentReceiveState(); st != "" {
		status["receive_state"] = st
	}
	return status
}

// Start launches the Direwolf subprocess (when bundled), then connects to
// its KISS server and starts the read/write workers.
//
// The supervisor gets a DETACHED context (not the caller's ctx). When
// Start is invoked from handlePutGateway via ConfigureInstance, the
// caller's ctx is the HTTP request ctx — cancelled the moment the PUT
// response returns, which would SIGTERM Direwolf 1-2 s after spawn.
// Gateway lifetime is controlled explicitly by Stop() instead.
// [MESHSAT-514, diagnosed 2026-04-17]
func (g *APRSGateway) Start(ctx context.Context) error {
	// Detached ctx owns the supervisor + workers. The caller's ctx is
	// only used to time-bound the initial dial (if a request-level
	// cancel comes in mid-dial, we abort dialing and Stop cleanly).
	bgCtx, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	g.startTime = time.Now()

	if g.supervisor != nil {
		if err := g.supervisor.Start(bgCtx); err != nil {
			g.cancel()
			return fmt.Errorf("aprs: direwolf supervisor: %w", err)
		}
	}

	// Direwolf binds KISS a few hundred ms after start; in the external
	// case the TNC is already up. Honour the caller's ctx for the dial
	// budget so a request-level cancel can abort the dial — but the
	// supervisor keeps running on bgCtx regardless.
	if err := g.dialWithRetry(ctx, 30*time.Second); err != nil {
		if g.supervisor != nil {
			g.supervisor.Stop()
		}
		g.cancel()
		return fmt.Errorf("aprs: %w", err)
	}
	g.connected.Store(true)

	g.lifeMu.Lock()
	g.done = make(chan struct{})
	g.lifeMu.Unlock()

	g.wg.Add(3)
	go g.readWorker(bgCtx)
	go g.writeWorker(bgCtx)
	go g.silenceWatchdog(bgCtx)
	if g.config.BeaconSecs > 0 {
		g.wg.Add(1)
		go g.beaconWorker(bgCtx)
	}

	log.Info().
		Str("kiss_addr", g.kiss.Target()).
		Bool("tnc_serial", g.kiss.Serial()).
		Str("callsign", FormatCallsign(AX25Address{Call: g.config.Callsign, SSID: g.config.SSID})).
		Float64("freq_mhz", g.config.FrequencyMHz).
		Msg("aprs gateway started")

	// Soft regulatory warning: encryption on an amateur-radio frequency
	// is commonly prohibited. One log line on startup is enough — not
	// per-frame. Checks egress_transforms on aprs_0 for the "encrypt"
	// transform name (matches the dispatcher's own detection heuristic
	// at dispatcher.go ~L1154).
	if g.db != nil {
		if iface, err := g.db.GetInterface("aprs_0"); err == nil && iface != nil &&
			strings.Contains(iface.EgressTransforms, "encrypt") &&
			IsLikelyAmateurBand(g.config.FrequencyMHz) {
			log.Warn().
				Float64("freq_mhz", g.config.FrequencyMHz).
				Msg("aprs: encryption enabled on a frequency inside an amateur-radio allocation — verify your licence permits this content before transmitting")
		}
	}
	return nil
}

// Stop shuts down the APRS gateway.
func (g *APRSGateway) Stop() error {
	g.lifeMu.Lock()
	if g.done != nil {
		close(g.done)
		g.done = nil
	}
	g.lifeMu.Unlock()
	if g.cancel != nil {
		g.cancel()
	}
	g.kiss.Close()
	g.wg.Wait()
	g.connected.Store(false)
	g.closeSubscribers()
	if g.supervisor != nil {
		g.supervisor.Stop()
	}
	log.Info().Msg("aprs gateway stopped")
	return nil
}

// dialWithRetry attempts to connect to the KISS server, retrying every 1s
// until budget is exhausted. Used only during Start — long-lived outages
// are handled by reconnect().
func (g *APRSGateway) dialWithRetry(ctx context.Context, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := g.kiss.Dial(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
	return fmt.Errorf("kiss dial timed out after %s: %w", budget, lastErr)
}

// Forward sends a MeshSat message over APRS. An encrypted message, which only a
// MeshSat peer can read, waits for the peer's ack and fails when none comes
// (forwardAcked); everything else is queued for the write worker and returns at
// once. [MESHSAT-1021]
func (g *APRSGateway) Forward(ctx context.Context, msg *transport.MeshMessage) error {
	if msg.Encrypted && g.config.ackAttempts() > 0 {
		return g.forwardAcked(ctx, msg)
	}
	return g.enqueue(msg)
}

// Enqueue submits a message for outbound delivery without waiting for an ack.
func (g *APRSGateway) Enqueue(msg *transport.MeshMessage) error {
	return g.enqueue(msg)
}

func (g *APRSGateway) enqueue(msg *transport.MeshMessage) error {
	select {
	case g.outCh <- &aprsOutbound{msg: msg}:
		return nil
	default:
		g.errors.Add(1)
		return fmt.Errorf("aprs outbound queue full")
	}
}

// Receive returns the inbound message channel.
func (g *APRSGateway) Receive() <-chan InboundMessage {
	return g.inCh
}

// Status returns the current gateway status.
//
// Connected is AND of KISS-TCP and (when bundled) supervisor-running —
// same definition as /api/aprs/status so the dashboard widget and the
// shared gateway list agree. MessagesIn/MessagesOut are OTA frame
// counters from the supervisor when bundled; otherwise the meshsat-
// originated counters (best-effort fallback for external mode).
func (g *APRSGateway) Status() GatewayStatus {
	kissUp := g.connected.Load()
	connected := kissUp
	if g.supervisor != nil {
		connected = kissUp && g.supervisor.Running()
	}
	var msgsIn, msgsOut int64
	if g.supervisor != nil {
		msgsIn = g.supervisor.RxFrames()
		msgsOut = g.supervisor.TxFrames()
	} else {
		msgsIn = g.msgsIn.Load()
		msgsOut = g.msgsOut.Load()
	}
	s := GatewayStatus{
		Type:        "aprs",
		Connected:   connected,
		MessagesIn:  msgsIn,
		MessagesOut: msgsOut,
		Errors:      g.errors.Load(),
	}
	if ts := g.lastActive.Load(); ts > 0 {
		s.LastActivity = time.Unix(ts, 0)
	}
	if connected && !g.startTime.IsZero() {
		s.ConnectionUptime = time.Since(g.startTime).Truncate(time.Second).String()
	}
	bundled := g.supervisor != nil
	s.DirewolfBundled = &bundled
	if bad := g.badFrames.Load(); bad > 0 {
		s.BadFrames = &bad
	}
	if g.supervisor != nil {
		running := g.supervisor.Running()
		restarts := g.supervisor.RestartCount()
		s.DirewolfRunning = &running
		s.DirewolfRestarts = &restarts
		h := g.supervisor.ReceiveHealth()
		level := h.Level
		s.ReceiveLevel = &level
		if !h.LevelAt.IsZero() {
			at := h.LevelAt.UTC()
			s.ReceiveLevelAt = &at
		}
		if !h.LastDecodeAt.IsZero() {
			at := h.LastDecodeAt.UTC()
			s.LastDecodeAt = &at
		}
	}
	if g.kiss.Serial() {
		if ts := g.lastFrameAt.Load(); ts > 0 {
			at := time.Unix(0, ts).UTC()
			s.LastDecodeAt = &at
		}
	}
	if st := g.currentReceiveState(); st != "" {
		s.ReceiveState = &st
	}
	return s
}

// Type returns the gateway type identifier.
func (g *APRSGateway) Type() string {
	return "aprs"
}

// readWorker reads APRS packets from Direwolf via KISS.
func (g *APRSGateway) readWorker(ctx context.Context) {
	defer g.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		payload, err := g.kiss.ReadFrame()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Timeout is normal — just retry
			if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
				continue
			}
			// A frame that does not decode is dropped and the link kept:
			// only a transport error (EOF, a vanished port) reopens it.
			var bad *kissFrameError
			if errors.As(err, &bad) {
				raw := bad.raw
				if len(raw) > 96 {
					raw = raw[:96]
				}
				log.Warn().Err(bad.err).Int("len", len(bad.raw)).Hex("frame", raw).
					Msg("aprs: dropped undecodable KISS frame")
				g.errors.Add(1)
				g.badFrames.Add(1)
				continue
			}
			log.Warn().Err(err).Msg("aprs: read frame error")
			g.errors.Add(1)
			g.connected.Store(false)
			g.reconnect(ctx)
			continue
		}

		g.lastFrameAt.Store(time.Now().UnixNano())
		g.fanOut(payload)
		g.recordFrame(DirRX, payload, "")

		frame, err := DecodeAX25Frame(payload)
		if err != nil {
			log.Debug().Err(err).Msg("aprs: decode AX.25")
			continue
		}
		// A decoded frame from another station means the channel was busy
		// just now: the transmit gate holds off after it. Our own callsign is
		// excluded so a TNC echo of our transmission does not hold us.
		// [MESHSAT-1021, MESHSAT-1069]
		if frame != nil && !g.isOwnSource(frame.Src) {
			g.lastPeerRX.Store(time.Now().UnixNano())
		}

		srcAddr := ""
		if frame != nil {
			srcAddr = FormatCallsign(frame.Src)
		}

		// Encrypted-APRS ingress branch: bypass the APRS parser entirely
		// when the info field is wrapped with our `{E1}` user-defined-
		// data-type prefix. The parser's job is to decode APRS
		// semantics (position, message, telemetry); for ciphertext
		// there are no semantics to parse. Emit the raw base64 payload
		// as InboundMessage.Text and let StartGatewayReceiver apply the
		// interface's ingress_transforms (base64→decrypt→decompress).
		// Keeping the parser pure also avoids its printable-char
		// heuristic accidentally throwing away a frame whose base64
		// happens to look like padding.
		if frame != nil && len(frame.Info) >= len(aprsEncryptedPrefix) &&
			string(frame.Info[:len(aprsEncryptedPrefix)]) == aprsEncryptedPrefix {
			if srcAddr != "" {
				g.tracker.RecordAX25(srcAddr, "")
			}
			// A trailing message id asks for an ack. Our own frame echoed by
			// the TNC is never acked. [MESHSAT-1021]
			body, ackID := splitAckRequest(string(frame.Info[len(aprsEncryptedPrefix):]))
			if ackID != "" && !g.isOwnSource(frame.Src) {
				g.sendAck(frame.Src, ackID)
			}
			msg := InboundMessage{
				Text:     body,
				Source:   "aprs",
				FromAddr: srcAddr,
			}
			select {
			case g.inCh <- msg:
				g.msgsIn.Add(1)
				g.lastActive.Store(time.Now().Unix())
			default:
				log.Warn().Msg("aprs: inbound channel full (encrypted frame dropped)")
			}
			continue
		}

		pkt, err := ParseAPRSPacket(frame)
		if err != nil {
			// AX.25 decode succeeded but the payload isn't APRS-formatted
			// (Reticulum, FlexNet, TNC beacons, custom protocols).
			// Operators still want to see WHO is on the air — record the
			// AX.25 source + path so the widget lists every station heard
			// on 144.8 MHz regardless of payload type. [operator widget fix]
			if frame != nil && frame.Src.Call != "" {
				pathParts := make([]string, 0, len(frame.Path))
				for _, p := range frame.Path {
					pathParts = append(pathParts, FormatCallsign(p))
				}
				g.tracker.RecordAX25(srcAddr, strings.Join(pathParts, ","))
			}
			log.Debug().Err(err).Msg("aprs: parse APRS")
			continue
		}

		// Track heard station and activity [MESHSAT-403]
		g.tracker.RecordRX(pkt)

		// APRS acks and rejects are protocol traffic, not messages: an ack for
		// one of our frames releases its sender, and none is ever forwarded to
		// the mesh. [MESHSAT-1021]
		if id, reject, ok := aprsAckReply(pkt); ok {
			g.handleAckReply(pkt, id, reject)
			continue
		}

		// Status frames ('>', the peer kit's beacon and any station's status
		// report) are liveness, not messages: they update the heard list and
		// the receive health and stop here, so an aprs -> mesh relay rule
		// never forwards a beacon to the handhelds. [MESHSAT-857]
		if pkt.DataType == '>' {
			continue
		}

		// Anything else from a station that is not talking to this bridge
		// (a passing station's position, weather, object, or a message to
		// someone else) is heard-list only: an aprs -> mesh relay rule must
		// never put it on the handhelds or the booth screen. [MESHSAT-1128]
		if !g.relayable(pkt) {
			g.thirdPartyDropped.Add(1)
			log.Debug().Str("from", pkt.Source).Str("type", string(pkt.DataType)).
				Msg("aprs: third-party frame kept out of the message pipeline")
			continue
		}

		text := g.formatInboundText(pkt)
		msg := InboundMessage{
			Text:     text,
			Source:   "aprs",
			FromAddr: srcAddr,
		}

		select {
		case g.inCh <- msg:
			g.msgsIn.Add(1)
			g.lastActive.Store(time.Now().Unix())
		default:
			log.Warn().Msg("aprs: inbound channel full")
		}
	}
}

// writeWorker sends MeshSat messages as APRS packets via KISS.
func (g *APRSGateway) writeWorker(ctx context.Context) {
	defer g.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case item := <-g.outCh:
			g.sendMessage(ctx, item)
		case frame := <-g.rawOut:
			g.sendRaw(ctx, frame)
		}
	}
}

// beaconWorker transmits the status beacon every BeaconSecs. The frame is
// plain APRS (status data type '>'), no digipeater path: it is meant for
// the peer kit a few metres away and for anyone listening on 144.800, and
// it is the liveness signal the peer's receive watchdog expects on a
// two-kit network, so an idle booth never reads as a deaf receiver. The
// first beacon goes out a few seconds after start. [MESHSAT-857]
func (g *APRSGateway) beaconWorker(ctx context.Context) {
	defer g.wg.Done()
	interval := time.Duration(g.config.BeaconSecs) * time.Second
	// The wait between beacons is jittered by ±20 percent. Two kits with
	// the same fixed period keep whatever phase they happen to start with,
	// and if that phase puts each beacon inside the peer's own transmission
	// (a half-duplex radio hears nothing while it keys) every beacon is
	// lost until something restarts a gateway. Measured 10 Sep 2026 on the
	// PicoAPRS chain: identical ~34 s periods on both kits. Randomising the
	// beacon is also plain APRS practice. The receive watchdog allows
	// 3 min of silence, so 36 s plus the repeat copy is nowhere near it.
	first := time.NewTimer(5 * time.Second)
	defer first.Stop()
	select {
	case <-ctx.Done():
		return
	case <-first.C:
	}
	n := 0
	for {
		n++
		frame := g.beaconFrame(n)
		// The beacon is the peer's liveness signal, and a single short
		// frame drops in 20 to 30 percent of cases on the kit chain even at
		// a good level (measured 7 Sep 2026), so it gets the same repeat as
		// a message: copies are the cheapest insurance against a false
		// "deaf" verdict on the far kit. [MESHSAT-857]
		copies := g.config.TXRepeat
		if copies < 1 {
			copies = 1
		}
		// Hold the beacon back while the channel is busy. The beacon is
		// liveness only: a few seconds late costs nothing, and going out on
		// top of a message costs that message both copies. [MESHSAT-1021]
		if !g.waitForQuietChannel(ctx) {
			return
		}
		for i := 0; i < copies; i++ {
			if i > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(jitterDuration(g.beaconRepeatGap(), 0.2)):
				}
			}
			select {
			case g.rawOut <- frame:
			default:
				log.Debug().Msg("aprs: beacon skipped, transmit queue busy")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitterDuration(interval, 0.2)):
		}
	}
}

// jitterDuration returns d scaled by a uniform random factor in
// [1-spread, 1+spread]. A non-positive d or spread comes back unchanged.
func jitterDuration(d time.Duration, spread float64) time.Duration {
	if d <= 0 || spread <= 0 {
		return d
	}
	f := 1 + spread*(2*rand.Float64()-1)
	return time.Duration(float64(d) * f)
}

// beaconFrame builds beacon number n as an AX.25 UI frame.
func (g *APRSGateway) beaconFrame(n int) []byte {
	src := AX25Address{Call: g.config.Callsign, SSID: g.config.SSID}
	dst := AX25Address{Call: "APMSHT", SSID: 0}
	text := g.config.BeaconText
	if text == "" {
		text = fmt.Sprintf("MeshSat %s ok", g.config.Callsign)
	}
	return EncodeAX25Frame(dst, src, nil, []byte(fmt.Sprintf(">%s %d", text, n)))
}

// sendRaw transmits a ready AX.25 frame on the write worker's turn.
func (g *APRSGateway) sendRaw(ctx context.Context, frame []byte) {
	if !g.waitClearToSend(ctx) {
		return
	}
	if err := g.kiss.SendFrame(frame); err != nil {
		log.Warn().Err(err).Msg("aprs: send beacon")
		g.errors.Add(1)
		return
	}
	g.lastTX.Store(time.Now().UnixNano())
	g.tracker.RecordTX()
	g.lastActive.Store(time.Now().Unix())
	g.recordFrame(DirTX, frame, "")
}

// sendMessage transmits one queued message and reports the outcome to whoever
// waits on it. [MESHSAT-1021]
func (g *APRSGateway) sendMessage(ctx context.Context, item *aprsOutbound) {
	item.report(g.transmitMessage(ctx, item))
}

// transmitMessage puts a message on the air, with its repeat copies. The error
// is about the first copy only: once that is out, a lost repeat is left to the
// ack or to the far kit's dedup.
func (g *APRSGateway) transmitMessage(ctx context.Context, item *aprsOutbound) error {
	msg := item.msg
	src := AX25Address{Call: g.config.Callsign, SSID: g.config.SSID}
	dst := AX25Address{Call: "APMSHT", SSID: 0} // APMSxx = MeshSat tocall

	var info []byte
	// Digipeater path defaults to WIDE1-1,WIDE2-1 for local-repeat
	// propagation. Dropped when the payload is encrypted — ciphertext
	// has no business being relayed onto APRS-IS / aprs.fi, and cutting
	// the two path-slot addresses (14 bytes) recovers precious space
	// for the base64-expanded encrypted info.
	path := []AX25Address{{Call: "WIDE1", SSID: 1}, {Call: "WIDE2", SSID: 1}}

	if msg.Encrypted {
		// DeliveryWorker's egress pipeline produced the ciphertext and
		// prepended the binary protocol version byte (codec.ProtoVersion1
		// = 0x01). APRS uses `{E1}` as its own in-protocol version marker
		// so strip the raw byte here — leaving it in would double-version
		// and also push a non-printable byte into an ASCII-only APRS info
		// field, which many igates/parsers reject.
		info = append([]byte(aprsEncryptedPrefix), aprsCipherText(msg)...)
		// The trailing message id asks the peer for an ack. [MESHSAT-1021]
		if item.ackID != "" {
			info = append(append(info, '{'), item.ackID...)
		}
		path = nil
	} else if msg.Destination != "" {
		// Directed APRS message to a station (`:ADDRESSEE:text`), used by
		// OOB replies and any delivery row carrying a destination. No
		// message id is attached, so no ack handling is needed. [MESHSAT-756]
		info = EncodeAPRSMessage(msg.Destination, msg.DecodedText, "")
	} else if msg.DecodedText != "" {
		// Plaintext: send as third-party traffic with attribution.
		comment := fmt.Sprintf("[MeshSat !%08x] %s", msg.From, msg.DecodedText)
		info = EncodeAPRSPosition(0, 0, '/', '-', comment) // 0,0 = no position
	} else {
		info = []byte(fmt.Sprintf(">MeshSat bridge: packet from !%08x", msg.From))
	}

	frame := EncodeAX25Frame(dst, src, path, info)
	if !g.waitClearToSend(ctx) {
		return fmt.Errorf("aprs: shutting down: %w", transport.ErrNotConnected)
	}
	if err := g.kiss.SendFrame(frame); err != nil {
		log.Warn().Err(err).Msg("aprs: send frame")
		g.errors.Add(1)
		return fmt.Errorf("aprs: send frame: %v: %w", err, transport.ErrNotConnected)
	}
	g.lastTX.Store(time.Now().UnixNano())

	g.msgsOut.Add(1)
	g.tracker.RecordTX()
	g.lastActive.Store(time.Now().Unix())
	g.recordFrame(DirTX, frame, msg.MsgRef)
	log.Debug().Str("callsign", FormatCallsign(src)).Bool("encrypted", msg.Encrypted).
		Int("info_len", len(info)).Str("ack_id", item.ackID).Msg("aprs: sent packet")
	// Repeat copies: the same frame again after the gap, so a copy lost on
	// the air is covered by the other; the far kit dedups. [MESHSAT-857]
	// A frame that asks for an ack goes out once per attempt instead: the
	// peer's ack comes back 1.5 to 3 s after the frame, exactly when a blind
	// repeat keys up, and both radios are half-duplex, so the repeat and the
	// ack destroyed each other (14 Sep 2026: 38 acks sent, 18 heard, 2 of 20
	// texts exhausted every attempt although all 20 arrived). The
	// retransmission after the ack timeout is the repeat. [MESHSAT-1021]
	copies := g.config.TXRepeat
	if item.ackID != "" {
		copies = 1
	}
	for i := 1; i < copies; i++ {
		// Jittered, so two kits transmitting at the same nominal cadence
		// cannot keep a fixed offset between their pairs. [MESHSAT-1021]
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(jitterDuration(g.repeatGap(), 0.25)):
		}
		// Every copy passes the gate: a peer that keyed up during the gap
		// must not lose its frame to our repeat. [MESHSAT-1069]
		if !g.waitClearToSend(ctx) {
			return nil
		}
		if err := g.kiss.SendFrame(frame); err != nil {
			log.Warn().Err(err).Int("copy", i+1).Msg("aprs: send repeat")
			g.errors.Add(1)
			return nil
		}
		g.lastTX.Store(time.Now().UnixNano())
		g.tracker.RecordTX()
		g.recordFrame(DirTX, frame, msg.MsgRef)
		log.Debug().Int("copy", i+1).Str("msg_ref", msg.MsgRef).Msg("aprs: sent repeat copy")
	}
	return nil
}

// beaconRepeatGap is the pause between the copies of one beacon. It is
// deliberately NOT the message gap: sharing it made both pairs advance in
// lockstep, so a beacon pair aligned with a message pair took out both copies
// of the message rather than one. [MESHSAT-1021]
func (g *APRSGateway) beaconRepeatGap() time.Duration {
	if g.config.BeaconRepeatGapMs > 0 {
		return time.Duration(g.config.BeaconRepeatGapMs) * time.Millisecond
	}
	// Seven quarters of the message gap: far enough from it that the two
	// cadences drift apart within one pair, and still inside the receive
	// watchdog's patience.
	return g.repeatGap() * 7 / 4
}

// waitForQuietChannel delays a due beacon while the channel has recent
// traffic, up to a cap. It returns false only if the gateway is shutting down.
// Traffic is the later of the last frame decoded from a peer and our own last
// transmission, so this covers the sender's own message pair and a peer's
// transmission alike. [MESHSAT-1021]
func (g *APRSGateway) waitForQuietChannel(ctx context.Context) bool {
	deadline := time.Now().Add(beaconDeferMax)
	for {
		last := g.lastPeerRX.Load()
		if tx := g.lastTX.Load(); tx > last {
			last = tx
		}
		if last == 0 {
			return true
		}
		quiet := time.Since(time.Unix(0, last))
		if quiet >= beaconQuietFor {
			return true
		}
		// Each wait is clamped to the remaining deferral budget. Without the
		// clamp a long quiet threshold slept straight past the cap, which
		// would hold the liveness beacon far longer than intended on a busy
		// channel.
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return true
		}
		wait := beaconQuietFor - quiet
		if wait > remaining {
			wait = remaining
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(wait):
		}
	}
}

// Beacon deferral knobs. Package vars so tests can shorten them.
var (
	// How quiet the channel must be before a beacon goes out.
	beaconQuietFor = 3 * time.Second
	// How long a beacon may be held back. A busy channel must not silence
	// the liveness signal the peer's receive watchdog waits for.
	beaconDeferMax = 20 * time.Second
)

// Transmit gate knobs. Package vars so tests can shorten them.
var (
	// How long after the last frame decoded from a peer the channel still
	// counts as busy. A decoded frame means the peer has just unkeyed; its
	// repeat copy or a digipeater may follow straight after.
	aprsRXHoldoff = 1500 * time.Millisecond
	// The longest the gate holds one transmission. A channel that never goes
	// quiet must not stall the write worker, so after this the frame goes out.
	aprsCTSMax = 3 * time.Second
	// Source for the p-persistence roll, uniform in [0, 1).
	aprsRand = rand.Float64
)

// waitClearToSend is a software listen-before-talk gate in front of every
// frame the write worker hands to the TNC. The PicoAPRS V4 does its own
// carrier sensing, but nothing published says it honours KISS TXDELAY,
// PERSIST or SLOTTIME frames, so none are sent and the bridge sequences its
// own traffic: measured 12 Sep 2026, 14 to 21 percent of frames never
// arrived with bad_frames at 0.
//
// If no peer frame was decoded within aprsRXHoldoff the frame goes out at
// once. Otherwise the gate waits until the hold-off has passed since the
// latest peer frame (a new frame extends the wait), then rolls p-persistence
// with the configured Persist and SlotTime (Direwolf units and defaults): send
// with probability (Persist+1)/256, else wait a slot and check again. The
// total wait never exceeds aprsCTSMax; the frame then goes out anyway. It
// returns false only when ctx is done. [MESHSAT-1021, MESHSAT-1069]
func (g *APRSGateway) waitClearToSend(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	start := time.Now()
	holdoff := aprsRXHoldoff
	if !g.peerHeardWithin(holdoff, start) {
		return true
	}

	persist := orDefault(g.config.Persist, direwolfDefaultPersist)
	if persist > 255 {
		persist = 255
	}
	p := float64(persist+1) / 256
	slot := time.Duration(orDefault(g.config.SlotTime, direwolfDefaultSlotTime)) * 10 * time.Millisecond
	deadline := start.Add(aprsCTSMax)

	g.ctsDeferred.Add(1)
	rolls := 0
	capped := false
	defer func() {
		log.Debug().Dur("waited", time.Since(start)).Int("rolls", rolls).Bool("capped", capped).
			Msg("aprs: transmit held, channel busy")
	}()

	for {
		var wait time.Duration
		if last := g.lastPeerRX.Load(); last > 0 {
			wait = time.Until(time.Unix(0, last).Add(holdoff))
		}
		if wait <= 0 {
			// Quiet for the whole hold-off: p-persistence decides.
			rolls++
			if aprsRand() < p {
				return true
			}
			wait = slot
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			capped = true
			return true
		}
		if wait > remaining {
			wait = remaining
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(wait):
		}
		if !time.Now().Before(deadline) {
			capped = true
			return true
		}
	}
}

// peerHeardWithin reports whether a peer frame was decoded less than d
// before now.
func (g *APRSGateway) peerHeardWithin(d time.Duration, now time.Time) bool {
	last := g.lastPeerRX.Load()
	return last > 0 && now.Sub(time.Unix(0, last)) < d
}

// isOwnSource reports whether an AX.25 source address is this gateway's own
// callsign and SSID, compared the way EncodeAX25Frame puts them on the air
// (upper case, six characters, four-bit SSID), so a TNC echo matches.
func (g *APRSGateway) isOwnSource(src AX25Address) bool {
	call := strings.ToUpper(strings.TrimSpace(g.config.Callsign))
	if len(call) > 6 {
		call = call[:6]
	}
	if call == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(src.Call), call) && src.SSID == g.config.SSID&0x0F
}

// aprsMeshSatMarker opens the comment of every plaintext relay a MeshSat
// bridge transmits (see transmitMessage), so a peer in plaintext mode is
// recognised without a callsign list.
const aprsMeshSatMarker = "[MeshSat "

// relayable reports whether a decoded, non-status frame is for this bridge
// and may enter the message pipeline: a message addressed to our callsign
// (OOB requests and replies, operator texts), or a plaintext relay from
// another MeshSat bridge. Encrypted frames never reach here (own branch),
// acks and status beacons are handled before. With RelayThirdParty set the
// old behaviour, everything decoded is a message, is kept. [MESHSAT-1128]
func (g *APRSGateway) relayable(pkt *APRSPacket) bool {
	if g.config.RelayThirdParty {
		return true
	}
	switch pkt.DataType {
	case ':':
		return g.addressedToUs(pkt.MsgTo)
	case '!', '=', '/', '@':
		return strings.HasPrefix(pkt.Comment, aprsMeshSatMarker)
	default:
		return strings.Contains(pkt.Raw, aprsMeshSatMarker)
	}
}

// addressedToUs matches an APRS message addressee against our callsign,
// with or without the SSID, case-insensitively.
func (g *APRSGateway) addressedToUs(to string) bool {
	to = strings.ToUpper(strings.TrimSpace(to))
	call := strings.ToUpper(strings.TrimSpace(g.config.Callsign))
	if to == "" || call == "" {
		return false
	}
	if len(call) > 6 {
		call = call[:6]
	}
	full := FormatCallsign(AX25Address{Call: call, SSID: g.config.SSID & 0x0F})
	return to == full || to == call
}

// repeatGap is the pause between repeat copies of one message.
func (g *APRSGateway) repeatGap() time.Duration {
	if g.config.TXRepeatGapMs > 0 {
		return time.Duration(g.config.TXRepeatGapMs) * time.Millisecond
	}
	return 1500 * time.Millisecond
}

func (g *APRSGateway) formatInboundText(pkt *APRSPacket) string {
	switch pkt.DataType {
	case '!', '=', '/', '@': // Position
		return fmt.Sprintf("[APRS:%s] %.4f,%.4f %s", pkt.Source, pkt.Lat, pkt.Lon, pkt.Comment)
	case ':': // Message
		return fmt.Sprintf("[APRS:%s→%s] %s", pkt.Source, pkt.MsgTo, pkt.Message)
	default:
		return fmt.Sprintf("[APRS:%s] %s", pkt.Source, pkt.Raw)
	}
}

func (g *APRSGateway) reconnect(ctx context.Context) {
	wait := 5 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		if err := g.kiss.Dial(); err != nil {
			log.Warn().Err(err).Dur("retry_in", wait).Msg("aprs: reconnect failed")
			wait *= 2
			if wait > 5*time.Minute {
				wait = 5 * time.Minute
			}
			continue
		}

		g.connected.Store(true)
		log.Info().Str("kiss", g.kiss.Target()).Msg("aprs: reconnected to the TNC")
		return
	}
}

// silenceWatchdog monitors for extended periods without receiving any APRS
// packets. If the gateway is connected but no packets arrive for 30 minutes,
// it logs a warning (likely antenna/radio issue, not a Direwolf bug).
// If no packets arrive for 60 minutes, it forces a reconnect cycle to
// recover from potential KISS TCP desynchronization. [MESHSAT-403]
func (g *APRSGateway) silenceWatchdog(ctx context.Context) {
	defer g.wg.Done()

	const warnAfter = 30 * time.Minute
	const reconnectAfter = 60 * time.Minute

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	warned := false

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !g.connected.Load() {
				warned = false
				continue
			}

			lastRX := g.lastActive.Load()
			if lastRX == 0 {
				// Never received a packet — skip watchdog until first packet
				continue
			}

			silence := time.Since(time.Unix(lastRX, 0))

			if silence >= reconnectAfter {
				log.Warn().Dur("silence", silence).
					Msg("aprs: no packets for 60min — forcing KISS reconnect")
				g.kiss.Close()
				g.connected.Store(false)
				g.reconnect(ctx)
				warned = false
			} else if silence >= warnAfter && !warned {
				log.Warn().Dur("silence", silence).
					Msg("aprs: no packets for 30min — check antenna/radio/frequency")
				warned = true
			}
		}
	}
}
