package rnode

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"meshsat/internal/kiss"
)

// fakeRNode answers the host like RNode firmware 1.75 on an ESP32: detect,
// version, platform, MCU, echoes every configuration command, loops data
// frames back, and can inject stats and errors.
type fakeRNode struct {
	hostR *io.PipeReader // what the host writes
	hostW *io.PipeWriter
	devR  *io.PipeReader
	devW  *io.PipeWriter // what the device writes to the host
	mu    sync.Mutex
	seen  [][]byte
	echo  bool
	ready bool
}

type pipeLink struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (p *pipeLink) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipeLink) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *pipeLink) Close() error                { p.w.Close(); return p.r.Close() }

func newFake(t *testing.T) (*fakeRNode, io.ReadWriteCloser) {
	f := &fakeRNode{echo: true, ready: true}
	f.hostR, f.hostW = io.Pipe() // host -> device
	f.devR, f.devW = io.Pipe()   // device -> host
	go f.run()
	return f, &pipeLink{r: f.devR, w: f.hostW}
}

func (f *fakeRNode) run() {
	sp := &kiss.Splitter{}
	buf := make([]byte, 1024)
	for {
		n, err := f.hostR.Read(buf)
		if err != nil {
			return
		}
		for _, fr := range sp.Feed(buf[:n], time.Now()) {
			f.mu.Lock()
			f.seen = append(f.seen, append([]byte{fr.Cmd}, fr.Payload...))
			f.mu.Unlock()
			switch fr.Cmd {
			case CmdDetect:
				f.devW.Write([]byte{kiss.FEND, CmdDetect, DetectResp, kiss.FEND})
			case CmdFWVersion:
				f.devW.Write([]byte{kiss.FEND, CmdFWVersion, 1, 75, kiss.FEND})
			case CmdPlatform:
				f.devW.Write([]byte{kiss.FEND, CmdPlatform, PlatformESP32, kiss.FEND})
			case CmdMCU:
				f.devW.Write([]byte{kiss.FEND, CmdMCU, 0x81, kiss.FEND})
			case CmdFrequency, CmdBandwidth, CmdTXPower, CmdSF, CmdCR, CmdRadioState, CmdSTALock, CmdLTALock:
				if f.echo {
					f.devW.Write(kiss.Encode(fr.Cmd, fr.Payload))
				}
			case CmdData:
				// loop back, then READY (flow control)
				f.devW.Write(kiss.Encode(CmdData, fr.Payload))
				if f.ready {
					f.devW.Write([]byte{kiss.FEND, CmdReady, 0x01, kiss.FEND})
				}
			}
		}
	}
}

func (f *fakeRNode) inject(b []byte) { f.devW.Write(b) }

func (f *fakeRNode) sawCmd(cmd byte) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.seen {
		if s[0] == cmd {
			return s[1:]
		}
	}
	return nil
}

func TestDriverOpenConfigureAndLoopback(t *testing.T) {
	fake, link := newFake(t)
	var got [][]byte
	var gotMu sync.Mutex
	d := New(link, Options{Name: "rnode_test", Params: Presets[1].Params, FlowControl: true,
		OnPacket: func(p []byte) { gotMu.Lock(); got = append(got, p); gotMu.Unlock() },
		Sleep:    func(time.Duration) {}})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.Open(ctx); err != nil {
		t.Fatalf("open: %v", err)
	}
	if !d.Online() {
		t.Fatalf("not online")
	}
	// Configuration went out in the upstream order with the right encoding.
	if f := fake.sawCmd(CmdFrequency); !bytes.Equal(f, []byte{0x33, 0xB0, 0x6C, 0x00}) { // 867200000
		t.Fatalf("frequency bytes %x", f)
	}
	if bw := fake.sawCmd(CmdBandwidth); !bytes.Equal(bw, []byte{0x00, 0x01, 0xE8, 0x48}) { // 125000
		t.Fatalf("bandwidth bytes %x", bw)
	}
	if sf := fake.sawCmd(CmdSF); !bytes.Equal(sf, []byte{8}) {
		t.Fatalf("sf %x", sf)
	}
	if st := fake.sawCmd(CmdRadioState); !bytes.Equal(st, []byte{1}) {
		t.Fatalf("state %x", st)
	}
	s := d.Stats()
	if !s.Detected || s.FirmwareMajor != 1 || s.FirmwareMinor != 75 || s.Platform != PlatformESP32 || s.Frequency != 867200000 || !s.RadioOn {
		t.Fatalf("stats %+v", s)
	}
	if s.BitrateBps < 3000 || s.BitrateBps > 3200 { // SF8 BW125 CR5 = 3125 bps
		t.Fatalf("bitrate %v", s.BitrateBps)
	}
	// Data with bytes that need escaping loops back intact; flow control releases the queue.
	pkt := []byte{0x01, kiss.FEND, 0x02, kiss.FESC, 0x03}
	if err := d.Send(pkt); err != nil {
		t.Fatal(err)
	}
	if err := d.Send([]byte("second")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		gotMu.Lock()
		n := len(got)
		gotMu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	gotMu.Lock()
	defer gotMu.Unlock()
	if len(got) != 2 || !bytes.Equal(got[0], pkt) || string(got[1]) != "second" {
		t.Fatalf("loopback got %x", got)
	}
	// Stats frames parse.
	fake.inject(kiss.Encode(CmdStatRSSI, []byte{157 - 80}))
	fake.inject(kiss.Encode(CmdStatSNR, []byte{0xF8}))
	fake.inject(kiss.Encode(CmdStatCHTM, []byte{0x00, 0x7B, 0x01, 0x2C, 0x00, 0x0A, 0x00, 0x14, 157 - 90, 157 - 110, 0xFF}))
	time.Sleep(50 * time.Millisecond)
	s = d.Stats()
	if s.RSSI != -80 || s.SNR != -2 || s.AirtimeShort != 1.23 || s.AirtimeLong != 3.0 || s.NoiseFloor != -110 || s.Interference != nil {
		t.Fatalf("parsed stats %+v", s)
	}
	// A reset while online is a fatal error for the owner.
	fake.inject([]byte{kiss.FEND, CmdReset, ResetMagic, kiss.FEND})
	select {
	case err := <-d.Errors():
		if err != ErrDeviceReset {
			t.Fatalf("err %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no reset error")
	}
	d.Close()
}

func TestDriverRejectsMismatchedRadio(t *testing.T) {
	fake, link := newFake(t)
	fake.echo = false // radio never confirms
	d := New(link, Options{Name: "x", Params: Presets[0].Params, Sleep: func(time.Duration) {}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := d.Open(ctx)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("did not confirm")) {
		t.Fatalf("expected validation error, got %v", err)
	}
	d.Close()
}

func TestParamsValidateAndPresets(t *testing.T) {
	for _, p := range Presets {
		if err := p.Validate(); err != nil {
			t.Fatalf("%s: %v", p.ID, err)
		}
	}
	bad := Params{Frequency: 1, Bandwidth: 125000, SF: 7, CR: 5}
	if bad.Validate() == nil {
		t.Fatalf("bad frequency accepted")
	}
	if PresetByID("eu-868").Frequency != 867_200_000 || PresetByID("nope") != nil {
		t.Fatalf("preset lookup")
	}
}
