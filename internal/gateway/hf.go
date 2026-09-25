package gateway

// HFGateway: the 10 m HF hop of Data Slayer's CrossTalk, received on the
// kit's RTL-SDR and, with an operator callsign, transmitted through a
// USB-audio radio keyed over CAT. Receive borrows the dongle from the
// spectrum monitor (Suspend/Resume), runs its own rtl_tcp at 240 kHz with
// the LO parked 1 kHz below the centre, decimates to 2 kHz and hands the
// baseband to the hf10m demodulator. Every decoded shout becomes an
// InboundMessage (plaintext, from the callsign, to lxmf:<dest>) and a
// packet-feed record on bearer "hf". Nothing here is a Reticulum packet
// and nothing is encrypted; the recipe is public. [MESHSAT-1353]

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/hf10m"
	"meshsat/internal/transport"
)

// SDRBorrower is what the spectrum monitor offers: park scanning and
// release the dongle, and take it back.
type SDRBorrower interface {
	Suspend(ctx context.Context)
	Resume()
}

const (
	hfSampleRate    = 240_000
	hfLOOffsetHz    = 1000 // LO parked below the centre so the DC spike stays clear
	hfChunkSeconds  = 1
	hfBufferSeconds = 45 // longest shout ~40 s
	hfMinNewSeconds = 2
	hfRetryMin      = 5 * time.Second
	hfRetryMax      = 60 * time.Second
	hfBinary        = "rtl_tcp"

	rtlCmdSetFreq       = 0x01
	rtlCmdSetSampleRate = 0x02
	rtlCmdSetGainMode   = 0x03
	rtlCmdSetGain       = 0x04
	rtlCmdSetAGCMode    = 0x08
)

// ErrHFTransmitLocked is returned by Forward without an operator callsign.
var ErrHFTransmitLocked = errors.New("hf_0 transmit locked: no operator callsign")

// HFGateway is the gateway.
type HFGateway struct {
	config     HFConfig
	instanceID string
	inCh       chan InboundMessage
	sdr        func() SDRBorrower
	packetSink PacketSink
	emit       EventEmitFunc

	// Injectable for tests: how to start the reader process and where to dial.
	spawn func(addr string) (stop func(), done <-chan struct{}, err error)
	dial  func(ctx context.Context, addr string) (net.Conn, error)

	connected atomic.Bool
	msgsIn    atomic.Int64
	msgsOut   atomic.Int64
	errors    atomic.Int64
	decodes   atomic.Int64
	lastHeard atomic.Int64
	lastErr   atomic.Value // string
	lastInfo  atomic.Value // hf10m.DemodInfo
	startedAt time.Time

	reasm   *hf10m.Reassembler
	seenMu  sync.Mutex
	seen    map[string]time.Time
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	txMu    sync.Mutex
	seq     uint32
	stopped atomic.Bool
}

// NewHFGateway creates the gateway. sdr may return nil until the spectrum
// monitor exists; the receiver waits for it.
func NewHFGateway(cfg HFConfig, sdr func() SDRBorrower) *HFGateway {
	g := &HFGateway{
		config:     cfg,
		instanceID: "hf_0",
		inCh:       make(chan InboundMessage, 16),
		sdr:        sdr,
		reasm:      hf10m.NewReassembler(),
		seen:       make(map[string]time.Time),
	}
	g.lastErr.Store("")
	g.lastInfo.Store(hf10m.DemodInfo{})
	g.spawn = spawnRTLTCPFor
	g.dial = func(ctx context.Context, addr string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", addr)
	}
	return g
}

// SetPacketSink wires the live packet feed.
func (g *HFGateway) SetPacketSink(sink PacketSink, instanceID string) {
	g.packetSink = sink
	if instanceID != "" {
		g.instanceID = instanceID
	}
}

// SetEventEmitter wires SSE events.
func (g *HFGateway) SetEventEmitter(fn EventEmitFunc) { g.emit = fn }

// Type returns "hf".
func (g *HFGateway) Type() string { return "hf" }

// Receive returns decoded shouts.
func (g *HFGateway) Receive() <-chan InboundMessage { return g.inCh }

// Enqueue is not supported: sends go through Forward from the delivery worker.
func (g *HFGateway) Enqueue(msg *transport.MeshMessage) error {
	return errors.New("hf: use the delivery ledger (Forward)")
}

// Start begins receiving.
func (g *HFGateway) Start(ctx context.Context) error {
	ctx, g.cancel = context.WithCancel(ctx)
	g.startedAt = time.Now()
	g.wg.Add(1)
	go g.rxLoop(ctx)
	log.Info().Int("freq_hz", g.config.FreqHz).Int("port", g.config.RTLTCPPort).
		Bool("tx_enabled", g.config.TXCallsign != "").Msg("hf: gateway started")
	return nil
}

// Stop ends receiving and returns the dongle.
func (g *HFGateway) Stop() error {
	if g.stopped.Swap(true) {
		return nil
	}
	if g.cancel != nil {
		g.cancel()
	}
	g.wg.Wait()
	return nil
}

// Status reports the gateway state (HF fields ride in HFStatus).
func (g *HFGateway) Status() GatewayStatus {
	s := GatewayStatus{Type: "hf", Connected: g.connected.Load(), MessagesIn: g.msgsIn.Load(), MessagesOut: g.msgsOut.Load(), Errors: g.errors.Load()}
	if ts := g.lastHeard.Load(); ts > 0 {
		s.LastActivity = time.Unix(ts, 0)
	}
	if s.Connected && !g.startedAt.IsZero() {
		s.ConnectionUptime = time.Since(g.startedAt).Truncate(time.Second).String()
	}
	return s
}

// HFStatus is GET /api/hf/status.
type HFStatus struct {
	Receiving      bool    `json:"receiving"`
	FreqHz         int     `json:"freq_hz"`
	Decodes        int64   `json:"decodes"`
	MessagesIn     int64   `json:"messages_in"`
	MessagesOut    int64   `json:"messages_out"`
	Errors         int64   `json:"errors"`
	LastHeard      string  `json:"last_heard,omitempty"`
	LastOffsetHz   float64 `json:"last_offset_hz"`
	LastUWMatches  int     `json:"last_uw_matches"`
	LastMeanLLR    float64 `json:"last_mean_llr"`
	LastError      string  `json:"last_error,omitempty"`
	TXEnabled      bool    `json:"tx_enabled"`
	TXCallsign     string  `json:"tx_callsign,omitempty"`
	SpectrumParked bool    `json:"spectrum_parked"`
}

// HFStatus returns the detailed status.
func (g *HFGateway) HFStatus() HFStatus {
	info, _ := g.lastInfo.Load().(hf10m.DemodInfo)
	st := HFStatus{
		Receiving: g.connected.Load(), FreqHz: g.config.FreqHz, Decodes: g.decodes.Load(),
		MessagesIn: g.msgsIn.Load(), MessagesOut: g.msgsOut.Load(), Errors: g.errors.Load(),
		LastOffsetHz: info.OffsetHz, LastUWMatches: info.UWMatches, LastMeanLLR: info.MeanLLR,
		TXEnabled: g.config.TXCallsign != "", TXCallsign: g.config.TXCallsign,
		SpectrumParked: g.connected.Load(),
	}
	if e, _ := g.lastErr.Load().(string); e != "" {
		st.LastError = e
	}
	if ts := g.lastHeard.Load(); ts > 0 {
		st.LastHeard = time.Unix(ts, 0).UTC().Format(time.RFC3339)
	}
	return st
}

func (g *HFGateway) fail(err error) {
	g.errors.Add(1)
	g.lastErr.Store(err.Error())
}

// rxLoop borrows the dongle, runs a reader session, returns the dongle,
// and retries with backoff.
func (g *HFGateway) rxLoop(ctx context.Context) {
	defer g.wg.Done()
	backoff := hfRetryMin
	for ctx.Err() == nil {
		sdr := g.sdr()
		if sdr == nil {
			g.lastErr.Store("waiting for the spectrum monitor to hand over the RTL-SDR")
		} else {
			sdr.Suspend(ctx)
			start := time.Now()
			err := g.session(ctx)
			sdr.Resume()
			g.connected.Store(false)
			if err != nil && ctx.Err() == nil {
				g.fail(err)
				log.Warn().Err(err).Msg("hf: receiver session ended")
			}
			if time.Since(start) > time.Minute {
				backoff = hfRetryMin
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > hfRetryMax {
			backoff = hfRetryMax
		}
	}
}

// session runs one rtl_tcp reader until it fails or the context ends.
func (g *HFGateway) session(ctx context.Context) error {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(g.config.RTLTCPPort))
	stop, done, err := g.spawn(addr)
	if err != nil {
		return err
	}
	defer stop()
	var conn net.Conn
	deadline := time.Now().Add(20 * time.Second)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-done:
			return errors.New("rtl_tcp exited before accepting a connection")
		default:
		}
		c, err := g.dial(ctx, addr)
		if err == nil {
			conn = c
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("rtl_tcp did not accept on %s: %v", addr, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	hdr := make([]byte, 12)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return fmt.Errorf("rtl_tcp header: %w", err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	if string(hdr[:4]) != "RTL0" {
		return fmt.Errorf("rtl_tcp header magic %q", hdr[:4])
	}
	lo := g.config.FreqHz - hfLOOffsetHz
	for _, c := range [][2]uint32{
		{rtlCmdSetSampleRate, hfSampleRate},
		{rtlCmdSetFreq, uint32(lo)},
		{rtlCmdSetGainMode, 1},
		{rtlCmdSetGain, uint32(math.Round(g.config.GainDB * 10))},
		{rtlCmdSetAGCMode, 0},
	} {
		if err := writeRTLCommand(conn, byte(c[0]), c[1]); err != nil {
			return fmt.Errorf("rtl_tcp command 0x%02x: %w", c[0], err)
		}
	}
	g.connected.Store(true)
	g.lastErr.Store("")
	log.Info().Str("addr", addr).Int("lo_hz", lo).Msg("hf: rtl_tcp streaming, listening for shouts")

	// Rolling 2 kHz baseband buffer; demodulate after every hfMinNewSeconds.
	chunk := make([]byte, 2*hfSampleRate*hfChunkSeconds)
	var baseband []complex128
	newSince := 0
	iq := make([]complex128, hfSampleRate*hfChunkSeconds)
	for ctx.Err() == nil {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.ReadFull(conn, chunk); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("rtl_tcp stream: %w", err)
		}
		for i := range iq {
			iq[i] = complex((float64(chunk[2*i])-127.5)/127.5, (float64(chunk[2*i+1])-127.5)/127.5)
		}
		bb, err := hf10m.DecimateTo2k(iq, hfSampleRate, hfLOOffsetHz)
		if err != nil {
			return err
		}
		baseband = append(baseband, bb...)
		if max := hfBufferSeconds * hf10m.BasebandRate; len(baseband) > max {
			baseband = baseband[len(baseband)-max:]
		}
		newSince += hfChunkSeconds
		if newSince < hfMinNewSeconds {
			continue
		}
		newSince = 0
		f, info, err := hf10m.Demodulate(baseband)
		if info != nil {
			g.lastInfo.Store(*info)
		}
		if err != nil {
			continue
		}
		g.decodes.Add(1)
		// Drop what was consumed so the same shout is not decoded twice.
		end := (info.StartSym + 64 + 24 + info.Blocks*hf10m.LDPCN + 128) * hf10m.SamplesSym
		if end < len(baseband) {
			baseband = baseband[end:]
		} else {
			baseband = baseband[:0]
		}
		g.deliver(f, info)
	}
	return nil
}

// deliver hands a decoded shout up, once per (origin, msg id, fragment).
func (g *HFGateway) deliver(f *hf10m.Frame, info *hf10m.DemodInfo) {
	key := fmt.Sprintf("%s/%d/%d", f.Origin, f.MsgID, f.FragIndex)
	now := time.Now()
	g.seenMu.Lock()
	for k, t := range g.seen {
		if now.Sub(t) > 10*time.Minute {
			delete(g.seen, k)
		}
	}
	if _, dup := g.seen[key]; dup {
		g.seenMu.Unlock()
		return
	}
	g.seen[key] = now
	g.seenMu.Unlock()
	g.lastHeard.Store(now.Unix())
	dest := hex.EncodeToString(f.Dest[:])
	if g.packetSink != nil {
		g.packetSink(PacketRecord{Time: now, Bearer: BearerHF, Dir: DirRX, Iface: g.instanceID, From: f.Origin, To: "lxmf:" + dest,
			Bytes: len(f.Payload) + hf10m.FrameHeaderLen + hf10m.FrameCRCLen, SNR: float32(info.MeanLLR * 10), Text: CapPacketText(string(f.Payload))})
	}
	text, complete := g.reasm.Add(f)
	if !complete {
		log.Info().Str("origin", f.Origin).Uint16("msg_id", f.MsgID).Uint8("frag", f.FragIndex).Uint8("of", f.FragTotal).Msg("hf: fragment received, waiting for the rest")
		return
	}
	g.msgsIn.Add(1)
	log.Info().Str("origin", f.Origin).Str("dest", dest).Float64("offset_hz", info.OffsetHz).Int("uw", info.UWMatches).Msg("hf: shout received")
	if g.emit != nil {
		g.emit("hf_shout", fmt.Sprintf("HF shout from %s", f.Origin))
	}
	select {
	case g.inCh <- InboundMessage{Text: text, To: "lxmf:" + dest, Source: "hf", FromAddr: f.Origin, Plain: true}:
	default:
		g.errors.Add(1)
		log.Warn().Msg("hf: inbound channel full, shout dropped")
	}
}

// Forward transmits a message as one or more shouts. Locked without an
// operator callsign. The text may name its recipient as "lxmf:<32 hex> "
// in front; otherwise the configured default destination is used.
func (g *HFGateway) Forward(ctx context.Context, msg *transport.MeshMessage) error {
	if g.config.TXCallsign == "" {
		return ErrHFTransmitLocked
	}
	if g.config.TXAudioDevice == "" {
		return errors.New("hf_0 transmit: no audio device configured")
	}
	text := msg.DecodedText
	var dest [16]byte
	if g.config.TXDestHash != "" {
		b, _ := hex.DecodeString(g.config.TXDestHash)
		copy(dest[:], b)
	}
	if strings.HasPrefix(text, "lxmf:") {
		if sp := strings.IndexByte(text, ' '); sp == 5+32 {
			if b, err := hex.DecodeString(text[5:sp]); err == nil && len(b) == 16 {
				copy(dest[:], b)
				text = text[sp+1:]
			}
		}
	}
	g.txMu.Lock()
	defer g.txMu.Unlock()
	g.seq++
	frames, err := hf10m.Fragment(g.config.TXCallsign, dest, uint16(g.seq), text)
	if err != nil {
		return err
	}
	for _, f := range frames {
		packed, err := f.Marshal()
		if err != nil {
			return err
		}
		burst, _, err := hf10m.BuildBurst(packed)
		if err != nil {
			return err
		}
		audio := hf10m.ModulateAudio(burst, 8000, g.config.TXAudioCentreHz)
		if err := g.transmitAudio(ctx, audio, 8000); err != nil {
			g.fail(err)
			return err
		}
		g.msgsOut.Add(1)
		if g.packetSink != nil {
			g.packetSink(PacketRecord{Time: time.Now(), Bearer: BearerHF, Dir: DirTX, Iface: g.instanceID, From: g.config.TXCallsign,
				To: "lxmf:" + hex.EncodeToString(dest[:]), Bytes: len(packed), Text: CapPacketText(string(f.Payload))})
		}
		log.Info().Str("callsign", g.config.TXCallsign).Uint16("msg_id", f.MsgID).Uint8("frag", f.FragIndex).Uint8("of", f.FragTotal).
			Int("bytes_on_air", len(burst)).Msg("hf: shout transmitted (audit)")
		if g.emit != nil {
			g.emit("hf_transmit", fmt.Sprintf("HF shout transmitted as %s", g.config.TXCallsign))
		}
	}
	return nil
}

// transmitAudio keys the radio over CAT, plays the WAV through aplay,
// and unkeys. The tone rendering is the codec's; the radio is the legal
// gate.
func (g *HFGateway) transmitAudio(ctx context.Context, audio []float64, rate int) error {
	if _, err := exec.LookPath("aplay"); err != nil {
		return errors.New("hf_0 transmit: aplay (alsa-utils) is not in the image")
	}
	dir, err := os.MkdirTemp("", "meshsat-hf-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	wav := filepath.Join(dir, "shout.wav")
	if err := os.WriteFile(wav, WAVFromSamples(audio, rate), 0o600); err != nil {
		return err
	}
	var cat io.WriteCloser
	if g.config.TXCATPort != "" {
		port, err := transport.OpenKISSSerial(g.config.TXCATPort, 9600)
		if err != nil {
			return fmt.Errorf("hf_0 transmit: CAT port: %w", err)
		}
		cat = port
		defer cat.Close()
		if _, err := cat.Write([]byte("TX;")); err != nil {
			return fmt.Errorf("hf_0 transmit: CAT TX: %w", err)
		}
		time.Sleep(150 * time.Millisecond)
	}
	cmd := exec.CommandContext(ctx, "aplay", "-q", "-D", g.config.TXAudioDevice, wav)
	out, err := cmd.CombinedOutput()
	if cat != nil {
		time.Sleep(100 * time.Millisecond)
		_, _ = cat.Write([]byte("RX;"))
	}
	if err != nil {
		return fmt.Errorf("hf_0 transmit: aplay: %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// WAVFromSamples renders mono 16-bit PCM.
func WAVFromSamples(samples []float64, rate int) []byte {
	data := make([]byte, 0, 44+2*len(samples))
	data = append(data, "RIFF"...)
	data = binary.LittleEndian.AppendUint32(data, uint32(36+2*len(samples)))
	data = append(data, "WAVEfmt "...)
	data = binary.LittleEndian.AppendUint32(data, 16)
	data = binary.LittleEndian.AppendUint16(data, 1)
	data = binary.LittleEndian.AppendUint16(data, 1)
	data = binary.LittleEndian.AppendUint32(data, uint32(rate))
	data = binary.LittleEndian.AppendUint32(data, uint32(rate*2))
	data = binary.LittleEndian.AppendUint16(data, 2)
	data = binary.LittleEndian.AppendUint16(data, 16)
	data = append(data, "data"...)
	data = binary.LittleEndian.AppendUint32(data, uint32(2*len(samples)))
	for _, s := range samples {
		v := int16(math.Round(math.Max(-1, math.Min(1, s)) * 0.8 * 32767))
		data = binary.LittleEndian.AppendUint16(data, uint16(v))
	}
	return data
}

func writeRTLCommand(conn net.Conn, cmd byte, param uint32) error {
	var b [5]byte
	b[0] = cmd
	binary.BigEndian.PutUint32(b[1:], param)
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, err := conn.Write(b[:])
	_ = conn.SetWriteDeadline(time.Time{})
	return err
}

// spawnRTLTCPFor starts rtl_tcp bound to addr at the HF sample rate.
func spawnRTLTCPFor(addr string) (func(), <-chan struct{}, error) {
	bin, err := exec.LookPath(hfBinary)
	if err != nil {
		return nil, nil, fmt.Errorf("hf: %s not in PATH", hfBinary)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(bin, "-a", host, "-p", port, "-s", strconv.Itoa(hfSampleRate), "-b", "16", "-n", "32")
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("hf: rtl_tcp start: %w", err)
	}
	done := make(chan struct{})
	go func() {
		err := cmd.Wait()
		log.Info().Err(err).Msg("hf: rtl_tcp exited")
		close(done)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = cmd.Process.Signal(os.Interrupt)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
		})
	}
	return stop, done, nil
}
