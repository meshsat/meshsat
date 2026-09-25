package gateway

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"meshsat/internal/hf10m"
	"meshsat/internal/transport"
)

type fakeSDR struct {
	suspends, resumes atomic.Int32
}

func (f *fakeSDR) Suspend(context.Context) { f.suspends.Add(1) }
func (f *fakeSDR) Resume()                 { f.resumes.Add(1) }

// fakeRTLTCP speaks enough of the rtl_tcp protocol: the RTL0 header, then
// it streams u8 IQ of the given wideband signal followed by silence.
func fakeRTLTCP(t *testing.T, signal []complex128) (addr string, cmds chan [5]byte, closeFn func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cmds = make(chan [5]byte, 32)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		hdr := append([]byte("RTL0"), make([]byte, 8)...)
		binary.BigEndian.PutUint32(hdr[4:], 1)  // tuner type
		binary.BigEndian.PutUint32(hdr[8:], 29) // gain count
		c.Write(hdr)
		go func() {
			for {
				var b [5]byte
				if _, err := io.ReadFull(c, b[:]); err != nil {
					return
				}
				select {
				case cmds <- b:
				default:
				}
			}
		}()
		// Silence, the signal, silence: 1 s each side.
		buf := make([]byte, 0, 2*len(signal)+4*hfSampleRate)
		silence := make([]byte, 2*hfSampleRate)
		for i := range silence {
			silence[i] = 127
		}
		buf = append(buf, silence...)
		for _, s := range signal {
			buf = append(buf, byte(real(s)*100+127.5), byte(imag(s)*100+127.5))
		}
		buf = append(buf, silence...)
		buf = append(buf, silence...)
		// Feed in 0.25 s pieces so the reader's 1 s chunks arrive steadily.
		piece := 2 * hfSampleRate / 4
		for i := 0; i < len(buf); i += piece {
			end := i + piece
			if end > len(buf) {
				end = len(buf)
			}
			if _, err := c.Write(buf[i:end]); err != nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		// Then keep the stream alive with silence until closed.
		for {
			if _, err := c.Write(silence); err != nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	return ln.Addr().String(), cmds, func() { ln.Close(); wg.Wait() }
}

// A shout on the air reaches the inbound channel as plaintext from the
// callsign, addressed to the LXMF hash, and the dongle is borrowed from
// and returned to the spectrum monitor around the session.
func TestHFGatewayReceivesAShout(t *testing.T) {
	var dest [16]byte
	copy(dest[:], mustHexT(t, "0123456789abcdef0123456789abcdef"))
	f := &hf10m.Frame{Origin: "N0CALL", Dest: dest, MsgID: 42, FragTotal: 1, Payload: []byte("no internet here. all ok. next check 0900")}
	packed, _ := f.Marshal()
	burst, _, _ := hf10m.BuildBurst(packed)
	// The LO sits 1 kHz below the centre, so the shout appears at +1 kHz (+23 Hz error).
	signal := hf10m.ModulateIQ(burst, hfSampleRate, hfLOOffsetHz+23)
	addr, cmds, closeFake := fakeRTLTCP(t, signal)
	defer closeFake()

	sdr := &fakeSDR{}
	cfg := DefaultHFConfig()
	g := NewHFGateway(cfg, func() SDRBorrower { return sdr })
	g.spawn = func(string) (func(), <-chan struct{}, error) { return func() {}, make(chan struct{}), nil }
	g.dial = func(ctx context.Context, _ string) (net.Conn, error) { return net.Dial("tcp", addr) }
	var records []PacketRecord
	var rmu sync.Mutex
	g.SetPacketSink(func(r PacketRecord) { rmu.Lock(); records = append(records, r); rmu.Unlock() }, "hf_0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := g.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-g.Receive():
		if m.Source != "hf" || m.FromAddr != "N0CALL" || m.To != "lxmf:"+hex.EncodeToString(dest[:]) || !m.Plain || m.Text != string(f.Payload) {
			t.Fatalf("inbound %+v", m)
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("no shout decoded; status %+v", g.HFStatus())
	}
	st := g.HFStatus()
	if !st.Receiving || st.Decodes != 1 || st.MessagesIn != 1 || st.LastUWMatches < hf10m.UWMinMatch || st.LastOffsetHz < 17 || st.LastOffsetHz > 29 {
		t.Fatalf("status %+v", st)
	}
	rmu.Lock()
	if len(records) != 1 || records[0].Bearer != BearerHF || records[0].Dir != DirRX || records[0].From != "N0CALL" {
		t.Fatalf("packet records %+v", records)
	}
	rmu.Unlock()
	// The tuner was set to the LO 1 kHz below the centre and to 240 kS/s.
	seen := map[byte]uint32{}
	deadline := time.After(2 * time.Second)
collect:
	for {
		select {
		case c := <-cmds:
			seen[c[0]] = binary.BigEndian.Uint32(c[1:])
		case <-deadline:
			break collect
		default:
			if len(seen) >= 5 {
				break collect
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if seen[rtlCmdSetFreq] != uint32(cfg.FreqHz-hfLOOffsetHz) || seen[rtlCmdSetSampleRate] != hfSampleRate || seen[rtlCmdSetGain] != 200 {
		t.Fatalf("tuner commands %v", seen)
	}
	if sdr.suspends.Load() != 1 || sdr.resumes.Load() != 0 {
		t.Fatalf("suspend/resume during the session: %d/%d", sdr.suspends.Load(), sdr.resumes.Load())
	}
	g.Stop()
	if sdr.resumes.Load() != 1 {
		t.Fatalf("dongle not returned after Stop: resumes=%d", sdr.resumes.Load())
	}
}

func TestHFGatewayTransmitIsLockedWithoutACallsign(t *testing.T) {
	g := NewHFGateway(DefaultHFConfig(), func() SDRBorrower { return nil })
	err := g.Forward(context.Background(), &transport.MeshMessage{DecodedText: "hello"})
	if err != ErrHFTransmitLocked {
		t.Fatalf("err = %v", err)
	}
	if g.HFStatus().TXEnabled {
		t.Fatal("tx reported enabled")
	}
	cfg := DefaultHFConfig()
	cfg.TXCallsign = "N0CALL"
	g = NewHFGateway(cfg, func() SDRBorrower { return nil })
	if err := g.Forward(context.Background(), &transport.MeshMessage{DecodedText: "hello"}); err == nil || err == ErrHFTransmitLocked {
		t.Fatalf("with a callsign but no audio device: %v", err)
	}
}

func TestHFConfigValidate(t *testing.T) {
	c, err := ParseHFConfig(`{"freq_hz": 28124000, "tx_dest_hash": "0123456789abcdef0123456789abcdef"}`)
	if err != nil || c.Validate() != nil || c.RTLTCPPort != 6057 || c.GainDB != 20 {
		t.Fatalf("%+v %v", c, err)
	}
	bad := []string{`{"freq_hz": 14000000}`, `{"rtltcp_port": 80}`, `{"gain_db": 99}`, `{"tx_dest_hash": "zz"}`, `{"tx_audio_centre_hz": 10}`}
	for _, b := range bad {
		c, err := ParseHFConfig(b)
		if err != nil {
			continue
		}
		if c.Validate() == nil {
			t.Fatalf("%s accepted", b)
		}
	}
	wav := WAVFromSamples([]float64{0, 0.5, -0.5, 1, -1}, 8000)
	if len(wav) != 44+10 || string(wav[:4]) != "RIFF" || binary.LittleEndian.Uint32(wav[24:]) != 8000 {
		t.Fatalf("wav header %x", wav[:44])
	}
}

func mustHexT(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
