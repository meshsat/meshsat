package transport

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.bug.st/serial"
)

// fakeZNPPort answers every write with the queued frames, once.
type fakeZNPPort struct {
	serial.Port
	mu     sync.Mutex
	writes [][]byte
	rx     []byte
}

func (f *fakeZNPPort) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.writes = append(f.writes, append([]byte(nil), p...))
	f.mu.Unlock()
	return len(p), nil
}

func (f *fakeZNPPort) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.rx) == 0 {
		time.Sleep(10 * time.Millisecond) // behave like a read timeout
		return 0, nil
	}
	n := copy(p, f.rx)
	f.rx = f.rx[n:]
	return n, nil
}

func (f *fakeZNPPort) SetReadTimeout(time.Duration) error { return nil }
func (f *fakeZNPPort) Close() error                       { return nil }

// Ping sends SYS_PING under the serial lock and is satisfied by the SRSP;
// a coordinator that never answers (bootloader, wedge) times out. [MESHSAT-817]
func TestZigBeePing(t *testing.T) {
	rsp, err := EncodeZNP(ZNPFrame{Cmd: CmdSysPingRsp, Data: []byte{0x79, 0x07}})
	if err != nil {
		t.Fatal(err)
	}
	z := NewDirectZigBeeTransport()
	port := &fakeZNPPort{rx: rsp}
	z.mu.Lock()
	z.running = true
	z.port = port
	z.mu.Unlock()

	if err := z.Ping(context.Background()); err != nil {
		t.Fatalf("ping with reply: %v", err)
	}
	want, _ := EncodeZNP(BuildSysPing())
	port.mu.Lock()
	got := port.writes
	port.mu.Unlock()
	if len(got) != 1 || string(got[0]) != string(want) {
		t.Fatalf("wrote %x, want SYS_PING %x", got, want)
	}
	if z.LastFrameAt().IsZero() {
		t.Fatal("LastFrameAt not stamped by a ping reply")
	}

	// Silent coordinator: no reply within the 2 s window.
	silent := NewDirectZigBeeTransport()
	silent.mu.Lock()
	silent.running = true
	silent.port = &fakeZNPPort{}
	silent.mu.Unlock()
	start := time.Now()
	if err := silent.Ping(context.Background()); err == nil {
		t.Fatal("ping of a silent coordinator succeeded")
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("ping did not time out promptly")
	}

	stopped := NewDirectZigBeeTransport()
	if err := stopped.Ping(context.Background()); err == nil {
		t.Fatal("ping of a stopped transport succeeded")
	}
}

// lockSerial gives up when readLoop holds the lock (a wedged read(2)).
func TestZigBeeLockSerialBounded(t *testing.T) {
	z := NewDirectZigBeeTransport()
	z.serialMu.Lock()
	defer z.serialMu.Unlock()
	start := time.Now()
	if err := z.lockSerial(context.Background(), 50*time.Millisecond); err == nil {
		t.Fatal("lockSerial acquired a held lock")
	}
	if time.Since(start) > time.Second {
		t.Fatal("lockSerial did not respect its bound")
	}
}
