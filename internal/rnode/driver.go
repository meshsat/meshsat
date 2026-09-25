package rnode

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/kiss"
)

// Transport kinds change the detect and validation waits.
type Transport int

const (
	Serial Transport = iota
	TCP
	BLE
)

// Options configure a Driver.
type Options struct {
	Name        string
	Transport   Transport
	Params      Params
	FlowControl bool
	IDCallsign  string
	IDInterval  time.Duration
	// OnPacket receives every data frame from the radio.
	OnPacket func(packet []byte)
	// OnError is called when the device reports a fatal error or resets;
	// the owner should close and reopen.
	OnError func(err error)
	// Now for tests.
	Now func() time.Time
	// Sleep for tests (nil = time.Sleep).
	Sleep func(time.Duration)
}

// Driver is one RNode over an io.ReadWriteCloser.
type Driver struct {
	opts Options
	link io.ReadWriteCloser

	mu        sync.Mutex
	writeMu   sync.Mutex
	stats     Stats
	online    bool
	ready     bool // flow control: device accepts the next frame
	queue     [][]byte
	firstTX   time.Time
	lastWrite time.Time
	closed    bool
	errCh     chan error
	detectCh  chan struct{}
	readDone  chan struct{}
}

// Errors.
var (
	ErrNotDetected  = errors.New("rnode: device not detected")
	ErrFirmware     = errors.New("rnode: firmware older than 1.52")
	ErrValidation   = errors.New("rnode: radio did not confirm the configuration")
	ErrNotOnline    = errors.New("rnode: interface not online")
	ErrHardware     = errors.New("rnode: hardware error")
	ErrDeviceReset  = errors.New("rnode: device reset while online")
	ErrPacketTooBig = errors.New("rnode: packet exceeds HW MTU")
)

// New wraps an open link. Call Open to detect and configure the radio.
func New(link io.ReadWriteCloser, opts Options) *Driver {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Sleep == nil {
		opts.Sleep = time.Sleep
	}
	return &Driver{opts: opts, link: link, errCh: make(chan error, 4), detectCh: make(chan struct{}, 1), readDone: make(chan struct{})}
}

func (d *Driver) frameTimeout() time.Duration {
	switch d.opts.Transport {
	case BLE:
		return 1250 * time.Millisecond
	case TCP:
		return 1500 * time.Millisecond
	}
	return 100 * time.Millisecond
}

func (d *Driver) write(b []byte) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	n, err := d.link.Write(b)
	if err != nil {
		return err
	}
	if n != len(b) {
		return fmt.Errorf("rnode: short write %d of %d", n, len(b))
	}
	d.mu.Lock()
	d.lastWrite = d.opts.Now()
	d.mu.Unlock()
	return nil
}

// Open runs the RNodeInterface configure_device sequence: settle, start the
// reader, detect, initialise the radio, validate what it reports.
func (d *Driver) Open(ctx context.Context) error {
	if err := d.opts.Params.Validate(); err != nil {
		return err
	}
	d.opts.Sleep(2 * time.Second)
	go d.readLoop()
	if err := d.write(DetectBurst()); err != nil {
		return err
	}
	detectWait := 200 * time.Millisecond
	if d.opts.Transport != Serial {
		detectWait = 5 * time.Second
	}
	select {
	case <-d.detectCh:
	case <-time.After(detectWait):
		// Serial: Python waits 0.2 s unconditionally then checks; give a
		// slow USB stack a little more before giving up.
		if d.opts.Transport == Serial {
			select {
			case <-d.detectCh:
			case <-time.After(1800 * time.Millisecond):
			}
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	// The version, platform and MCU replies follow the detect reply in the
	// same burst; give them a moment (Python waits a flat 0.2 s).
	for i := 0; i < 50; i++ {
		d.mu.Lock()
		have := d.stats.FirmwareMajor != 0
		d.mu.Unlock()
		if have {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	d.mu.Lock()
	detected := d.stats.Detected
	maj, min := d.stats.FirmwareMajor, d.stats.FirmwareMinor
	d.mu.Unlock()
	if !detected {
		return ErrNotDetected
	}
	if maj < RequiredFWMajor || (maj == RequiredFWMajor && min < RequiredFWMinor) {
		return fmt.Errorf("%w: device reports %d.%d", ErrFirmware, maj, min)
	}
	if err := d.initRadio(); err != nil {
		return err
	}
	validateWait := 250 * time.Millisecond
	switch d.opts.Transport {
	case BLE:
		validateWait = time.Second
	case TCP:
		validateWait = 1500 * time.Millisecond
	}
	d.opts.Sleep(validateWait)
	if err := d.validate(); err != nil {
		return err
	}
	d.mu.Lock()
	d.online = true
	d.ready = true
	d.mu.Unlock()
	log.Info().Str("rnode", d.opts.Name).Uint32("freq", d.opts.Params.Frequency).Uint32("bw", d.opts.Params.Bandwidth).
		Uint8("sf", d.opts.Params.SF).Uint8("cr", d.opts.Params.CR).Uint8("txp", d.opts.Params.TXPower).
		Str("platform", PlatformName(d.stats.Platform)).Int("fw_major", maj).Int("fw_minor", min).
		Msg("rnode: configured and powered up")
	return nil
}

func (d *Driver) initRadio() error {
	p := d.opts.Params
	cmds := [][]byte{SetFrequency(p.Frequency), SetBandwidth(p.Bandwidth), SetTXPower(p.TXPower), SetSF(p.SF), SetCR(p.CR)}
	if p.STALock > 0 {
		cmds = append(cmds, SetSTALock(p.STALock))
	}
	if p.LTALock > 0 {
		cmds = append(cmds, SetLTALock(p.LTALock))
	}
	cmds = append(cmds, SetRadioState(true))
	for _, c := range cmds {
		if err := d.write(c); err != nil {
			return err
		}
	}
	if d.opts.Transport == BLE {
		d.opts.Sleep(2 * time.Second)
	}
	return nil
}

func (d *Driver) validate() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.opts.Params
	s := d.stats
	var problems []string
	if s.Frequency != 0 && absDiff(s.Frequency, p.Frequency) > 100 {
		problems = append(problems, fmt.Sprintf("frequency %d != %d", s.Frequency, p.Frequency))
	}
	if s.Bandwidth != p.Bandwidth {
		problems = append(problems, fmt.Sprintf("bandwidth %d != %d", s.Bandwidth, p.Bandwidth))
	}
	if s.TXPower != p.TXPower {
		problems = append(problems, fmt.Sprintf("txpower %d != %d", s.TXPower, p.TXPower))
	}
	if s.SF != p.SF {
		problems = append(problems, fmt.Sprintf("sf %d != %d", s.SF, p.SF))
	}
	if !s.RadioOn {
		problems = append(problems, "radio state off")
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %v", ErrValidation, problems)
	}
	return nil
}

func absDiff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}

// Online reports whether the radio is configured and up.
func (d *Driver) Online() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.online
}

// Stats returns the last reported values.
func (d *Driver) Stats() Stats {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.stats
	s.BitrateBps = Bitrate(s.SF, s.CR, s.Bandwidth)
	return s
}

// Errors delivers device errors that require a reopen.
func (d *Driver) Errors() <-chan error { return d.errCh }

// Send transmits one Reticulum packet (RNodeInterface.process_outgoing).
func (d *Driver) Send(packet []byte) error {
	if len(packet) > HWMTU {
		return ErrPacketTooBig
	}
	d.mu.Lock()
	if !d.online {
		d.mu.Unlock()
		return ErrNotOnline
	}
	if d.opts.FlowControl && !d.ready {
		d.queue = append(d.queue, append([]byte(nil), packet...))
		d.mu.Unlock()
		return nil
	}
	if d.opts.FlowControl {
		d.ready = false
	}
	if d.firstTX.IsZero() {
		d.firstTX = d.opts.Now()
	}
	d.stats.TXPackets++
	d.stats.TXBytes += uint64(len(packet))
	d.mu.Unlock()
	return d.write(DataFrame(packet))
}

func (d *Driver) processQueue() {
	d.mu.Lock()
	if len(d.queue) == 0 {
		d.ready = true
		d.mu.Unlock()
		return
	}
	next := d.queue[0]
	d.queue = d.queue[1:]
	d.ready = true
	d.mu.Unlock()
	_ = d.Send(next)
}

// Close turns the radio off, says goodbye and closes the link.
func (d *Driver) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	d.online = false
	d.mu.Unlock()
	_ = d.write(SetRadioState(false))
	_ = d.write(Leave())
	err := d.link.Close()
	select {
	case <-d.readDone:
	case <-time.After(2 * time.Second):
	}
	return err
}

// readLoop parses frames and updates state (RNodeInterface.readLoop).
func (d *Driver) readLoop() {
	defer close(d.readDone)
	sp := &kiss.Splitter{MaxLen: HWMTU + 1, Timeout: d.frameTimeout()}
	buf := make([]byte, 4096)
	lastKeepalive := d.opts.Now()
	for {
		n, err := d.link.Read(buf)
		now := d.opts.Now()
		if n > 0 {
			for _, f := range sp.Feed(buf[:n], now) {
				d.handleFrame(f)
			}
		}
		if err != nil {
			d.mu.Lock()
			closed := d.closed
			d.online = false
			d.mu.Unlock()
			if !closed {
				d.fail(fmt.Errorf("rnode: read: %w", err))
			}
			return
		}
		// Housekeeping that Python does between reads.
		d.mu.Lock()
		online := d.online
		idOK := d.opts.IDCallsign != "" && d.opts.IDInterval > 0 && !d.firstTX.IsZero() && now.After(d.firstTX.Add(d.opts.IDInterval))
		tcpIdle := d.opts.Transport == TCP && now.After(d.lastWrite.Add(3500*time.Millisecond)) && now.After(lastKeepalive.Add(3500*time.Millisecond))
		d.mu.Unlock()
		if online && idOK {
			d.mu.Lock()
			d.firstTX = time.Time{}
			d.mu.Unlock()
			_ = d.write(DataFrame([]byte(d.opts.IDCallsign)))
		}
		if tcpIdle {
			lastKeepalive = now
			_ = d.write(DetectBurst())
		}
		if n == 0 {
			// Serial read timeout with nothing to read: avoid a hot loop on
			// links that return (0, nil).
			d.opts.Sleep(20 * time.Millisecond)
		}
	}
}

func (d *Driver) fail(err error) {
	select {
	case d.errCh <- err:
	default:
	}
	if d.opts.OnError != nil {
		d.opts.OnError(err)
	}
}

func be16(b []byte) int { return int(b[0])<<8 | int(b[1]) }

// handleFrame applies one reply.
func (d *Driver) handleFrame(f kiss.Frame) {
	p := f.Payload
	switch f.Cmd {
	case CmdData:
		d.mu.Lock()
		d.stats.RXPackets++
		d.stats.RXBytes += uint64(len(p))
		d.mu.Unlock()
		if d.opts.OnPacket != nil && len(p) > 0 {
			d.opts.OnPacket(append([]byte(nil), p...))
		}
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	switch f.Cmd {
	case CmdFrequency:
		if len(p) >= 4 {
			d.stats.Frequency = uint32(p[0])<<24 | uint32(p[1])<<16 | uint32(p[2])<<8 | uint32(p[3])
		}
	case CmdBandwidth:
		if len(p) >= 4 {
			d.stats.Bandwidth = uint32(p[0])<<24 | uint32(p[1])<<16 | uint32(p[2])<<8 | uint32(p[3])
		}
	case CmdTXPower:
		if len(p) >= 1 {
			d.stats.TXPower = p[0]
		}
	case CmdSF:
		if len(p) >= 1 {
			d.stats.SF = p[0]
		}
	case CmdCR:
		if len(p) >= 1 {
			d.stats.CR = p[0]
		}
	case CmdRadioState:
		if len(p) >= 1 {
			d.stats.RadioOn = p[0] != 0
		}
	case CmdFWVersion:
		if len(p) >= 2 {
			d.stats.FirmwareMajor, d.stats.FirmwareMinor = int(p[0]), int(p[1])
		}
	case CmdStatRSSI:
		if len(p) >= 1 {
			d.stats.RSSI = int(p[0]) - RSSIOffset
		}
	case CmdStatSNR:
		if len(p) >= 1 {
			d.stats.SNR = float64(int8(p[0])) * 0.25
			sfs := float64(d.stats.SF) - 7
			qMin := -9 - sfs*2.5
			q := (d.stats.SNR - qMin) / (6 - qMin) * 100
			if q > 100 {
				q = 100
			}
			if q < 0 {
				q = 0
			}
			d.stats.Quality = q
		}
	case CmdSTALock:
		if len(p) >= 2 {
			d.stats.STALock = float64(be16(p)) / 100
		}
	case CmdLTALock:
		if len(p) >= 2 {
			d.stats.LTALock = float64(be16(p)) / 100
		}
	case CmdStatCHTM:
		if len(p) >= 11 {
			d.stats.AirtimeShort = float64(be16(p[0:])) / 100
			d.stats.AirtimeLong = float64(be16(p[2:])) / 100
			d.stats.ChannelLoadShort = float64(be16(p[4:])) / 100
			d.stats.ChannelLoadLong = float64(be16(p[6:])) / 100
			d.stats.CurrentRSSI = int(p[8]) - RSSIOffset
			d.stats.NoiseFloor = int(p[9]) - RSSIOffset
			if p[10] == 0xFF {
				d.stats.Interference = nil
			} else {
				v := int(p[10]) - RSSIOffset
				d.stats.Interference = &v
			}
		}
	case CmdStatPHYPRM:
		if len(p) >= 12 {
			d.stats.SymbolTimeMs = float64(be16(p[0:])) / 1000
			d.stats.SymbolRate = be16(p[2:])
			d.stats.PreambleSymbols = be16(p[4:])
			d.stats.PreambleTimeMs = be16(p[6:])
			d.stats.CSMASlotTimeMs = be16(p[8:])
			d.stats.CSMADIFSMs = be16(p[10:])
		}
	case CmdStatBat:
		if len(p) >= 2 {
			d.stats.BatteryState = int(p[0])
			pct := int(p[1])
			if pct > 100 {
				pct = 100
			}
			d.stats.BatteryPercent = pct
		}
	case CmdStatTemp:
		if len(p) >= 1 {
			t := int(p[0]) - 120
			if t >= -30 && t <= 90 {
				d.stats.Temperature = &t
			} else {
				d.stats.Temperature = nil
			}
		}
	case CmdPlatform:
		if len(p) >= 1 {
			d.stats.Platform = p[0]
		}
	case CmdMCU:
		if len(p) >= 1 {
			d.stats.MCU = p[0]
		}
	case CmdDetect:
		if len(p) >= 1 && p[0] == DetectResp {
			d.stats.Detected = true
			select {
			case d.detectCh <- struct{}{}:
			default:
			}
		}
	case CmdError:
		if len(p) >= 1 {
			switch p[0] {
			case ErrorInitRadio:
				go d.fail(fmt.Errorf("%w: radio initialisation failure", ErrHardware))
			case ErrorTXFailed:
				go d.fail(fmt.Errorf("%w: transmit failure", ErrHardware))
			case ErrorMemoryLow, ErrorModemTimeout, ErrorQueueFull:
				log.Warn().Str("rnode", d.opts.Name).Uint8("code", p[0]).Msg("rnode: device reports a non-fatal hardware error")
			default:
				go d.fail(fmt.Errorf("%w: code 0x%02x", ErrHardware, p[0]))
			}
		}
	case CmdReset:
		if len(p) >= 1 && p[0] == ResetMagic && d.online && d.stats.Platform == PlatformESP32 {
			d.online = false
			go d.fail(ErrDeviceReset)
		}
	case CmdReady:
		if d.opts.FlowControl {
			go d.processQueue()
		}
	}
}
