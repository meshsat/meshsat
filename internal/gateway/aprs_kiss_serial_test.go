package gateway

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// pipeRW is an in-memory serial-port stand-in: reads block until data or a
// timeout, then return (0, nil) like go.bug.st/serial with a read timeout.
type pipeRW struct {
	mu      sync.Mutex
	cond    *sync.Cond
	buf     bytes.Buffer
	written bytes.Buffer
	closed  bool
	timeout time.Duration
}

func newPipeRW() *pipeRW {
	p := &pipeRW{timeout: 20 * time.Millisecond}
	p.cond = sync.NewCond(&p.mu)
	return p
}

// feed appends bytes the "TNC" sends to the host.
func (p *pipeRW) feed(b []byte) {
	p.mu.Lock()
	p.buf.Write(b)
	p.mu.Unlock()
	p.cond.Broadcast()
}

func (p *pipeRW) Read(b []byte) (int, error) {
	deadline := time.Now().Add(p.timeout)
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.buf.Len() == 0 && !p.closed {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, nil // timeout: nothing arrived
		}
		t := time.AfterFunc(remaining, p.cond.Broadcast)
		p.cond.Wait()
		t.Stop()
	}
	if p.closed && p.buf.Len() == 0 {
		return 0, io.EOF
	}
	return p.buf.Read(b)
}

func (p *pipeRW) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, errors.New("closed")
	}
	return p.written.Write(b)
}

func (p *pipeRW) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.cond.Broadcast()
	return nil
}

func TestKISSConnSerial_ReadFrameFromStream(t *testing.T) {
	rw := newPipeRW()
	k := newKISSConnRW(rw)

	payload := []byte{0x82, 0xA0, 0xC0, 0xDB, 0x01, 0x02}
	// Idle FENDs before the frame and a split delivery across two writes.
	enc := KISSEncode(payload)
	rw.feed([]byte{kissFEND, kissFEND})
	rw.feed(enc[:3])
	go func() {
		time.Sleep(5 * time.Millisecond)
		rw.feed(enc[3:])
	}()

	got, err := k.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: %x vs %x", got, payload)
	}
	if k.RX.Load() != 1 {
		t.Fatalf("RX counter %d, want 1", k.RX.Load())
	}
}

func TestKISSConnSerial_TimeoutIsTimeout(t *testing.T) {
	rw := newPipeRW()
	k := newKISSConnRW(rw)
	_, err := k.ReadFrame()
	if err == nil {
		t.Fatal("expected a timeout error on a silent port")
	}
	te, ok := err.(interface{ Timeout() bool })
	if !ok || !te.Timeout() {
		t.Fatalf("expected Timeout() error, got %v", err)
	}
}

func TestKISSConnSerial_SendFrameWritesKISS(t *testing.T) {
	rw := newPipeRW()
	k := newKISSConnRW(rw)
	if err := k.SendFrame([]byte{0xC0, 0x41}); err != nil {
		t.Fatalf("SendFrame: %v", err)
	}
	want := []byte{kissFEND, kissData, kissFESC, kissTFEND, 0x41, kissFEND}
	if !bytes.Equal(rw.written.Bytes(), want) {
		t.Fatalf("wire %x, want %x", rw.written.Bytes(), want)
	}
	if k.TX.Load() != 1 {
		t.Fatalf("TX counter %d, want 1", k.TX.Load())
	}
}

func TestKISSConnSerial_EOFIsNotTimeout(t *testing.T) {
	rw := newPipeRW()
	k := newKISSConnRW(rw)
	rw.Close()
	_, err := k.ReadFrame()
	if err == nil {
		t.Fatal("expected an error after close")
	}
	if te, ok := err.(interface{ Timeout() bool }); ok && te.Timeout() {
		t.Fatalf("EOF must not read as a timeout: %v", err)
	}
}

func TestAPRSGateway_FanOutAndReceiveHealth(t *testing.T) {
	cfg := DefaultAPRSConfig()
	cfg.Callsign = "TEST"
	cfg.KISSDevice = "/dev/null-tnc"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !cfg.ExternalDirewolf || cfg.KISSBaud != 115200 {
		t.Fatalf("serial TNC must force external mode and default baud: %+v", cfg)
	}
	g := NewAPRSGateway(cfg, nil)
	if !g.SerialTNC() {
		t.Fatal("gateway should report a serial TNC")
	}
	if g.supervisor != nil {
		t.Fatal("no Direwolf supervisor in TNC mode")
	}

	// Before any frame: health known, nothing decoded.
	h, ok := g.ReceiveHealth()
	if !ok || h.Level != -1 || !h.LastDecodeAt.IsZero() || !h.LevelAt.IsZero() {
		t.Fatalf("unexpected initial health %+v ok=%v", h, ok)
	}

	ch1, cancel1 := g.SubscribeFrames()
	ch2, cancel2 := g.SubscribeFrames()
	defer cancel2()
	payload := []byte{1, 2, 3}
	g.fanOut(payload)
	g.lastFrameAt.Store(time.Now().UnixNano())
	for i, ch := range []<-chan []byte{ch1, ch2} {
		select {
		case got := <-ch:
			if !bytes.Equal(got, payload) {
				t.Fatalf("subscriber %d got %x", i, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d received nothing", i)
		}
	}
	cancel1()
	if _, open := <-ch1; open {
		t.Fatal("cancelled subscription should be closed")
	}
	h, _ = g.ReceiveHealth()
	if h.LastDecodeAt.IsZero() {
		t.Fatal("LastDecodeAt should be stamped")
	}

	g.closeSubscribers()
	if _, open := <-ch2; open {
		t.Fatal("closeSubscribers should close the remaining channel")
	}
	if st := g.currentReceiveState(); st != "" {
		t.Fatalf("serial TNC must not read as receive-unknown before the watchdog judged, got %q", st)
	}
	status := g.GetAPRSStatus()
	if status["tnc_serial"] != true || status["kiss_addr"] != "/dev/null-tnc@115200" {
		t.Fatalf("status %+v", status)
	}
}
