package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/tetra"
	"meshsat/internal/transport"
)

// TetraGateway carries texts as SDS through a TETRA radio's PEI (gateway
// type tetra, link tetra_0): a rule forwards a mesh text to tetra_0 and the
// radio sends it to its group, and an SDS the radio receives meets the
// access rules as tetra_0 with sender tetra:<ISSI>. The texts are SDS-TL
// text messages, the kind every TETRA handheld shows, so a kit and a plain
// radio read each other. Proven radio to radio in Direct Mode between the
// two kits on 17 Oct 2026. [MESHSAT-1588]
type TetraGateway struct {
	config     TetraConfig
	instanceID string
	findPort   func() string
	open       func(path string) (io.ReadWriteCloser, error)

	inCh       chan InboundMessage
	emitEvent  EventEmitFunc
	nodeNameFn func(uint32) string

	packetMu    sync.RWMutex
	packetSink  PacketSink
	packetIface string

	mu        sync.RWMutex
	sess      *tetra.Session
	port      string
	ident     tetra.Identity
	mode      int
	maxBits   int
	battery   int
	state     string
	lastError string
	since     time.Time

	sendMu sync.Mutex // the radio sends one SDS at a time
	ref    atomic.Uint32

	asmMu sync.Mutex // parts of split texts waiting for the rest
	asm   map[string]*tetraAssembly

	msgsIn, msgsOut, errs, reports, refusedLong, nonText atomic.Int64
	lastIn, lastOut                                      atomic.Int64

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Gateway states.
const (
	tetraDisconnected = "disconnected"
	tetraConnecting   = "connecting"
	tetraConnected    = "connected"
	tetraError        = "error"
)

// tetraHealthEvery is how often a running gateway reads the radio's
// battery and mode; three failed reads in a row reopen the port.
var tetraHealthEvery = 60 * time.Second

// tetraDefaultMaxBits is the MTP3550's AT+CMGS limit (1184 bits, 148
// bytes), used when the radio does not say.
const tetraDefaultMaxBits = 1184

// NewTetraGateway makes the gateway; findPort names the radio's AT port
// the device supervisor claimed when the config gives none.
func NewTetraGateway(cfg TetraConfig, instanceID string, findPort func() string) *TetraGateway {
	return &TetraGateway{
		config:     cfg,
		instanceID: instanceID,
		findPort:   findPort,
		open:       transport.OpenTetraPEI,
		inCh:       make(chan InboundMessage, 32),
		state:      tetraDisconnected,
		mode:       -1,
		battery:    -1,
		maxBits:    tetraDefaultMaxBits,
	}
}

func (g *TetraGateway) SetEventEmitter(fn EventEmitFunc)           { g.emitEvent = fn }
func (g *TetraGateway) SetNodeNameResolver(fn func(uint32) string) { g.nodeNameFn = fn }

// SetPacketSink feeds the live packet ring.
func (g *TetraGateway) SetPacketSink(sink PacketSink, iface string) {
	g.packetMu.Lock()
	g.packetSink, g.packetIface = sink, iface
	g.packetMu.Unlock()
}

// Config returns the configuration as validated.
func (g *TetraGateway) Config() TetraConfig { return g.config }

// InstanceID is the gateway's link id (tetra_0).
func (g *TetraGateway) InstanceID() string { return g.instanceID }

// Type returns the gateway type identifier.
func (g *TetraGateway) Type() string { return "tetra" }

// Receive returns the inbound message channel.
func (g *TetraGateway) Receive() <-chan InboundMessage { return g.inCh }

func (g *TetraGateway) emit(eventType, message string) {
	if g.emitEvent != nil {
		g.emitEvent(eventType, message)
	}
}

func (g *TetraGateway) record(rec PacketRecord) {
	g.packetMu.RLock()
	sink, iface := g.packetSink, g.packetIface
	g.packetMu.RUnlock()
	if sink == nil {
		return
	}
	rec.Iface = iface
	sink(rec)
}

// Start opens the radio and sets it up, and from then on reopens it by
// itself until Stop: a first open that fails is retried in the background
// (the reconcile at boot starts a gateway once and never again). With no
// radio on USB at all it returns transport.ErrNotConnected and runs
// nothing.
func (g *TetraGateway) Start(ctx context.Context) error {
	if g.config.Port == "" && (g.findPort == nil || g.findPort() == "") {
		g.setState(tetraDisconnected, "no TETRA radio on USB")
		return fmt.Errorf("tetra: no radio: %w", transport.ErrNotConnected)
	}
	ctx, cancel := context.WithCancel(ctx)
	g.cancel = cancel
	sess, err := g.connect(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("tetra: the radio did not open, retrying in the background")
	}
	g.wg.Add(1)
	go g.run(ctx, sess)
	return nil
}

// Stop gives incoming texts back to the radio's screen and closes the port.
func (g *TetraGateway) Stop() error {
	if g.cancel != nil {
		g.cancel()
	}
	g.wg.Wait()
	log.Info().Str("instance", g.instanceID).Msg("tetra gateway stopped")
	return nil
}

// Enqueue sends in the background (legacy callers); Forward is the path.
func (g *TetraGateway) Enqueue(msg *transport.MeshMessage) error {
	return g.Forward(context.Background(), msg)
}

func (g *TetraGateway) setState(state, lastErr string) {
	g.mu.Lock()
	g.state = state
	if lastErr != "" || state == tetraConnected {
		g.lastError = lastErr
	}
	if state == tetraConnected {
		g.since = time.Now()
	}
	g.mu.Unlock()
}

// connect opens the port and puts the radio in the shape the gateway
// needs: echo off, the configured mode, texts to the PEI.
func (g *TetraGateway) connect(ctx context.Context) (*tetra.Session, error) {
	port := g.config.Port
	if port == "" && g.findPort != nil {
		port = g.findPort()
	}
	if port == "" {
		g.setState(tetraDisconnected, "no TETRA radio on USB")
		return nil, fmt.Errorf("tetra: no radio: %w", transport.ErrNotConnected)
	}
	g.setState(tetraConnecting, "")
	rwc, err := g.open(port)
	if err != nil {
		g.setState(tetraError, err.Error())
		return nil, fmt.Errorf("tetra: %w", err)
	}
	sess := tetra.NewSession(rwc, g.onSDS, g.onLine)
	sess.OnDecodeFail = func(header, data string, err error) {
		g.errs.Add(1)
		log.Warn().Err(err).Str("header", header).Str("data", data).Msg("tetra: an SDS from the radio did not decode, dropped")
	}
	fail := func(err error) (*tetra.Session, error) {
		sess.Close()
		g.setState(tetraError, err.Error())
		return nil, fmt.Errorf("tetra: set up the radio on %s: %w", port, err)
	}
	// The radio greets a fresh open with +MCCFG: 0 and may miss the
	// first command; echo off is the one that matters for the rest.
	var echoErr error
	for i := 0; i < 3; i++ {
		if _, echoErr = sess.Command(ctx, "ATE0", 2*time.Second); echoErr == nil {
			break
		}
	}
	if echoErr != nil {
		return fail(echoErr)
	}
	id, err := sess.Identify(ctx)
	if err != nil {
		return fail(err)
	}
	mode, err := sess.Mode(ctx)
	if err != nil {
		return fail(err)
	}
	want := map[string]int{"dmo": 1, "tmo": 0, "keep": mode}[g.config.Mode]
	if mode != want {
		log.Info().Int("from", mode).Int("to", want).Msg("tetra: switching the radio's mode")
		if err := sess.SetMode(ctx, want); err != nil {
			return fail(err)
		}
		// The identity and group change with the mode (TMO and DMO carry
		// their own): read them again.
		if mode, err = sess.Mode(ctx); err != nil {
			return fail(err)
		}
		if id, err = sess.Identify(ctx); err != nil {
			return fail(err)
		}
	}
	maxBits, err := sess.MaxSDSBits(ctx)
	if err != nil || maxBits < 64 {
		log.Warn().Err(err).Int("bits", maxBits).Msg("tetra: the radio did not give its SDS limit, using the MTP3550's")
		maxBits = tetraDefaultMaxBits
	}
	if err := sess.RouteTexts(ctx, true); err != nil {
		return fail(err)
	}
	battery, _ := sess.Battery(ctx)

	g.mu.Lock()
	g.sess, g.port, g.ident, g.mode, g.maxBits, g.battery = sess, port, id, mode, maxBits, battery
	g.mu.Unlock()
	g.setState(tetraConnected, "")
	log.Info().Str("port", port).Str("model", id.Model).Str("firmware", id.Firmware).Str("itsi", id.ITSI).
		Str("group", tetra.SSIOf(id.GTSI)).Str("mode", modeName(mode)).Int("max_bits", maxBits).Int("battery", battery).
		Msg("tetra: radio ready, texts come to the Bridge")
	g.emit("tetra", fmt.Sprintf("TETRA radio ready (%s, group %s)", modeName(mode), tetra.SSIOf(id.GTSI)))
	return sess, nil
}

// textLimit is how many bytes of SDS-TL text (header included) the radio
// takes for one SDS. AT+CMGS=? says 1184 bits (148 bytes), but the MTP3550
// adds CR CR and its own ITSI to every text inside that budget and answers
// +CME ERROR: 24 (too long) past it: measured on 17 Oct 2026, a 130-byte
// PDU went, 131 was refused, 148 - 130 = 18 = 2 + the 16-digit ITSI.
// Must be called with g.mu held.
func (g *TetraGateway) textLimit() int {
	appended := 2 + len(g.ident.ITSI)
	if g.ident.ITSI == "" {
		appended = 18
	}
	return g.maxBits/8 - appended
}

func modeName(mode int) string {
	switch mode {
	case 0:
		return "tmo"
	case 1:
		return "dmo"
	}
	return "unknown"
}

// run keeps the radio: a health read every minute, a reopen after the
// port fails or three reads in a row fail, and the screen given back on
// the way out.
func (g *TetraGateway) run(ctx context.Context, sess *tetra.Session) {
	defer g.wg.Done()
	backoff := []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}
	attempt := 0
	for {
		if sess != nil {
			attempt = 0
			g.hold(ctx, sess)
			g.mu.Lock()
			g.sess = nil
			g.mu.Unlock()
			if ctx.Err() != nil {
				giveBack, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				if err := sess.RouteTexts(giveBack, false); err != nil {
					log.Debug().Err(err).Msg("tetra: could not give texts back to the radio's screen")
				}
				cancel()
				sess.Close()
				g.setState(tetraDisconnected, "")
				return
			}
			sess.Close()
		}
		wait := backoff[min(attempt, len(backoff)-1)]
		attempt++
		select {
		case <-ctx.Done():
			g.setState(tetraDisconnected, "")
			return
		case <-time.After(wait):
		}
		var err error
		if sess, err = g.connect(ctx); err != nil {
			log.Warn().Err(err).Int("attempt", attempt).Msg("tetra: reopen failed")
			sess = nil
		}
	}
}

// hold returns when the session ends or the gateway stops.
func (g *TetraGateway) hold(ctx context.Context, sess *tetra.Session) {
	t := time.NewTicker(tetraHealthEvery)
	defer t.Stop()
	failed := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-sess.Done():
			g.setState(tetraError, fmt.Sprint(sess.Err()))
			log.Warn().Err(sess.Err()).Msg("tetra: the radio's port closed, reopening")
			return
		case <-t.C:
			battery, err := sess.Battery(ctx)
			mode := -1
			if err == nil {
				mode, err = sess.Mode(ctx)
			}
			if err != nil {
				failed++
				log.Warn().Err(err).Int("failed", failed).Msg("tetra: health read failed")
				if failed >= 3 {
					g.setState(tetraError, err.Error())
					return
				}
				continue
			}
			failed = 0
			g.mu.Lock()
			g.battery = battery
			changed := mode != g.mode
			g.mode = mode
			g.mu.Unlock()
			if changed {
				log.Warn().Str("mode", modeName(mode)).Msg("tetra: the radio's mode changed under the gateway (someone used its menu?)")
				g.emit("tetra", "TETRA radio mode is now "+modeName(mode))
			}
		}
	}
}

// destination resolves where a message goes: its own address, else the
// configured one, else the radio's selected group.
func (g *TetraGateway) destination(msg *transport.MeshMessage) (string, error) {
	if msg.Destination != "" {
		ssi, ok := tetra.ParseAddress(msg.Destination)
		if !ok {
			return "", fmt.Errorf("tetra: %q is not an SDS address (an SSI or tetra:<ssi>): %w", msg.Destination, transport.ErrNoRecipient)
		}
		return ssi, nil
	}
	if g.config.Destination != "" {
		if ssi, ok := tetra.ParseAddress(g.config.Destination); ok {
			return ssi, nil
		}
	}
	g.mu.RLock()
	gtsi := g.ident.GTSI
	g.mu.RUnlock()
	if ssi, ok := tetra.ParseAddress(tetra.SSIOf(gtsi)); ok {
		return ssi, nil
	}
	return "", fmt.Errorf("tetra: the radio has no selected group and no destination is set: %w", transport.ErrNoRecipient)
}

// outboundText is the words the radio sends: verbatim for an OOB frame, a
// sealed text or text written on this kit; "<prefix> <sender>: <text>" for
// a text relayed from the mesh.
func (g *TetraGateway) outboundText(msg *transport.MeshMessage) string {
	if msg.Encrypted || msg.RawText || msg.AsWritten || msg.From == 0 {
		return msg.DecodedText
	}
	sender := fmt.Sprintf("!%08x", msg.From)
	if g.nodeNameFn != nil {
		if name := g.nodeNameFn(msg.From); name != "" {
			sender = name
		}
	}
	out := sender + ": " + msg.DecodedText
	if g.config.Prefix != "" {
		out = g.config.Prefix + " " + out
	}
	return out
}

// splitText cuts plain words into parts that each fit one SDS, at spaces
// where it can, numbered "1/2 " and on. Every part is whole text; nothing
// is dropped. A sealed or raw payload is never split (see Forward).
func splitText(text string, maxBytes int) []string {
	if tetra.TextBytes(text) <= maxBytes {
		return []string{text}
	}
	const marker = "99/99 "
	var parts []string
	rest := []rune(text)
	for len(rest) > 0 {
		n := len(rest)
		for n > 0 && tetra.TextBytes(marker+string(rest[:n])) > maxBytes {
			n--
		}
		if n == 0 {
			return nil
		}
		if n < len(rest) {
			if sp := strings.LastIndex(string(rest[:n]), " "); sp > 0 {
				if c := len([]rune(string(rest[:n])[:sp])); c > n/2 {
					n = c
				}
			}
		}
		parts = append(parts, strings.TrimSpace(string(rest[:n])))
		rest = []rune(strings.TrimLeft(string(rest[n:]), " "))
	}
	for i := range parts {
		parts[i] = fmt.Sprintf("%d/%d %s", i+1, len(parts), parts[i])
	}
	return parts
}

// Forward sends one message as SDS: one text if it fits, else plain words
// in numbered parts. A sealed, raw or OOB payload that does not fit one SDS
// is refused (transport.ErrPayloadTooLong): cutting it would destroy it.
func (g *TetraGateway) Forward(ctx context.Context, msg *transport.MeshMessage) error {
	g.mu.RLock()
	sess, maxBytes := g.sess, g.textLimit()
	g.mu.RUnlock()
	if sess == nil {
		return fmt.Errorf("tetra: radio %s: %w", g.stateString(), transport.ErrNotConnected)
	}
	dest, err := g.destination(msg)
	if err != nil {
		return err
	}
	text := g.outboundText(msg)
	if strings.TrimSpace(text) == "" {
		return errors.New("tetra: nothing to send")
	}
	parts := []string{text}
	if tetra.TextBytes(text) > maxBytes {
		if msg.Encrypted || msg.RawText {
			g.refusedLong.Add(1)
			return fmt.Errorf("tetra: %d bytes, one SDS holds %d: %w", tetra.TextBytes(text), maxBytes, transport.ErrPayloadTooLong)
		}
		if parts = splitText(text, maxBytes); parts == nil {
			g.refusedLong.Add(1)
			return fmt.Errorf("tetra: the text cannot be split to fit %d bytes: %w", maxBytes, transport.ErrPayloadTooLong)
		}
	}

	g.sendMu.Lock()
	defer g.sendMu.Unlock()
	for i, part := range parts {
		ref := byte(g.ref.Add(1))
		pdu, bits := tetra.EncodeText(ref, part)
		rep, err := sess.SendSDS(ctx, dest, ref, pdu, bits)
		if err != nil {
			g.errs.Add(1)
			var cme *tetra.CMEError
			switch {
			case errors.Is(err, tetra.ErrClosed):
				return fmt.Errorf("tetra: the radio's port closed: %w", transport.ErrNotConnected)
			case errors.As(err, &cme) && cme.Code == 24 && i == 0:
				// Too long for the radio: no retry can help.
				g.refusedLong.Add(1)
				return fmt.Errorf("tetra: the radio refused the SDS to %s as too long (%d bytes): %v: %w", dest, len(pdu), err, transport.ErrPayloadTooLong)
			case errors.As(err, &cme) && i == 0:
				// The radio refused before anything went on air.
				return fmt.Errorf("tetra: the radio refused the SDS to %s: %v: %w", dest, err, transport.ErrNotSubmitted)
			case i > 0:
				log.Warn().Str("to", dest).Int("part", i+1).Int("parts", len(parts)).
					Msg("tetra: a later part failed; the retry sends every part again")
			}
			return fmt.Errorf("tetra: send to %s: %w", dest, err)
		}
		g.msgsOut.Add(1)
		g.lastOut.Store(time.Now().Unix())
		status := -1
		if rep != nil {
			status = rep.Status
		}
		g.record(PacketRecord{Time: time.Now(), Bearer: BearerTetra, Dir: DirTX, From: g.selfSSI(), To: dest, Bytes: len(pdu), Text: CapPacketText(part), MsgRef: msg.MsgRef})
		log.Info().Str("to", dest).Int("ref", int(ref)).Int("bytes", len(pdu)).Int("report_status", status).
			Int("part", i+1).Int("parts", len(parts)).Str("text", part).Msg("tetra: SDS sent")
	}
	return nil
}

func (g *TetraGateway) selfSSI() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return tetra.SSIOf(g.ident.ITSI)
}

func (g *TetraGateway) stateString() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.lastError != "" {
		return g.state + " (" + g.lastError + ")"
	}
	return g.state
}

// onSDS runs on the PEI reader: a text goes to the processor as tetra_0,
// anything else is counted and logged.
func (g *TetraGateway) onSDS(r tetra.Received) {
	if r.IsReport {
		g.reports.Add(1)
		log.Info().Str("from", r.From).Int("ref", int(r.Ref)).Msg("tetra: SDS-TL report received")
		return
	}
	if !r.IsText {
		g.nonText.Add(1)
		log.Info().Str("from", r.From).Str("to", r.To).Int("pid", int(r.PID)).Int("bytes", len(r.PDU)).
			Msg("tetra: an SDS that is not a text, ignored")
		return
	}
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return
	}
	if g.holdPart(r, text) {
		return
	}
	g.deliver(r.From, r.To, text, len(r.PDU), r.SenderID, int(r.Ref))
}

// deliver hands one whole text to the processor as tetra_0.
func (g *TetraGateway) deliver(from, to, text string, nbytes int, senderID string, ref int) {
	inbound := InboundMessage{
		Text:     text,
		Source:   "tetra",
		FromAddr: tetra.Address(from), // reply address and attribution; never used for authentication
		To:       tetra.Address(to),
	}
	select {
	case g.inCh <- inbound:
	default:
		g.errs.Add(1)
		log.Warn().Str("from", from).Msg("tetra: inbound queue full, SDS dropped")
		return
	}
	g.msgsIn.Add(1)
	g.lastIn.Store(time.Now().Unix())
	g.record(PacketRecord{Time: time.Now(), Bearer: BearerTetra, Dir: DirRX, From: from, To: to, Bytes: nbytes, Text: CapPacketText(text)})
	log.Info().Str("from", from).Str("to", to).Str("sender_id", senderID).Int("ref", ref).
		Str("text", text).Msg("tetra: SDS received, forwarding")
	g.emit("tetra", fmt.Sprintf("SDS received from %s, forwarding", from))
}

// A text Forward split goes as "1/3 ...", "2/3 ...", "3/3 ..." with
// consecutive message references. The receiving gateway puts it back
// together before the rules see it, so the far mesh gets one text, not
// one broadcast per part.
var tetraPartRe = regexp.MustCompile(`^([1-9])/([2-9]) (.*)$`)

// tetraAssemblyWait is how long the parts of a text wait for the rest;
// after it the parts that came go on as one text with the gaps marked.
var tetraAssemblyWait = 60 * time.Second

type tetraAssembly struct {
	parts    []string
	got      int
	from, to string
	nbytes   int
	timer    *time.Timer
}

// holdPart keeps a part of a split text and delivers the whole once the
// last part is in. A text that only looks like a part ("1/2 cup of
// sugar") is not one: every part before the last fills at least half an
// SDS, as Forward cuts them.
func (g *TetraGateway) holdPart(r tetra.Received, text string) bool {
	m := tetraPartRe.FindStringSubmatch(text)
	if m == nil {
		return false
	}
	k, _ := strconv.Atoi(m[1])
	n, _ := strconv.Atoi(m[2])
	if k > n {
		return false
	}
	g.mu.RLock()
	maxBytes := g.textLimit()
	g.mu.RUnlock()
	if k < n && tetra.TextBytes(text) < maxBytes/2 {
		return false
	}
	base := (int(r.Ref) - (k - 1) + 256) % 256
	key := r.From + "|" + strconv.Itoa(n) + "|" + strconv.Itoa(base)

	g.asmMu.Lock()
	if g.asm == nil {
		g.asm = make(map[string]*tetraAssembly)
	}
	a := g.asm[key]
	if a == nil {
		a = &tetraAssembly{parts: make([]string, n), from: r.From, to: r.To}
		g.asm[key] = a
		a.timer = time.AfterFunc(tetraAssemblyWait, func() { g.flushParts(key, true) })
	}
	if a.parts[k-1] == "" {
		a.parts[k-1] = m[3]
		a.got++
		a.nbytes += len(r.PDU)
	}
	done := a.got == n
	g.asmMu.Unlock()
	log.Info().Str("from", r.From).Int("part", k).Int("parts", n).Int("ref", int(r.Ref)).Msg("tetra: part of a longer text held for the rest")
	if done {
		g.flushParts(key, false)
	}
	return true
}

// flushParts delivers an assembly: whole, or after the wait with "[...]"
// where a part never came.
func (g *TetraGateway) flushParts(key string, timedOut bool) {
	g.asmMu.Lock()
	a := g.asm[key]
	delete(g.asm, key)
	g.asmMu.Unlock()
	if a == nil {
		return
	}
	a.timer.Stop()
	parts := make([]string, len(a.parts))
	for i, p := range a.parts {
		if p == "" {
			p = "[...]"
		}
		parts[i] = p
	}
	if timedOut {
		log.Warn().Str("from", a.from).Int("got", a.got).Int("parts", len(a.parts)).
			Msg("tetra: not every part of a longer text came; forwarding what did, gaps marked")
	}
	g.deliver(a.from, a.to, strings.Join(parts, " "), a.nbytes, "", -1)
}

// onLine logs what the radio says on its own.
func (g *TetraGateway) onLine(line string) {
	log.Debug().Str("line", line).Msg("tetra: unsolicited line from the radio")
}

// Status reports the link and the counters.
func (g *TetraGateway) Status() GatewayStatus {
	g.mu.RLock()
	defer g.mu.RUnlock()
	s := GatewayStatus{
		Type:        "tetra",
		Connected:   g.state == tetraConnected && g.sess != nil,
		MessagesIn:  g.msgsIn.Load(),
		MessagesOut: g.msgsOut.Load(),
		Errors:      g.errs.Load(),
		Mode:        modeName(g.mode),
		State:       g.state,
		LastError:   g.lastError,
	}
	last := g.lastIn.Load()
	if o := g.lastOut.Load(); o > last {
		last = o
	}
	if last > 0 {
		s.LastActivity = time.Unix(last, 0)
	}
	if s.Connected && !g.since.IsZero() {
		s.ConnectionUptime = time.Since(g.since).Truncate(time.Second).String()
	}
	return s
}

// TetraStatus is GET /api/tetra/status's gateway part.
type TetraStatus struct {
	State          string         `json:"state"`
	LastError      string         `json:"last_error,omitempty"`
	Port           string         `json:"port,omitempty"`
	ConnectedSince *time.Time     `json:"connected_since,omitempty"`
	Radio          tetra.Identity `json:"radio"`
	Mode           string         `json:"mode"`            // dmo, tmo or unknown
	Group          string         `json:"group,omitempty"` // the radio's selected group (SSI)
	Self           string         `json:"self,omitempty"`  // the radio's own SSI
	Destination    string         `json:"destination"`     // where an unaddressed message goes
	BatteryPct     int            `json:"battery_pct"`     // -1 unknown
	MaxTextBytes   int            `json:"max_text_bytes"`  // one SDS-TL text, header included, after the ID the radio adds
	Config         TetraConfig    `json:"config"`
	MessagesIn     int64          `json:"messages_in"`
	MessagesOut    int64          `json:"messages_out"`
	Errors         int64          `json:"errors"`
	Reports        int64          `json:"reports"`
	RefusedTooLong int64          `json:"refused_too_long"`
	NonTextIn      int64          `json:"non_text_in"`
	LastIn         *time.Time     `json:"last_in,omitempty"`
	LastOut        *time.Time     `json:"last_out,omitempty"`
}

// TetraStatus reports the radio, the link and the counters.
func (g *TetraGateway) TetraStatus() TetraStatus {
	dest, _ := g.destination(&transport.MeshMessage{})
	g.mu.RLock()
	defer g.mu.RUnlock()
	st := TetraStatus{
		State:          g.state,
		LastError:      g.lastError,
		Port:           g.port,
		Radio:          g.ident,
		Mode:           modeName(g.mode),
		Group:          tetra.SSIOf(g.ident.GTSI),
		Self:           tetra.SSIOf(g.ident.ITSI),
		Destination:    dest,
		BatteryPct:     g.battery,
		MaxTextBytes:   g.textLimit(),
		Config:         g.config,
		MessagesIn:     g.msgsIn.Load(),
		MessagesOut:    g.msgsOut.Load(),
		Errors:         g.errs.Load(),
		Reports:        g.reports.Load(),
		RefusedTooLong: g.refusedLong.Load(),
		NonTextIn:      g.nonText.Load(),
	}
	if g.state == tetraConnected && !g.since.IsZero() {
		t := g.since
		st.ConnectedSince = &t
	}
	if v := g.lastIn.Load(); v > 0 {
		t := time.Unix(v, 0)
		st.LastIn = &t
	}
	if v := g.lastOut.Load(); v > 0 {
		t := time.Unix(v, 0)
		st.LastOut = &t
	}
	return st
}
